package coreexport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"filippo.io/age"
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

const nativeConfig = `{
  "data_dir": "~/.fridolin",
  "claude_path": "~/private/claude-wrapper.sh",
  "codex_path": "codex",
  "agent_provider": "codex",
  "brain_dirs": [{"path": "~/Personal", "approved": {"id": "personal"}}],
  "sandbox": {"enabled": true, "credentials_dir": "~/.secrets", "allow_paths": ["~/shared"]},
  "plaud": {"enabled": true, "cli_path": "/Users/you/.nvm/versions/node/v24/bin/plaud", "brain_access": true},
  "backup": {"enabled": true, "root": "fridolin-backup"},
  "unknown_setting": {"kept": 1}
}`

func TestFixupsOfAMoveFromANativeInstall(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "config.json", nativeConfig, 0o600)
	notes, err := ResetProgramPaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 {
		t.Errorf("notes %q", notes)
	}
	notes, err = AddAllowPaths(dir, []string{"~/private", "~/shared"})
	if err != nil || len(notes) != 1 || !strings.Contains(notes[0], "~/private") {
		t.Fatalf("allow paths: %q %v", notes, err)
	}
	c := readJSON(t, filepath.Join(dir, "config.json"))
	if _, ok := c["claude_path"]; ok {
		t.Error("claude_path stayed")
	}
	if c["codex_path"] != "codex" {
		t.Error("a bare program name was removed")
	}
	plaud := c["plaud"].(map[string]any)
	if _, ok := plaud["cli_path"]; ok || plaud["brain_access"] != true {
		t.Errorf("plaud %v", plaud)
	}
	sb := c["sandbox"].(map[string]any)
	if got := sb["allow_paths"].([]any); len(got) != 2 || got[1] != "~/private" || sb["credentials_dir"] != "~/.secrets" {
		t.Errorf("sandbox %v", sb)
	}
	if c["unknown_setting"] == nil {
		t.Error("a setting this version does not know was dropped")
	}
	// Nothing to do the second time: the file stays as it is.
	before, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if notes, _ := ResetProgramPaths(dir); len(notes) != 0 {
		t.Errorf("second run: %q", notes)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "config.json")); string(after) != string(before) {
		t.Error("a run that changed nothing rewrote the file")
	}

	// The way back takes the Mac's values from the native config again.
	if _, err := FixDataDir(dir, ""); err != nil {
		t.Fatal(err)
	}
	notes, err = RestoreHostKeys(dir, []byte(nativeConfig))
	if err != nil {
		t.Fatal(err)
	}
	c = readJSON(t, filepath.Join(dir, "config.json"))
	if c["data_dir"] != "~/.fridolin" || c["claude_path"] != "~/private/claude-wrapper.sh" || c["plaud"].(map[string]any)["cli_path"] == nil {
		t.Errorf("not restored (%q): %v", notes, c)
	}
	if got := c["sandbox"].(map[string]any)["allow_paths"].([]any); len(got) != 1 {
		t.Errorf("allow_paths %v", got)
	}
}

func TestForRehearsal(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "config.json", nativeConfig, 0o600)
	if _, err := ForRehearsal(dir); err != nil {
		t.Fatal(err)
	}
	c := readJSON(t, filepath.Join(dir, "config.json"))
	if c["claude_path"] != "/usr/bin/false" || c["codex_path"] != "/usr/bin/false" {
		t.Errorf("agents %v %v", c["claude_path"], c["codex_path"])
	}
	if c["plaud"].(map[string]any)["enabled"] != false || c["backup"].(map[string]any)["enabled"] != false {
		t.Error("plaud or the backup stayed on")
	}
	if _, ok := c["vault"]; ok {
		t.Error("a section that was not there was added")
	}
}

func TestFixupsWithoutAConfig(t *testing.T) {
	if notes, err := ResetProgramPaths(t.TempDir()); err != nil || notes != nil {
		t.Errorf("%q %v", notes, err)
	}
}

// A native install's export carries media/ and its Codex state's SQLite
// databases, consistently, and leaves its own migration/ snapshots out.
func TestExportOfANativeInstall(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "config.json", nativeConfig, 0o600)
	write(t, dir, "media/plaud/rec.m4a", "audio", 0o600)
	write(t, dir, "migration/bridge.db.pre-move", "old", 0o600)
	write(t, dir, "migration/old/bridge.db", "not a database", 0o600)
	if err := os.MkdirAll(filepath.Join(dir, "brains", "personal", "codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	liveDB(t, filepath.Join(dir, "brains", "personal", "codex", "state_5.sqlite"), "codex-thread")
	write(t, dir, "brains/personal/codex/auth.json", "{}", 0o600)
	st := newMemStore()
	id := identity(t)
	res, err := Export(context.Background(), dir, []age.Recipient{id.Recipient()}, st, ExportOptions{
		DB: sqliteDB{}, IncludeMedia: true, TempDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.Manifest.Databases, "brains/personal/codex/state_5.sqlite") {
		t.Errorf("databases %q", res.Manifest.Databases)
	}
	if _, err := os.Stat(filepath.Join(dir, "core")); err == nil {
		t.Error("the export wrote core/ into the data dir it read")
	}
	staging := filepath.Join(t.TempDir(), "s")
	if _, err := Import(context.Background(), st, []age.Identity{id}, staging, ImportOptions{DB: sqliteDB{}}); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]bool{"media/plaud/rec.m4a": true, "brains/personal/codex/auth.json": true, "migration": false} {
		if _, err := os.Stat(filepath.Join(staging, rel)); (err == nil) != want {
			t.Errorf("%s in the import: %v, want %v", rel, err == nil, want)
		}
	}
}

func TestActivateWithAsideAndKeep(t *testing.T) {
	noLock := func(string) (func(), error) { return func() {}, nil }
	target := t.TempDir()
	write(t, target, "bridge.db", "old", 0o600)
	write(t, target, "migration/earlier/x", "kept", 0o600)
	staged := filepath.Join(target, "migration", "now", "staged")
	write(t, staged, "bridge.db", "new", 0o600)
	aside := filepath.Join(target, "migration", "now", "replaced")
	if err := ActivateWith(staged, target, ActivateOptions{Force: true, Now: t0, Lock: noLock, Aside: aside, Keep: []string{"migration"}}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(aside, "bridge.db")); string(data) != "old" {
		t.Error("the old bridge.db is not aside")
	}
	if data, _ := os.ReadFile(filepath.Join(target, "bridge.db")); string(data) != "new" {
		t.Error("the new bridge.db is not in place")
	}
	if data, _ := os.ReadFile(filepath.Join(target, "migration", "earlier", "x")); string(data) != "kept" {
		t.Error("migration/ moved")
	}
}

func TestFixDataDirBrainObjects(t *testing.T) {
	dir := t.TempDir()
	present := t.TempDir()
	write(t, dir, "config.json", `{"data_dir": "/old", "brain_dirs": [{"path": "/nowhere/brain"}, {"path": "`+present+`"}, "/nowhere/other"]}`, 0o600)
	warnings, err := FixDataDir(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "/nowhere/brain") || !strings.Contains(joined, "/nowhere/other") || strings.Contains(joined, present) {
		t.Errorf("warnings %q", warnings)
	}
}
