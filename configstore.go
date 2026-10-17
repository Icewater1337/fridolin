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
	cs := &ConfigStore{path: cfgPath, file: file, startup: eff}
	cs.eff.Store(eff)
	cs.etag.Store(etagOf(raw))
	return cs, nil
}

// Path is the config file this store reads and writes.
func (cs *ConfigStore) Path() string { return cs.path }

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
}{
	{"http_listen", func(c *Config) string { return c.HTTPListen }},
	{"data_dir", func(c *Config) string { return c.DataDir }},
	{"db_path", func(c *Config) string { return c.DBPath }},
	{"media_dir", func(c *Config) string { return c.MediaDir }},
	{"log_level", func(c *Config) string { return c.LogLevel }},
	{"dashboard.disabled", func(c *Config) string { return fmt.Sprint(c.Dashboard.Disabled) }},
}

func restartRequired(old, new *Config) []string {
	var out []string
	for _, f := range restartFields {
		if f.get(old) != f.get(new) {
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
