package runsnap

import (
	"encoding/json"
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

// write writes files into dir: path → content.
func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// later moves p's time on, as a write a second later would.
func later(t *testing.T, p string) {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	at := fi.ModTime().Add(time.Second)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

// ops is a record's changes as "op path" lines.
func ops(rec *Record) []string {
	if rec == nil {
		return nil
	}
	var out []string
	for _, c := range rec.Changes {
		out = append(out, c.Op+" "+c.Path)
	}
	return out
}

func brainRoot(dir string) []Root {
	return []Root{{Name: "brain", Dir: dir, Skip: func(rel string, isDir bool) bool {
		return rel == "conversation-history.jsonl" || (isDir && rel == ".git")
	}}}
}

func TestRunRecordsWhatChanged(t *testing.T) {
	t.Parallel()
	dir, data := t.TempDir(), t.TempDir()
	write(t, dir, map[string]string{"notes.md": "one", "memories/a.md": "a", "old.txt": "old", "script.sh": "echo", "conversation-history.jsonl": "{}"})
	s := Open(data)
	run := s.Begin(RunInfo{ID: "r1", Brain: "home", Chat: "120363000000000000@g.us"}, brainRoot(dir), Limits{})
	write(t, dir, map[string]string{"notes.md": "two", "new/b.md": "b", "conversation-history.jsonl": "{}\n{}"})
	if err := os.Remove(filepath.Join(dir, "old.txt")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Join(dir, "script.sh"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rec, err := run.End()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"created new/b.md", "changed notes.md", "deleted old.txt"}
	if runtime.GOOS != "windows" {
		want = []string{"created new/b.md", "changed notes.md", "deleted old.txt", "changed script.sh"}
	}
	if got := ops(rec); !slices.Equal(got, want) {
		t.Fatalf("changes = %v, want %v", got, want)
	}
	loaded, err := s.Load("r1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ops(loaded), want) || loaded.Brain != "home" || loaded.Chat != "120363000000000000@g.us" {
		t.Fatalf("loaded = %+v", loaded)
	}
	for _, c := range loaded.Changes {
		for _, f := range []*File{c.Before, c.After} {
			if f != nil && !s.obj.has(f.Hash) {
				t.Errorf("%s %s: the store lacks %s", c.Op, c.Path, f.Hash)
			}
		}
	}
	if got := rec.Summary(2); !strings.HasPrefix(got, "1 created, ") || !strings.Contains(got, ": new/b.md, notes.md and ") {
		t.Errorf("summary = %q", got)
	}
}

func TestRunThatChangedNothingKeepsNoRecord(t *testing.T) {
	t.Parallel()
	dir, data := t.TempDir(), t.TempDir()
	write(t, dir, map[string]string{"notes.md": "one"})
	s := Open(data)
	rec, err := s.Begin(RunInfo{ID: "r1", Brain: "home"}, brainRoot(dir), Limits{}).End()
	if err != nil || rec != nil {
		t.Fatalf("End = %v, %v; want no record", rec, err)
	}
	if _, err := s.Load("r1"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Load = %v, want not found", err)
	}
	if _, err := s.Load("../x"); !errors.Is(err, ErrBadID) {
		t.Fatalf("Load(../x) = %v", err)
	}
}

func TestUnchangedFilesAreNotReadAgain(t *testing.T) {
	t.Parallel()
	dir, data := t.TempDir(), t.TempDir()
	write(t, dir, map[string]string{"a.md": "aaaa"})
	s := Open(data)
	run := s.Begin(RunInfo{ID: "r1", Brain: "home"}, brainRoot(dir), Limits{})
	// The same size and time: the walk takes the index's hash and does
	// not see the new bytes. That is the price of not reading every file
	// twice a run; a write that keeps both is rare.
	p := filepath.Join(dir, "a.md")
	fi, _ := os.Stat(p)
	write(t, dir, map[string]string{"a.md": "bbbb"})
	if err := os.Chtimes(p, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if rec, _ := run.End(); rec != nil {
		t.Fatalf("a file whose size and time stayed was read again: %v", ops(rec))
	}
	run = s.Begin(RunInfo{ID: "r2", Brain: "home"}, brainRoot(dir), Limits{})
	write(t, dir, map[string]string{"a.md": "cccc"})
	later(t, p)
	if rec, _ := run.End(); !slices.Equal(ops(rec), []string{"changed a.md"}) {
		t.Fatalf("changes = %v", ops(rec))
	}
}

func TestIgnoredAndSkippedFilesStayOut(t *testing.T) {
	t.Parallel()
	dir, data := t.TempDir(), t.TempDir()
	write(t, dir, map[string]string{
		".gitignore":         "build/\n*.log\n!keep.log\n",
		"sub/.gitignore":     "/local.txt\n",
		"build/out.bin":      "x",
		"a.log":              "x",
		"keep.log":           "x",
		"sub/local.txt":      "x",
		"sub/deep/local.txt": "x",
		".git/HEAD":          "ref",
	})
	s := Open(data)
	run := s.Begin(RunInfo{ID: "r1", Brain: "home"}, brainRoot(dir), Limits{})
	write(t, dir, map[string]string{
		"build/out.bin": "y", "a.log": "y", "keep.log": "y", "sub/local.txt": "y", "sub/deep/local.txt": "y", ".git/HEAD": "other",
		// A .gitignore the run writes does not hide what it changes.
		"secret.md":  "new",
		".gitignore": "build/\n*.log\n!keep.log\nsecret.md\n",
	})
	rec, err := run.End()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"changed .gitignore", "changed keep.log", "created secret.md", "changed sub/deep/local.txt"}
	if got := ops(rec); !slices.Equal(got, want) {
		t.Fatalf("changes = %v, want %v", got, want)
	}
}

func TestSymlinksAreNotFollowed(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir, data, outside := t.TempDir(), t.TempDir(), t.TempDir()
	write(t, outside, map[string]string{"secret": "key"})
	s := Open(data)
	run := s.Begin(RunInfo{ID: "r1", Brain: "home"}, brainRoot(dir), Limits{})
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if rec, _ := run.End(); rec != nil {
		t.Fatalf("a symlink was recorded: %v", ops(rec))
	}
}

func TestRunOverItsLimitsIsSkipped(t *testing.T) {
	t.Parallel()
	dir, data := t.TempDir(), t.TempDir()
	write(t, dir, map[string]string{"a": "1", "b": "2", "c": "3"})
	s := Open(data)
	rec, err := s.Begin(RunInfo{ID: "r1", Brain: "home"}, brainRoot(dir), Limits{MaxFiles: 2}).End()
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil || rec.Skipped != "its folders hold more than 2 files" || len(rec.Changes) != 0 {
		t.Fatalf("record = %+v", rec)
	}
	if got := rec.Summary(3); got != "not recorded: its folders hold more than 2 files" {
		t.Errorf("summary = %q", got)
	}
	// Grown past the bound during the run: skipped at the end.
	run := s.Begin(RunInfo{ID: "r2", Brain: "home"}, brainRoot(dir), Limits{MaxBytes: 4})
	write(t, dir, map[string]string{"d": "4444"})
	rec, _ = run.End()
	if rec == nil || rec.Skipped != "its folders hold more than 0 MB" {
		t.Fatalf("record = %+v", rec)
	}
	if loaded, err := s.Load("r2"); err != nil || loaded.Skipped == "" {
		t.Fatalf("Load = %+v, %v", loaded, err)
	}
}

func TestOverlappingRunsNameEachOther(t *testing.T) {
	t.Parallel()
	dir, data := t.TempDir(), t.TempDir()
	write(t, dir, map[string]string{"a.md": "a"})
	s := Open(data)
	chat := s.Begin(RunInfo{ID: "r1", Brain: "home"}, brainRoot(dir), Limits{})
	other := s.Begin(RunInfo{ID: "o1", Brain: "work"}, brainRoot(t.TempDir()), Limits{})
	background := s.Begin(RunInfo{ID: "r2", Brain: "home"}, brainRoot(dir), Limits{})
	if !s.Running("r1") || !s.Running("r2") {
		t.Fatal("the runs are not running")
	}
	write(t, dir, map[string]string{"b.md": "b"})
	rec2, _ := background.End()
	write(t, dir, map[string]string{"c.md": "c"})
	rec1, _ := chat.End()
	_, _ = other.End()
	if !slices.Equal(rec1.Alongside, []string{"r2"}) || !slices.Equal(rec2.Alongside, []string{"r1"}) {
		t.Fatalf("alongside = %v and %v", rec1.Alongside, rec2.Alongside)
	}
	// Each record holds what changed while its run ran.
	if !slices.Equal(ops(rec2), []string{"created b.md"}) || !slices.Equal(ops(rec1), []string{"created b.md", "created c.md"}) {
		t.Fatalf("changes = %v and %v", ops(rec1), ops(rec2))
	}
	if s.Running("r1") {
		t.Fatal("r1 still runs")
	}
}

func TestListNewestFirst(t *testing.T) {
	t.Parallel()
	dir, data := t.TempDir(), t.TempDir()
	s := Open(data)
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return at })
	for i, id := range []string{"r1", "r2", "r3"} {
		run := s.Begin(RunInfo{ID: id, Brain: "home"}, brainRoot(dir), Limits{})
		write(t, dir, map[string]string{id + ".md": id})
		at = at.Add(time.Duration(i+1) * time.Minute)
		if _, err := run.End(); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := s.List("home", 2)
	if err != nil || len(recs) != 2 || recs[0].Run != "r3" || recs[1].Run != "r2" {
		t.Fatalf("List = %v, %v", recs, err)
	}
	if recs, _ := s.List("work", 0); len(recs) != 0 {
		t.Fatalf("work has %d records", len(recs))
	}
}

func TestPruneKeepsWhatIsNeeded(t *testing.T) {
	t.Parallel()
	dir, data := t.TempDir(), t.TempDir()
	s := Open(data)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	write(t, dir, map[string]string{"a.md": strings.Repeat("a", 100)})
	run := s.Begin(RunInfo{ID: "old", Brain: "home"}, brainRoot(dir), Limits{})
	write(t, dir, map[string]string{"a.md": strings.Repeat("b", 100)})
	later(t, filepath.Join(dir, "a.md"))
	old, _ := run.End()
	now = now.Add(20 * 24 * time.Hour)
	run = s.Begin(RunInfo{ID: "new", Brain: "home"}, brainRoot(dir), Limits{})
	write(t, dir, map[string]string{"a.md": strings.Repeat("c", 100)})
	later(t, filepath.Join(dir, "a.md"))
	newer, _ := run.End()
	// A run in flight holds what its folder had as it began.
	inFlight := s.Begin(RunInfo{ID: "flight", Brain: "home"}, brainRoot(dir), Limits{})
	write(t, dir, map[string]string{"a.md": strings.Repeat("d", 100)})
	later(t, filepath.Join(dir, "a.md"))
	idx := s.Begin(RunInfo{ID: "other", Brain: "home"}, brainRoot(dir), Limits{})
	_, _ = idx.End()

	got, err := s.Prune(Retention{KeepDays: 14})
	if err != nil {
		t.Fatal(err)
	}
	if got.Records != 1 || got.Objects != 1 {
		t.Fatalf("pruned %+v, want the old record and its first copy", got)
	}
	if _, err := s.Load("old"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the old record is still there: %v", err)
	}
	for _, h := range []string{newer.Changes[0].Before.Hash, newer.Changes[0].After.Hash} {
		if !s.obj.has(h) {
			t.Fatal("a copy the new record needs is gone")
		}
	}
	if s.obj.has(old.Changes[0].Before.Hash) {
		t.Fatal("the old record's own copy is still there")
	}
	rec, err := inFlight.End()
	if err != nil || !slices.Equal(ops(rec), []string{"changed a.md"}) || !s.obj.has(rec.Changes[0].Before.Hash) {
		t.Fatalf("the run in flight lost its copy: %+v, %v", rec, err)
	}

	// Over the size bound: the oldest records go, the folder's own
	// copies stay.
	got, err = s.Prune(Retention{MaxBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.Records != 2 || got.Bytes != 100 {
		t.Fatalf("pruned %+v, want both records and the folder's copy left", got)
	}
	if recs, _ := s.List("", 0); len(recs) != 0 {
		t.Fatalf("%d records left", len(recs))
	}
	if _, err := os.Stat(filepath.Join(data, "runs", "home")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the empty runs folder stayed: %v", err)
	}

	// An index of a brain that is gone goes with its copies.
	got, err = s.Prune(Retention{Brains: []string{"work"}})
	if err != nil || got.Objects != 1 || got.Bytes != 0 {
		t.Fatalf("pruned %+v, %v", got, err)
	}
}

func TestBadStoreFilesAreRecovered(t *testing.T) {
	t.Parallel()
	dir, data := t.TempDir(), t.TempDir()
	write(t, dir, map[string]string{"a.md": "a"})
	s := Open(data)
	_, _ = s.Begin(RunInfo{ID: "r1", Brain: "home"}, brainRoot(dir), Limits{}).End()
	if err := os.WriteFile(filepath.Join(data, "index", "home.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(data, "runs", "home"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "runs", "home", "bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A new store reads the index from disk.
	s = Open(data)
	run := s.Begin(RunInfo{ID: "r2", Brain: "home"}, brainRoot(dir), Limits{})
	write(t, dir, map[string]string{"b.md": "b"})
	rec, err := run.End()
	if err != nil || !slices.Equal(ops(rec), []string{"created b.md"}) {
		t.Fatalf("End = %v, %v", ops(rec), err)
	}
	var idx map[string]state
	data2, _ := os.ReadFile(filepath.Join(data, "index", "home.json"))
	if err := json.Unmarshal(data2, &idx); err != nil || len(idx[dir]) != 2 {
		t.Fatalf("index = %s, %v", data2, err)
	}
	got, err := s.Prune(Retention{})
	if err != nil || got.Records != 1 {
		t.Fatalf("pruned %+v, %v; want the unreadable record gone", got, err)
	}
}

func TestBadRunIDsKeepNoRecord(t *testing.T) {
	t.Parallel()
	dir, data := t.TempDir(), t.TempDir()
	s := Open(data)
	run := s.Begin(RunInfo{ID: "../x", Brain: "home"}, brainRoot(dir), Limits{})
	rec, err := run.End()
	if err != nil || rec == nil || rec.Skipped == "" {
		t.Fatalf("End = %+v, %v", rec, err)
	}
	if entries, _ := os.ReadDir(data); len(entries) != 0 {
		t.Fatalf("the store wrote %d entries", len(entries))
	}
}
