package app

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/snapshot"
)

// writeBrainJSON writes a brain.json with backup settings b into dir.
func writeBrainJSON(t *testing.T, dir, id string, b manifest.Backup) manifest.Manifest {
	t.Helper()
	m := manifest.Manifest{ID: id, Name: id, Backup: b}
	data, err := json.Marshal(m.Normalized())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, manifest.File), string(data))
	return m.Normalized()
}

// brainBackupFixture is the owner's setup: brain "a" in the data dir,
// "personal" outside it with work folders excluded, "off" with its backup
// off, and "broken" (not loaded) whose brain.json does not parse; each
// with the same files.
type brainBackupFixture struct {
	cfg                            *config.Settings
	data, a, personal, off, broken string
	layout                         vaultLayout
}

func newBrainBackupFixture(t *testing.T) brainBackupFixture {
	t.Helper()
	home := t.TempDir()
	f := brainBackupFixture{data: filepath.Join(home, "data")}
	f.a = filepath.Join(f.data, "brains-folders", "a")
	f.broken = filepath.Join(f.data, "brains-folders", "broken")
	f.personal = filepath.Join(home, "Personal")
	f.off = filepath.Join(home, "off")
	for _, dir := range []string{f.a, f.personal, f.off, f.broken} {
		for _, name := range []string{"memories/a.md", "work/contract.pdf", "work/deep/x.md", "work-clones/repo/f.go", "run.log", "app/node_modules/x.js", "notes/work/n.md", ".env"} {
			writeFile(t, filepath.Join(dir, filepath.FromSlash(name)), "data of "+name)
		}
	}
	writeFile(t, filepath.Join(f.data, "audit", "2026-10.jsonl"), "{}\n")
	writeFile(t, filepath.Join(f.data, "config.json"), "{}\n")
	mA := writeBrainJSON(t, f.a, "a", manifest.Backup{Enabled: true, Exclude: []string{"node_modules/"}})
	mP := writeBrainJSON(t, f.personal, "personal", manifest.Backup{Enabled: true, Exclude: []string{
		"node_modules/", "*.log", ".DS_Store", ".venv/", "__pycache__/", "/work/", "/work-clones/"}})
	mO := writeBrainJSON(t, f.off, "off", manifest.Backup{Enabled: false, Exclude: []string{}})
	writeFile(t, filepath.Join(f.broken, manifest.File), "{broken")

	f.cfg = &config.Settings{}
	f.cfg.DataDir = f.data
	f.cfg.MediaDir = filepath.Join(f.data, "media")
	loaded := []vaultBrain{{ID: "a", Dir: f.a, Manifest: mA}, {ID: "personal", Dir: f.personal, Manifest: mP}, {ID: "off", Dir: f.off, Manifest: mO}}
	refs := []vaultBrainRef{{Dir: f.a}, {Dir: f.personal}, {Dir: f.off}, {Dir: f.broken}}
	f.layout = vaultLayoutOf(f.cfg, map[string]string{"a": f.a, "personal": f.personal, "off": f.off}, vaultBrainBackups(loaded, refs, false))
	return f
}

func (f brainBackupFixture) snapshots() *vaultSnapshots {
	s := &vaultService{}
	s.layout.Store(&f.layout)
	return &vaultSnapshots{s: s, run: &vaultRun{}, cfg: f.cfg}
}

// TestBrainBackupOf: a brain's backup settings are those of its
// brain.json and of the manifest the bridge runs it with, whichever
// leaves out more; a brain.json that cannot be read leaves the folder out.
func TestBrainBackupOf(t *testing.T) {
	dir := t.TempDir()
	disk := writeBrainJSON(t, dir, "x", manifest.Backup{Enabled: true, Exclude: []string{"/work/"}})
	b := brainBackupOf("x", dir, nil, true)
	if b.Skip != "" || !b.excluded("work/a") || b.excluded("memories/a.md") {
		t.Errorf("from disk: %+v", b)
	}
	// The brain removed the exclude from its brain.json; the approved
	// manifest still has it, and wins.
	approved := disk
	approved.Backup.Exclude = []string{"/work/", "/private/"}
	writeBrainJSON(t, dir, "x", manifest.Backup{Enabled: true})
	b = brainBackupOf("x", dir, &approved, true)
	if !b.excluded("work/a") || !b.excluded("private/a") {
		t.Errorf("the approved excludes do not count: %+v", b)
	}
	// Off on either side is off.
	off := approved
	off.Backup.Enabled = false
	if b := brainBackupOf("x", dir, &off, true); b.Skip == "" || b.Unreadable || !b.excluded("memories/a.md") {
		t.Errorf("approved off: %+v", b)
	}
	writeBrainJSON(t, dir, "x", manifest.Backup{Enabled: false})
	if b := brainBackupOf("x", dir, &approved, true); b.Skip == "" || b.Unreadable {
		t.Errorf("disk off: %+v", b)
	}
	// Unreadable: skipped, and marked so.
	writeFile(t, filepath.Join(dir, manifest.File), "{not json")
	if b := brainBackupOf("x", dir, &approved, true); b.Skip == "" || !b.Unreadable || !b.excluded("memories/a.md") {
		t.Errorf("unreadable: %+v", b)
	}
	if err := os.Remove(filepath.Join(dir, manifest.File)); err != nil {
		t.Fatal(err)
	}
	if b := brainBackupOf("x", dir, &approved, true); b.Skip == "" || !b.Unreadable {
		t.Errorf("no brain.json: %+v", b)
	}
	if b := brainBackupOf("x", filepath.Join(dir, "gone"), &approved, true); b.Skip == "" || b.Unreadable {
		t.Errorf("a missing folder: %+v", b)
	}
}

// TestVaultSnapshotPathsFollowBackupSettings: what restic is told for
// each brain folder.
func TestVaultSnapshotPathsFollowBackupSettings(t *testing.T) {
	f := newBrainBackupFixture(t)
	vs := f.snapshots()
	paths, excludes := vs.paths(snapshotNightly)
	if !slices.Equal(paths, []string{f.data, f.personal}) {
		t.Errorf("nightly paths = %q", paths)
	}
	for _, want := range []string{
		f.personal + "/work", f.personal + "/work-clones", f.personal + "/**/*.log",
		f.a + "/**/node_modules", f.a + "/**/.env", f.off, f.broken,
	} {
		if !slices.Contains(excludes, want) {
			t.Errorf("no exclude %q in %q", want, excludes)
		}
	}
	for _, e := range excludes {
		if strings.HasSuffix(e, "/work") && e != f.personal+"/work" {
			t.Errorf("personal's /work/ reached another folder: %q", e)
		}
	}
	paths, _ = vs.paths(snapshotHourly)
	if !slices.Equal(paths, []string{f.a, f.personal, filepath.Join(f.data, "audit")}) {
		t.Errorf("hourly paths = %q", paths)
	}
	skipped := map[string]bool{}
	for _, b := range f.layout.backups {
		if b.Skip != "" {
			skipped[b.ID] = b.Unreadable
		}
	}
	if len(skipped) != 2 || skipped["off"] || !skipped["broken"] {
		t.Errorf("skipped (id → unreadable): %v", skipped)
	}
}

// TestVaultLayoutFollowsBackupSettings: the cold tier leaves out of a
// brain folder in the hot root what its backup settings do, and all of a
// folder whose brain.json cannot be read.
func TestVaultLayoutFollowsBackupSettings(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Settings{}
	cfg.DataDir = dir
	in := filepath.Join(dir, "brains-folders", "personal")
	m := writeBrainJSON(t, in, "personal", manifest.Backup{Enabled: true, Exclude: []string{"/work/", "*.log"}})
	other := filepath.Join(dir, "brains-folders", "other")
	mo := writeBrainJSON(t, other, "other", manifest.Backup{Enabled: true})
	broken := filepath.Join(dir, "brains-folders", "broken")
	writeFile(t, filepath.Join(broken, manifest.File), "{")
	loaded := []vaultBrain{{ID: "personal", Dir: in, Manifest: m}, {ID: "other", Dir: other, Manifest: mo}, {ID: "broken", Dir: broken}}
	l := vaultLayoutOf(cfg, map[string]string{"personal": in, "other": other, "broken": broken}, vaultBrainBackups(loaded, nil, false))
	for rel, ignored := range map[string]bool{
		"brains-folders/personal/memories/a.md":     false,
		"brains-folders/personal/brain.json":        false,
		"brains-folders/personal/work":              true,
		"brains-folders/personal/work/contract.pdf": true,
		"brains-folders/personal/notes/work/n.md":   false,
		"brains-folders/personal/a/run.log":         true,
		"brains-folders/personal/.env":              true,
		"brains-folders/other/work/contract.pdf":    false, // personal's patterns stay in personal
		"brains-folders/other/run.log":              false,
		"brains-folders/other/.env":                 true,
		"brains-folders/broken":                     true,
		"brains-folders/broken/memories/a.md":       true,
	} {
		if got := l.ignore(rel); got != ignored {
			t.Errorf("ignore(%q) = %v, want %v", rel, got, ignored)
		}
	}
	// A loaded brain the backups were not read for is left out whole.
	l = vaultLayoutOf(cfg, map[string]string{"personal": in}, nil)
	if !l.ignore("brains-folders/personal/memories/a.md") {
		t.Error("a brain without backup settings went into the cold tier")
	}
}

// TestVaultSnapshotHonoursBackupExcludeWithRestic runs the real restic
// against a local repository: the owner's personal brain's work folders,
// a folder whose backup is off and one whose brain.json is broken stay out
// of the snapshot; every other file is in it, the same names in other
// folders too.
func TestVaultSnapshotHonoursBackupExcludeWithRestic(t *testing.T) {
	if testing.Short() {
		t.Skip("runs restic")
	}
	bin, err := exec.LookPath("restic")
	if err != nil {
		if bin = findVaultTool("restic"); bin == "" {
			t.Skip("restic is not installed")
		}
	}
	f := newBrainBackupFixture(t)
	vs := f.snapshots()
	tmp := t.TempDir()
	r := &snapshot.Restic{Binary: bin, Repo: filepath.Join(tmp, "repo"),
		Env: []string{"RESTIC_PASSWORD=test-password", "HOME=" + tmp, "TMPDIR=" + tmp, "PATH=/usr/bin:/bin"}}
	ctx := context.Background()
	if err := r.Init(ctx, nil); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{snapshotNightly, snapshotHourly} {
		paths, excludes := vs.paths(kind)
		sum, err := r.Backup(ctx, snapshot.BackupOptions{Paths: paths, Excludes: excludes, Tags: []string{kind}})
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		target := filepath.Join(tmp, "restore-"+kind)
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := r.Restore(ctx, sum.SnapshotID, target); err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		_ = filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				got[strings.TrimPrefix(p, target)] = true
			}
			return nil
		})
		in := func(dir, rel string) bool { return got[filepath.Join(dir, filepath.FromSlash(rel))] }
		for _, rel := range []string{"work/contract.pdf", "work/deep/x.md", "work-clones/repo/f.go", "run.log", "app/node_modules/x.js", ".env"} {
			if in(f.personal, rel) {
				t.Errorf("%s: personal's %s is in the snapshot", kind, rel)
			}
		}
		for _, rel := range []string{"memories/a.md", "notes/work/n.md", "brain.json"} {
			if !in(f.personal, rel) {
				t.Errorf("%s: personal's %s is not in the snapshot", kind, rel)
			}
		}
		// Brain a: personal's patterns do not reach it, its own do.
		for _, rel := range []string{"work/contract.pdf", "work-clones/repo/f.go", "run.log", "memories/a.md"} {
			if !in(f.a, rel) {
				t.Errorf("%s: a's %s is not in the snapshot", kind, rel)
			}
		}
		for _, rel := range []string{"app/node_modules/x.js", ".env"} {
			if in(f.a, rel) {
				t.Errorf("%s: a's %s is in the snapshot", kind, rel)
			}
		}
		for _, dir := range []string{f.off, f.broken} {
			if in(dir, "memories/a.md") || in(dir, "work/contract.pdf") {
				t.Errorf("%s: %s is in the snapshot", kind, dir)
			}
		}
		if kind == snapshotNightly && !in(f.data, "config.json") {
			t.Errorf("the data dir's config.json is not in the nightly snapshot")
		}
	}
}
