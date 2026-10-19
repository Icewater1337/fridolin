package brain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/history"
	prompttext "github.com/Icewater1337/fridolin/internal/prompt"
)

// A run's prompt shows the chat's history before the line Submit wrote for
// its message, and of what came after that line, only what went out: the
// reply to the message before, which came while it waited, and what a
// script sent through /send. Without the line, the history is the chat's
// last entries. The window holds both, the newest first.
func TestHistoryBefore(t *testing.T) {
	br := bareBrain(t)
	for _, text := range []string{"one", "two", "three"} {
		br.AppendHistory(testDMChat, "15551234567", text, history.In)
	}
	br.AppendHistory(testGroup, "15551234567", "elsewhere", history.In)
	logged := br.AppendEntry(history.Entry{Chat: testDMChat, From: "15551234567", Text: "four", Type: history.In})
	br.AppendHistory(testDMChat, "15551234567", "five", history.In)
	br.AppendHistory(testDMChat, br.ID(), "reply", history.Out)
	br.AppendHistory(testGroup, br.ID(), "reply elsewhere", history.Out)
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
	br.AppendHistory(testDMChat, "15551234567", "one", history.In)
	m := dm("bad \xff text")
	m.logged = br.AppendEntry(history.Entry{Chat: m.JID, From: m.Sender, Text: m.Text, Type: history.In})
	br.AppendHistory(testDMChat, "15551234567", "later", history.In)
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
			m := br.logInbound(first.Merge(second))
			c := chat.Change{Kind: chat.ChangeRevoke, Chat: testDMChat, Sender: first.Sender, Target: "3EB0A"}
			m, _, _ = m.ApplyChange(c)
			br.RecordChange(c, true, nil)
			br.AppendHistory(testDMChat, br.ID(), "reply", history.Out)
			return m
		}, "one", "reply"},
		{"a message edited while it waited", func(br *Brain) Inbound {
			msg := dm("book Tuesday")
			msg.ID = "3EB0A"
			m := br.logInbound(msg)
			br.AppendHistory(testDMChat, "15551234567", "later", history.In)
			c := chat.Change{Kind: chat.ChangeEdit, Chat: testDMChat, Sender: msg.Sender, Target: "3EB0A", Text: "book Wednesday"}
			m, _, _ = m.ApplyChange(c)
			br.RecordChange(c, true, &m)
			return m
		}, "one", ""},
		{"answers in the outbox", func(br *Brain) Inbound {
			// Written before the message came in, and still waiting; and
			// one delivered while the prompt was read.
			early := time.Now().Add(-time.Hour).Format(time.RFC3339)
			br.sink = sinkForTest(&testSink{Pending: func(string) []history.Entry {
				return []history.Entry{
					{TS: early, From: br.ID(), Chat: testDMChat, Text: "waiting", Type: history.Out},
					{TS: early, From: br.ID(), Chat: testDMChat, Text: "delivered", Type: history.Out},
					{TS: early, From: br.ID(), Chat: testGroup, Text: "elsewhere", Type: history.Out},
				}
			}})
			m := br.logInbound(dm("two"))
			br.AppendHistory(testDMChat, br.ID(), "delivered", history.Out)
			return m
		}, "one", "waiting delivered"},
		{"a quiet chat behind a busy one", func(br *Brain) Inbound {
			filler := strings.Repeat("chatter ", 128)
			// More than a block of the reader, well within its budget.
			for range (2 << 20) / len(filler) {
				br.AppendHistory(testGroup, "15559876543", filler, history.In)
			}
			return br.logInbound(dm("two"))
		}, "one", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			br := bareBrain(t)
			br.AppendHistory(testDMChat, "15551234567", "one", history.In)
			m := c.setup(br)
			before, since := br.historyBefore(m, prompttext.DMHistoryWindow)
			if texts(before) != c.before || texts(since) != c.since {
				t.Errorf("historyBefore = %q, %q; want %q, %q", texts(before), texts(since), c.before, c.since)
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
