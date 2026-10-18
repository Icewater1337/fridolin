package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The prompts in testdata/prompt are what renderPrompt makes of
// promptCases. After a change to what runs are told, read the diff and
// rewrite them with
//
//	go test -run TestRenderPromptGolden -update-prompt-golden .
var updatePromptGolden = flag.Bool("update-prompt-golden", false, "rewrite testdata/prompt from renderPrompt")

// promptCases are the runs the golden prompts are made for.
func promptCases() map[string]promptInput {
	const alex, sam = "15551234567", "15559876543"
	names := map[string]string{alex: "Alex"}
	return map[string]promptInput{
		"dm": {
			Instructions: "You are Main, Alex's assistant.",
			BrainID:      "main",
			History: []historyEntry{
				{TS: "2026-10-12T09:12:00+02:00", From: alex, Chat: alex + "@s.whatsapp.net", Text: "Can we meet next week?", Type: historyIn},
				{TS: "2026-10-12T09:13:00+02:00", From: "main", Chat: alex + "@s.whatsapp.net", Text: "Sure, which day?", Type: historyOut},
			},
			Names:   names,
			Message: Inbound{JID: alex + "@s.whatsapp.net", Sender: alex, Text: "Tuesday at 3?"},
		},
		"group-files-quote": {
			Instructions: "You are Team.",
			BrainID:      "team",
			History: []historyEntry{
				{TS: "2026-10-18T08:55:00+02:00", From: sam, Chat: testGroup, Text: "Here are the slides", Type: historyIn},
			},
			Names: names,
			Message: Inbound{JID: testGroup, Sender: alex, Text: "file these", Media: []string{"/media/a.pdf", "/media/b.pdf"},
				Quoted: &Quote{From: sam, Text: "Here are the slides"}},
		},
		"answer-after-question": {
			// The answer came while the run that asked was still at work, and
			// that run's reply went out before the answer's run started.
			BrainID: "main",
			History: []historyEntry{
				{TS: "2026-10-18T09:20:00+02:00", From: alex, Chat: alex + "@s.whatsapp.net", Text: "Book us a table for Friday", Type: historyIn},
				{TS: "2026-10-18T09:20:30+02:00", From: "main", Chat: alex + "@s.whatsapp.net", Text: "❓ Where?\n1. Rosa\n2. Luigi", Type: historyOut},
			},
			Since: []historyEntry{
				{TS: "2026-10-18T09:21:10+02:00", From: "main", Chat: alex + "@s.whatsapp.net", Text: "I booked Rosa for 7pm.", Type: historyOut},
			},
			Names:   names,
			Message: Inbound{JID: alex + "@s.whatsapp.net", Sender: alex, Text: "Luigi, please"},
		},
		"first-message": {
			BrainID: "main",
			Message: Inbound{JID: alex + "@s.whatsapp.net", Sender: alex, Text: "hello", Media: []string{"/media/voice.ogg"}},
		},
	}
}

func TestRenderPromptGolden(t *testing.T) {
	for name, in := range promptCases() {
		t.Run(name, func(t *testing.T) {
			got := renderPrompt(in)
			path := filepath.Join("testdata", "prompt", name+".txt")
			if *updatePromptGolden {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (rewrite the golden prompts with -update-prompt-golden)", err)
			}
			if got != string(want) {
				t.Errorf("renderPrompt =\n%s\n\nwant %s:\n%s", got, path, want)
			}
		})
	}
}

// promptBlock is what prompt holds between <tag> and </tag>, or "".
func promptBlock(prompt, tag string) string {
	i, j := strings.Index(prompt, "<"+tag+">"), strings.Index(prompt, "</"+tag+">")
	if i < 0 || j < i {
		return ""
	}
	return prompt[i:j]
}

// A run's prompt has the message it answers once, as the new message: the
// chat history ends before it. Messages that came in while it waited in
// the queue are not in its history either; they have runs of their own.
func TestPromptShowsTheMessageOnce(t *testing.T) {
	mgr, d, home := queueTestManager(t, 300, "")
	promptLog := filepath.Join(home, "prompts.log")
	t.Setenv("FRIDOLIN_FAKE_PROMPT_LOG", promptLog)
	for _, text := range []string{"MSG-ONE", "MSG-TWO", "MSG-THREE"} {
		if _, _, _, err := mgr.Submit(dm(text)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 10*time.Second, "three runs", func() bool { return d.count() == 3 })

	prompts := loggedPrompts(t, promptLog)
	cases := []struct {
		msg            string
		before, absent []string
	}{
		{"MSG-ONE", nil, []string{"MSG-TWO", "MSG-THREE"}},
		{"MSG-TWO", []string{"MSG-ONE"}, []string{"MSG-THREE"}},
		{"MSG-THREE", []string{"MSG-ONE", "MSG-TWO"}, nil},
	}
	for _, c := range cases {
		t.Run(c.msg, func(t *testing.T) {
			var p string
			for msg, prompt := range prompts {
				if strings.HasSuffix(msg, "says: "+c.msg) {
					p = prompt
				}
			}
			if p == "" {
				t.Fatalf("no prompt for %s in %q", c.msg, prompts)
			}
			history := ""
			if i, j := strings.Index(p, "<chat_history>"), strings.Index(p, "</chat_history>"); i >= 0 && j > i {
				history = p[i:j]
			}
			if n := strings.Count(p, c.msg); n != 1 {
				t.Errorf("%s is in its prompt %d times, want once:\n%s", c.msg, n, p)
			}
			for _, b := range c.before {
				if strings.Count(history, b) != 1 {
					t.Errorf("the history in the prompt for %s lacks %s:\n%s", c.msg, b, p)
				}
			}
			for _, a := range c.absent {
				if strings.Contains(p, a) {
					t.Errorf("the prompt for %s has %s, which came in after it:\n%s", c.msg, a, p)
				}
			}
			if want := map[int]string{0: "", 1: "The message before the new one", 2: "The 2 messages before the new one"}[len(c.before)]; !strings.Contains(history, want) {
				t.Errorf("the history in the prompt for %s does not start %q:\n%s", c.msg, want, history)
			}
		})
	}
}

// A run's prompt shows the chat's history before the line Submit wrote for
// its message, and of what came after that line, only what went out: the
// reply to the message before, which came while it waited, and what a
// script sent through /send. Without the line, the history is the chat's
// last entries. The window holds both, the newest first.
func TestHistoryBefore(t *testing.T) {
	br := bareBrain(t)
	for _, text := range []string{"one", "two", "three"} {
		br.appendHistory(testDMChat, "15551234567", text, historyIn)
	}
	br.appendHistory(testGroup, "15551234567", "elsewhere", historyIn)
	logged := br.appendEntry(historyEntry{Chat: testDMChat, From: "15551234567", Text: "four", Type: historyIn})
	br.appendHistory(testDMChat, "15551234567", "five", historyIn)
	br.appendHistory(testDMChat, br.ID(), "reply", historyOut)
	br.appendHistory(testGroup, br.ID(), "reply elsewhere", historyOut)
	br.appendHistoryVia(testDMChat, "script", "scripted", historyOut, "script")
	texts := func(es []historyEntry) string {
		var out []string
		for _, e := range es {
			out = append(out, e.Text)
		}
		return strings.Join(out, " ")
	}
	cases := []struct {
		name          string
		logged        historyEntry
		limit         int
		before, since string
	}{
		{"the message's line", logged, 10, "one two three", "reply scripted"},
		{"a window", logged, 4, "two three", "reply scripted"},
		{"a window of one", logged, 1, "", "scripted"},
		{"no line", historyEntry{}, 10, "one two three four five reply scripted", ""},
		{"a line that is gone", historyEntry{TS: "2026-01-01T00:00:00Z", Chat: testDMChat, Text: "gone"}, 2, "reply scripted", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := dm("four")
			m.logged = c.logged
			before, since := br.historyBefore(m, c.limit)
			if texts(before) != c.before || texts(since) != c.since {
				t.Errorf("historyBefore = %q, %q; want %q, %q", texts(before), texts(since), c.before, c.since)
			}
		})
	}
}

// Submit's line is found when its text is not valid UTF-8 too: the line
// it is looked for as is the one that reads back.
func TestHistoryBeforeFindsInvalidUTF8(t *testing.T) {
	br := bareBrain(t)
	br.appendHistory(testDMChat, "15551234567", "one", historyIn)
	m := dm("bad \xff text")
	m.logged = br.appendEntry(historyEntry{Chat: m.JID, From: m.Sender, Text: m.Text, Type: historyIn})
	br.appendHistory(testDMChat, "15551234567", "later", historyIn)
	if before, since := br.historyBefore(m, 10); len(before) != 1 || before[0].Text != "one" || len(since) != 0 {
		t.Errorf("historyBefore = %+v, %+v; want only the line before", before, since)
	}
}

// A message's own lines are found however the history changed since
// Submit wrote them: the first part of a merged message deleted, or the
// message edited while it waited. What waits in the outbox reached
// nobody yet, so it shows as sent since, and once when it was delivered
// meanwhile. A chat that was quiet while another filled the end of the
// history still gets its window, from further back.
func TestHistoryBeforeAfterChanges(t *testing.T) {
	texts := func(es []historyEntry) string {
		var out []string
		for _, e := range es {
			out = append(out, e.Text)
		}
		return strings.Join(out, " ")
	}
	cases := []struct {
		name          string
		setup         func(br *Brain) Inbound // the message, its lines written
		before, since string
	}{
		{"a merged message, its first part deleted", func(br *Brain) Inbound {
			first, second := dm("first part"), dm("second part")
			first.ID, second.ID = "3EB0A", "3EB0B"
			m := br.logInbound(first.merge(second))
			c := messageChange{Kind: changeRevoke, Chat: testDMChat, Sender: first.Sender, Target: "3EB0A"}
			m, _, _ = m.applyChange(c)
			br.recordChange(c, true, nil)
			br.appendHistory(testDMChat, br.ID(), "reply", historyOut)
			return m
		}, "one", "reply"},
		{"a message edited while it waited", func(br *Brain) Inbound {
			msg := dm("book Tuesday")
			msg.ID = "3EB0A"
			m := br.logInbound(msg)
			br.appendHistory(testDMChat, "15551234567", "later", historyIn)
			c := messageChange{Kind: changeEdit, Chat: testDMChat, Sender: msg.Sender, Target: "3EB0A", Text: "book Wednesday"}
			m, _, _ = m.applyChange(c)
			br.recordChange(c, true, &m)
			return m
		}, "one", ""},
		{"answers in the outbox", func(br *Brain) Inbound {
			// Written before the message came in, and still waiting; and
			// one delivered while the prompt was read.
			early := time.Now().Add(-time.Hour).Format(time.RFC3339)
			br.hooks = &BrainHooks{Undelivered: func(string) []historyEntry {
				return []historyEntry{
					{TS: early, From: br.ID(), Chat: testDMChat, Text: "waiting", Type: historyOut},
					{TS: early, From: br.ID(), Chat: testDMChat, Text: "delivered", Type: historyOut},
					{TS: early, From: br.ID(), Chat: testGroup, Text: "elsewhere", Type: historyOut},
				}
			}}
			m := br.logInbound(dm("two"))
			br.appendHistory(testDMChat, br.ID(), "delivered", historyOut)
			return m
		}, "one", "waiting delivered"},
		{"a quiet chat behind a busy one", func(br *Brain) Inbound {
			filler := strings.Repeat("chatter ", 128)
			for range promptHistoryTail / len(filler) {
				br.appendHistory(testGroup, "15559876543", filler, historyIn)
			}
			return br.logInbound(dm("two"))
		}, "one", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			br := bareBrain(t)
			br.appendHistory(testDMChat, "15551234567", "one", historyIn)
			m := c.setup(br)
			before, since := br.historyBefore(m, dmHistoryWindow)
			if texts(before) != c.before || texts(since) != c.since {
				t.Errorf("historyBefore = %q, %q; want %q, %q", texts(before), texts(since), c.before, c.since)
			}
		})
	}
}
