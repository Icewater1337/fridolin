package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
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
	path    string
	startup *Config // the effective configuration the process started with

	mu   sync.Mutex // serialises Update
	snap atomic.Pointer[ConfigSnapshot]

	// notifyMu is held while the subscribers hear of an update. commit
	// takes it before it gives back mu, so they hear of the snapshots in
	// the order they were swapped in, one at a time.
	notifyMu sync.Mutex
	subsMu   sync.Mutex
	subs     []func(old, new *Config)
}

// ConfigSnapshot is one version of the configuration: the file layer, the
// effective configuration made of it, and the etag of the config.json both
// came from. The store swaps in a whole snapshot at a time, so whoever reads
// one gets the three together, never one write's file with another's etag.
// A snapshot is never changed: treat it and what it points to as read-only.
type ConfigSnapshot struct {
	File      *Config
	Effective *Config
	ETag      string
	// Version counts the snapshots of this store: the one it opened with is
	// 1, and each one swapped in after it is one more.
	Version uint64
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

	file := &Config{Version: currentConfigVersion}
	var raw []byte
	if data, err := os.ReadFile(cfgPath); err == nil {
		raw = data
		// An older version is migrated in memory only, and the next save
		// writes it as this one. A migration logs what it changed.
		migrated, notes, err := migrateConfigJSON(data)
		if errors.Is(err, errConfigTooNew) {
			return nil, fmt.Errorf("config file %q: %w. Run that newer version again, or put back a config.json from before it (see docs/CONFIGURATION.md#version)", cfgPath, err)
		}
		if err != nil {
			return nil, fmt.Errorf("parsing config file %q: %w", cfgPath, err)
		}
		if err := json.Unmarshal(migrated, file); err != nil {
			return nil, fmt.Errorf("parsing config file %q: %w", cfgPath, err)
		}
		for _, note := range notes {
			log.Printf("config %s: %s", cfgPath, note)
		}
		warnUnknownKeys(cfgPath, migrated)
		warnOddChats(cfgPath, file)
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
	warnGroupsWithoutMembers(nil, eff)
	cs := &ConfigStore{path: cfgPath, startup: eff}
	cs.snap.Store(&ConfigSnapshot{File: file, Effective: eff, ETag: etagOf(raw), Version: 1})
	return cs, nil
}

// Path is the config file this store reads and writes.
func (cs *ConfigStore) Path() string { return cs.path }

// Startup returns the effective configuration the process started with:
// what the settings that need a restart still are.
func (cs *ConfigStore) Startup() *Config { return cs.startup }

// Get returns the effective configuration. Treat it as read-only: it is
// shared by every goroutine until the next Update replaces it.
func (cs *ConfigStore) Get() *Config { return cs.snap.Load().Effective }

// ETag identifies the current file contents for optimistic concurrency.
func (cs *ConfigStore) ETag() string { return cs.snap.Load().ETag }

// Snapshot returns the current configuration, the file layer, the effective
// one and the etag together. Read-only, like Get.
func (cs *ConfigStore) Snapshot() ConfigSnapshot { return *cs.snap.Load() }

// FileCopy returns a deep copy of the file layer for editing.
func (cs *ConfigStore) FileCopy() *Config { return mustClone(cs.snap.Load().File) }

// mustClone returns a deep copy of c, a configuration the store holds.
func mustClone(c *Config) *Config {
	out, err := c.clone()
	if err != nil {
		// Unreachable: what the store holds always round-trips through JSON.
		panic(err)
	}
	return out
}

// Subscribe registers fn to run after every successful Update, with the old
// and new effective configurations. The subscribers hear of the updates in
// the order they were made, and of one at a time, so fn must not call
// Update: that waits for fn to return.
func (cs *ConfigStore) Subscribe(fn func(old, new *Config)) {
	cs.subsMu.Lock()
	defer cs.subsMu.Unlock()
	cs.subs = append(cs.subs, fn)
}

// Update applies mutate to a copy of the file layer, validates the result,
// writes it atomically and swaps in the new effective configuration. etag ""
// skips the conflict check. It returns RestartRequired as of the new
// configuration.
func (cs *ConfigStore) Update(etag string, mutate func(*Config) error) (restart []string, err error) {
	res, err := cs.UpdateTx(etag, func(c *Config, _ *Tx) error { return mutate(c) })
	return res.Restart, err
}

// UpdateResult is what an update saved.
type UpdateResult struct {
	// Restart is RestartRequired as of the new configuration.
	Restart []string
}

// UpdateTx is Update for an edit that writes brain.json files too: mutate
// edits the file layer and stages those in tx, which UpdateTx writes only
// once the new configuration has passed its checks (see Tx).
func (cs *ConfigStore) UpdateTx(etag string, mutate func(c *Config, tx *Tx) error) (UpdateResult, error) {
	prev, next, err := cs.commit(etag, mutate)
	if err != nil {
		return UpdateResult{}, err
	}
	cs.notify(prev, next)
	return UpdateResult{Restart: restartRequired(cs.startup, next.Effective)}, nil
}

// notify tells the subscribers that next replaced prev, and gives back
// notifyMu, which commit took. A subscriber that panics is logged: the
// update is saved by then, and the other subscribers must still hear of it.
func (cs *ConfigStore) notify(prev, next *ConfigSnapshot) {
	defer cs.notifyMu.Unlock()
	warnGroupsWithoutMembers(prev.Effective, next.Effective)
	cs.subsMu.Lock()
	subs := slices.Clone(cs.subs)
	cs.subsMu.Unlock()
	for _, fn := range subs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("config: a subscriber panicked on the saved update: %v\n%s", r, debug.Stack())
				}
			}()
			fn(prev.Effective, next.Effective)
		}()
	}
}

// commit is the part of Update that holds the lock: it writes the new
// configuration and swaps it in, and returns its snapshot with the one it
// replaced. Once it has swapped one in, it returns with notifyMu held, for
// notify to give back. A panic on the way, in mutate or in deriving,
// encoding or writing the configuration, comes back as an error, with the
// lock given back and the configuration as it was: net/http recovers a
// handler's panic and the bridge runs on, and a lock left held would stop
// every later save, brain edit and calendar sign-in for good.
func (cs *ConfigStore) commit(etag string, mutate func(*Config, *Tx) error) (prev, next *ConfigSnapshot, err error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("config: an update panicked, nothing was changed: %v\n%s", r, debug.Stack())
			prev, next, err = nil, nil, fmt.Errorf("the config update failed: %v", r)
		}
	}()
	prev = cs.snap.Load()
	if etag != "" && etag != prev.ETag {
		return nil, nil, ErrConfigConflict
	}
	file, err := prev.File.clone()
	if err != nil {
		return nil, nil, err
	}
	tx := &Tx{}
	if err := mutate(file, tx); err != nil {
		return nil, nil, err
	}
	// What is written is this version's file, whatever the edit made of it.
	file.Version = currentConfigVersion
	eff, err := effective(file)
	if err != nil {
		return nil, nil, err
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	data = append(data, '\n')

	// The brain.json files go first and config.json last, which is what
	// makes the edit count. Until it is written, a failure, or a panic,
	// puts back what they held.
	committed := false
	defer func() {
		if !committed {
			tx.rollback()
		}
	}()
	if err := tx.write(); err != nil {
		return nil, nil, err
	}
	if err := writeFileAtomic(cs.path, data, 0o600, true); err != nil {
		return nil, nil, err
	}
	committed = true

	next = &ConfigSnapshot{File: file, Effective: eff, ETag: etagOf(data), Version: prev.Version + 1}
	cs.snap.Store(next)
	cs.notifyMu.Lock()
	return prev, next, nil
}

// Tx holds the brain.json files an update writes besides config.json.
// mutate only stages them. The update writes them once the new
// configuration has passed its checks, each one atomically, and then
// config.json. When a write fails after the first, config.json's
// included, it puts back what each file it wrote held before, or removes
// one that was not there: a refused or failed save never leaves a
// brain.json changed that config.json does not approve, which the
// dashboard would show as changed on disk.
type Tx struct {
	writes []*stagedManifest
}

// stagedManifest is a brain.json a transaction writes.
type stagedManifest struct {
	dir  string
	data []byte
	// create is for a new brain's folder, which must have no brain.json.
	create bool

	// written says the file is written; existed and prev say whether it
	// was there before and what it held.
	written bool
	existed bool
	prev    []byte
}

// WriteManifest stages m as the brain.json of the folder dir. m is checked
// and encoded at once, so an invalid one fails the update before anything
// is written. A later write for the same folder replaces this one.
func (tx *Tx) WriteManifest(dir string, m *BrainManifest) error {
	return tx.stage(dir, m, false)
}

// CreateManifest stages m as the brain.json of dir, a new brain's folder.
// The update fails if the folder has one by the time it is written.
func (tx *Tx) CreateManifest(dir string, m *BrainManifest) error {
	return tx.stage(dir, m, true)
}

func (tx *Tx) stage(dir string, m *BrainManifest, create bool) error {
	data, err := encodeManifest(m)
	if err != nil {
		return err
	}
	dir = filepath.Clean(dir)
	for _, w := range tx.writes {
		if w.dir == dir {
			w.data, w.create = data, create
			return nil
		}
	}
	tx.writes = append(tx.writes, &stagedManifest{dir: dir, data: data, create: create})
	return nil
}

// write writes the staged files in the order they were staged, and stops
// at the first that fails. Each one's contents are read first, the way
// the bridge reads a brain's files (readBrainFile), so they can be put
// back: one that is no regular file, such as a symlink the brain put
// there, could not be, and is not replaced.
func (tx *Tx) write() error {
	for _, w := range tx.writes {
		path := filepath.Join(w.dir, ManifestFile)
		prev, err := readBrainFile(w.dir, ManifestFile)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			w.existed, w.prev = false, nil
		case err != nil:
			return fmt.Errorf("%s: %w", path, err)
		default:
			w.existed, w.prev = true, prev
		}
		if err := writeManifestData(w.dir, w.data, !w.create); err != nil {
			return err
		}
		w.written = true
	}
	return nil
}

// rollback puts back what the files write wrote held before, the last
// one first. It logs what it cannot put back: the dashboard then shows
// that brain's brain.json as changed on disk.
func (tx *Tx) rollback() {
	for i := len(tx.writes) - 1; i >= 0; i-- {
		w := tx.writes[i]
		if !w.written {
			continue
		}
		path := filepath.Join(w.dir, ManifestFile)
		var err error
		if w.existed {
			err = writeFileAtomic(path, w.prev, 0o600, false)
		} else {
			err = os.Remove(path)
		}
		if err != nil {
			log.Printf("config: putting back %s after the update failed: %v", path, err)
		}
		w.written = false
	}
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
// without a word. It is migrated like config.json at startup: one without
// a version, from a dashboard tab opened before an upgrade, keeps a model
// it still sends and cannot bring inline brains back, and one from a newer
// version is refused.
func decodeConfigReplacement(data []byte) (*Config, error) {
	if len(bytes.TrimSpace(data)) == 0 || string(bytes.TrimSpace(data)) == "null" {
		return nil, errors.New("config required: send the whole config GET /api/config returns, with your changes")
	}
	migrated, _, err := migrateConfigJSON(data)
	if err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(migrated))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	if missing := missingKeys(migrated, reflect.TypeOf(c), ""); len(missing) > 0 {
		return nil, fmt.Errorf("the config leaves out %s: send the whole config GET /api/config returns, with your changes", strings.Join(missing, ", "))
	}
	return &c, nil
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
	{"group_members", func(c *Config) any { return groupMemberPairs(c.GroupMembers) }},
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
	{"script_api.tokenless_send", func(c *Config) any { return fmt.Sprint(c.TokenlessSend()) }},
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
// the next time the dashboard saves the file. data is the file migrated to
// the current version: the retired keys of a version 1 file are gone from
// it, and got a line of their own (see foldRetiredKeys).
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
