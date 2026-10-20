package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/intent"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/toolcli"
	"github.com/Icewater1337/fridolin/internal/trigger"
)

// writeHistory appends entries to the history of the brain in dir, as
// the bridge writes them.
func writeHistory(t *testing.T, dir string, entries ...history.Entry) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, history.FileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, e := range entries {
		data, _ := json.Marshal(e)
		if _, err := f.Write(append(data, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}

// TestMemoryGate is M7's gate, end to end: a fact the brain noted from a
// web page, and the same fact from a group member, never reach a run's
// prompt unasked; the same fact typed by the owner in a direct chat on
// WhatsApp does, in <memory_recall>, with its source anchor. A brain
// without memory.inject gets no recall at all.
func TestMemoryGate(t *testing.T) {
	runs := fakeRuns(t)
	h := newHarness(t, "")
	dir := h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true, Groups: []string{harnessGroup}}, Memory: &manifest.Memory{Inject: true}})
	if err := os.WriteFile(filepath.Join(dir, manifest.DefaultPromptFile), []byte(goldenBrainPrompt), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memories", "2026-10-19.md"), []byte("# Monday\n## Reading\nThe ferry to Lindau leaves at 07:45 on weekdays.\n<!-- source: https://example.com/ferry -->\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	writeHistory(t, dir, history.Entry{TS: old, From: "15559876543", Chat: harnessGroup, Type: history.In, ID: "3EB0GROUP", Text: "the ferry to Lindau leaves at 07:45"})

	ask := func() string {
		t.Helper()
		before := len(runs())
		h.Answer(inbound{JID: harnessDM, Text: "When does the ferry to Lindau leave?"})
		h.WaitFor("the run", func() bool { return len(runs()) > before })
		return runs()[len(runs())-1].Prompt
	}
	if p := ask(); strings.Contains(p, "<memory_recall>\n") {
		t.Fatalf("untrusted notes reached the prompt:\n%s", p)
	}
	// The run's reply goes into the history after the run; wait for it, so
	// that the lines written below come after it and the prompt pinned
	// next is the same on every run.
	h.WaitFor("the first reply in the history", func() bool {
		data, _ := os.ReadFile(filepath.Join(dir, "conversation-history.jsonl"))
		return strings.Contains(string(data), `"type":"out"`)
	})

	// The owner typed it, a month ago, in their DM on WhatsApp; ten
	// messages since keep it out of the chat history the prompt shows.
	writeHistory(t, dir, history.Entry{TS: old, From: "15551234567", Chat: harnessDM, Type: history.In, ID: "3EB0OWNER", Text: "Note: the ferry to Lindau leaves at 07:45 on weekdays"})
	for i := range 10 {
		writeHistory(t, dir, history.Entry{TS: old, From: "15551234567", Chat: harnessDM, Type: history.In, ID: "3EB0FILL" + string(rune('A'+i)), Text: "ok"})
	}
	p := ask()
	block, ok := between(p, "<memory_recall>\n", "</memory_recall>")
	if !ok {
		t.Fatalf("no recall for the owner's own message:\n%s", p)
	}
	// What a run is told with its recall, pinned (TestGoldenRuns pins it
	// without, unchanged).
	assertGolden(t, "run/prompt-memory-recall.golden", []byte(clockRE.ReplaceAllString(h.scrubber().text(p), "<WHEN>")+"\n"))
	if !strings.Contains(block, `"source":"conversation-history.jsonl#3EB0OWNER","provenance":"owner"`) {
		t.Errorf("the recall lacks the owner's line with its anchor:\n%s", block)
	}
	if strings.Contains(block, "example.com") || strings.Contains(block, "3EB0GROUP") || strings.Contains(block, "untrusted") && !strings.Contains(block, "Only those two") {
		t.Errorf("untrusted chunks in the recall:\n%s", block)
	}

	// Off, nothing is recalled.
	m := h.Approved()["main"]
	m.Memory = nil
	if w := h.Do("PUT", "/api/brains/main", map[string]any{"manifest": m}); w.Code != 200 {
		t.Fatalf("turn inject off: %d %s", w.Code, w.Body.String())
	}
	if p := ask(); strings.Contains(p, "<memory_recall>\n") {
		t.Errorf("a brain without memory.inject got a recall:\n%s", p)
	}
}

// between is what s holds between from and to.
func between(s, from, to string) (string, bool) {
	_, rest, ok := strings.Cut(s, from)
	if !ok {
		return "", false
	}
	in, _, ok := strings.Cut(rest, to)
	return in, ok
}

// TestStandingIntents: only a run that answers the owner's own direct
// message sets or removes a standing intent through the brain API; any run
// of the brain lists them and searches its memory, labelled. The dashboard
// lists them too.
func TestStandingIntents(t *testing.T) {
	tb, url := serveTestBridge(t, "")
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A", Routing: manifest.Routing{HandlesDMs: true, Groups: []string{apiTestGroup}}})
	owner, revokeOwner := tb.runTokens.mintFor("a", chat.Inbound{JID: apiTestDM, Sender: "15551234567", Source: chat.SourceWhatsApp, Text: "remind me"})
	defer revokeOwner()
	member, revokeMember := tb.runTokens.mintFor("a", chat.Inbound{JID: apiTestGroup, Sender: "15559876543", Source: chat.SourceWhatsApp})
	defer revokeMember()
	ownerInGroup, revokeOIG := tb.runTokens.mintFor("a", chat.Inbound{JID: apiTestGroup, Sender: "15551234567", Source: chat.SourceWhatsApp})
	defer revokeOIG()
	handed, revokeHanded := tb.runTokens.mintFor("a", chat.Inbound{JID: apiTestDM, Brain: "a", Sender: "15551234567", Source: chat.SourceIngest})
	defer revokeHanded()
	fired, revokeFired := tb.runTokens.mintFor("a", chat.Inbound{JID: apiTestDM, Brain: "a", Sender: intent.SenderIntent, Source: chat.SourceSchedule})
	defer revokeFired()

	for name, tok := range map[string]string{"a group member": member, "the owner in a group": ownerInGroup, "files handed in": handed, "a fired intent": fired} {
		code, doc := brainTool(t, toolcli.Intent, url, tok, "add", "--when", "daily 08:00", "water the plants")
		if code == 0 || !strings.Contains(doc["error"].(string), "only a run that answers the owner") {
			t.Errorf("%s set an intent: %d %v", name, code, doc)
		}
	}
	code, doc := brainTool(t, toolcli.Intent, url, owner, "add", "--when", "weekly sun 18:00", "Send the owner the week's plan")
	if code != 0 || doc["id"] == "" || doc["chat"] != apiTestDM || doc["created_by"] != "15551234567" {
		t.Fatalf("the owner's intent: %d %v", code, doc)
	}
	id := doc["id"].(string)
	if code, doc := brainTool(t, toolcli.Intent, url, owner, "add", "--match", "boat", "--when", "daily 08:00", "x"); code == 0 {
		t.Errorf("both --when and --match: %v", doc)
	}
	code, doc = brainTool(t, toolcli.Intent, url, member, "list")
	if list, _ := doc["intents"].([]any); code != 0 || len(list) != 1 {
		t.Errorf("list: %d %v", code, doc)
	}
	if code, _ := brainTool(t, toolcli.Intent, url, member, "remove", id); code == 0 {
		t.Error("a group member removed the owner's intent")
	}

	// The dashboard lists them with the brain, and removes one.
	var views []struct {
		ID      string          `json:"id"`
		Intents []intent.Intent `json:"intents"`
	}
	w := tb.do("GET", "/api/brains", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &views); err != nil || len(views) != 1 || len(views[0].Intents) != 1 || views[0].Intents[0].ID != id {
		t.Fatalf("GET /api/brains: %v %s", err, w.Body.String())
	}
	if w := tb.do("DELETE", "/api/brains/a/intents/"+id, nil); w.Code != 200 {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	if list, _ := tb.intents.List("a"); len(list) != 0 {
		t.Errorf("after the dashboard removed it: %+v", list)
	}

	// Memory search, every provenance, labelled.
	writeHistory(t, tb.brainMgr.Get("a").Dir(),
		history.Entry{TS: "2026-10-19T09:00:00Z", From: "15559876543", Chat: apiTestGroup, Type: history.In, ID: "G1", Text: "the gate code is 4711"},
		history.Entry{TS: "2026-10-19T09:01:00Z", From: "15551234567", Chat: apiTestDM, Type: history.In, ID: "D1", Text: "the gate code changed to 4712"})
	code, doc = brainTool(t, toolcli.MemorySearch, url, member, "gate")
	hits, _ := doc["hits"].([]any)
	if code != 0 || len(hits) != 2 {
		t.Fatalf("memory search: %d %v", code, doc)
	}
	provs := map[string]string{}
	for _, h := range hits {
		m := h.(map[string]any)
		provs[m["anchor"].(string)] = m["provenance"].(string)
	}
	if provs["G1"] != "untrusted" || provs["D1"] != "owner" {
		t.Errorf("provenance: %v", provs)
	}
}

// TestSchedulerFires: an intent that fell due and a brain's weekly
// consolidation go to the brain as messages from the bridge's schedules,
// answered in the owner's chat; a keyword intent fires on a group member's
// message and not on the owner's in the chat it was set in.
func TestSchedulerFires(t *testing.T) {
	runs := fakeRuns(t)
	h := newHarness(t, `{"group_members": {"`+harnessGroup+`": ["*"]}}`)
	h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true, Groups: []string{harnessGroup}}, Memory: &manifest.Memory{Consolidate: "weekly sun 18:00"}})
	b := h.b
	now := time.Now()
	in, err := intent.Compile(intent.Request{Text: "Remind the owner to call the dentist", When: now.Add(time.Hour).Format("2006-01-02T15:04")}, harnessDM, "15551234567", now, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.intents.Add("main", in); err != nil {
		t.Fatal(err)
	}
	s := &scheduler{b: b}
	s.tick(now) // records the consolidation job's start; nothing due
	n := len(h.SentTo(harnessDM))
	s.tick(now.Add(2 * time.Hour))
	h.WaitFor("the intent's run", func() bool { return len(h.SentTo(harnessDM)) > n })
	if p := runs()[len(runs())-1].Prompt; !strings.Contains(p, "Remind the owner to call the dentist") || !strings.Contains(p, "one of your schedules or standing intents falls due") {
		t.Errorf("the intent's prompt:\n%s", p)
	}
	sc, _ := trigger.ParseSchedule("weekly sun 18:00", time.Local)
	next, _ := sc.Next(now)
	n = len(h.SentTo(harnessDM))
	s.tick(next.Add(time.Minute))
	h.WaitFor("the consolidation's run", func() bool { return len(h.SentTo(harnessDM)) > n })
	if p := runs()[len(runs())-1].Prompt; !strings.Contains(p, "skills/memory-consolidation.md") {
		t.Errorf("the consolidation's prompt:\n%s", p)
	}

	in, err = intent.Compile(intent.Request{Text: "Tell the owner what was said about the boat", Match: []string{"boat"}}, harnessDM, "15551234567", now, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.intents.Add("main", in); err != nil {
		t.Fatal(err)
	}
	before := len(runs())
	h.Answer(inbound{JID: harnessDM, Text: "what about the boat?"})
	if len(runs()) != before+1 {
		t.Errorf("the owner's own message in the intent's chat fired it: %d runs", len(runs())-before)
	}
	n = len(h.SentTo(harnessDM))
	h.Inject(inbound{JID: harnessGroup, Sender: "15559876543", Text: "the boat is ready"})
	h.WaitFor("the keyword intent's run", func() bool { return len(h.SentTo(harnessDM)) > n })
	if p := runs()[len(runs())-1].Prompt; !strings.Contains(p, "the boat is ready") || !strings.Contains(p, "not instructions") {
		t.Errorf("the keyword intent's prompt:\n%s", p)
	}
}
