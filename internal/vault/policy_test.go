package vault

import (
	"testing"
	"time"
)

func TestMatchPattern(t *testing.T) {
	cases := []struct {
		pat, rel string
		want     bool
	}{
		{"config.json", "config.json", true},
		{"config.json", "brains/x/config.json", false},
		{"secrets/**", "secrets/a.age", true},
		{"secrets/**", "secrets/deep/b.age", true},
		{"secrets/**", "notsecrets/a", false},
		{"**/brain.json", "brain.json", true},
		{"**/brain.json", "brains/family/brain.json", true},
		{"**/skills/**", "brains/x/skills/plaud/SKILL.md", true},
		{"**/memories/**/*.md", "brains/x/memories/core.md", true},
		{"**/memories/**/*.md", "brains/x/memories/2026/10-01.md", true},
		{"**/memories/**/*.md", "brains/x/memories/photo.jpg", false},
		{"bridge.db-*", "bridge.db-wal", true},
	}
	for _, c := range cases {
		if got := MatchPattern(c.pat, c.rel); got != c.want {
			t.Errorf("MatchPattern(%q, %q) = %v, want %v", c.pat, c.rel, got, c.want)
		}
	}
}

func TestClassify(t *testing.T) {
	p := DefaultPolicy()
	now := start
	ago := func(d int) time.Time { return now.Add(-nDays(d)) }
	cases := []struct {
		name   string
		facts  Facts
		class  Class
		reason string
	}{
		{"pinned config", Facts{Path: "config.json", LastAccess: ago(400)}, ClassPinned, ReasonPinned},
		{"pinned brain prompt", Facts{Path: "brains/a/brain-prompt.md", LastAccess: ago(400)}, ClassPinned, ReasonPinned},
		{"pinned memory", Facts{Path: "brains/a/memories/core.md", LastAccess: ago(400)}, ClassPinned, ReasonPinned},
		{"pinned history", Facts{Path: "brains/a/conversation-history.jsonl", LastAccess: ago(400)}, ClassPinned, ReasonPinned},
		{"audit this month", Facts{Path: "audit/2026-10/thread.jsonl", LastAccess: ago(1)}, ClassPinned, ReasonAuditCurrent},
		{"audit file named by month", Facts{Path: "audit/2026-10.jsonl", LastAccess: ago(1)}, ClassPinned, ReasonAuditCurrent},
		{"audit last month", Facts{Path: "audit/2026-09/thread.jsonl", LastAccess: ago(1)}, ClassColdEligible, ReasonAuditPast},
		{"open thread beats age", Facts{Path: "documents/old.pdf", LastAccess: ago(400), InOpenThread: true}, ClassActive, ReasonOpenThread},
		{"hydrated hold", Facts{Path: "documents/old.pdf", LastAccess: ago(400), HydratedAt: ago(6)}, ClassActive, ReasonHydrated},
		{"hydrated hold over", Facts{Path: "documents/old.pdf", LastAccess: ago(400), HydratedAt: ago(8)}, ClassColdEligible, ReasonInactive},
		{"transcribed media goes at once", Facts{Path: "media/rec.m4a", LastAccess: ago(0), HasCompanion: true}, ClassColdEligible, ReasonTranscribed},
		{"untranscribed media recent", Facts{Path: "media/rec.m4a", LastAccess: ago(0)}, ClassActive, ReasonRecent},
		{"young run log", Facts{Path: "runs/abc.log", LastAccess: ago(10)}, ClassActive, ReasonRecent},
		{"old run log", Facts{Path: "runs/abc.log", LastAccess: ago(15)}, ClassColdEligible, ReasonOldRunLog},
		{"workspace", Facts{Path: "workspaces/t1.tar.zst", LastAccess: ago(0)}, ClassColdEligible, ReasonWorkspace},
		{"workspace in the data dir", Facts{Path: "vault/workspaces/t1.tar.zst", LastAccess: ago(0)}, ClassColdEligible, ReasonWorkspace},
		{"recent document", Facts{Path: "documents/a.pdf", LastAccess: ago(59)}, ClassActive, ReasonRecent},
		{"markdown-first holds a binary", Facts{Path: "documents/a.pdf", LastAccess: ago(70)}, ClassActive, ReasonMarkdownFirst},
		{"markdown-first ends at 90 days", Facts{Path: "documents/a.pdf", LastAccess: ago(90)}, ClassColdEligible, ReasonInactive},
		{"binary with companion after 60 days", Facts{Path: "documents/a.pdf", LastAccess: ago(61), HasCompanion: true}, ClassColdEligible, ReasonInactive},
		{"text needs no companion", Facts{Path: "documents/notes.md", LastAccess: ago(61)}, ClassColdEligible, ReasonInactive},
	}
	for _, c := range cases {
		d := p.Classify(c.facts, now)
		if d.Class != c.class || d.Reason != c.reason || d.Pinned != (c.class == ClassPinned) {
			t.Errorf("%s: Classify = %+v, want %s/%s", c.name, d, c.class, c.reason)
		}
	}
}

// The audit log names its files by the UTC month, so around the month
// change a process east or west of UTC must still keep the file the log
// writes now, and may let go of the one it no longer writes (#668).
func TestClassifyAuditMonthIsTheUTCMonth(t *testing.T) {
	p := DefaultPolicy()
	cet := time.FixedZone("CET", 3600)
	cest := time.FixedZone("CEST", 2*3600)
	est := time.FixedZone("EST", -5*3600)
	cases := []struct {
		name   string
		now    time.Time
		path   string
		class  Class
		reason string
	}{
		// East of UTC, after local midnight on the 1st: still November in UTC.
		{"east: the live file", time.Date(2026, 12, 1, 0, 30, 0, 0, cet), "audit/2026-11.jsonl", ClassPinned, ReasonAuditCurrent},
		{"east: next month", time.Date(2026, 12, 1, 0, 30, 0, 0, cet), "audit/2026-12.jsonl", ClassPinned, ReasonAuditCurrent},
		{"east: the month before", time.Date(2026, 12, 1, 0, 30, 0, 0, cet), "audit/2026-10.jsonl", ClassColdEligible, ReasonAuditPast},
		{"east in summer: the live file", time.Date(2026, 7, 1, 1, 30, 0, 0, cest), "audit/2026-06.jsonl", ClassPinned, ReasonAuditCurrent},
		{"east over the year: the live file", time.Date(2027, 1, 1, 0, 30, 0, 0, cet), "audit/2026-12.jsonl", ClassPinned, ReasonAuditCurrent},
		{"east, month folder: the live file", time.Date(2026, 12, 1, 0, 30, 0, 0, cet), "audit/2026-11/thread.jsonl", ClassPinned, ReasonAuditCurrent},
		// West of UTC, before local midnight: already December in UTC.
		{"west: the live file", time.Date(2026, 11, 30, 20, 0, 0, 0, est), "audit/2026-12.jsonl", ClassPinned, ReasonAuditCurrent},
		{"west: the month the log left", time.Date(2026, 11, 30, 20, 0, 0, 0, est), "audit/2026-11.jsonl", ClassColdEligible, ReasonAuditPast},
	}
	for _, c := range cases {
		d := p.Classify(Facts{Path: c.path, LastAccess: c.now}, c.now)
		if d.Class != c.class || d.Reason != c.reason || d.Pinned != (c.class == ClassPinned) {
			t.Errorf("%s: Classify(%s at %s) = %+v, want %s/%s", c.name, c.path, c.now.Format(time.RFC3339), d, c.class, c.reason)
		}
	}
}

func TestDefaultCompanions(t *testing.T) {
	got := DefaultCompanions("media/2026-10-01 - call.m4a")
	want := map[string]bool{"media/2026-10-01 - call.txt": true, "media/2026-10-01 - call.md": true, "media/2026-10-01 - call.m4a.md": true}
	n := 0
	for _, c := range got {
		if want[c] {
			n++
		}
	}
	if n != len(want) {
		t.Fatalf("DefaultCompanions = %v", got)
	}
	if IsBinary("a.md") || IsBinary("b.TXT") || !IsBinary("c.m4a") || !IsBinary("noext") {
		t.Fatal("IsBinary misclassifies")
	}
}
