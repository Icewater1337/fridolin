package coreexport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/Icewater1337/fridolin/internal/vault"
)

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// brainDirsOf reads brain_dirs of the config.json in dataDir.
func brainDirsOf(t *testing.T, dataDir string) []map[string]any {
	t.Helper()
	var cfg struct {
		BrainDirs []map[string]any `json:"brain_dirs"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dataDir, "config.json"))), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg.BrainDirs
}

// importInto imports the latest export of st into a new data dir with
// home as ~ and places the brain folders.
func importInto(t *testing.T, st Store, id age.Identity, home string, now time.Time) (string, []FolderPlacement) {
	t.Helper()
	target := t.TempDir()
	staging := filepath.Join(target, "core", "import", "1")
	res, err := Import(context.Background(), st, []age.Identity{id}, staging, ImportOptions{DB: sqliteDB{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := Activate(staging, target, false, now, func(string) (func(), error) { return func() {}, nil }); err != nil {
		t.Fatal(err)
	}
	placed, err := PlaceFolders(target, staging, home, res.Manifest, now)
	if err != nil {
		t.Fatal(err)
	}
	return target, placed
}

// TestBrainFolderMovesAwayAndHome: a brain folder bound in from the home
// machine goes along with the export as far as its backup settings let it,
// lands in the volume where it is not there, and comes home with what the
// brain changed, nothing at home deleted or lost.
func TestBrainFolderMovesAwayAndHome(t *testing.T) {
	homeMac := t.TempDir()
	write(t, homeMac, "Personal/brain.json", `{"id":"personal"}`, 0o644)
	write(t, homeMac, "Personal/notes.md", "old notes", 0o644)
	write(t, homeMac, "Personal/sub/keep.md", "keep", 0o644)
	write(t, homeMac, "Personal/settings.md", "settings v1", 0o644)
	write(t, homeMac, "Personal/plan.md", "plan v1", 0o644)
	write(t, homeMac, "Personal/work/contract.pdf", "work data", 0o600)
	write(t, homeMac, "Personal/node_modules/x/index.js", "x", 0o644)
	write(t, homeMac, "Personal/tool/venv/pyvenv.cfg", "home = /opt/homebrew", 0o644)
	write(t, homeMac, "Personal/tool/venv/bin/python", "mach-o", 0o755)
	if err := os.Symlink("/etc", filepath.Join(homeMac, "Personal", "etc-link")); err != nil {
		t.Fatal(err)
	}
	personal := filepath.Join(homeMac, "Personal")

	mac := t.TempDir()
	write(t, mac, "config.json", `{"data_dir": "/data", "brain_dirs": [{"path": "~/Personal", "approved": {"id": "personal"}}]}`, 0o600)
	st := newMemStore()
	id := identity(t)
	excluded := func(rel string) bool { return rel == "work" || strings.HasPrefix(rel, "work/") }
	exp := func(dataDir string, folders []Folder, now time.Time) Manifest {
		t.Helper()
		res, err := Export(context.Background(), dataDir, []age.Recipient{id.Recipient()}, st, ExportOptions{
			DB: sqliteDB{}, Version: "v1", Now: func() time.Time { return now }, Folders: folders,
		})
		if err != nil {
			t.Fatal(err)
		}
		return res.Manifest
	}
	var listed []string
	infos, err := ListFolders(mac, []Folder{{ID: "personal", Configured: "~/Personal", Dir: personal, Excluded: excluded}}, func(p string) { listed = append(listed, p) }, nil)
	slices.Sort(listed)
	if err != nil || !slices.Equal(listed, []string{"personal/brain.json", "personal/notes.md", "personal/plan.md", "personal/settings.md", "personal/sub/keep.md"}) || len(infos) != 1 || infos[0].Left != 4 {
		t.Fatalf("listed %v, %+v, %v", listed, infos, err)
	}
	m := exp(mac, []Folder{{ID: "personal", Configured: "~/Personal", Dir: personal, Excluded: excluded}}, t0)
	if m.Format != formatFolders || len(m.Folders) != 1 || m.Folders[0].Files != 5 || m.Folders[0].Left != 4 {
		t.Fatalf("manifest = %+v %+v", m, m.Folders)
	}

	// Away: on a machine without ~/Personal the folder goes into the volume.
	vm, placed := importInto(t, st, id, t.TempDir(), t0.Add(time.Minute))
	away := filepath.Join(vm, "brains-folders", "Personal")
	if len(placed) != 1 || placed[0].Home || placed[0].Dir != away || placed[0].Forget != "" {
		t.Fatalf("placed away = %+v", placed)
	}
	if _, err := os.Stat(filepath.Join(vm, "core", vault.ForgetNoteName)); err == nil {
		t.Error("the vault is told to forget a folder that just arrived")
	}
	if got := listFiles(t, away); !slices.Equal(got, []string{"brain.json", "notes.md", "plan.md", "settings.md", "sub/keep.md"}) {
		t.Errorf("in the volume: %v (work data, venvs, node_modules and links stay home)", got)
	}
	bd := brainDirsOf(t, vm)
	if bd[0]["path"] != away || bd[0]["moved_from"] != "~/Personal" {
		t.Errorf("brain_dirs away = %v", bd)
	}
	if _, err := os.Stat(filepath.Join(vm, "core", "import", "1", "core", "folders")); err == nil {
		// Import's caller removes the staging folder; PlaceFolders moved
		// the folder out of it.
		if left := listFiles(t, filepath.Join(vm, "core", "import", "1", "core", "folders")); len(left) != 0 {
			t.Errorf("left in staging: %v", left)
		}
	}

	// The brain works there: it changes a file, adds one, deletes one.
	write(t, away, "notes.md", "new notes", 0o644)
	write(t, away, "added.md", "added", 0o644)
	// What its backup settings keep out of the cloud stays on this
	// machine on the way back too (#195): work data, programs.
	write(t, away, "work/made-away.md", "work data made away", 0o600)
	write(t, away, "node_modules/y/index.js", "y", 0o644)
	if err := os.Remove(filepath.Join(away, "sub", "keep.md")); err != nil {
		t.Fatal(err)
	}
	// Meanwhile at home, a file only the Mac has, settings.md edited on
	// both sides (#182: the home edit must not be lost) and plan.md
	// edited at home only.
	write(t, homeMac, "Personal/mac-only.md", "mac", 0o644)
	write(t, away, "settings.md", "settings v2 away", 0o644)
	write(t, homeMac, "Personal/settings.md", "settings v2 home", 0o644)
	write(t, homeMac, "Personal/plan.md", "plan v2 home", 0o644)
	if _, err := os.Stat(filepath.Join(vm, "brains-folders", ".moved-Personal.json")); err != nil {
		t.Fatalf("no record of what the folder held when it moved: %v", err)
	}

	// Back: the folder lies in the data dir now, and travels with it.
	m2 := exp(vm, []Folder{{ID: "personal", Configured: away, Dir: away, Excluded: excluded, Visiting: true}}, t0.Add(2*time.Minute))
	if len(m2.Folders) != 0 || m2.Format != 1 {
		t.Errorf("the way back carries the folder in data/, not folders/: %+v", m2.Folders)
	}
	back, placed := importInto(t, st, id, homeMac, t0.Add(3*time.Minute))
	if len(placed) != 1 || !placed[0].Home || placed[0].Dir != personal || placed[0].Added != 1 || placed[0].Updated != 1 ||
		placed[0].Kept != 1 || !slices.Equal(placed[0].Conflicts, []string{"settings.md"}) {
		t.Fatalf("placed home = %+v", placed)
	}
	if got := readFile(t, filepath.Join(personal, "settings.md")); got != "settings v2 home" {
		t.Errorf("settings.md at home = %q (the home edit was lost)", got)
	}
	if got := readFile(t, filepath.Join(placed[0].Aside, "settings.md")); got != "settings v2 away" {
		t.Errorf("the other machine's settings.md aside = %q", got)
	}
	if got := readFile(t, filepath.Join(personal, "plan.md")); got != "plan v2 home" {
		t.Errorf("plan.md at home = %q", got)
	}
	if _, err := os.Stat(filepath.Join(back, "brains-folders", ".moved-Personal.json")); err == nil {
		t.Error("the record of the move stayed in brains-folders")
	}
	if got := readFile(t, filepath.Join(personal, "notes.md")); got != "new notes" {
		t.Errorf("notes.md = %q", got)
	}
	if got := readFile(t, filepath.Join(personal, "added.md")); got != "added" {
		t.Errorf("added.md = %q", got)
	}
	for _, stayed := range []string{"work/made-away.md", "node_modules/y/index.js"} {
		if _, err := os.Stat(filepath.Join(personal, filepath.FromSlash(stayed))); err == nil {
			t.Errorf("%s came home through the cloud, though the brain's backup settings leave it out", stayed)
		}
	}
	for _, kept := range []string{"sub/keep.md", "work/contract.pdf", "mac-only.md", "tool/venv/bin/python"} {
		if _, err := os.Stat(filepath.Join(personal, filepath.FromSlash(kept))); err != nil {
			t.Errorf("%s at home: %v (nothing at home is deleted)", kept, err)
		}
	}
	if got := readFile(t, filepath.Join(placed[0].Aside, "notes.md")); got != "old notes" {
		t.Errorf("the replaced notes.md = %q", got)
	}
	bd = brainDirsOf(t, back)
	if bd[0]["path"] != "~/Personal" || bd[0]["moved_from"] != nil {
		t.Errorf("brain_dirs home = %v", bd)
	}
	if _, err := os.Stat(filepath.Join(back, "brains-folders", "Personal")); err == nil {
		t.Error("the volume's copy stayed in brains-folders after it came home")
	}
	// What the vault uploaded of the volume's copy is no one's now: the
	// vault forgets it when it next starts (#182).
	if placed[0].Forget != "brains-folders/Personal" {
		t.Errorf("Forget = %q", placed[0].Forget)
	}
	note, err := vault.ReadForgetNote(filepath.Join(back, "core", vault.ForgetNoteName))
	if err != nil || len(note.Folders) != 1 || note.Folders[0].Prefix != "brains-folders/Personal" || !note.Folders[0].At.Equal(t0.Add(3*time.Minute)) {
		t.Errorf("forget note = %+v, %v", note, err)
	}
}

func TestBrainFolderSkippedStays(t *testing.T) {
	home := t.TempDir()
	write(t, home, "Off/brain.json", `{}`, 0o644)
	src := t.TempDir()
	write(t, src, "config.json", `{"brain_dirs": [{"path": "~/Off", "approved": {"id": "off"}}]}`, 0o600)
	var logs []string
	res, err := Export(context.Background(), src, []age.Recipient{identity(t).Recipient()}, newMemStore(), ExportOptions{
		DB: sqliteDB{}, Now: func() time.Time { return t0 }, BrainFolders: []string{filepath.Join(home, "Off")},
		Folders: []Folder{{ID: "off", Configured: "~/Off", Dir: filepath.Join(home, "Off"), Skip: "its backup is off (backup.enabled)"}},
		Logf:    func(f string, a ...any) { logs = append(logs, f) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Manifest.Folders) != 0 || res.Manifest.Format != 1 || len(res.Manifest.OutsideBrainFolders) != 1 {
		t.Errorf("manifest = %+v", res.Manifest)
	}
}

func TestMergeHomeRefusesLinks(t *testing.T) {
	home, src, elsewhere := t.TempDir(), t.TempDir(), t.TempDir()
	write(t, src, "docs/a.md", "a", 0o644)
	if err := os.Symlink(elsewhere, filepath.Join(home, "docs")); err != nil {
		t.Fatal(err)
	}
	if _, err := mergeHome(home, src, filepath.Join(t.TempDir(), "aside"), nil); err == nil {
		t.Error("a merge wrote below a link")
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "a.md")); err == nil {
		t.Error("the file landed where the link points")
	}
}

func TestCleanFolderMember(t *testing.T) {
	for _, bad := range []string{"folders/", "folders/../x", "folders/Personal/a", "folders/p/../../x", "folders/p//a", `folders/p/a\b`, "folders//a"} {
		if _, _, err := cleanFolderMember(bad); err == nil {
			t.Errorf("cleanFolderMember(%q) accepted", bad)
		}
	}
	if id, rel, err := cleanFolderMember("folders/personal/sub/a.md"); err != nil || id != "personal" || rel != "sub/a.md" {
		t.Errorf("= %q %q %v", id, rel, err)
	}
}
