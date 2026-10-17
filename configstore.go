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
// configuration the bridge runs with (environment overrides, defaults and
// expanded paths applied). The dashboard edits the file layer; every component
// reads the effective layer through Get, which is a cheap atomic load, so a
// change applies on the next message without a restart.
type ConfigStore struct {
	path string
	env  envLookup // override variables: the .env next to path, then the process environment

	mu        sync.Mutex // serialises Update
	file      *Config
	eff       atomic.Pointer[Config]
	envLocked atomic.Pointer[map[string]string]
	etag      atomic.Value // string
	subs      []func(old, new *Config)
}

// ErrConfigConflict means config.json changed since the caller read it.
var ErrConfigConflict = errors.New("config changed since it was loaded — reload and try again")

// OpenConfigStore loads cfgPath. A missing file is not an error: it means an
// empty configuration with defaults, which is what a fresh install starts from.
func OpenConfigStore(cfgPath string) (*ConfigStore, error) {
	env := loadDotEnv(cfgPath)

	file := &Config{}
	var raw []byte
	if data, err := os.ReadFile(cfgPath); err == nil {
		raw = data
		if err := json.Unmarshal(data, file); err != nil {
			return nil, fmt.Errorf("parsing config file %q: %w", cfgPath, err)
		}
		warnUnknownKeys(cfgPath, data)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading config file %q: %w", cfgPath, err)
	}

	eff, locked, err := effective(file, env)
	if err != nil {
		return nil, err
	}
	cs := &ConfigStore{path: cfgPath, env: env, file: file}
	cs.eff.Store(eff)
	cs.envLocked.Store(&locked)
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

// EnvLocked returns json field → environment variable for every setting an
// environment variable overrides; editing those in config.json has no effect.
func (cs *ConfigStore) EnvLocked() map[string]string { return *cs.envLocked.Load() }

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
// skips the conflict check. It returns the settings that changed but only
// take effect after a restart.
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
	eff, locked, err := effective(next, cs.env)
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
	cs.envLocked.Store(&locked)
	cs.etag.Store(etagOf(data))
	subs := append([]func(old, new *Config){}, cs.subs...)
	cs.mu.Unlock()

	for _, fn := range subs {
		fn(old, eff)
	}
	return restartRequired(old, eff), nil
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

// warnUnknownKeys logs top-level keys config.json has but the bridge does not
// know — usually a typo, or a setting from another version. They are dropped
// the next time the dashboard saves the file.
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
