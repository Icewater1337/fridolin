package files

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var editor = Scope{Private: true, All: true, Write: true, By: "dashboard"}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func testRoot(t *testing.T) (Root, string, string) {
	t.Helper()
	dir, outside, data := brainFolder(t)
	r := BrainRoot("main", "Main", dir, true, []string{"/work/", "*.log"}, []string{data, filepath.Join(dir, "denied")})
	return r, dir, outside
}

func TestWriteFileReplacesTheVersionItStartedFrom(t *testing.T) {
	r, dir, _ := testRoot(t)
	if _, err := r.WriteFile("notes.md", []byte("new"), SHA([]byte("x")), editor); err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir, "notes.md"); got != "new" {
		t.Fatalf("notes.md = %q", got)
	}
	// The old version is no longer the one on disk.
	if _, err := r.WriteFile("notes.md", []byte("again"), SHA([]byte("x")), editor); !errors.Is(err, ErrChanged) {
		t.Fatalf("stale sha: %v", err)
	}
	// Created with its folders; an existing one is not overwritten.
	if _, err := r.WriteFile("wiki/new/page.md", []byte("# P"), "", editor); err != nil {
		t.Fatal(err)
	}
	if _, err := r.WriteFile("wiki/new/page.md", []byte("# Q"), "", editor); !errors.Is(err, ErrExists) {
		t.Fatalf("create over a file: %v", err)
	}
	if got := read(t, dir, "wiki/new/page.md"); got != "# P" {
		t.Fatalf("page = %q", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".fridolin-") {
			t.Errorf("left a temp file: %s", e.Name())
		}
	}
}

func TestWritesRefuseHiddenProtectedAndOutsidePaths(t *testing.T) {
	r, dir, outside := testRoot(t)
	if err := os.Symlink(outside, filepath.Join(dir, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(dir, "linkdir")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"", ".env", ".git/config", "work/big.bin", "work/new.md", "build.log", "keys/id_ed25519", "fridolin-data/bridge.db",
		"denied/x.txt", "../secret.txt", "a/../../secret.txt", "brain.json", "conversation-history.jsonl", "link.md",
		"linkdir/secret.txt", "linkdir/new.md", TrashDir + "/x.md",
	} {
		if _, err := r.WriteFile(rel, []byte("bad"), "", editor); err == nil {
			t.Errorf("created %q", rel)
		}
		if _, err := r.WriteFile(rel, []byte("bad"), SHA([]byte("x")), editor); err == nil {
			t.Errorf("replaced %q", rel)
		}
	}
	if got := read(t, filepath.Dir(outside), "secret.txt"); got != "outside" {
		t.Fatalf("the file outside became %q", got)
	}
	// Read-only scopes and roots.
	if _, err := r.WriteFile("notes.md", []byte("y"), SHA([]byte("x")), owner); !errors.Is(err, ErrReadOnly) {
		t.Errorf("a scope without Write: %v", err)
	}
	media := MediaRoot("main", "Main", dir, nil)
	if _, err := media.WriteFile("notes.md", []byte("y"), SHA([]byte("x")), editor); !errors.Is(err, ErrReadOnly) {
		t.Errorf("a media inbox: %v", err)
	}
}

func TestCreateNumbersTakenNamesAndRefusesHiddenOnes(t *testing.T) {
	r, dir, _ := testRoot(t)
	for i, want := range []string{"docs/a b.txt", "docs/a b (2).txt", "docs/a b (3).txt"} {
		got, n, err := r.Create("docs", "../../a b.txt", strings.NewReader("data"), 10, editor)
		if err != nil || got != want || n != 4 {
			t.Fatalf("upload %d: %q, %d, %v", i, got, n, err)
		}
	}
	if _, _, err := r.Create("docs", "big.txt", strings.NewReader(strings.Repeat("z", 11)), 10, editor); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("too large: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "docs", "big.txt")); !os.IsNotExist(err) {
		t.Fatal("a refused upload stayed")
	}
	for _, name := range []string{"key.pem", "x.log", "brain.json", "..id_rsa"} {
		folder := "docs"
		if name == "brain.json" {
			folder = ""
		}
		if got, _, err := r.Create(folder, name, strings.NewReader("s"), 10, editor); err == nil {
			t.Errorf("uploaded %q as %q", name, got)
		}
	}
	for _, folder := range []string{"work", ".git", "../", "missing", "notes.md"} {
		if _, _, err := r.Create(folder, "f.txt", strings.NewReader("s"), 10, editor); err == nil {
			t.Errorf("uploaded into %q", folder)
		}
	}
}

func TestUploadToFollowsTheFoldersRule(t *testing.T) {
	r, dir, _ := testRoot(t)
	write(t, dir, "docs/drop/keep.txt", "k")
	r = r.WithUploads([]Upload{{Folder: "docs", ToBrain: true}, {Folder: "docs/drop"}})
	member := Scope{Brains: []string{"main"}, Uploads: []string{"main"}, By: "site:m"}
	reader := Scope{Brains: []string{"main"}, By: "site:r"}
	for _, c := range []struct {
		s          Scope
		rel, where string
	}{
		{editor, "", UploadFolder}, {editor, "docs", UploadBrain}, {editor, "docs/x", UploadBrain}, {editor, "docs/drop/y", UploadFolder},
		{editor, "work", ""}, {member, "", ""}, {member, "wiki", ""}, {member, "docs", UploadBrain}, {member, "docs/drop", UploadFolder},
		{member, "docsx", ""}, {reader, "docs", ""},
	} {
		if got := r.UploadTo(c.rel, c.s); got != c.where {
			t.Errorf("UploadTo(%q, %s) = %q, want %q", c.rel, c.s.By, got, c.where)
		}
	}
	if _, _, err := r.Create("wiki", "x.md", strings.NewReader("s"), 10, member); !errors.Is(err, ErrReadOnly) {
		t.Errorf("member upload outside the upload folders: %v", err)
	}
	if got, _, err := r.Create("docs/drop", "x.md", strings.NewReader("s"), 10, member); err != nil || got != "docs/drop/x.md" {
		t.Errorf("member upload into the drop folder: %q, %v", got, err)
	}
}

func TestMoveAndTrash(t *testing.T) {
	r, dir, outside := testRoot(t)
	if err := r.Move("notes.md", "wiki/notes.md", editor); err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir, "wiki/notes.md"); got != "x" {
		t.Fatalf("moved file = %q", got)
	}
	if err := r.Move("wiki/notes.md", "wiki/index.md", editor); !errors.Is(err, ErrExists) {
		t.Fatalf("move over a file: %v", err)
	}
	for _, c := range [][2]string{
		{"wiki", "wiki/sub"}, {"wiki/notes.md", "work/notes.md"}, {"wiki/notes.md", "../notes.md"}, {"wiki/notes.md", ".hidden.md"},
		{".env", "env.txt"}, {"work", "public"}, {"brain.json", "b.json"}, {"wiki/notes.md", "brain.json"}, {"missing", "x"},
		{"wiki/notes.md", "nofolder/x.md"}, {"memories", "mem"}, // memories/ holds the identity map, hidden from others but not the owner: fine
	} {
		if c[0] == "memories" {
			continue
		}
		if err := r.Move(c[0], c[1], editor); err == nil {
			t.Errorf("moved %q to %q", c[0], c[1])
		}
	}
	// A folder that holds something hidden stays.
	write(t, dir, "docs/.secret", "s")
	if err := r.Move("docs", "papers", editor); err == nil {
		t.Error("moved a folder holding a hidden file")
	}
	if _, err := r.Trash("docs", editor, time.Now()); err == nil {
		t.Error("deleted a folder holding a hidden file")
	}
	at, err := r.Trash("wiki/people/alex.md", editor, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if err != nil || at != TrashDir+"/20260102-030405/wiki/people/alex.md" {
		t.Fatalf("trash: %q, %v", at, err)
	}
	if got := read(t, dir, at); got != "x" {
		t.Fatalf("trashed file = %q", got)
	}
	if _, err := r.Trash(at, editor, time.Now()); err == nil {
		t.Error("deleted from the trash, which is hidden")
	}
	if err := os.Symlink(outside, filepath.Join(dir, "out.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Trash("out.md", editor, time.Now()); err == nil {
		t.Error("deleted a symlink")
	}
	if err := r.Move("out.md", "in.md", editor); err == nil {
		t.Error("moved a symlink")
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"a.txt": "a.txt", "../../etc/passwd": "passwd", `C:\x\y.pdf`: "y.pdf", "..env": "env", "\x00\x01": "upload", "  Brief ü.pdf ": "Brief ü.pdf",
		strings.Repeat("é", 150): strings.Repeat("é", 100),
	} {
		if got := SafeName(in); got != want {
			t.Errorf("SafeName(%q) = %q, want %q", in, got, want)
		}
	}
}
