package config

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
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/jsonx"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// Store holds the configuration in two layers: what is written in
// config.json (the "file" layer, ~ kept as typed) and the effective
// configuration the bridge runs with (defaults and expanded paths applied).
// The dashboard edits the file layer; every component reads the effective
// layer through Get, which is a cheap atomic load, so a change applies on
// the next message without a restart.
type Store struct {
	path        string
	boot        Boot
	paths       Paths
	goos        string
	startup     *Settings                       // the effective configuration the process started with
	checkBrains func(old, next *Settings) error // command-owned brain-set validation

	mu   sync.Mutex // serialises Update
	snap atomic.Pointer[Snapshot]

	// notifyMu is held while the subscribers hear of an update. commit
	// takes it before it gives back mu, so they hear of the snapshots in
	// the order they were swapped in, one at a time.
	notifyMu sync.Mutex
	subsMu   sync.Mutex
	subs     []func(old, new *Settings)
}

// Snapshot is one version of the configuration: the file layer, the
// effective configuration made of it, and the etag of the config.json both
// came from. The store swaps in a whole snapshot at a time, so whoever reads
// one gets the three together, never one write's file with another's etag.
// A snapshot is never changed: treat it and what it points to as read-only.
type Snapshot struct {
	File      *File
	Effective *File
	Settings  *Settings
	ETag      string
	// Version counts the snapshots of this store: the one it opened with is
	// 1, and each one swapped in after it is one more.
	Version uint64
}

// ErrConflict means config.json changed since the caller read it.
var ErrConflict = errors.New("config changed since it was loaded — reload and try again")

// Open loads cfgPath. A missing file is not an error: it means an empty
// configuration with defaults, which is what a fresh install starts from.
// Options.CheckBrains validates registry edits under the commit lock before any write.
// A nil callback permits reads and settings updates, but refuses brain edits.
func Open(cfgPath string, options Options) (*Store, error) {
	// BRAIN_MODE switched classic mode on as "brain_mode": false did, and
	// is refused like it (see retiredBrainMode). It is no setting: the
	// environment only gets this check, so a bridge that ran classic mode
	// from it does not quietly start without a brain for its chats.
	if v := options.getenv("BRAIN_MODE"); v != "" && !IsTruthy(v) {
		return nil, ClassicModeRemoved("BRAIN_MODE="+v, "Unset BRAIN_MODE in the bridge's environment")
	}
	warnDotEnvNotRead(cfgPath)

	file := &File{Version: Version}
	var raw []byte
	if data, err := os.ReadFile(cfgPath); err == nil {
		raw = data
		if file, err = decodeConfigFile(cfgPath, data); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading config file %q: %w", cfgPath, err)
	}

	eff, err := resolveFile(file, options.Paths, options.GOOS)
	if err != nil {
		return nil, err
	}
	if file.DataDir == "" {
		warnLegacyDataDir(eff.DataDir, options.Paths.Home)
	}
	settings := settingsOf(eff, options.Paths)
	WarnGroupsWithoutMembers(nil, settings)
	cs := &Store{path: cfgPath, startup: settings, boot: bootOf(eff, options.Paths), paths: options.Paths, goos: options.GOOS, checkBrains: options.CheckBrains}
	cs.snap.Store(&Snapshot{File: file, Effective: eff, Settings: settingsOf(eff, cs.paths), ETag: etagOf(raw), Version: 1})
	return cs, nil
}

// decodeConfigFile decodes data, what the config file path holds, into the
// file layer, and logs what is odd about it: retired keys, unknown ones,
// chats no entry can match.
func decodeConfigFile(path string, data []byte) (*File, error) {
	// Migrate in memory on startup and external edits; the next save writes
	// this version while the snapshot ETag still identifies the original bytes.
	migrated, notes, err := MigrateJSON(data)
	if errors.Is(err, ErrTooNew) {
		return nil, fmt.Errorf("config file %q: %w. Run that newer version again, or put back a config.json from before it (see docs/CONFIGURATION.md#version)", path, err)
	}
	if err != nil {
		return nil, fmt.Errorf("parsing config file %q: %w", path, err)
	}
	file := &File{Version: Version}
	if err := json.Unmarshal(migrated, file); err != nil {
		return nil, fmt.Errorf("parsing config file %q: %w", path, JSONError(err))
	}
	for _, note := range notes {
		log.Printf("config %s: %s", path, note)
	}
	warnUnknownKeys(path, migrated)
	WarnOddChats(path, file)
	return file, nil
}

// Path is the config file this store reads and writes.
func (cs *Store) Path() string { return cs.path }

// Startup returns the effective configuration the process started with:
// what the settings that need a restart still are.
func (cs *Store) Startup() *Settings { return cs.startup }

// Get returns resolved runtime settings from the atomic configuration snapshot.
// Treat it and its slices and maps as read-only.
func (cs *Store) Get() *Settings { return cs.snap.Load().Settings }

// ETag identifies the current file contents for optimistic concurrency.
func (cs *Store) ETag() string { return cs.snap.Load().ETag }

// Snapshot returns the current configuration, the file layer, the effective
// one and the etag together. Read-only, like Get.
func (cs *Store) Snapshot() Snapshot { return *cs.snap.Load() }

// FileCopy returns a deep copy of the file layer for editing.
func (cs *Store) FileCopy() *File { return MustClone(cs.snap.Load().File) }

// MustClone returns a deep copy of c, a configuration the store holds.
func MustClone(c *File) *File {
	out, err := c.Clone()
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
func (cs *Store) Subscribe(fn func(old, new *Settings)) {
	cs.subsMu.Lock()
	defer cs.subsMu.Unlock()
	cs.subs = append(cs.subs, fn)
}

// Update applies mutate to a copy of the file layer, validates the result,
// writes it atomically and swaps in the new effective configuration. etag ""
// skips the conflict check. It returns RestartRequired as of the new
// configuration. UpdateTx says what happens when config.json was changed
// by hand.
func (cs *Store) Update(etag string, mutate func(*File) error) (restart []string, err error) {
	res, err := cs.UpdateTx(etag, func(c *File, _ *Tx) error { return mutate(c) })
	return res.Restart, err
}

// UpdateResult is what an update saved.
type UpdateResult struct {
	// Restart is RestartRequired as of the new configuration.
	Restart []string
	// ETag is the etag of the config.json the update wrote. ETag() may
	// already be a later update's.
	ETag string
}

// UpdateTx is Update for an edit that writes brain.json files too: mutate
// edits the file layer and stages those in tx, which UpdateTx writes only
// once the new configuration has passed its checks (see Tx).
//
// It reads config.json first. When the file changed since the store last
// read or wrote it, by hand or by a restore, that is the configuration
// from then on, decoded as at startup, and the update starts from it: with
// etag "" mutate edits the file as it is now, so the change by hand
// stays; with an etag the update is refused with ErrConflict, since
// the caller sends a whole configuration made from the one before.
func (cs *Store) UpdateTx(etag string, mutate func(c *File, tx *Tx) error) (UpdateResult, error) {
	prev, next, err := cs.commit(etag, mutate)
	if next != nil {
		cs.notify(prev, next)
	}
	if err != nil {
		return UpdateResult{}, err
	}
	return UpdateResult{Restart: restartRequired(cs.startup, next.Settings), ETag: next.ETag}, nil
}

// notify tells the subscribers that next replaced prev, and gives back
// notifyMu, which commit took. A subscriber that panics is logged: the
// update is saved by then, and the other subscribers must still hear of it.
func (cs *Store) notify(prev, next *Snapshot) {
	defer cs.notifyMu.Unlock()
	WarnGroupsWithoutMembers(prev.Settings, next.Settings)
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
			fn(prev.Settings, next.Settings)
		}()
	}
}

// commit is the part of Update that holds the lock: it writes the new
// configuration and swaps it in. It returns the snapshot it found, and the
// one it swapped in last, nil if none: a config.json changed by hand is
// swapped in even when the update is then refused. With one swapped in it
// returns with notifyMu held, for notify to give back. A panic on the way,
// in mutate or in deriving, encoding or writing the configuration, comes
// back as an error, with the lock given back and nothing of the update
// saved: net/http recovers a handler's panic and the bridge runs on, and a
// lock left held would stop every later save, brain edit and calendar
// sign-in for good.
func (cs *Store) commit(etag string, mutate func(*File, *Tx) error) (prev, next *Snapshot, err error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	// Before the unlock: the subscribers hear of this snapshot before the
	// next update can swap in another.
	defer func() {
		if next != nil {
			cs.notifyMu.Lock()
		}
	}()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("config: an update panicked and was not saved: %v\n%s", r, debug.Stack())
			err = fmt.Errorf("the config update failed: %v", r)
		}
	}()
	prev = cs.snap.Load()
	base, err := cs.reread(prev)
	if err != nil {
		return prev, nil, err
	}
	if base != prev {
		cs.snap.Store(base)
		next = base
	}
	if etag != "" && etag != base.ETag {
		return prev, next, ErrConflict
	}
	file, err := base.File.Clone()
	if err != nil {
		return prev, next, err
	}
	tx := &Tx{}
	defer tx.close()
	if err := mutate(file, tx); err != nil {
		return prev, next, err
	}
	// What is written is this version's file, whatever the edit made of it.
	file.Version = Version
	eff, err := cs.resolveFile(file)
	if err != nil {
		return prev, next, err
	}
	if tx.checkBrains {
		if cs.checkBrains == nil {
			return prev, next, errors.New("config: brain-set validation is not configured")
		}
		if err := cs.checkBrains(base.Settings, settingsOf(eff, cs.paths)); err != nil {
			return prev, next, err
		}
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return prev, next, err
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
		return prev, next, err
	}
	if err := fsx.WriteFileAtomic(cs.path, data, 0o600, true); err != nil {
		return prev, next, err
	}
	committed = true

	saved := &Snapshot{File: file, Effective: eff, Settings: settingsOf(eff, cs.paths), ETag: etagOf(data), Version: base.Version + 1}
	cs.snap.Store(saved)
	next = saved
	return prev, next, nil
}

// reread returns the snapshot of config.json as it is now. That is cur
// unless the file changed since the store last read or wrote it; a
// changed file is decoded and derived as at startup, and one that does
// not load is an error, so that no update writes over it. A file that is
// gone is written again by the update, from the configuration that runs.
func (cs *Store) reread(cur *Snapshot) (*Snapshot, error) {
	data, err := os.ReadFile(cs.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return cur, nil
	case err != nil:
		return nil, fmt.Errorf("reading config file %q: %w", cs.path, err)
	}
	etag := etagOf(data)
	if etag == cur.ETag {
		return cur, nil
	}
	file, err := decodeConfigFile(cs.path, data)
	if err == nil {
		var eff *File
		if eff, err = cs.resolveFile(file); err == nil {
			log.Printf("config: %s changed on disk since the bridge read it, and applies from now on", cs.path)
			return &Snapshot{File: file, Effective: eff, Settings: settingsOf(eff, cs.paths), ETag: etag, Version: cur.Version + 1}, nil
		}
	}
	return nil, fmt.Errorf("%s changed on disk and does not load, so nothing was saved; fix the file (fridolin check says what is wrong): %w", cs.path, err)
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
	// files are the brains' other files it writes or removes, after the
	// brain.json files (brainfiles.go).
	files []*stagedFile
	// checkBrains has the update check the brain set, see CheckBrains.
	checkBrains bool
}

// CheckBrains has the update refused when it makes brains clash, as
// checkSet decides, once mutate is done and before anything is written.
// Staging a brain.json asks for it too: it changes a brain.
func (tx *Tx) CheckBrains() { tx.checkBrains = true }

// stagedManifest is a brain.json a transaction writes.
type stagedManifest struct {
	dir  string
	data []byte
	root *os.Root
	file *os.File
	info fs.FileInfo
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
func (tx *Tx) WriteManifest(dir string, m *manifest.Manifest) error {
	return tx.stage(dir, m, false)
}

// CreateManifest stages m as the brain.json of dir, a new brain's folder.
// The update fails if the folder has one by the time it is written.
func (tx *Tx) CreateManifest(dir string, m *manifest.Manifest) error {
	return tx.stage(dir, m, true)
}

func (tx *Tx) stage(dir string, m *manifest.Manifest, create bool) error {
	data, err := manifest.Encode(m)
	if err != nil {
		return err
	}
	tx.checkBrains = true
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
	if err := tx.writeManifests(); err != nil {
		return err
	}
	return tx.writeFiles()
}

func (tx *Tx) writeManifests() error {
	for _, w := range tx.writes {
		path := filepath.Join(w.dir, manifest.File)
		root, err := manifest.OpenRoot(w.dir, !w.create)
		if err != nil {
			return err
		}
		w.root = root
		prev, err := fsx.ReadBrainFileRoot(root, manifest.File)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			w.existed, w.prev = false, nil
		case err != nil:
			return fmt.Errorf("%s: %w", path, err)
		default:
			w.existed, w.prev = true, prev
		}
		file, info, err := manifest.WriteDataRoot(root, w.data, !w.create)
		if err != nil {
			return err
		}
		w.file, w.info, w.written = file, info, true
	}
	return nil
}

// rollback puts back what the files write wrote held before, the last
// one first. It logs what it cannot put back: the dashboard then shows
// that brain's brain.json as changed on disk.
func (tx *Tx) rollback() {
	tx.rollbackFiles()
	for i := len(tx.writes) - 1; i >= 0; i-- {
		w := tx.writes[i]
		if !w.written {
			continue
		}
		w.written = false
		path := filepath.Join(w.dir, manifest.File)
		info, err := w.root.Lstat(manifest.File)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err == nil && (!os.SameFile(w.info, info) || w.info.Mode() != info.Mode() ||
			w.info.Size() != info.Size() || !w.info.ModTime().Equal(info.ModTime())) {
			continue // The current entry belongs to another writer.
		}
		if err == nil {
			var current []byte
			current, err = fsx.ReadBrainFileRoot(w.root, manifest.File)
			if err == nil && !bytes.Equal(current, w.data) {
				continue
			}
		}
		// This comparison and mutation are not an atomic compare-and-unlink.
		// The retained root still confines the operation if its path moves.
		if err == nil {
			if w.existed {
				var restored *os.File
				restored, _, err = manifest.WriteDataRoot(w.root, w.prev, true)
				if restored != nil {
					restored.Close()
				}
			} else {
				err = w.root.Remove(manifest.File)
			}
		}
		if err != nil {
			log.Printf("config: putting back %s after the update failed: %v", path, err)
		}
	}
}

// close releases every retained handle after commit, rollback or an error.
func (tx *Tx) close() {
	tx.closeFiles()
	for _, w := range tx.writes {
		if w.file != nil {
			w.file.Close()
			w.file = nil
		}
		if w.root != nil {
			w.root.Close()
			w.root = nil
		}
	}
}

// RestartRequired lists the settings that are read once at startup and now
// differ from the value the bridge started with, so they wait for a
// restart. A setting changed and then changed back is not listed.
func (cs *Store) RestartRequired() []string {
	return restartRequired(cs.startup, cs.Get())
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
	path := DotEnvPath(cfgPath)
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
func warnLegacyDataDir(dataDir, home string) {
	if home == "" || dataDir != filepath.Join(home, LegacyDataDirName) {
		return
	}
	log.Printf("warning: data dir %s has the name from before the rename and is used because %s does not exist; set the data folder to it (Settings → Advanced) to keep it once the bridge no longer looks for that name", dataDir, filepath.Join(home, DataDirName))
}

// DecodeReplacement decodes data, a whole configuration that is to
// replace the file layer (PUT /api/config). It is strict. A key the bridge
// does not know is an error, not a setting dropped on save. So is leaving
// out a key that config.json always has, one without omitempty, at the top
// or in a section: the replacement would reset it to its default, and a
// partial body would turn the sandbox off and empty allowed_numbers
// without a word. It is migrated like config.json at startup: one without
// a version, from a dashboard tab opened before an upgrade, keeps a model
// it still sends and cannot bring inline brains back, and one from a newer
// version is refused.
func DecodeReplacement(data []byte) (*File, error) {
	if len(bytes.TrimSpace(data)) == 0 || string(bytes.TrimSpace(data)) == "null" {
		return nil, errors.New("config required: send the whole config GET /api/config returns, with your changes")
	}
	migrated, _, err := MigrateJSON(data)
	if err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(migrated))
	dec.DisallowUnknownFields()
	var c File
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("invalid config: %w", JSONError(err))
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
			// A key Go leaves out when it is empty (omitempty) or zero
			// (omitzero) may be left out here too.
			if o := strings.Split(opts, ","); !slices.Contains(o, "omitempty") && !slices.Contains(o, "omitzero") {
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
// what brains may reach. LogSecurityChanges logs every change to them.
var securitySettings = []struct {
	name string
	get  func(*Settings) any // a string, or a []string for a list
}{
	{"allowed_numbers", func(c *Settings) any { return c.AllowedNumbers }},
	{"allow_any_dm", func(c *Settings) any { return fmt.Sprint(c.AllowAnyDM) }},
	{"allowed_groups", func(c *Settings) any { return c.AllowedGroups }},
	{"group_members", func(c *Settings) any { return GroupMemberPairs(c.GroupMembers) }},
	{"dangerously_skip_permissions", func(c *Settings) any { return fmt.Sprint(c.DangerouslySkipPermissions) }},
	{"inherit_claude_user_config", func(c *Settings) any { return fmt.Sprint(c.InheritClaudeUserConfig) }},
	{"child_env_passthrough", func(c *Settings) any { return c.ChildEnvPassthrough }},
	{"sandbox.enabled", func(c *Settings) any { return fmt.Sprint(c.Sandbox.Enabled) }},
	{"sandbox.deny_paths", func(c *Settings) any { return c.Sandbox.DenyPaths }},
	{"sandbox.allow_paths", func(c *Settings) any { return c.Sandbox.AllowPaths }},
	{"sandbox.git_config", func(c *Settings) any { return c.Sandbox.GitConfig }},
	{"sandbox.credentials_dir", func(c *Settings) any { return c.Sandbox.CredentialsDir }},
	{"dashboard.require_token", func(c *Settings) any { return fmt.Sprint(c.Dashboard.RequireToken) }},
	{"ingest.enabled", func(c *Settings) any { return fmt.Sprint(c.Ingest.Enabled) }},
	{"script_api.tokenless_send", func(c *Settings) any { return fmt.Sprint(c.ScriptAPI.TokenlessSend) }},
	{"core.tailnet.enabled", func(c *Settings) any { return fmt.Sprint(c.Core.Tailnet.Enabled) }},
	{"nodes.enabled", func(c *Settings) any { return fmt.Sprint(c.Nodes.Enabled) }},
	{"nodes.listen", func(c *Settings) any { return c.Nodes.Listen }},
	{"nodes.git_identities", func(c *Settings) any { return GitIdentityPairs(c.Nodes.GitIdentities) }},
	{"http_listen", func(c *Settings) any { return c.HTTPListen }},
	{"data_dir", func(c *Settings) any { return c.DataDir }},
	{"db_path", func(c *Settings) any { return c.DBPath }},
	{"connections.openai.brains", func(c *Settings) any { return c.Connections.OpenAI.Brains }},
	{"connections.elevenlabs.brains", func(c *Settings) any { return c.Connections.ElevenLabs.Brains }},
}

// LogSecurityChanges logs, in one line, how an update changed the security
// settings, so the log shows afterwards when the sandbox went off or who
// was let in. A list shows what was added (+) and removed (-).
func LogSecurityChanges(prev, next *Settings) {
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

// warnUnknownKeys logs keys the bridge does not know at any depth, by
// their path. data is migrated to the current version: retired keys from
// version 1 are already removed and logged by the migration; the same keys
// in a version 2 file are unknown settings.
func warnUnknownKeys(path string, data []byte) {
	if unknown := jsonx.UnknownKeys(data, reflect.TypeFor[File](), ""); len(unknown) > 0 {
		log.Printf("config %s: ignoring unknown settings %v (they will be dropped when the dashboard saves)", path, unknown)
	}
}

// Paths returns the process inputs captured when the store opened.
func (cs *Store) Paths() Paths { return cs.paths }

// Resolve derives values using this store's captured process inputs.
func (cs *Store) resolveFile(file *File) (*File, error) { return resolveFile(file, cs.paths, cs.goos) }

// Boot returns the immutable process-start values.
func (cs *Store) Boot() Boot { return cs.boot }

// Resolve validates a proposed file and derives typed runtime values.
func (cs *Store) Resolve(file *File) (*Settings, error) { return Resolve(file, cs.paths, cs.goos) }
