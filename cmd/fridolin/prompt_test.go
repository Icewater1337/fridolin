package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/history"
	prompttext "github.com/Icewater1337/fridolin/internal/prompt"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

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
	testutil.WaitFor(t, 10*time.Second, "three runs", func() bool { return d.count() == 3 })

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
		br.appendHistory(testDMChat, "15551234567", text, history.In)
	}
	br.appendHistory(testGroup, "15551234567", "elsewhere", history.In)
	logged := br.appendEntry(history.Entry{Chat: testDMChat, From: "15551234567", Text: "four", Type: history.In})
	br.appendHistory(testDMChat, "15551234567", "five", history.In)
	br.appendHistory(testDMChat, br.ID(), "reply", history.Out)
	br.appendHistory(testGroup, br.ID(), "reply elsewhere", history.Out)
	br.appendHistoryVia(testDMChat, "script", "scripted", history.Out, "script")
	texts := func(es []history.Entry) string {
		var out []string
		for _, e := range es {
			out = append(out, e.Text)
		}
		return strings.Join(out, " ")
	}
	cases := []struct {
		name          string
		logged        history.Entry
		limit         int
		before, since string
	}{
		{"the message's line", logged, 10, "one two three", "reply scripted"},
		{"a window", logged, 4, "two three", "reply scripted"},
		{"a window of one", logged, 1, "", "scripted"},
		{"no line", history.Entry{}, 10, "one two three four five reply scripted", ""},
		{"a line that is gone", history.Entry{TS: "2026-01-01T00:00:00Z", Chat: testDMChat, Text: "gone"}, 2, "reply scripted", ""},
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
	br.appendHistory(testDMChat, "15551234567", "one", history.In)
	m := dm("bad \xff text")
	m.logged = br.appendEntry(history.Entry{Chat: m.JID, From: m.Sender, Text: m.Text, Type: history.In})
	br.appendHistory(testDMChat, "15551234567", "later", history.In)
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
	texts := func(es []history.Entry) string {
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
			c := chat.Change{Kind: chat.ChangeRevoke, Chat: testDMChat, Sender: first.Sender, Target: "3EB0A"}
			m, _, _ = m.applyChange(c)
			br.recordChange(c, true, nil)
			br.appendHistory(testDMChat, br.ID(), "reply", history.Out)
			return m
		}, "one", "reply"},
		{"a message edited while it waited", func(br *Brain) Inbound {
			msg := dm("book Tuesday")
			msg.ID = "3EB0A"
			m := br.logInbound(msg)
			br.appendHistory(testDMChat, "15551234567", "later", history.In)
			c := chat.Change{Kind: chat.ChangeEdit, Chat: testDMChat, Sender: msg.Sender, Target: "3EB0A", Text: "book Wednesday"}
			m, _, _ = m.applyChange(c)
			br.recordChange(c, true, &m)
			return m
		}, "one", ""},
		{"answers in the outbox", func(br *Brain) Inbound {
			// Written before the message came in, and still waiting; and
			// one delivered while the prompt was read.
			early := time.Now().Add(-time.Hour).Format(time.RFC3339)
			br.hooks = &BrainHooks{Undelivered: func(string) []history.Entry {
				return []history.Entry{
					{TS: early, From: br.ID(), Chat: testDMChat, Text: "waiting", Type: history.Out},
					{TS: early, From: br.ID(), Chat: testDMChat, Text: "delivered", Type: history.Out},
					{TS: early, From: br.ID(), Chat: testGroup, Text: "elsewhere", Type: history.Out},
				}
			}}
			m := br.logInbound(dm("two"))
			br.appendHistory(testDMChat, br.ID(), "delivered", history.Out)
			return m
		}, "one", "waiting delivered"},
		{"a quiet chat behind a busy one", func(br *Brain) Inbound {
			filler := strings.Repeat("chatter ", 128)
			// More than a block of the reader, well within its budget.
			for range (2 << 20) / len(filler) {
				br.appendHistory(testGroup, "15559876543", filler, history.In)
			}
			return br.logInbound(dm("two"))
		}, "one", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			br := bareBrain(t)
			br.appendHistory(testDMChat, "15551234567", "one", history.In)
			m := c.setup(br)
			before, since := br.historyBefore(m, prompttext.DMHistoryWindow)
			if texts(before) != c.before || texts(since) != c.since {
				t.Errorf("historyBefore = %q, %q; want %q, %q", texts(before), texts(since), c.before, c.since)
			}
		})
	}
}

// The prompt says what kind of chat the run is in, which a JID does not
// tell the model, and when the new message did not come from that chat.
func TestPromptSaysWhatChatItIs(t *testing.T) {
	cases := []struct {
		name     string
		m        Inbound
		want     string
		handedIn bool
	}{
		{"a direct chat", Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "hi", Source: chat.SourceWhatsApp}}, "This chat is a direct chat on WhatsApp.", false},
		{"a group", Inbound{Inbound: chat.Inbound{JID: testGroup, Sender: "15551234567", Text: "hi", Source: chat.SourceWhatsApp}}, "This chat is the WhatsApp group ", false},
		{"a build chat", Inbound{Inbound: chat.Inbound{JID: buildChatJID("main", 2), Sender: "owner", Text: "make the header blue", Source: chat.SourceDev}}, "This chat is the site-building chat on the dashboard's Build page, not a WhatsApp chat.", false},
		{"files handed in", Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "upload", Text: "a scan", Media: []string{"/media/a.pdf"}, Source: chat.SourceIngest}}, "This chat is a direct chat on WhatsApp.", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := prompttext.Build(prompttext.Input{Now: time.Now(), BrainID: "main", Message: c.m.Inbound, BuildChat: brainOfBuildJID(c.m.JID) != ""})
			if !strings.Contains(p, c.want) {
				t.Errorf("the prompt does not say %q:\n%s", c.want, p)
			}
			if got := strings.Contains(p, "handed to you outside WhatsApp"); got != c.handedIn {
				t.Errorf("the prompt says the files were handed in: %v, want %v:\n%s", got, c.handedIn, p)
			}
		})
	}
}

// The person a run answers is named as the history names them, from the
// brain's identity map, with the number they wrote from; someone the map
// does not know is their number.
func TestNewMessageNamesTheSender(t *testing.T) {
	br, home := folderBrain(t)
	table := "| WhatsApp Number | Person | Notes |\n| --- | --- | --- |\n| +15551234567 | Alex | |\n"
	if err := os.WriteFile(filepath.Join(home, "a", prompttext.IdentityMapPath), []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ sender, want string }{
		{"15551234567", `{"from":"Alex (15551234567)","text":"hello"}`},
		{"15559876543", `{"from":"15559876543","text":"hello"}`},
	}
	for _, c := range cases {
		t.Run(c.sender, func(t *testing.T) {
			m := Inbound{Inbound: chat.Inbound{JID: c.sender + "@s.whatsapp.net", Sender: c.sender, Text: "hello"}}
			if got := br.buildPrompt(br.settings.Load(), m); !strings.Contains(got, "<new_message>\n"+c.want+"\n</new_message>") {
				t.Errorf("prompt:\n%s\nwant the new message %q", got, c.want)
			}
		})
	}
}
