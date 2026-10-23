package vault

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A brain folder a core move had in the data dir went home: Forget drops
// its rows and cold copies, and the .cold.json listings a sync wrote for
// them, but nothing on disk, nothing outside the prefix and nothing of a
// neighbour whose name starts the same.
func TestForgetDropsAFolderThatWentHome(t *testing.T) {
	f := newFixture(t)
	f.write("brains-folders/Personal/notes/a.md", "a", 0)
	f.write("brains-folders/Personal/notes/deep/b.md", "b", 0)
	f.write("brains-folders/Personal/keep.md", "still here", 0)
	f.write("brains-folders/Personal2/c.md", "neighbour", 0)
	f.write("documents/d.md", "elsewhere", 0)
	f.sync()
	// The folder went home: what was below it is gone from disk, but
	// keep.md is there again (a file the vault must not lose).
	if err := os.RemoveAll(filepath.Join(f.root, "brains-folders/Personal/notes")); err != nil {
		t.Fatal(err)
	}
	f.sync() // the rows turn cold and .cold.json appears, as on the owner's core
	if l, _ := f.v.ReadColdListing("brains-folders/Personal/notes"); len(l.Files) != 1 {
		t.Fatalf(".cold.json = %+v", l)
	}
	// An object no row names, from an earlier forget that stopped.
	f.write("orphan.md", "o", 0)
	f.sync()
	if err := os.Rename(filepath.Join(f.cold.Root, "orphan.md"), filepath.Join(f.cold.Root, "brains-folders/Personal/orphan.md")); err != nil {
		t.Fatal(err)
	}

	rep, err := f.v.Forget(bg, "brains-folders/Personal/")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Forgotten != 2 || rep.Bytes != 2 || rep.Orphans != 1 || !slices.Equal(rep.Kept, []string{"brains-folders/Personal/keep.md"}) || rep.Failed != nil {
		t.Fatalf("Forget = %+v", rep)
	}
	for _, rel := range []string{"brains-folders/Personal/notes/a.md", "brains-folders/Personal/notes/deep/b.md", "brains-folders/Personal/orphan.md"} {
		if _, ok := f.coldBytes(rel); ok {
			t.Errorf("%s is still in the cold tier", rel)
		}
		if _, err := f.v.Stat(bg, rel); !errors.Is(err, ErrNotFound) {
			t.Errorf("Stat(%s) = %v, want not found", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.root, "brains-folders/Personal/notes")); !os.IsNotExist(err) {
		t.Errorf("the folder of the listings stays: %v", err)
	}
	for _, rel := range []string{"brains-folders/Personal/keep.md", "brains-folders/Personal2/c.md", "documents/d.md"} {
		if _, ok := f.coldBytes(rel); !ok {
			t.Errorf("%s left the cold tier", rel)
		}
		if e := f.entry(rel); e.Tier != TierBoth {
			t.Errorf("%s = %+v", rel, e)
		}
	}
	man, err := f.v.readManifest(bg)
	if err != nil {
		t.Fatal(err)
	}
	for p := range man {
		if strings.HasPrefix(p, "brains-folders/Personal/notes/") {
			t.Errorf("the manifest still lists %s", p)
		}
	}
	// A rebuild does not bring them back, and a second forget finds nothing.
	if _, err := f.v.Rebuild(bg); err != nil {
		t.Fatal(err)
	}
	if _, err := f.v.Stat(bg, "brains-folders/Personal/notes/a.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after a rebuild: %v", err)
	}
	if rep, err := f.v.Forget(bg, "brains-folders/Personal"); err != nil || rep.Forgotten+rep.Orphans != 0 {
		t.Fatalf("second Forget = %+v, %v", rep, err)
	}
}

func TestForgetRefusesBadPrefixes(t *testing.T) {
	f := newFixture(t)
	for _, p := range []string{"", "/", "../x", "a/../b", ".fridolin-vault", ".fridolin-vault/manifest.json"} {
		if _, err := f.v.Forget(bg, p); err == nil {
			t.Errorf("Forget(%q) went ahead", p)
		}
	}
}

// A cold copy that cannot be deleted keeps its row, so a later forget
// tries again.
func TestForgetKeepsTheRowWhenTheDeleteFails(t *testing.T) {
	f := newFixture(t)
	f.write("gone/a.md", "a", 0)
	f.sync()
	_ = os.RemoveAll(filepath.Join(f.root, "gone"))
	f.cold.Fail = func(op, key string) error {
		if op == "delete" {
			return errors.New("offline")
		}
		return nil
	}
	rep, err := f.v.Forget(bg, "gone")
	if err != nil || rep.Forgotten != 0 || rep.Failed["gone/a.md"] == "" {
		t.Fatalf("Forget = %+v, %v", rep, err)
	}
	if _, err := f.v.Stat(bg, "gone/a.md"); err != nil {
		t.Fatalf("the row went: %v", err)
	}
	f.cold.Fail = nil
	if rep, err := f.v.Forget(bg, "gone"); err != nil || rep.Forgotten != 1 {
		t.Fatalf("Forget again = %+v, %v", rep, err)
	}
}

func TestForgetNote(t *testing.T) {
	file := filepath.Join(t.TempDir(), "core", ForgetNoteName)
	if n, err := ReadForgetNote(file); err != nil || len(n.Folders) != 0 {
		t.Fatalf("missing note = %+v, %v", n, err)
	}
	if err := AddToForgetNote(file, ForgetFolder{Prefix: "brains-folders/a"}, ForgetFolder{Prefix: "brains-folders/b"}); err != nil {
		t.Fatal(err)
	}
	if err := AddToForgetNote(file, ForgetFolder{Prefix: "brains-folders/a", Why: "again"}); err != nil {
		t.Fatal(err)
	}
	if err := AddToForgetNote(file, ForgetFolder{Prefix: "../x"}); err == nil {
		t.Error("a bad prefix went into the note")
	}
	n, err := ReadForgetNote(file)
	if err != nil || len(n.Folders) != 2 || n.Folders[0].Why != "" {
		t.Fatalf("note = %+v, %v", n, err)
	}
	if err := WriteForgetNote(file, ForgetNote{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("an empty note stays: %v", err)
	}
}
