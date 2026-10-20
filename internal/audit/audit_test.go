package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendAndRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), DirName)
	now := time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)
	l := &Log{Dir: dir, Now: func() time.Time { return now }}
	l.Append(Entry{Kind: RunStart, Thread: "a", Brain: "core", Run: "r1"})
	now = now.Add(2 * time.Hour) // October
	l.Append(Entry{Kind: ApprovalRequested, Thread: "a", Brain: "core", Run: "r1", ID: "x", Detail: "Submit the return with password: hunter2 and key " + strings.Repeat("a", 40)})
	l.Append(Entry{Kind: RunStart, Thread: "b", Brain: "family", Run: "r2"})

	all, err := Read(dir, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].Run != "r2" || all[2].TS != "2026-09-30T23:00:00Z" {
		t.Fatalf("newest first across months: %+v", all)
	}
	if d := all[1].Detail; strings.Contains(d, "hunter2") || strings.Contains(d, strings.Repeat("a", 40)) {
		t.Errorf("a secret reached the log: %q", d)
	}
	got, _ := Read(dir, Filter{Thread: "a", Kind: RunStart})
	if len(got) != 1 || got[0].Run != "r1" {
		t.Errorf("filter: %+v", got)
	}
	got, _ = Read(dir, Filter{Months: 1})
	if len(got) != 2 {
		t.Errorf("one month: %+v", got)
	}
	got, _ = Read(dir, Filter{Limit: 1})
	if len(got) != 1 {
		t.Errorf("limit: %+v", got)
	}
	got, _ = Read(dir, Filter{Query: "SUBMIT"})
	if len(got) != 1 {
		t.Errorf("query: %+v", got)
	}
	// A broken line and a huge one are skipped.
	f, _ := os.OpenFile(filepath.Join(dir, "2026-10.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("{broken\n" + `{"kind":"x","detail":"` + strings.Repeat("y", 200<<10) + "\"}\n" + `{"kind":"lockdown"}` + "\n")
	f.Close()
	got, _ = Read(dir, Filter{})
	if len(got) != 4 || got[0].Kind != Lockdown {
		t.Errorf("after junk: %+v", got[0])
	}
	if got, err := Read(filepath.Join(dir, "none"), Filter{}); err != nil || len(got) != 0 {
		t.Errorf("a missing folder: %v %v", got, err)
	}
}
