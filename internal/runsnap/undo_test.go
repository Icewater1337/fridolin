package runsnap

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// read is the content of dir's file p, or "<none>".
func read(t *testing.T, dir, p string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
	if errors.Is(err, fs.ErrNotExist) {
		return "<none>"
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// actions is an undo's steps as "action path" lines.
func actions(u *UndoResult) []string {
	var out []string
	for _, s := range u.Steps {
		out = append(out, s.Action+" "+s.Path)
	}
	return out
}

// changedRun is a store with run r1 of brain home, which created new.md,
// changed notes.md, deleted old.md and made run.sh executable.
func changedRun(t *testing.T) (s *Store, dir string) {
	t.Helper()
	dir = t.TempDir()
	write(t, dir, map[string]string{"notes.md": "before", "old.md": "old", "keep.md": "keep", "run.sh": "echo"})
	s = Open(t.TempDir())
	s.SetClock(func() time.Time { return time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC) })
	run := s.Begin(RunInfo{ID: "r1", Brain: "home"}, brainRoot(dir), Limits{})
	write(t, dir, map[string]string{"notes.md": "after", "sub/new.md": "new"})
	if err := os.Remove(filepath.Join(dir, "old.md")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := run.End(); err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestUndoPutsTheRunBack(t *testing.T) {
	t.Parallel()
	s, dir := changedRun(t)
	plan, err := s.Undo("r1", UndoOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"restore notes.md", "restore old.md", "trash sub/new.md"}
	if runtime.GOOS != "windows" {
		want = []string{"restore notes.md", "restore old.md", "restore run.sh", "trash sub/new.md"}
	}
	if got := actions(plan); !slices.Equal(got, want) || !plan.DryRun {
		t.Fatalf("plan = %v, want %v", got, want)
	}
	if read(t, dir, "notes.md") != "after" || read(t, dir, "sub/new.md") != "new" {
		t.Fatal("a dry run changed the folder")
	}

	res, err := s.Undo("r1", UndoOptions{By: "15551234567", Trash: ".fridolin-trash"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Count(ActRestore, true)+res.Count(ActTrash, true) != len(want) || len(res.Failed()) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if read(t, dir, "notes.md") != "before" || read(t, dir, "old.md") != "old" || read(t, dir, "sub/new.md") != "<none>" || read(t, dir, "keep.md") != "keep" {
		t.Fatal("the folder is not as it was before the run")
	}
	trashed := res.Steps[len(res.Steps)-1].To
	if trashed != ".fridolin-trash/20261119-100000/sub/new.md" || read(t, dir, trashed) != "new" {
		t.Fatalf("the new file went to %q", trashed)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(filepath.Join(dir, "run.sh")); fi.Mode().Perm() != 0o644 {
			t.Fatalf("run.sh's mode is %v", fi.Mode().Perm())
		}
	}
	rec, err := s.Load("r1")
	if err != nil || rec.Undone == nil || rec.Undone.By != "15551234567" {
		t.Fatalf("record = %+v, %v", rec, err)
	}

	// Again: everything is as it was before the run.
	again, err := s.Undo("r1", UndoOptions{Trash: ".fridolin-trash"})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range again.Steps {
		if st.Action != ActNone || st.Done {
			t.Errorf("a second undo: %+v", st)
		}
	}
}

func TestUndoLeavesWhatChangedSince(t *testing.T) {
	t.Parallel()
	s, dir := changedRun(t)
	// Since the run: notes.md edited again, old.md made anew, the new file
	// edited, run.sh deleted.
	write(t, dir, map[string]string{"notes.md": "later", "old.md": "another", "sub/new.md": "edited"})
	later(t, filepath.Join(dir, "notes.md"))
	if err := os.Remove(filepath.Join(dir, "run.sh")); err != nil {
		t.Fatal(err)
	}
	res, err := s.Undo("r1", UndoOptions{Trash: ".fridolin-trash"})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range res.Steps {
		if st.Action != ActConflict || st.Done || st.Why == "" {
			t.Errorf("%s: %+v, want a conflict", st.Path, st)
		}
	}
	if read(t, dir, "notes.md") != "later" || read(t, dir, "old.md") != "another" || read(t, dir, "sub/new.md") != "edited" {
		t.Fatal("an undo took back a later change")
	}
}

func TestUndoRefuses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := Open(t.TempDir())
	run := s.Begin(RunInfo{ID: "r1", Brain: "home"}, brainRoot(dir), Limits{})
	if _, err := s.Undo("r1", UndoOptions{DryRun: true}); !errors.Is(err, ErrRunning) {
		t.Fatalf("a running run: %v", err)
	}
	write(t, dir, map[string]string{"a": "1", "b": "2"})
	_, _ = run.End()
	if _, err := s.Undo("nope", UndoOptions{DryRun: true}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("an unknown run: %v", err)
	}
	if _, err := s.Undo("../r1", UndoOptions{DryRun: true}); !errors.Is(err, ErrBadID) {
		t.Fatalf("a bad id: %v", err)
	}
	if _, err := s.Undo("r1", UndoOptions{}); err == nil || !strings.Contains(err.Error(), "trash") {
		t.Fatalf("no trash: %v", err)
	}
	_, _ = s.Begin(RunInfo{ID: "r2", Brain: "home"}, brainRoot(dir), Limits{MaxFiles: 1}).End()
	if _, err := s.Undo("r2", UndoOptions{DryRun: true}); !errors.Is(err, ErrNotRecorded) || !strings.Contains(err.Error(), "more than 1 files") {
		t.Fatalf("a skipped run: %v", err)
	}
}

func TestUndoWithoutItsCopies(t *testing.T) {
	t.Parallel()
	s, dir := changedRun(t)
	rec, _ := s.Load("r1")
	var notes, old Change
	for _, c := range rec.Changes {
		switch c.Path {
		case "notes.md":
			notes = c
		case "old.md":
			old = c
		}
	}
	// One copy gone, one damaged.
	if err := os.Remove(s.obj.path(notes.Before.Hash)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.obj.path(old.Before.Hash), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := s.Undo("r1", UndoOptions{Trash: ".fridolin-trash"})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range res.Steps {
		switch st.Path {
		case "notes.md":
			if st.Action != ActConflict || st.Why != "its earlier contents are no longer stored" {
				t.Errorf("notes.md: %+v", st)
			}
		case "old.md":
			if st.Error == "" || !strings.Contains(st.Error, "damaged") || st.Done {
				t.Errorf("old.md: %+v", st)
			}
		}
	}
	if read(t, dir, "old.md") != "<none>" || read(t, dir, "notes.md") != "after" {
		t.Fatal("a missing or damaged copy was written")
	}
	if len(res.Failed()) != 1 {
		t.Fatalf("failed = %v", res.Failed())
	}
}

func TestUndoFollowsNoSymlink(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	s, dir := changedRun(t)
	outside := t.TempDir()
	write(t, outside, map[string]string{"notes.md": "yours"})
	// A brain puts a symlink where a file it changed was, and links its
	// folder sub/ out.
	if err := os.Remove(filepath.Join(dir, "notes.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "notes.md"), filepath.Join(dir, "notes.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "sub")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "sub")); err != nil {
		t.Fatal(err)
	}
	write(t, outside, map[string]string{"new.md": "new"})
	res, err := s.Undo("r1", UndoOptions{Trash: ".fridolin-trash"})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range res.Steps {
		if (st.Path == "notes.md" || st.Path == "sub/new.md") && (st.Done || st.Action == ActRestore || st.Action == ActTrash) {
			t.Errorf("%s: %+v", st.Path, st)
		}
	}
	if read(t, outside, "notes.md") != "yours" || read(t, outside, "new.md") != "new" {
		t.Fatal("the undo went through a symlink")
	}
}

func TestUndoOfARunWhoseFolderIsGone(t *testing.T) {
	t.Parallel()
	s, dir := changedRun(t)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	res, err := s.Undo("r1", UndoOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range res.Steps {
		if st.Action != ActConflict || st.Why != "its folder is not there any more" {
			t.Errorf("%+v", st)
		}
	}
}

// renamedRun is a store with run r1 of brain home, which renamed
// Notes.md to notes.md and, with edit, changed its contents too.
func renamedRun(t *testing.T, edit bool) (s *Store, dir string) {
	t.Helper()
	dir = t.TempDir()
	write(t, dir, map[string]string{"Notes.md": "before", "keep.md": "keep"})
	s = Open(t.TempDir())
	s.SetClock(func() time.Time { return time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC) })
	run := s.Begin(RunInfo{ID: "r1", Brain: "home"}, brainRoot(dir), Limits{})
	if err := os.Rename(filepath.Join(dir, "Notes.md"), filepath.Join(dir, "notes.md")); err != nil {
		t.Fatal(err)
	}
	if edit {
		write(t, dir, map[string]string{"notes.md": "after the run"})
	}
	if _, err := run.End(); err != nil {
		t.Fatal(err)
	}
	return s, dir
}

// names are the names of dir's entries, its trash left out.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.Name() != ".fridolin-trash" {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestUndoOfARenameTheFilesystemDoesNotTellApart is fridolin#732: on a
// case-insensitive filesystem Notes.md and notes.md are one file, and an
// undo that took them apart left the folder without it. The test stands in
// such a filesystem with the store's same-file check.
func TestUndoOfARenameTheFilesystemDoesNotTellApart(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		edit  bool
		since string // what the file is edited to after the run, if at all
	}{
		{name: "renamed", edit: false},
		{name: "renamed and edited", edit: true},
		{name: "changed since", edit: true, since: "the owner's later edit"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, dir := renamedRun(t, c.edit)
			s.sameFile = func(_ *os.Root, a, b string) bool { return strings.EqualFold(a, b) }
			if c.since != "" {
				write(t, dir, map[string]string{"notes.md": c.since})
			}
			plan, err := s.Undo("r1", UndoOptions{DryRun: true})
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"restore Notes.md", "none notes.md"}
			if c.since != "" {
				want = []string{"conflict Notes.md", "conflict notes.md"}
			}
			if got := actions(plan); !slices.Equal(got, want) {
				t.Fatalf("plan = %v, want %v", got, want)
			}
			res, err := s.Undo("r1", UndoOptions{Trash: ".fridolin-trash"})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Failed()) != 0 || res.Count(ActTrash, false) != 0 {
				t.Fatalf("result = %+v", res)
			}
			if c.since != "" {
				if got := names(t, dir); !slices.Equal(got, []string{"keep.md", "notes.md"}) || read(t, dir, "notes.md") != c.since {
					t.Fatalf("a conflict changed the folder: %v", got)
				}
				return
			}
			if res.Count(ActRestore, true) != 1 {
				t.Fatalf("result = %+v", res)
			}
			if got := names(t, dir); !slices.Equal(got, []string{"Notes.md", "keep.md"}) || read(t, dir, "Notes.md") != "before" {
				t.Fatalf("the folder is %v, Notes.md %q", got, read(t, dir, "Notes.md"))
			}
		})
	}
}

// TestUndoOfACaseOnlyRename is fridolin#732 on a real case-insensitive
// filesystem (macOS's default), where there is one.
func TestUndoOfACaseOnlyRename(t *testing.T) {
	t.Parallel()
	probe := t.TempDir()
	write(t, probe, map[string]string{"a": ""})
	if _, err := os.Lstat(filepath.Join(probe, "A")); err != nil {
		t.Skip("the temporary folder's filesystem is case-sensitive")
	}
	for _, edit := range []bool{false, true} {
		s, dir := renamedRun(t, edit)
		res, err := s.Undo("r1", UndoOptions{Trash: ".fridolin-trash"})
		if err != nil {
			t.Fatal(err)
		}
		if got := actions(res); !slices.Equal(got, []string{"restore Notes.md", "none notes.md"}) || len(res.Failed()) != 0 {
			t.Fatalf("edit %v: %v, %+v", edit, got, res.Failed())
		}
		if got := names(t, dir); !slices.Equal(got, []string{"Notes.md", "keep.md"}) || read(t, dir, "Notes.md") != "before" {
			t.Fatalf("edit %v: the folder is %v, Notes.md %q", edit, got, read(t, dir, "Notes.md"))
		}
	}
}
