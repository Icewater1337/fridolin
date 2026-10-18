package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// run is one run of the fake Claude CLI, from FRIDOLIN_FAKE_RUN_LOG.
type run struct {
	Args   []string `json:"args"`
	Prompt string   `json:"prompt"`
	Dir    string   `json:"dir"`
}

// fakeRuns sets FRIDOLIN_FAKE_RUN_LOG for this test and returns a function
// that reads the runs logged so far.
func fakeRuns(t *testing.T) func() []run {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	t.Setenv("FRIDOLIN_FAKE_RUN_LOG", path)
	return func() []run {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		// A run may be appending its line while this reads: the last
		// part, after the last newline, is not a whole line yet.
		lines := bytes.Split(data, []byte("\n"))
		var out []run
		for _, line := range lines[:len(lines)-1] {
			var r run
			if err := json.Unmarshal(line, &r); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
		return out
	}
}

func TestFakeRunsReadsWholeLinesOnly(t *testing.T) {
	for _, c := range []struct {
		name, log string
		want      int
	}{
		{"no line yet", ``, 0},
		{"a part of the first", `{"args":["-p"`, 0},
		{"one whole line", `{"args":["-p"],"prompt":"a"}` + "\n", 1},
		{"and a part of the next", `{"args":["-p"],"prompt":"a"}` + "\n" + `{"args":["-p"],"pro`, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			runs := fakeRuns(t)
			if err := os.WriteFile(os.Getenv("FRIDOLIN_FAKE_RUN_LOG"), []byte(c.log), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := runs(); len(got) != c.want {
				t.Errorf("%d runs, want %d: %+v", len(got), c.want, got)
			}
		})
	}
}

// clockRE matches the times a prompt gives, in the local time zone, which
// differs between machines: when each message of the chat history and the
// new one was sent, and, with the zone, when the run starts. The lines the
// test sends are written with the time they arrive.
var clockRE = regexp.MustCompile(`\b(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun) \d{4}-\d\d-\d\d \d\d:\d\d(?: \S+ \(UTC[+-]\d\d:\d\d\))?`)

// scrubArgs masks the paths in the CLI's arguments. --settings is JSON that
// lists a pattern for every folder above the brain's, up to /; once those
// between / and the test's root are <ABOVE>, each pattern is listed once,
// with how many times it came (collapseAbove).
func scrubArgs(t *testing.T, s *scrubber, args []string) []byte {
	t.Helper()
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = s.text(a)
		if i == 0 || args[i-1] != "--settings" {
			continue
		}
		var settings map[string][]string
		if err := json.Unmarshal([]byte(a), &settings); err != nil {
			t.Fatalf("--settings %s: %v", a, err)
		}
		var patterns []string
		for _, p := range settings["claudeMdExcludes"] {
			patterns = append(patterns, strings.Trim(s.text(`"`+p+`"`), `"`))
		}
		settings["claudeMdExcludes"] = s.collapseAbove(patterns)
		out[i] = strings.TrimSpace(string(s.marshal(t, settings)))
	}
	return s.marshal(t, out)
}

// goldenIdentityMap is a brain's contact map, as its memories hold one.
const goldenIdentityMap = `# WhatsApp identity map

| WhatsApp Number | Person | Notes |
| --------------- | ------ | ----- |
| 15551234567     | You    | the owner |
| 15559876543     | Sam    | family    |
`

// goldenBrainPrompt is the brain-prompt.md of the brains in TestGoldenRuns.
const goldenBrainPrompt = `# Test brain

Answer in one sentence.
`

// TestGoldenRuns pins what a brain's run gets: the prompt for a DM, a group
// message, one and several attachments, a quoted reply and an addressed
// group message after an untagged one, with the chat history and the
// names from the identity map; the Claude CLI's arguments with the global
// settings and with a brain's own; and the history lines the bridge writes
// for each kind of message.
func TestGoldenRuns(t *testing.T) {
	runs := fakeRuns(t)
	h := newHarness(t, `{"brain_model": "sonnet", "brain_effort": "high", "disallowed_tools": ["WebFetch"], "dangerously_skip_permissions": true, "ingest": {"enabled": true}}`)
	s := h.scrubber()
	dir := h.AddBrain(BrainManifest{ID: "main", Name: "Main", Routing: BrainRouting{HandlesDMs: true, Groups: []string{harnessGroup}}})
	family := h.AddBrain(BrainManifest{ID: "family", Name: "Family", Model: "opus", Effort: "low", DisallowedTools: &[]string{}, Routing: BrainRouting{Groups: []string{"120363000000000001@g.us"}}})
	if w := h.Do("POST", "/api/chats/allow", map[string]string{"kind": "group", "jid": "120363000000000001@g.us"}); w.Code != 200 {
		t.Fatalf("allow the second group: %d %s", w.Code, w.Body.String())
	}

	history := filepath.Join(dir, historyFileName)
	for path, data := range map[string][]byte{
		// A short prompt of the brains' own, so that these golden files pin
		// how a run's prompt is put together. The starter prompt from
		// examples/, which changes more often, is TestGoldenScaffold's.
		filepath.Join(dir, defaultPromptFile):    []byte(goldenBrainPrompt),
		filepath.Join(family, defaultPromptFile): []byte(goldenBrainPrompt),
		history:                                  fixture(t, "history-v1.jsonl"),
		filepath.Join(dir, identityMapFile):      []byte(goldenIdentityMap),
		filepath.Join(dir, ".mcp.json"):          []byte(`{"mcpServers": {"notes": {"command": "notes-mcp"}}}`),
		filepath.Join(h.DataDir(), "media", "a"): nil,
	} {
		if data == nil {
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	media := func(name string) string {
		p := filepath.Join(h.DataDir(), "media", "a", name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	fixtureLines := len(strings.Split(strings.TrimSpace(string(fixture(t, "history-v1.jsonl"))), "\n"))

	quoted := events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: types.NewJID("15551234567", types.DefaultUserServer), Sender: types.NewJID("15551234567", types.DefaultUserServer)},
			ID:            "3EB0REPLY", Timestamp: time.Now(),
		},
		Message: replyMessage("yes, book it", "Lunch at 12?"),
	}
	tests := []struct {
		name  string
		send  func()
		reply string // the chat the answer goes to
		args  bool   // pin the CLI's arguments too
	}{
		{"dm", func() { h.Inject(inbound{JID: harnessDM, Text: "What is on tomorrow?"}) }, harnessDM, true},
		{"group", func() {
			h.Inject(inbound{JID: harnessGroup, Sender: "15559876543", Text: "@claude who brings the cake?"})
		}, harnessGroup, false},
		{"one-attachment", func() {
			h.Inject(inbound{JID: harnessDM, Text: "what is this?", Media: []string{media("photo.jpg")}})
		}, harnessDM, false},
		{"several-attachments", func() {
			h.Inject(inbound{JID: harnessDM, Text: "file these", Media: []string{media("scan-1.pdf"), media("scan-2.pdf")}})
		}, harnessDM, false},
		{"quote", func() { h.WhatsApp(&quoted) }, harnessDM, false},
		{"ingest", func() {
			if w := h.Script("POST", "/ingest", `{"brain": "main", "paths": ["`+media("upload.txt")+`"]}`); w.Code != 202 {
				t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
			}
		}, harnessDM, false},
		{"brain-settings", func() {
			h.Inject(inbound{JID: "120363000000000001@g.us", Sender: "15551234567", Text: "@claude hello family"})
		}, "120363000000000001@g.us", true},
	}
	// recorded reports whether a brain's history holds answer as sent. The
	// history records a reply once it went out, after the chat has it, so
	// each case waits for that too: the next message's line would
	// otherwise come before it at times.
	recorded := func(answer string) bool {
		for _, d := range []string{dir, family} {
			for _, l := range historyLines(t, d) {
				var e historyEntry
				if json.Unmarshal([]byte(l), &e) == nil && e.Type == historyOut && e.Text == answer {
					return true
				}
			}
		}
		return false
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := len(runs())
			n := len(h.SentTo(tc.reply))
			tc.send()
			h.WaitFor("the answer to "+tc.name, func() bool { return len(h.SentTo(tc.reply)) > n && len(runs()) > before })
			answer := h.SentTo(tc.reply)[n]
			h.WaitFor("the answer to "+tc.name+" in the history", func() bool { return recorded(answer) })
			r := runs()[len(runs())-1]
			assertGolden(t, "run/prompt-"+tc.name+".golden", []byte(clockRE.ReplaceAllString(s.text(r.Prompt), "<WHEN>")+"\n"))
			if tc.args {
				assertGolden(t, "run/args-"+tc.name+".golden", scrubArgs(t, s, r.Args))
			}
		})
	}

	// An untagged group message runs no brain: nothing is sent, and the
	// message goes into the history, where the next run in the chat sees
	// it.
	t.Run("chatter", func(t *testing.T) {
		before, lines, sent := len(runs()), len(historyLines(t, dir)), len(h.SentTo(harnessGroup))
		h.Inject(inbound{JID: harnessGroup, Sender: "15559876543", Text: "the cake is chocolate"})
		h.WaitFor("the message in the history", func() bool { return len(historyLines(t, dir)) >= lines+1 })
		answer := h.Answer(inbound{JID: harnessGroup, Sender: "15551234567", Text: "@claude and the candles?"})
		h.WaitFor("the answer in the history", func() bool { return recorded(answer) })
		if got := h.SentTo(harnessGroup); len(got) != sent+1 {
			t.Errorf("sent %q, want only the answer to the addressed message", got[sent:])
		}
		if n := len(runs()) - before; n != 1 {
			t.Errorf("%d runs, want one: the untagged message runs no brain", n)
		}
		assertGolden(t, "run/prompt-after-chatter.golden", []byte(clockRE.ReplaceAllString(s.text(runs()[len(runs())-1].Prompt), "<WHEN>")+"\n"))
	})

	// /send writes the message into the history of the brain that owns the
	// chat, as if the brain had said it.
	if w := h.Script("POST", "/send", `{"jid": "`+harnessDM+`", "text": "Sent by a script."}`); w.Code != 200 {
		t.Fatalf("send: %d %s", w.Code, w.Body.String())
	}
	data, err := os.ReadFile(history)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	assertGolden(t, "run/history-lines.golden", []byte(s.text(strings.Join(lines[fixtureLines:], "\n"))+"\n"))
}
