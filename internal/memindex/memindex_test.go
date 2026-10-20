package memindex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/history"
)

const owner = "15551234567"

// testBrain is a brain folder in a temp dir, with its index beside it.
func testBrain(t *testing.T) Brain {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "brain")
	if err := os.MkdirAll(filepath.Join(dir, "memories"), 0o700); err != nil {
		t.Fatal(err)
	}
	return Brain{ID: "main", Dir: dir, Index: filepath.Join(root, "data", "memory", "main.db"), Owners: []string{owner}}
}

func write(t *testing.T, b Brain, rel, content string) {
	t.Helper()
	p := filepath.Join(b.Dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// appendHistory adds lines to b's history as the bridge writes them.
func appendHistory(t *testing.T, b Brain, entries ...history.Entry) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(b.Dir, history.FileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, e := range entries {
		if e.TS == "" {
			e.TS = "2026-10-19T09:00:00Z"
		}
		data, _ := json.Marshal(e)
		if _, err := f.Write(append(data, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}

func refs(hits []Hit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, string(h.Provenance)+" "+h.Ref())
	}
	return out
}

func TestHistoryProvenance(t *testing.T) {
	b := testBrain(t)
	dm := owner + "@s.whatsapp.net"
	group := "120363000000000000@g.us"
	appendHistory(t, b,
		history.Entry{From: owner, Chat: dm, Type: history.In, ID: "3EB0OWNER", Text: "zebra owner typed"},
		history.Entry{From: owner, Chat: dm, Type: history.In, Text: "zebra handed in through ingest"},
		history.Entry{From: owner, Chat: group, Type: history.In, ID: "3EB0GROUP", Text: "zebra owner in a group"},
		history.Entry{From: "15559876543", Chat: "15559876543@s.whatsapp.net", Type: history.In, ID: "3EB0OTHER", Text: "zebra someone else"},
		history.Entry{From: "main", Chat: dm, Type: history.Out, Text: "zebra reply"},
		history.Entry{From: "script", Chat: dm, Type: history.Out, Via: "script", Text: "zebra script"},
		history.Entry{From: "intent", Chat: dm, Type: history.In, Text: "zebra intent fired"},
		// A task the core brain delegated carries the delegation's id, and
		// a brain id could be all digits: still not the owner's words.
		history.Entry{From: owner, Chat: "delegate:r1", Type: history.In, ID: "d1", Text: "zebra delegated task"},
		history.Entry{From: owner, Chat: dm, Type: history.Reset},
	)
	hits, err := Search(context.Background(), b, "zebra", SearchOptions{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Provenance{}
	for _, h := range hits {
		got[h.Text] = h.Provenance
	}
	want := map[string]Provenance{
		"zebra owner typed":              Owner,
		"zebra handed in through ingest": Untrusted,
		"zebra owner in a group":         Untrusted,
		"zebra someone else":             Untrusted,
		"zebra reply":                    System,
		"zebra script":                   System,
		"zebra intent fired":             System,
		"zebra delegated task":           Untrusted,
	}
	for text, p := range want {
		if got[text] != p {
			t.Errorf("%q: %q, want %q", text, got[text], p)
		}
	}
	if len(hits) != len(want) {
		t.Errorf("%d hits, want %d: %v", len(hits), len(want), refs(hits))
	}
	if h := hits[slices.IndexFunc(hits, func(h Hit) bool { return h.Provenance == Owner })]; h.Ref() != "conversation-history.jsonl#3EB0OWNER" {
		t.Errorf("owner line's anchor %q", h.Ref())
	}
}

func TestNotesChunksAndComments(t *testing.T) {
	b := testBrain(t)
	write(t, b, "memories/2026-10-19.md", `# Monday

## Dentist
Appointment moved to Friday 10:00.
<!-- triggers: tooth, dentist; importance: 4 -->

## Groceries
Buy oat milk.
`)
	write(t, b, "memories/web/boats.md", `<!-- source: https://example.com/boats -->
# Boat prices
The harbour fee is 400 a year.
`)
	write(t, b, "memories/core.md", `# Core
## Harbour
The harbour fee is 380 a year, the owner said. <!-- source: conversation-history.jsonl#3EB0FEE -->
`)
	write(t, b, "transcripts/2026-10-15 - call.txt", "The harbour fee might go up next year.\n\nUnrelated paragraph.")
	appendHistory(t, b, history.Entry{From: owner, Chat: owner + "@s.whatsapp.net", Type: history.In, ID: "3EB0FEE", Text: "the harbour fee is 380 a year"})

	hits, err := Search(context.Background(), b, "tooth", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Ref() != "memories/2026-10-19.md#dentist" || hits[0].Importance != 4 || !slices.Equal(hits[0].Triggers, []string{"tooth", "dentist"}) {
		t.Fatalf("trigger search: %+v", hits)
	}
	if strings.Contains(hits[0].Text, "<!--") {
		t.Errorf("the notes comment stays in the text: %q", hits[0].Text)
	}
	if day := hits[0].TS.Format("2006-01-02"); day != "2026-10-19" {
		t.Errorf("a daily note is dated %s, want its name's day", day)
	}

	hits, err = Search(context.Background(), b, "harbour fee", SearchOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	got := refs(hits)
	slices.Sort(got)
	want := []string{
		"owner conversation-history.jsonl#3EB0FEE",
		"owner memories/core.md#harbour",
		"untrusted memories/web/boats.md#boat-prices",
		"untrusted transcripts/2026-10-15 - call.txt#p1",
	}
	if !slices.Equal(got, want) {
		t.Errorf("hits\n%v\nwant\n%v", got, want)
	}
}

// TestGateWebFactNeverRecalled is M7's gate in the index: a fact the
// brain took from a web page is found when it searches, labelled, but
// never recalled into a prompt; the same fact typed by the owner in a DM
// is, with its source anchor.
func TestGateWebFactNeverRecalled(t *testing.T) {
	b := testBrain(t)
	ctx := context.Background()
	write(t, b, "memories/2026-10-19.md", `# Monday
## Reading
The ferry to Lindau leaves at 07:45 on weekdays.
<!-- source: https://example.com/ferry -->
`)
	appendHistory(t, b, history.Entry{From: "15559876543", Chat: "120363000000000000@g.us", Type: history.In, ID: "3EB0GRP", Text: "the ferry to Lindau leaves at 07:45"})

	recalled, err := Recall(ctx, b, "When does the ferry to Lindau leave?", RecallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recalled) != 0 {
		t.Fatalf("recalled untrusted chunks: %v", refs(recalled))
	}
	found, err := Search(ctx, b, "ferry Lindau", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 || found[0].Provenance != Untrusted || found[1].Provenance != Untrusted {
		t.Fatalf("search: %v, want both untrusted", refs(found))
	}

	// Typed by the owner, in a DM: recalled, with the line's anchor.
	appendHistory(t, b, history.Entry{From: owner, Chat: owner + "@s.whatsapp.net", Type: history.In, ID: "3EB0MINE", Text: "Note: the ferry to Lindau leaves at 07:45 on weekdays"})
	recalled, err = Recall(ctx, b, "When does the ferry to Lindau leave?", RecallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := refs(recalled); !slices.Equal(got, []string{"owner conversation-history.jsonl#3EB0MINE"}) {
		t.Fatalf("recalled %v, want the owner's line alone", got)
	}
}

func TestIncrementalSync(t *testing.T) {
	b := testBrain(t)
	ctx := context.Background()
	write(t, b, "memories/a.md", "# A\nalpha\n")
	appendHistory(t, b, history.Entry{From: owner, Chat: owner + "@s.whatsapp.net", Type: history.In, ID: "1", Text: "first kiwi"})
	st, err := Sync(ctx, b)
	if err != nil || st.Changed != 2 || st.Chunks != 2 {
		t.Fatalf("first sync %+v %v", st, err)
	}
	if st, _ := Sync(ctx, b); st.Changed != 0 {
		t.Errorf("a sync without changes indexed %d sources again", st.Changed)
	}
	if stale, err := Stale(ctx, b); err != nil || len(stale) != 0 {
		t.Errorf("stale after sync: %v %v", stale, err)
	}

	// An append is read from where the index stopped.
	appendHistory(t, b, history.Entry{From: owner, Chat: owner + "@s.whatsapp.net", Type: history.In, ID: "2", Text: "second kiwi"})
	if stale, _ := Stale(ctx, b); !slices.Equal(stale, []string{history.FileName}) {
		t.Errorf("stale = %v, want the history", stale)
	}
	st, _ = Sync(ctx, b)
	if st.Chunks != 3 {
		t.Errorf("after an append %d chunks, want 3", st.Chunks)
	}
	// A half-written line waits for its newline.
	f, _ := os.OpenFile(filepath.Join(b.Dir, history.FileName), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"ts":"2026-10-19T09:00:00Z","from":"` + owner + `","chat":"x","text":"third kiwi","type":"in"`)
	f.Close()
	if st, _ = Sync(ctx, b); st.Chunks != 3 {
		t.Errorf("a half-written line was indexed: %d chunks", st.Chunks)
	}
	appendHistory(t, b) // nothing
	f, _ = os.OpenFile(filepath.Join(b.Dir, history.FileName), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(",\"id\":\"3\"}\n")
	f.Close()
	if st, _ = Sync(ctx, b); st.Chunks != 4 {
		t.Errorf("the finished line: %d chunks, want 4", st.Chunks)
	}
	// A rewrite (a deleted message) indexes the history afresh.
	if err := os.WriteFile(filepath.Join(b.Dir, history.FileName), []byte(`{"ts":"2026-10-19T09:00:00Z","from":"x","chat":"y","text":"only line","type":"in"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, _ = Sync(ctx, b); st.Chunks != 2 {
		t.Errorf("after a rewrite %d chunks, want 2", st.Chunks)
	}
	hits, _ := Search(ctx, b, "kiwi", SearchOptions{})
	if len(hits) != 0 {
		t.Errorf("rewritten lines still found: %v", refs(hits))
	}
	// A note removed goes from the index.
	os.Remove(filepath.Join(b.Dir, "memories", "a.md"))
	if st, _ = Sync(ctx, b); st.Removed != 1 || st.Chunks != 1 {
		t.Errorf("after removing a note: %+v", st)
	}
	// Reindex builds the same.
	if st, err = Reindex(ctx, b); err != nil || st.Chunks != 1 || st.Changed != 1 {
		t.Errorf("reindex %+v %v", st, err)
	}
}

func TestOwnersChangeReclassifies(t *testing.T) {
	b := testBrain(t)
	ctx := context.Background()
	appendHistory(t, b, history.Entry{From: "15559876543", Chat: "15559876543@s.whatsapp.net", Type: history.In, ID: "A", Text: "mango"})
	hits, _ := Search(ctx, b, "mango", SearchOptions{})
	if hits[0].Provenance != Untrusted {
		t.Fatalf("%v", refs(hits))
	}
	b.Owners = append(b.Owners, "+1 555 987 6543")
	hits, _ = Search(ctx, b, "mango", SearchOptions{})
	if hits[0].Provenance != Owner {
		t.Errorf("after the number was allowed: %v", refs(hits))
	}
}

func TestSymlinksLeftOut(t *testing.T) {
	b := testBrain(t)
	secret := filepath.Join(t.TempDir(), "secret.md")
	os.WriteFile(secret, []byte("# Secret\npapaya\n"), 0o600)
	if err := os.Symlink(secret, filepath.Join(b.Dir, "memories", "link.md")); err != nil {
		t.Skip(err)
	}
	if err := os.Symlink(filepath.Dir(secret), filepath.Join(b.Dir, "memories", "linkdir")); err != nil {
		t.Fatal(err)
	}
	hits, err := Search(context.Background(), b, "papaya", SearchOptions{})
	if err != nil || len(hits) != 0 {
		t.Errorf("found through a symlink: %v %v", refs(hits), err)
	}
}

func TestRecallBudget(t *testing.T) {
	b := testBrain(t)
	long := strings.Repeat("walnut ", 400)
	write(t, b, "memories/a.md", "# A\n"+long+"\n# B\n"+long+"\n# C\n"+long)
	hits, err := Recall(context.Background(), b, "walnut", RecallOptions{TopK: 5, MaxChars: 1000})
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, h := range hits {
		total += len(h.Text)
	}
	if total > 1000+3*len("…") || len(hits) == 0 {
		t.Errorf("%d hits, %d chars, want at most 1000", len(hits), total)
	}
	hits, _ = Recall(context.Background(), b, "walnut", RecallOptions{TopK: 2, MaxChars: 100000})
	if len(hits) != 2 {
		t.Errorf("top 2: %d hits", len(hits))
	}
}

func TestMatchQuery(t *testing.T) {
	for _, empty := range []string{"", "the and of", "?!"} {
		if got := matchQuery(empty); got != "" {
			t.Errorf("%q: %s", empty, got)
		}
	}
	if got := matchQuery(`When "is" the NEAR(x) dentist?`); got != `"near" OR "dentist"` {
		t.Errorf("got %s", got)
	}
	if got := matchQuery("Zahnarzt am Freitag"); got != `"zahnarzt" OR "freitag"` {
		t.Errorf("got %s", got)
	}
}

func FuzzChunkMarkdown(f *testing.F) {
	f.Add("# A\ntext <!-- triggers: x; importance: 9 -->\n## B\n```\n# not a heading\n```\n")
	f.Add("<!-- source: history#1 -->\n# Ü ber\n")
	f.Fuzz(func(t *testing.T, s string) {
		for _, c := range chunkMarkdown("memories/x.md", []byte(s), time.Unix(0, 0)) {
			if c.text == "" || len(c.text) > maxChunk || c.importance < 0 || c.importance > 5 || c.anchor == "" {
				t.Fatalf("chunk %+v", c)
			}
		}
		if q := matchQuery(s); strings.Count(q, `"`)%2 != 0 {
			t.Fatalf("unbalanced query %q", q)
		}
	})
}

func FuzzHistoryChunk(f *testing.F) {
	f.Add(`{"ts":"2026-01-02T09:00:00Z","from":"15551234567","chat":"15551234567@s.whatsapp.net","text":"hi","type":"in","id":"X"}`)
	f.Fuzz(func(t *testing.T, line string) {
		c, ok := historyChunk(map[string]bool{owner: true}, 1, []byte(line))
		if ok && (!ValidProvenance(c.prov) || c.anchor == "") {
			t.Fatalf("%+v", c)
		}
	})
}

func TestSelfTest(t *testing.T) {
	if err := SelfTest(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
}
