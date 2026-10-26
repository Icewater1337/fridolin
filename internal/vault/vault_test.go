package vault

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var bg = context.Background()

func TestSyncUploadsAndSkipsIgnored(t *testing.T) {
	f := newFixture(t)
	f.write("documents/a.pdf", "pdf bytes", time.Hour)
	f.write("brains/x/memories/core.md", "# core", time.Hour)
	f.write("bridge.db", "pairing", 0)
	f.write("bridge.db-wal", "wal", 0)
	f.write("documents/partial.tmp", "tmp", 0)
	f.write("documents/.cold.json", `{"files":[]}`, 0)
	f.write(".fridolin-vault/manifest.json", "local", 0)

	rep := f.sync()
	if !slices.Equal(rep.Uploaded, []string{"brains/x/memories/core.md", "documents/a.pdf"}) {
		t.Fatalf("Uploaded = %v", rep.Uploaded)
	}
	for _, rel := range []string{"documents/a.pdf", "brains/x/memories/core.md"} {
		e := f.entry(rel)
		content, _ := f.read(rel)
		if e.Tier != TierBoth || e.SHA256 != sum(content) {
			t.Fatalf("%s: %+v", rel, e)
		}
		if got, ok := f.coldBytes(rel); !ok || got != content {
			t.Fatalf("cold %s = %q, %v", rel, got, ok)
		}
	}
	for _, rel := range []string{"bridge.db", "bridge.db-wal", "documents/partial.tmp", "documents/.cold.json", "vault.db"} {
		if _, ok := f.coldBytes(rel); ok {
			t.Fatalf("%s was uploaded", rel)
		}
		if _, err := f.v.Stat(bg, rel); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s is in the index: %v", rel, err)
		}
	}
	if m, err := f.v.readManifest(bg); err != nil || m["documents/a.pdf"].SHA256 != sum("pdf bytes") {
		t.Fatalf("manifest = %v, %v", m, err)
	}

	// Unchanged: nothing goes up again.
	if rep := f.sync(); len(rep.Uploaded) != 0 || rep.Skipped != 2 {
		t.Fatalf("second sync = %+v", rep)
	}
	// Changed: the new bytes go up.
	f.write("documents/a.pdf", "pdf bytes, edited", 0)
	if rep := f.sync(); !slices.Equal(rep.Uploaded, []string{"documents/a.pdf"}) {
		t.Fatalf("sync after edit = %+v", rep)
	}
	if got, _ := f.coldBytes("documents/a.pdf"); got != "pdf bytes, edited" {
		t.Fatalf("cold after edit = %q", got)
	}
	// Touched only: no upload, still both.
	m := start.Add(time.Minute)
	if err := os.Chtimes(filepath.Join(f.root, "documents/a.pdf"), m, m); err != nil {
		t.Fatal(err)
	}
	if rep := f.sync(); len(rep.Uploaded) != 0 || f.entry("documents/a.pdf").Tier != TierBoth {
		t.Fatalf("sync after touch = %+v", rep)
	}
}

func TestFailedUploadStaysHotAndIsNeverEvicted(t *testing.T) {
	f := newFixture(t)
	f.cold.Fail = func(op, key string) error {
		if op == "put" && key == "documents/old.md" {
			return errors.New("network down")
		}
		return nil
	}
	f.write("documents/old.md", "only copy", nDays(200))
	rep := f.sync()
	if rep.Failed["documents/old.md"] == "" {
		t.Fatalf("sync = %+v", rep)
	}
	if e := f.entry("documents/old.md"); e.Tier != TierHot {
		t.Fatalf("tier = %s", e.Tier)
	}
	ev := f.evict()
	if len(ev.Evicted) != 0 || !slices.Contains(ev.NotUploaded, "documents/old.md") {
		t.Fatalf("evict = %+v", ev)
	}
	if got, ok := f.read("documents/old.md"); !ok || got != "only copy" {
		t.Fatal("the only copy was lost")
	}
	// Once the network is back the next sync uploads it, and it can go.
	f.cold.Fail = nil
	f.sync()
	if ev := f.evict(); len(ev.Evicted) != 1 {
		t.Fatalf("evict after upload = %+v", ev)
	}
}

func TestEvictWritesColdListingAndKeepsData(t *testing.T) {
	f := newFixture(t)
	files := map[string]time.Duration{
		"documents/2025/old-notes.md":   nDays(100), // inactive text: goes
		"documents/2025/old-scan.pdf":   nDays(100), // inactive binary, past 90 days: goes
		"documents/2025/fresh.md":       nDays(5),   // recent: stays
		"documents/2025/held.pdf":       nDays(70),  // markdown-first: stays
		"brains/x/brain-prompt.md":      nDays(400), // pinned: stays
		"brains/x/memories/2025/old.md": nDays(400), // pinned: stays
		"runs/r1.log":                   nDays(20),  // old run log: goes
		"audit/2026-09/t.jsonl":         nDays(20),  // last month's audit: goes
		"audit/2026-10/t.jsonl":         nDays(20),  // this month's audit: stays
	}
	for rel, age := range files {
		f.write(rel, "content of "+rel, age)
	}
	f.sync()
	ev := f.evict()
	var evicted []string
	for _, e := range ev.Evicted {
		evicted = append(evicted, e.Path)
	}
	slices.Sort(evicted)
	want := []string{"audit/2026-09/t.jsonl", "documents/2025/old-notes.md", "documents/2025/old-scan.pdf", "runs/r1.log"}
	if !slices.Equal(evicted, want) {
		t.Fatalf("evicted %v, want %v", evicted, want)
	}
	for rel, age := range files {
		_, onDisk := f.read(rel)
		cold, inCold := f.coldBytes(rel)
		if !inCold || cold != "content of "+rel {
			t.Fatalf("%s: cold copy %q, %v", rel, cold, inCold)
		}
		gone := slices.Contains(want, rel)
		if onDisk == gone {
			t.Fatalf("%s: on disk %v", rel, onDisk)
		}
		e := f.entry(rel)
		if gone && e.Tier != TierCold || !gone && e.Tier != TierBoth {
			t.Fatalf("%s: tier %s", rel, e.Tier)
		}
		_ = age
	}
	l, err := f.v.ReadColdListing("documents/2025")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Files) != 2 || l.Files[0].Name != "old-notes.md" || l.Files[1].Name != "old-scan.pdf" {
		t.Fatalf(".cold.json = %+v", l)
	}
	scan := l.Files[1]
	if scan.Size != int64(len("content of documents/2025/old-scan.pdf")) || scan.SHA256 != sum("content of documents/2025/old-scan.pdf") ||
		!scan.MTime.Equal(start.Add(-nDays(100))) {
		t.Fatalf("listing entry = %+v", scan)
	}
	if l, _ := f.v.ReadColdListing("audit/2026-10"); len(l.Files) != 0 {
		t.Fatalf("a .cold.json in a folder without cold members: %+v", l)
	}
	log, err := f.v.EvictionLog(bg, 10)
	if err != nil || len(log) != 4 {
		t.Fatalf("eviction log = %v, %v", log, err)
	}
	// Nothing left to evict.
	if ev := f.evict(); len(ev.Evicted) != 0 {
		t.Fatalf("second evict = %+v", ev)
	}
}

func TestEvictRefusesWhenTheColdCopyIsWrong(t *testing.T) {
	f := newFixture(t)
	f.write("documents/a.md", "the real bytes", nDays(100))
	f.write("documents/b.md", "other real bytes", nDays(100))
	f.sync()
	// The cold copy of a.md shrank behind the vault's back.
	if err := os.WriteFile(filepath.Join(f.cold.Root, "documents/a.md"), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	ev := f.evict()
	if ev.Failed["documents/a.md"] == "" || len(ev.Evicted) != 1 || ev.Evicted[0].Path != "documents/b.md" {
		t.Fatalf("evict = %+v", ev)
	}
	if got, _ := f.read("documents/a.md"); got != "the real bytes" {
		t.Fatalf("a.md on disk = %q", got)
	}
}

func TestEvictWithDownloadCatchesSameSizeCorruption(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.VerifyByDownload = true })
	f.write("documents/a.md", "the real bytes", nDays(100))
	f.sync()
	if err := os.WriteFile(filepath.Join(f.cold.Root, "documents/a.md"), []byte("the fake bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	ev := f.evict()
	if len(ev.Evicted) != 0 || !strings.Contains(ev.Failed["documents/a.md"], "sha256") {
		t.Fatalf("evict = %+v", ev)
	}
	if got, _ := f.read("documents/a.md"); got != "the real bytes" {
		t.Fatalf("on disk = %q", got)
	}
}

func TestEvictPutsBackAFileChangedInPlace(t *testing.T) {
	f := newFixture(t)
	f.write("documents/a.md", "version one", nDays(100))
	f.sync()
	// Same size, same mtime, other bytes: only hashing the file finds it.
	f.write("documents/a.md", "version two", nDays(100))
	ev := f.evict()
	if len(ev.Evicted) != 0 || ev.Failed["documents/a.md"] == "" {
		t.Fatalf("evict = %+v", ev)
	}
	if got, _ := f.read("documents/a.md"); got != "version two" {
		t.Fatalf("on disk = %q", got)
	}
	if l, _ := f.v.ReadColdListing("documents"); len(l.Files) != 0 {
		t.Fatalf(".cold.json = %+v", l)
	}
	if matches, _ := filepath.Glob(filepath.Join(f.root, "documents", ".*.tmp")); len(matches) != 0 {
		t.Fatalf("temp files left: %v", matches)
	}
}

func TestEvictBudgetLRU(t *testing.T) {
	// Budget 1000 bytes, 10% headroom: eviction aims for 900.
	f := newFixture(t, func(o *Options) { o.BudgetBytes = 1000 })
	big := strings.Repeat("x", 300)
	f.write("notes/a.md", big, nDays(30)) // oldest
	f.write("notes/b.md", big, nDays(20))
	f.write("notes/c.md", big, nDays(10))
	f.write("notes/d.md", big, nDays(1)) // newest
	f.sync()
	ev := f.evict()
	if ev.HotBytesBefore != 1200 || ev.TargetBytes != 900 {
		t.Fatalf("report = %+v", ev)
	}
	if len(ev.Evicted) != 1 || ev.Evicted[0].Path != "notes/a.md" || ev.Evicted[0].Reason != "budget" {
		t.Fatalf("evicted = %+v", ev.Evicted)
	}
	if ev.HotBytesAfter != 900 || ev.OverBudget {
		t.Fatalf("after = %d, over %v", ev.HotBytesAfter, ev.OverBudget)
	}

	// Pinned files are never evicted, even over budget.
	f.write("secrets/k1.age", strings.Repeat("s", 1000), 0)
	f.sync()
	ev = f.evict()
	if !ev.OverBudget {
		t.Fatalf("pinned bytes over budget not reported: %+v", ev)
	}
	if _, ok := f.read("secrets/k1.age"); !ok {
		t.Fatal("a pinned file was evicted")
	}
}

func TestMarkdownFirstAndTheRecordingGate(t *testing.T) {
	f := newFixture(t)
	recording := strings.Repeat("\x00\x01", 4096)
	f.write("media/2026-10-15 - call.m4a", recording, time.Hour)
	f.write("documents/scan.pdf", "scan", nDays(70))
	f.sync()
	if ev := f.evict(); len(ev.Evicted) != 0 {
		t.Fatalf("evicted before transcription: %+v", ev.Evicted)
	}
	// The transcript arrives: the recording goes cold, the transcript stays.
	f.write("media/2026-10-15 - call.txt", "the transcript", 0)
	f.sync()
	ev := f.evict()
	if len(ev.Evicted) != 1 || ev.Evicted[0].Path != "media/2026-10-15 - call.m4a" || ev.Evicted[0].Reason != ReasonTranscribed {
		t.Fatalf("evict = %+v", ev)
	}
	if _, ok := f.read("media/2026-10-15 - call.txt"); !ok {
		t.Fatal("the transcript went cold")
	}
	if e := f.entry("media/2026-10-15 - call.m4a"); !e.HasCompanion {
		t.Fatalf("entry = %+v", e)
	}
	// The scan without a companion goes once it is 90 days unused.
	f.clock.Advance(nDays(21))
	ev = f.evict()
	if len(ev.Evicted) != 1 || ev.Evicted[0].Path != "documents/scan.pdf" {
		t.Fatalf("evict after 91 days = %+v", ev)
	}
	// vault_get brings the recording back, bit for bit.
	p, err := f.v.Get(bg, "media/2026-10-15 - call.m4a", GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if readAll(t, p) != recording {
		t.Fatal("hydrated recording differs")
	}
}

func TestHydrateRoundTrip(t *testing.T) {
	f := newFixture(t)
	f.write("documents/2025/report.pdf", "report bytes", nDays(120))
	f.write("documents/2025/other.pdf", "other bytes", nDays(120))
	f.sync()
	f.evict()
	if _, ok := f.read("documents/2025/report.pdf"); ok {
		t.Fatal("not evicted")
	}
	f.clock.Advance(time.Hour)
	p, err := f.v.Get(bg, "documents/2025/report.pdf", GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(f.root, "documents", "2025", "report.pdf") || readAll(t, p) != "report bytes" {
		t.Fatalf("Get = %s", p)
	}
	fi, err := os.Stat(p)
	if err != nil || !fi.ModTime().Equal(start.Add(-nDays(120))) {
		t.Fatalf("mtime = %v, %v", fi.ModTime(), err)
	}
	e := f.entry("documents/2025/report.pdf")
	if e.Tier != TierBoth || !e.LastAccess.Equal(f.clock.Now()) {
		t.Fatalf("entry = %+v", e)
	}
	l, _ := f.v.ReadColdListing("documents/2025")
	if len(l.Files) != 1 || l.Files[0].Name != "other.pdf" {
		t.Fatalf(".cold.json = %+v", l)
	}
	// The hydrated file is not uploaded again and is held for the job.
	if rep := f.sync(); len(rep.Uploaded) != 0 {
		t.Fatalf("sync after hydrate uploaded %v", rep.Uploaded)
	}
	if ev := f.evict(); len(ev.Evicted) != 0 {
		t.Fatalf("hydrated file evicted at once: %+v", ev)
	}
	// Getting the other one removes the last listing entry and the file.
	if _, err := f.v.Get(bg, "documents/2025/other.pdf", GetOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.root, "documents/2025/.cold.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf(".cold.json left: %v", err)
	}
	// Past the hold and the activity window it goes again.
	f.clock.Advance(nDays(91)) // past the hold and markdown-first
	if ev := f.evict(); len(ev.Evicted) != 2 {
		t.Fatalf("evict after the hold = %+v", ev)
	}
	// A hot file is just returned.
	f.write("documents/hot.md", "hot", 0)
	if p, err := f.v.Get(bg, "documents/hot.md", GetOptions{}); err != nil || readAll(t, p) != "hot" {
		t.Fatalf("Get hot = %v", err)
	}
	if _, err := f.v.Get(bg, "documents/nothing.md", GetOptions{}); !errors.Is(err, ErrNotFound) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Get missing = %v", err)
	}
	if _, err := f.v.Get(bg, "../escape", GetOptions{}); err == nil {
		t.Fatal("Get accepted ../escape")
	}
}

func TestHydrateRefusesABadHash(t *testing.T) {
	f := newFixture(t)
	f.write("documents/a.md", "true content", nDays(100))
	f.sync()
	f.evict()
	if err := os.WriteFile(filepath.Join(f.cold.Root, "documents/a.md"), []byte("tampered bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := f.v.Get(bg, "documents/a.md", GetOptions{})
	var mm *HashMismatchError
	if !errors.As(err, &mm) || !errors.Is(err, ErrHashMismatch) || mm.Want != sum("true content") {
		t.Fatalf("Get = %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(f.root, "documents"))
	for _, e := range entries {
		if e.Name() != ".cold.json" {
			t.Fatalf("left on disk: %s", e.Name())
		}
	}
	if e := f.entry("documents/a.md"); e.Tier != TierCold {
		t.Fatalf("tier = %s", e.Tier)
	}
}

func TestHydrateRespectsBudgetAndHeadroom(t *testing.T) {
	free := int64(1 << 40)
	f := newFixture(t, func(o *Options) {
		o.BudgetBytes = 1000
		o.FreeSpace = func(string) (int64, error) { return free, nil }
	})
	f.write("notes/big.md", strings.Repeat("b", 500), nDays(100))
	f.write("notes/keep.md", strings.Repeat("k", 500), 0)
	f.sync()
	f.evict()
	// 500 hot + 500 = 1000 > 900 (budget less 10%).
	_, err := f.v.Get(bg, "notes/big.md", GetOptions{})
	var ise *InsufficientSpaceError
	if !errors.As(err, &ise) || !errors.Is(err, ErrInsufficientSpace) || ise.Size != 500 || ise.Target != 900 || ise.HotBytes != 500 {
		t.Fatalf("Get = %v", err)
	}
	if _, ok := f.read("notes/big.md"); ok {
		t.Fatal("hydrated despite the refusal")
	}
	// Asked and allowed.
	if _, err := f.v.Get(bg, "notes/big.md", GetOptions{AllowOverBudget: true}); err != nil {
		t.Fatal(err)
	}

	// The disk's own headroom (100 bytes here) counts too.
	g := newFixture(t, func(o *Options) {
		o.BudgetBytes = 1000
		o.FreeSpace = func(string) (int64, error) { return 150, nil }
	})
	g.write("notes/small.md", strings.Repeat("s", 100), nDays(100))
	g.sync()
	g.evict()
	if _, err := g.v.Get(bg, "notes/small.md", GetOptions{}); !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("Get with a full disk = %v", err)
	}
}

func TestConcurrentGetsDownloadOnce(t *testing.T) {
	f := newFixture(t)
	f.write("documents/a.pdf", strings.Repeat("a", 1<<16), nDays(100))
	f.sync()
	f.evict()
	var gets atomic.Int32
	f.cold.Fail = func(op, key string) error {
		if op == "get" {
			gets.Add(1)
		}
		return nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			if _, err := f.v.Get(bg, "documents/a.pdf", GetOptions{}); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if n := gets.Load(); n != 1 {
		t.Fatalf("%d downloads", n)
	}
}

func TestDeletedHotFileKeepsItsColdCopy(t *testing.T) {
	f := newFixture(t)
	f.write("documents/a.md", "keep me", 0)
	f.write("documents/scratch.md", "never uploaded", 0)
	f.sync()
	f.cold.Fail = func(op, key string) error {
		if op == "put" && key == "documents/scratch.md" {
			return errors.New("offline")
		}
		return nil
	}
	f.write("documents/scratch.md", "changed, never uploaded", 0)
	f.sync()
	if err := os.Remove(filepath.Join(f.root, "documents/a.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.root, "documents/scratch.md")); err != nil {
		t.Fatal(err)
	}
	f.cold.Fail = nil
	rep, err := f.v.SyncPaths(bg, []string{"documents/a.md", "documents/scratch.md"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.ColdOnly, []string{"documents/a.md", "documents/scratch.md"}) {
		t.Fatalf("sync = %+v", rep)
	}
	// scratch.md keeps the version cold has, the first one.
	if e := f.entry("documents/scratch.md"); e.Tier != TierCold || e.SHA256 != sum("never uploaded") {
		t.Fatalf("scratch = %+v", e)
	}
	p, err := f.v.Get(bg, "documents/a.md", GetOptions{})
	if err != nil || readAll(t, p) != "keep me" {
		t.Fatalf("Get = %v", err)
	}

	// A file that never reached cold is simply forgotten.
	f.cold.Fail = func(op, key string) error {
		if op == "put" {
			return errors.New("offline")
		}
		return nil
	}
	f.write("tmp/new.md", "x", 0)
	f.sync()
	_ = os.Remove(filepath.Join(f.root, "tmp/new.md"))
	if rep := f.sync(); !slices.Equal(rep.Forgotten, []string{"tmp/new.md"}) {
		t.Fatalf("sync = %+v", rep)
	}
}

// An upload that reached cold but failed a later check must record what
// cold now holds, or a delete leaves a row whose hash no copy matches.
func TestUploadFailingAfterPutRecordsWhatColdHolds(t *testing.T) {
	for name, fail := range map[string]func(f *fixture) func(op, key string) error{
		"verify fails": func(f *fixture) func(op, key string) error {
			return func(op, key string) error {
				if op == "stat" {
					f.cold.Fail = nil
					return errors.New("network blip")
				}
				return nil
			}
		},
		"changed during upload": func(f *fixture) func(op, key string) error {
			return func(op, key string) error {
				if op == "put" {
					f.cold.Fail = nil
					f.clock.Advance(time.Minute)
					f.write("documents/a.md", "B, saved again", 0)
				}
				return nil
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("documents/a.md", "A", 0)
			f.sync()
			f.clock.Advance(time.Minute)
			f.write("documents/a.md", "B, edited", 0)
			f.cold.Fail = fail(f)
			if rep := f.sync(); rep.Failed["documents/a.md"] == "" {
				t.Fatalf("sync = %+v", rep)
			}
			want, _ := f.coldBytes("documents/a.md")
			if err := os.Remove(filepath.Join(f.root, "documents/a.md")); err != nil {
				t.Fatal(err)
			}
			f.sync()
			p, err := f.v.Get(bg, "documents/a.md", GetOptions{})
			if err != nil {
				t.Fatalf("Get = %v", err)
			}
			if got := readAll(t, p); got != want {
				t.Fatalf("Get = %q, cold holds %q", got, want)
			}
		})
	}
}

func TestRemovedFolderSyncsItsRows(t *testing.T) {
	f := newFixture(t)
	f.write("dir/sub/a.md", "a", 0)
	f.write("dir/sub/b.md", "b", 0)
	f.sync()
	if err := os.RemoveAll(filepath.Join(f.root, "dir")); err != nil {
		t.Fatal(err)
	}
	rep, err := f.v.SyncPaths(bg, []string{"dir"})
	if err != nil || len(rep.ColdOnly) != 2 {
		t.Fatalf("SyncPaths(dir) = %+v, %v", rep, err)
	}
	if l, _ := f.v.ReadColdListing("dir/sub"); len(l.Files) != 2 {
		t.Fatalf(".cold.json = %+v", l)
	}
}

func TestRebuildFromColdAndHot(t *testing.T) {
	f := newFixture(t)
	f.write("brains/x/brain.json", `{"id":"x"}`, nDays(200))
	f.write("documents/old.md", "old", nDays(200))
	f.write("documents/old.pdf", "old pdf", nDays(200))
	f.write("documents/new.md", "new", 0)
	f.sync()
	f.evict()
	// The pinned brain.json goes too, as on a fresh core.
	if err := os.Remove(filepath.Join(f.root, "brains/x/brain.json")); err != nil {
		t.Fatal(err)
	}
	f.write("documents/local-only.md", "not uploaded yet", 0)
	before := f.entry("documents/old.pdf")

	// Lose vault.db and every .cold.json.
	if err := f.v.Close(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{f.db, filepath.Join(f.root, "documents/.cold.json")} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	f.open()
	rep, err := f.v.Rebuild(bg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ManifestMissing || rep.Rows != 5 || rep.Cold != 3 || rep.Both != 1 || rep.Hot != 1 || len(rep.UnknownHash) != 0 {
		t.Fatalf("rebuild = %+v", rep)
	}
	if !slices.Equal(rep.PinnedCold, []string{"brains/x/brain.json"}) {
		t.Fatalf("pinned cold = %v", rep.PinnedCold)
	}
	after := f.entry("documents/old.pdf")
	if after.Tier != TierCold || after.SHA256 != before.SHA256 || !after.ModTime.Equal(before.ModTime) {
		t.Fatalf("old.pdf before %+v after %+v", before, after)
	}
	if e := f.entry("documents/new.md"); e.Tier != TierBoth {
		t.Fatalf("new.md = %+v", e)
	}
	if e := f.entry("documents/local-only.md"); e.Tier != TierHot {
		t.Fatalf("local-only.md = %+v", e)
	}
	l, _ := f.v.ReadColdListing("documents")
	if len(l.Files) != 2 || l.Files[0].Name != "old.md" || l.Files[1].SHA256 != sum("old pdf") {
		t.Fatalf(".cold.json = %+v", l)
	}
	if l, _ := f.v.ReadColdListing("brains/x"); len(l.Files) != 1 {
		t.Fatalf("brains/x/.cold.json = %+v", l)
	}
	got, err := f.v.HydratePinned(bg)
	if err != nil || !slices.Equal(got, []string{"brains/x/brain.json"}) {
		t.Fatalf("HydratePinned = %v, %v", got, err)
	}
	if c, _ := f.read("brains/x/brain.json"); c != `{"id":"x"}` {
		t.Fatalf("brain.json = %q", c)
	}
	p, err := f.v.Get(bg, "documents/old.pdf", GetOptions{})
	if err != nil || readAll(t, p) != "old pdf" {
		t.Fatalf("Get after rebuild = %v", err)
	}
}

func TestRebuildWithoutManifestUsesColdListings(t *testing.T) {
	f := newFixture(t)
	f.write("documents/a.md", "aaa", nDays(200))
	f.write("documents/b.md", "bbb", nDays(200))
	f.sync()
	f.evict()
	if err := f.cold.Delete(bg, manifestKey); err != nil {
		t.Fatal(err)
	}
	// b.md's listing entry is lost too: its hash is unknown after.
	l, _ := f.v.ReadColdListing("documents")
	if err := f.v.writeColdListing("documents", l.Files[:1]); err != nil {
		t.Fatal(err)
	}
	if err := f.v.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.db); err != nil {
		t.Fatal(err)
	}
	f.open()
	rep, err := f.v.Rebuild(bg)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.ManifestMissing || !slices.Equal(rep.UnknownHash, []string{"documents/b.md"}) {
		t.Fatalf("rebuild = %+v", rep)
	}
	if e := f.entry("documents/a.md"); e.SHA256 != sum("aaa") {
		t.Fatalf("a.md = %+v", e)
	}
	// An unknown hash is learned on hydration.
	if _, err := f.v.Get(bg, "documents/b.md", GetOptions{}); err != nil {
		t.Fatal(err)
	}
	if e := f.entry("documents/b.md"); e.SHA256 != sum("bbb") || e.Tier != TierBoth {
		t.Fatalf("b.md = %+v", e)
	}
}

func TestRecoverInterruptedEviction(t *testing.T) {
	f := newFixture(t)
	f.write("documents/a.md", "content", nDays(100))
	f.sync()
	if err := f.v.Close(); err != nil {
		t.Fatal(err)
	}
	// A crash right after the move aside, and a half download.
	dir := filepath.Join(f.root, "documents")
	if err := os.Rename(filepath.Join(dir, "a.md"), filepath.Join(dir, ".a.md.vault-evict-0011aa.tmp")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".b.md.vault-hydrate-22bb.tmp"), []byte("part"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.open()
	if c, ok := f.read("documents/a.md"); !ok || c != "content" {
		t.Fatal("the file set aside was not put back")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("documents/ holds %d entries", len(entries))
	}
}

func TestPutLsSearch(t *testing.T) {
	f := newFixture(t)
	e, err := f.v.Put(bg, "brains/family/documents/tax 2025.pdf", strings.NewReader("tax"), ProvenanceOwner)
	if err != nil {
		t.Fatal(err)
	}
	if e.Tier != TierBoth || e.Provenance != ProvenanceOwner || e.SHA256 != sum("tax") {
		t.Fatalf("Put = %+v", e)
	}
	if _, err := f.v.Put(bg, "brains/family/vault.db", strings.NewReader("x"), ProvenanceOwner); err == nil {
		t.Fatal("Put of an ignored name succeeded")
	}
	if _, err := f.v.Put(bg, "x.md", strings.NewReader("x"), "made-up"); err == nil {
		t.Fatal("Put with a bad provenance succeeded")
	}
	f.write("brains/family/notes/a_b.md", "n", 0)
	f.write("brains/work/notes/c.md", "n", 0)
	f.write("media/voice.ogg", "v", 0)
	f.sync()
	if e := f.entry("media/voice.ogg"); e.Provenance != ProvenanceUntrusted {
		t.Fatalf("media provenance = %s", e.Provenance)
	}

	ls, err := f.v.Ls(bg, "brains/family")
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 2 || !ls[0].IsDir || ls[0].Path != "brains/family/documents" || ls[1].Path != "brains/family/notes" {
		t.Fatalf("Ls = %+v", ls)
	}
	root, _ := f.v.Ls(bg, "")
	if len(root) != 2 || root[0].Path != "brains" || root[1].Path != "media" {
		t.Fatalf("Ls root = %+v", root)
	}
	res, err := f.v.Search(bg, "TAX pdf", 10)
	if err != nil || len(res) != 1 || res[0].Path != "brains/family/documents/tax 2025.pdf" {
		t.Fatalf("Search = %+v, %v", res, err)
	}
	// LIKE wildcards in the query are literal.
	if res, _ := f.v.Search(bg, "a_b", 10); len(res) != 1 {
		t.Fatalf("Search a_b = %+v", res)
	}
	if res, _ := f.v.Search(bg, "%", 10); len(res) != 0 {
		t.Fatalf("Search %% = %+v", res)
	}
}

func TestScope(t *testing.T) {
	f := newFixture(t)
	f.write("brains/family/notes/a.md", "a", 0)
	f.write("brains/familyx/notes/b.md", "b", 0)
	f.write("documents/family/c.pdf", "c", 0)
	f.write("documents/work/d.pdf", "d", 0)
	f.sync()
	s, err := f.v.Scoped([]string{"brains/family", "documents/family/"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.v.Scoped([]string{"../x"}); err == nil {
		t.Fatal("Scoped accepted ../x")
	}
	for rel, want := range map[string]bool{
		"brains/family/notes/a.md":  true,
		"brains/family":             true,
		"brains/familyx/notes/b.md": false,
		"documents/work/d.pdf":      false,
		"brains/family/../work/x":   false,
		"/brains/family/a":          false,
	} {
		if got := s.Allowed(rel); got != want {
			t.Errorf("Allowed(%q) = %v", rel, got)
		}
	}
	if _, err := s.Get(bg, "documents/work/d.pdf", GetOptions{}); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("Get outside = %v", err)
	}
	if _, err := s.Put(bg, "brains/work/x.md", strings.NewReader("x"), ProvenanceAgent); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("Put outside = %v", err)
	}
	if _, err := s.Stat(bg, "brains/familyx/notes/b.md"); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("Stat outside = %v", err)
	}
	if p, err := s.Get(bg, "documents/family/c.pdf", GetOptions{}); err != nil || readAll(t, p) != "c" {
		t.Fatalf("Get inside = %v", err)
	}
	root, err := s.Ls(bg, "")
	if err != nil || len(root) != 2 || root[0].Path != "brains" || root[1].Path != "documents" {
		t.Fatalf("Ls root = %+v, %v", root, err)
	}
	docs, err := s.Ls(bg, "documents")
	if err != nil || len(docs) != 1 || docs[0].Path != "documents/family" {
		t.Fatalf("Ls documents = %+v, %v", docs, err)
	}
	if _, err := s.Ls(bg, "media"); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("Ls media = %v", err)
	}
	inside, err := s.Ls(bg, "brains/family/notes")
	if err != nil || len(inside) != 1 {
		t.Fatalf("Ls inside = %+v, %v", inside, err)
	}
	res, err := s.Search(bg, "notes", 10)
	if err != nil || len(res) != 1 || res[0].Path != "brains/family/notes/a.md" {
		t.Fatalf("Search = %+v, %v", res, err)
	}
	none, _ := f.v.Scoped(nil)
	if res, _ := none.Search(bg, "notes", 10); len(res) != 0 || none.Allowed("brains/family/notes/a.md") {
		t.Fatal("an empty scope sees something")
	}
}

func TestOpenThreadsKeepFilesHot(t *testing.T) {
	var refs []string
	f := newFixture(t, func(o *Options) {
		o.OpenThreadPaths = func(context.Context) ([]string, error) { return refs, nil }
	})
	f.write("documents/contract.pdf", "c", nDays(200))
	f.sync()
	refs = []string{"documents/contract.pdf"}
	if ev := f.evict(); len(ev.Evicted) != 0 {
		t.Fatalf("evicted a file an open thread uses: %+v", ev)
	}
	refs = nil
	if ev := f.evict(); len(ev.Evicted) != 1 {
		t.Fatalf("evict after the thread closed = %+v", ev)
	}
}

func TestReopenKeepsTheIndexAndSchema(t *testing.T) {
	f := newFixture(t)
	f.write("a.md", "a", 0)
	f.sync()
	if err := f.v.Close(); err != nil {
		t.Fatal(err)
	}
	f.open()
	if e := f.entry("a.md"); e.Tier != TierBoth {
		t.Fatalf("after reopen = %+v", e)
	}
	var version int
	if err := f.v.db.QueryRow(`SELECT version FROM vault_version`).Scan(&version); err != nil || version != len(migrations) {
		t.Fatalf("version = %d, %v", version, err)
	}
	fi, err := os.Stat(f.db)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("vault.db mode = %v, %v", fi.Mode(), err)
	}
}
