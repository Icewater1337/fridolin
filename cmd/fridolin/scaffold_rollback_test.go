package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

func TestScaffoldRollbackKeepsSelectedSymlink(t *testing.T) {
	target := t.TempDir()
	selected := filepath.Join(t.TempDir(), "selected")
	if err := os.Symlink(target, selected); err != nil {
		t.Fatal(err)
	}
	s, err := scaffoldFolderTx(selected, &manifest.Manifest{ID: "a", Name: "A"})
	defer s.close()
	if err != nil {
		t.Fatal(err)
	}
	s.rollback()
	if link, err := os.Readlink(selected); err != nil || link != target {
		t.Fatalf("rollback changed the selected symlink: %q, %v", link, err)
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Fatalf("rollback did not remove only its target's templates: %v, %v", entries, err)
	}
}

func TestScaffoldRejectsEmptyDirectory(t *testing.T) {
	m := testManifest("candidate")
	before, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	created, err := ScaffoldBrain("", &m)
	if err == nil || len(created) != 0 {
		t.Fatalf("empty folder scaffold = %v, %v", created, err)
	}
	after, err := os.ReadDir(".")
	if err != nil || len(after) != len(before) {
		t.Fatalf("empty folder scaffold changed the working directory: %v, %v", after, err)
	}
}

func scaffoldForRollback(t *testing.T, dir string) func() {
	t.Helper()
	m := testManifest("candidate")
	scaffold, err := scaffoldFolderTx(dir, &m)
	t.Cleanup(scaffold.close)
	if err != nil {
		t.Fatal(err)
	}
	return scaffold.rollback
}

func TestScaffoldRollbackKeepsEscapingParentTargets(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	undo := scaffoldForRollback(t, dir)
	if err := os.Rename(filepath.Join(dir, "skills"), filepath.Join(dir, "original-skills")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "README.md"), []byte("external file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "skills")); err != nil {
		t.Fatal(err)
	}
	undo()
	if got, err := os.ReadFile(filepath.Join(outside, "README.md")); err != nil || string(got) != "external file" {
		t.Fatalf("rollback changed a file outside the brain: %q, %v", got, err)
	}
}

func TestScaffoldRollbackKeepsReplacementFiles(t *testing.T) {
	dir := t.TempDir()
	undo := scaffoldForRollback(t, dir)
	path := filepath.Join(dir, "brain-prompt.md")
	if err := os.Rename(path, path+".created"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("external replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	undo()
	if got, err := os.ReadFile(path); err != nil || string(got) != "external replacement" {
		t.Fatalf("rollback changed a replacement file: %q, %v", got, err)
	}
}

func TestScaffoldRollbackKeepsEditedFiles(t *testing.T) {
	dir := t.TempDir()
	undo := scaffoldForRollback(t, dir)
	path := filepath.Join(dir, "brain-prompt.md")
	if err := os.WriteFile(path, []byte("edited after creation"), 0o600); err != nil {
		t.Fatal(err)
	}
	undo()
	if got, err := os.ReadFile(path); err != nil || string(got) != "edited after creation" {
		t.Fatalf("rollback changed an edited file: %q, %v", got, err)
	}
}

func TestScaffoldRollbackKeepsExistingEmptyDirectories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "skills")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	undo := scaffoldForRollback(t, dir)
	undo()
	if entries, err := os.ReadDir(path); err != nil || len(entries) != 0 {
		t.Fatalf("rollback did not preserve the existing empty directory: %v, %v", entries, err)
	}
}

func TestScaffoldRollbackKeepsReplacementBrainDirectory(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "candidate")
	undo := scaffoldForRollback(t, dir)
	if err := os.Rename(dir, filepath.Join(parent, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "brain-prompt.md"), []byte("external replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	undo()
	if got, err := os.ReadFile(filepath.Join(dir, "brain-prompt.md")); err != nil || string(got) != "external replacement" {
		t.Fatalf("rollback changed a replacement brain directory: %q, %v", got, err)
	}
}

// Invalid relative paths are rejected before a file can become owned by the
// transaction; rollback never receives a caller-supplied list of path strings.
func TestScaffoldOwnershipStaysInTheFolder(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "candidate")
	scaffold, err := newScaffold(dir)
	t.Cleanup(scaffold.close)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside.md")
	if err := os.WriteFile(outside, []byte("external"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"../outside.md", outside} {
		if err := scaffold.write(rel, "replace"); err == nil {
			t.Fatalf("write outside the folder succeeded: %q", rel)
		}
	}
	if err := scaffold.write("memories/README.md", "owned"); err != nil {
		t.Fatal(err)
	}
	scaffold.rollback()
	if got, err := os.ReadFile(outside); err != nil || string(got) != "external" {
		t.Fatalf("outside file changed: %q, %v", got, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the created folder was left behind: %v", err)
	}
}
