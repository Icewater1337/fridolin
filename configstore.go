package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// ConfigStore holds the configuration in two layers: what is written in
// config.json (the "file" layer, ~ kept as typed) and the effective
// configuration the bridge runs with (defaults and expanded paths applied).
// The dashboard edits the file layer; every component reads the effective
// layer through Get, which is a cheap atomic load, so a change applies on
// the next message without a restart.
type ConfigStore struct {
	path string

	mu      sync.Mutex // serialises Update
	file    *Config
	startup *Config // the effective configuration the process started with
	eff     atomic.Pointer[Config]
	etag    atomic.Value // string
	subs    []func(old, new *Config)
}

// ErrConfigConflict means config.json changed since the caller read it.
var ErrConfigConflict = errors.New("config changed since it was loaded — reload and try again")

// OpenConfigStore loads cfgPath. A missing file is not an error: it means an
// empty configuration with defaults, which is what a fresh install starts from.
func OpenConfigStore(cfgPath string) (*ConfigStore, error) {
	// BRAIN_MODE switched classic mode on as "brain_mode": false did, and
	// is refused like it (see retiredBrainMode). It is no setting: the
	// environment only gets this check, so a bridge that ran classic mode
	// from it does not quietly start without a brain for its chats.
	if v := os.Getenv("BRAIN_MODE"); v != "" && !isTruthy(v) {
		return nil, classicModeRemoved("BRAIN_MODE="+v, "Unset BRAIN_MODE in the bridge's environment")
	}
	warnDotEnvNotRead(cfgPath)

	file := &Config{}
	var raw []byte
	if data, err := os.ReadFile(cfgPath); err == nil {
		raw = data
		if err := json.Unmarshal(data, file); err != nil {
			return nil, fmt.Errorf("parsing config file %q: %w", cfgPath, err)
		}
		notes, err := foldRetiredKeys(data, file)
		if err != nil {
			return nil, fmt.Errorf("parsing config file %q: %w", cfgPath, err)
		}
		for _, note := range notes {
			log.Printf("config %s: %s", cfgPath, note)
		}
		warnUnknownKeys(cfgPath, data)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading config file %q: %w", cfgPath, err)
	}

	eff, err := effective(file)
	if err != nil {
		return nil, err
	}
	if file.DataDir == "" {
		warnLegacyDataDir(eff.DataDir)
	}
	cs := &ConfigStore{path: cfgPath, file: file, startup: eff}
	cs.eff.Store(eff)
	cs.etag.Store(etagOf(raw))
	return cs, nil
}

// Path is the config file this store reads and writes.
func (cs *ConfigStore) Path() string { return cs.path }

// Startup returns the effective configuration the process started with:
// what the settings that need a restart still are.
func (cs *ConfigStore) Startup() *Config { return cs.startup }

// Get returns the effective configuration. Treat it as read-only: it is
// shared by every goroutine until the next Update replaces it.
func (cs *ConfigStore) Get() *Config { return cs.eff.Load() }

// ETag identifies the current file contents for optimistic concurrency.
func (cs *ConfigStore) ETag() string { return cs.etag.Load().(string) }

// FileCopy returns a deep copy of the file layer for editing.
func (cs *ConfigStore) FileCopy() *Config {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	c, err := cs.file.clone()
	if err != nil {
		// Unreachable: the file layer always round-trips through JSON.
		panic(err)
	}
	return c
}

// Subscribe registers fn to run after every successful Update, with the old
// and new effective configurations.
func (cs *ConfigStore) Subscribe(fn func(old, new *Config)) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.subs = append(cs.subs, fn)
}

// Update applies mutate to a copy of the file layer, validates the result,
// writes it atomically and swaps in the new effective configuration. etag ""
// skips the conflict check. It returns RestartRequired as of the new
// configuration.
func (cs *ConfigStore) Update(etag string, mutate func(*Config) error) (restart []string, err error) {
	cs.mu.Lock()
	if etag != "" && etag != cs.ETag() {
		cs.mu.Unlock()
		return nil, ErrConfigConflict
	}
	next, err := cs.file.clone()
	if err != nil {
		cs.mu.Unlock()
		return nil, err
	}
	if err := mutate(next); err != nil {
		cs.mu.Unlock()
		return nil, err
	}
	eff, err := effective(next)
	if err != nil {
		cs.mu.Unlock()
		return nil, err
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		cs.mu.Unlock()
		return nil, err
	}
	data = append(data, '\n')
	if err := writeFileAtomic(cs.path, data, 0o600, true); err != nil {
		cs.mu.Unlock()
		return nil, err
	}

	old := cs.eff.Load()
	cs.file = next
	cs.eff.Store(eff)
	cs.etag.Store(etagOf(data))
	subs := append([]func(old, new *Config){}, cs.subs...)
	cs.mu.Unlock()

	for _, fn := range subs {
		fn(old, eff)
	}
	return restartRequired(cs.startup, eff), nil
}

// RestartRequired lists the settings that are read once at startup and now
// differ from the value the bridge started with, so they wait for a
// restart. A setting changed and then changed back is not listed.
func (cs *ConfigStore) RestartRequired() []string {
	return restartRequired(cs.startup, cs.Get())
}

// restartFields are settings read once at startup.
var restartFields = []struct {
	name string
	get  func(*Config) string
	// waits, when set, decides instead of get whether a change from the
	// value at the start waits for a restart.
	waits func(start, now *Config) bool
}{
	{name: "http_listen", get: func(c *Config) string { return c.HTTPListen }},
	{name: "data_dir", get: func(c *Config) string { return c.DataDir }},
	{name: "db_path", get: func(c *Config) string { return c.DBPath }},
	{name: "media_dir", get: func(c *Config) string { return c.MediaDir }},
	{name: "log_level", get: func(c *Config) string { return c.LogLevel }},
	{name: "dashboard.disabled", get: func(c *Config) string { return fmt.Sprint(c.Dashboard.Disabled) }},
	// Turning the token off waits for a restart (see apiGuard); turning it
	// on applies at once.
	{name: "dashboard.require_token", waits: func(start, now *Config) bool {
		return start.DashboardRequiresToken() && !now.DashboardRequiresToken()
	}},
}

func restartRequired(old, new *Config) []string {
	var out []string
	for _, f := range restartFields {
		if f.waits != nil && f.waits(old, new) || f.waits == nil && f.get(old) != f.get(new) {
			out = append(out, f.name)
		}
	}
	return out
}

// writeFileAtomic writes data to a temp file in the same directory, syncs it
// and renames it over path, so a crash never leaves a half-written file. With
// backup, the previous version is kept as path.bak.
func writeFileAtomic(path string, data []byte, perm os.FileMode, backup bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if backup {
		if prev, err := os.ReadFile(path); err == nil && !bytes.Equal(prev, data) {
			_ = os.WriteFile(path+".bak", prev, perm)
		}
	}
	return os.Rename(tmpName, path)
}

func etagOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// warnDotEnvNotRead logs that the .env next to the config, if there is
// one, is not read. Older versions took settings from it, and a key meant
// for the Claude CLI or a brain's tool may sit there too: without this line
// the first sign would be a setting gone back to its config.json value, or
// brains failing with "Not logged in". The file is not opened, so nothing
// in it, a secret say, can end up in the log.
func warnDotEnvNotRead(cfgPath string) {
	path := dotEnvPath(cfgPath)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs // so the line names the folder, for a relative --config too
	}
	if _, err := os.Lstat(path); err == nil {
		log.Printf("warning: %s is no longer read: settings belong in config.json, and variables for the Claude CLI or brains in the bridge's environment (see docs/CONFIGURATION.md#environment-variables)", path)
	}
}

// warnLegacyDataDir says when the default data dir is the one from before
// the rename, taken because ~/.fridolin does not exist, so that it can be
// named in data_dir before the bridge stops looking for it.
func warnLegacyDataDir(dataDir string) {
	home := homeDir()
	if home == "" || dataDir != filepath.Join(home, legacyDataDirName) {
		return
	}
	log.Printf("warning: data dir %s has the name from before the rename and is used because %s does not exist; set the data folder to it (Settings → Advanced) to keep it once the bridge no longer looks for that name", dataDir, filepath.Join(home, dataDirName))
}

// decodeConfigReplacement decodes data, a whole configuration that is to
// replace the file layer (PUT /api/config). It is strict. A key the bridge
// does not know is an error, not a setting dropped on save. So is leaving
// out a key that config.json always has, one without omitempty, at the top
// or in a section: the replacement would reset it to its default, and a
// partial body would turn the sandbox off and empty allowed_numbers
// without a word. Retired keys are folded in as at startup, so a dashboard
// tab opened before an upgrade keeps a model it still sends, and cannot
// bring inline brains back.
func decodeConfigReplacement(data []byte) (*Config, error) {
	if len(bytes.TrimSpace(data)) == 0 || string(bytes.TrimSpace(data)) == "null" {
		return nil, errors.New("config required: send the whole config GET /api/config returns, with your changes")
	}
	dec := json.NewDecoder(bytes.NewReader(withoutRetiredKeys(data)))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	if missing := missingKeys(data, reflect.TypeOf(c), ""); len(missing) > 0 {
		return nil, fmt.Errorf("the config leaves out %s: send the whole config GET /api/config returns, with your changes", strings.Join(missing, ", "))
	}
	if _, err := foldRetiredKeys(data, &c); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return &c, nil
}

// withoutRetiredKeys returns the JSON object data without the retiredKeys,
// which are no Config fields any more, so that the strict decode does not
// call them unknown. Anything but an object comes back unchanged for the
// decoder to refuse.
func withoutRetiredKeys(data []byte) []byte {
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil || obj == nil {
		return data
	}
	for _, rk := range retiredKeys {
		delete(obj, rk.name)
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return data
	}
	return out
}

var jsonUnmarshaler = reflect.TypeFor[json.Unmarshaler]()

// missingKeys lists the keys of the struct type t that the JSON object data
// lacks although they have no omitempty, each with prefix in front. It
// looks into sections (struct fields) the same way, but not into types that
// decode themselves.
func missingKeys(data []byte, t reflect.Type, prefix string) []string {
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil || obj == nil {
		return nil // not an object: Decode has said what it thinks of it
	}
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		if !f.IsExported() || name == "" || name == "-" {
			continue
		}
		raw, ok := obj[name]
		if !ok {
			if !slices.Contains(strings.Split(opts, ","), "omitempty") {
				out = append(out, prefix+name)
			}
			continue
		}
		if f.Type.Kind() == reflect.Struct && !reflect.PointerTo(f.Type).Implements(jsonUnmarshaler) {
			out = append(out, missingKeys(raw, f.Type, prefix+name+".")...)
		}
	}
	return out
}

// securitySettings are the settings that decide who may use the bridge and
// what brains may reach. logSecurityChanges logs every change to them.
var securitySettings = []struct {
	name string
	get  func(*Config) any // a string, or a []string for a list
}{
	{"allowed_numbers", func(c *Config) any { return c.AllowedNumbers }},
	{"allow_any_dm", func(c *Config) any { return fmt.Sprint(c.AllowsAnyDM()) }},
	{"allowed_groups", func(c *Config) any { return c.AllowedGroups }},
	{"dangerously_skip_permissions", func(c *Config) any { return fmt.Sprint(c.DangerouslySkipPermissions) }},
	{"inherit_claude_user_config", func(c *Config) any { return fmt.Sprint(c.InheritClaudeUserConfig) }},
	{"child_env_passthrough", func(c *Config) any { return c.ChildEnvPassthrough }},
	{"sandbox.enabled", func(c *Config) any { return fmt.Sprint(c.Sandbox.Enabled) }},
	{"sandbox.deny_paths", func(c *Config) any { return c.Sandbox.DenyPaths }},
	{"sandbox.allow_paths", func(c *Config) any { return c.Sandbox.AllowPaths }},
	{"sandbox.git_config", func(c *Config) any { return c.Sandbox.GitConfig }},
	{"sandbox.credentials_dir", func(c *Config) any { return c.Sandbox.CredentialsDir }},
	{"dashboard.require_token", func(c *Config) any { return fmt.Sprint(c.DashboardRequiresToken()) }},
	{"ingest.enabled", func(c *Config) any { return fmt.Sprint(c.Ingest.Enabled) }},
	{"http_listen", func(c *Config) any { return c.HTTPListen }},
	{"data_dir", func(c *Config) any { return c.DataDir }},
	{"db_path", func(c *Config) any { return c.DBPath }},
}

// logSecurityChanges logs, in one line, how an update changed the security
// settings, so the log shows afterwards when the sandbox went off or who
// was let in. A list shows what was added (+) and removed (-).
func logSecurityChanges(prev, next *Config) {
	var changes []string
	for _, s := range securitySettings {
		switch a, b := s.get(prev), s.get(next); a := a.(type) {
		case string:
			if b := b.(string); a != b {
				changes = append(changes, fmt.Sprintf("%s %q → %q", s.name, a, b))
			}
		case []string:
			if b := b.([]string); !slices.Equal(a, b) {
				changes = append(changes, s.name+" "+listDiff(a, b))
			}
		}
	}
	if len(changes) > 0 {
		log.Printf("config: security settings changed: %s", strings.Join(changes, "; "))
	}
}

// listDiff says what b adds to a (+) and drops from it (-).
func listDiff(a, b []string) string {
	var diff []string
	for _, x := range b {
		if !slices.Contains(a, x) {
			diff = append(diff, "+"+x)
		}
	}
	for _, x := range a {
		if !slices.Contains(b, x) {
			diff = append(diff, "-"+x)
		}
	}
	if len(diff) == 0 {
		return "reordered"
	}
	return strings.Join(diff, " ")
}

// warnUnknownKeys logs top-level keys config.json has but the bridge does not
// know — usually a typo, or a setting from another version. They are dropped
// the next time the dashboard saves the file. Retired keys are known: they
// get a line of their own, see foldRetiredKeys.
func warnUnknownKeys(path string, data []byte) {
	var m map[string]json.RawMessage
	if json.Unmarshal(data, &m) != nil {
		return
	}
	known := map[string]bool{}
	t := reflect.TypeOf(Config{})
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		known[name] = true
	}
	for _, rk := range retiredKeys {
		known[rk.name] = true
	}
	var unknown []string
	for k := range m {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		log.Printf("config %s: ignoring unknown settings %v (they will be dropped when the dashboard saves)", path, unknown)
	}
}
