package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// historyTexts returns the text of the brain's history lines of type typ.
func historyTexts(t *testing.T, br *brain.Brain, typ string) []string {
	t.Helper()
	var out []string
	for _, e := range readHistory(t, br) {
		if e.Type == typ {
			out = append(out, e.Text)
		}
	}
	return out
}

// A message deleted for everyone is not answered and leaves the history,
// wherever it was: waiting, being answered, or answered already.
func TestDeletedMessageIsNotAnsweredAndLeavesTheHistory(t *testing.T) {
	t.Run("waiting", func(t *testing.T) {
		tb, br := newMainBridge(t, "")
		startLongRun(t, tb, br, brain.Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "a long report", Source: chat.SourceWhatsApp}})
		wa := tb.whatsApp(t)
		wa.ProcessMessage(dmEvent("3EB0WAIT", textMsg("my card number is 4111")))
		testutil.WaitFor(t, 5*time.Second, "the message to wait", func() bool { return len(br.Snapshot().Queue) == 1 })
		wa.ProcessMessage(dmEvent("3EB0DEL", revokeMsg("3EB0WAIT")))
		if q := br.Snapshot().Queue; len(q) != 0 {
			t.Errorf("queue = %+v, want the deleted message gone", q)
		}
		if got := historyTexts(t, br, history.In); !slices.Equal(got, []string{"a long report"}) {
			t.Errorf("history holds %q, want the deleted message out", got)
		}
		if ev := historyTexts(t, br, history.Event); len(ev) != 0 {
			t.Errorf("events = %q, want none for a message no run read", ev)
		}
	})

	t.Run("being answered", func(t *testing.T) {
		tb, br := newMainBridge(t, "")
		t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "30000")
		wa := tb.whatsApp(t)
		wa.ProcessMessage(dmEvent("3EB0RUN", textMsg("write it all down")))
		testutil.WaitFor(t, 5*time.Second, "the run", func() bool { r := br.Snapshot().Running; return r != nil && r.PID > 0 })
		wa.ProcessMessage(dmEvent("3EB0DEL", revokeMsg("3EB0RUN")))
		testutil.WaitFor(t, 10*time.Second, "the run to stop", func() bool { return br.Snapshot().Running == nil })
		time.Sleep(200 * time.Millisecond)
		if sent := tb.sentTo(testDMChat); len(sent) != 0 {
			t.Errorf("sent %q for a deleted message", sent)
		}
		if got := historyTexts(t, br, history.In); len(got) != 0 {
			t.Errorf("history holds %q, want the deleted message out", got)
		}
		if ev := historyTexts(t, br, history.Event); !slices.Equal(ev, []string{"deleted a message they had sent"}) {
			t.Errorf("events = %q, want the deletion noted", ev)
		}
	})

	t.Run("answered", func(t *testing.T) {
		tb, br := newMainBridge(t, "")
		wa := tb.whatsApp(t)
		wa.ProcessMessage(dmEvent("3EB0DONE", textMsg("what is my PIN again")))
		testutil.WaitFor(t, 10*time.Second, "the answer", func() bool { return len(tb.sentTo(testDMChat)) == 1 })
		testutil.WaitFor(t, 5*time.Second, "the answer in the history", func() bool { return len(historyTexts(t, br, history.Out)) == 1 })
		wa.ProcessMessage(dmEvent("3EB0DEL", revokeMsg("3EB0DONE")))
		if got := historyTexts(t, br, history.In); len(got) != 0 {
			t.Errorf("history holds %q, want the deleted message out", got)
		}
		if ev := historyTexts(t, br, history.Event); !slices.Equal(ev, []string{"deleted a message they had sent"}) {
			t.Errorf("events = %q, want the deletion noted", ev)
		}
		if out := historyTexts(t, br, history.Out); len(out) != 1 {
			t.Errorf("the brain's answer = %q, want it kept", out)
		}
	})

	t.Run("someone else's", func(t *testing.T) {
		tb, br := newMainBridge(t, "")
		startLongRun(t, tb, br, brain.Inbound{Inbound: chat.Inbound{JID: testGroup, Sender: "15551234567", Text: "a long report", Source: chat.SourceWhatsApp}})
		tb.handleInbound(t.Context(), brain.Inbound{Inbound: chat.Inbound{JID: testGroup, Sender: "15551234567", Text: "book Tuesday", Source: chat.SourceWhatsApp, ID: "3EB0MINE"}})
		tb.handleChange(chat.Change{Kind: chat.ChangeRevoke, Chat: testGroup, Sender: otherNumber, Target: "3EB0MINE"})
		if q := br.Snapshot().Queue; len(q) != 1 {
			t.Errorf("queue = %+v: another member's deletion took the message", q)
		}
		if got := historyTexts(t, br, history.In); !slices.Contains(got, "book Tuesday") {
			t.Errorf("history holds %q, want the message kept", got)
		}
	})
}

// An edit of a message that waits replaces its text, in the run and in the
// history. One the brain already acted on starts no run of its own, which
// could do it all again; the history notes it for the next run.
func TestEditedMessage(t *testing.T) {
	t.Run("waiting", func(t *testing.T) {
		tb, br := newMainBridge(t, "")
		startLongRun(t, tb, br, brain.Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "a long report", Source: chat.SourceWhatsApp}})
		wa := tb.whatsApp(t)
		wa.ProcessMessage(dmEvent("3EB0WAIT", textMsg("book Tuesday")))
		testutil.WaitFor(t, 5*time.Second, "the message to wait", func() bool { return len(br.Snapshot().Queue) == 1 })
		wa.ProcessMessage(dmEvent("3EB0EDIT", editMsg("3EB0WAIT", "book Wednesday")))
		if q := br.Snapshot().Queue; len(q) != 1 || q[0].Preview != "book Wednesday" {
			t.Errorf("queue = %+v, want the edited text waiting", q)
		}
		if got := historyTexts(t, br, history.In); !slices.Equal(got, []string{"a long report", "book Wednesday"}) {
			t.Errorf("history holds %q, want the edit in place", got)
		}
		t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "10")
		br.StopCurrent(false)
		testutil.WaitFor(t, 10*time.Second, "the edited message's answer", func() bool { return tb.sentPrefix(testDMChat, "echo:") })
		if got := tb.sentTo(testDMChat); !strings.HasSuffix(got[len(got)-1], "15551234567 says: book Wednesday") {
			t.Errorf("answered %q, want the edited text", got)
		}
	})

	t.Run("answered", func(t *testing.T) {
		tb, br := newMainBridge(t, "")
		wa := tb.whatsApp(t)
		wa.ProcessMessage(dmEvent("3EB0DONE", textMsg("book Tuesday")))
		testutil.WaitFor(t, 10*time.Second, "the answer", func() bool { return len(tb.sentTo(testDMChat)) == 1 })
		wa.ProcessMessage(dmEvent("3EB0EDIT", editMsg("3EB0DONE", "book Wednesday")))
		time.Sleep(300 * time.Millisecond)
		if sent := tb.sentTo(testDMChat); len(sent) != 1 {
			t.Errorf("sent %q: the edit started a run", sent)
		}
		if got := historyTexts(t, br, history.In); !slices.Equal(got, []string{"book Tuesday"}) {
			t.Errorf("history holds %q, want what the run read", got)
		}
		if ev := historyTexts(t, br, history.Event); !slices.Equal(ev, []string{`edited a message to: "book Wednesday"`}) {
			t.Errorf("events = %q, want the edit noted", ev)
		}
		if preface := capturedPrompt(t, br, brain.Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "next"}}); !strings.Contains(preface, `"from":"15551234567","did":"edited a message to: \"book Wednesday\""`) {
			t.Errorf("preface = %q, want the edit", preface)
		}
	})
}

// An edit of a message the history never held writes nothing: such an
// edit used to be noted whatever it changed, and so put a one-time code
// the OTP guard had kept out, corrected by its sender, into the history
// and the next run's prompt.
func TestEditOfAMessageTheHistoryLacks(t *testing.T) {
	cases := []struct {
		name string
		// code is a message the OTP guard catches, sent as 3EB0OTP.
		code bool
		edit string // the new text of 3EB0OTP
		// handed is the code the script that asked for one finds, "" for
		// none.
		handed string
		// alt is the DM's LID, which the request went to and which the
		// code and its edit carry, "" for none.
		alt string
	}{
		{"a corrected code", true, "483921", "483921", ""},
		{"a code corrected in words", true, "483921, sorry", "483920", ""},
		{"a message never received", false, "book Wednesday", "", ""},
		{"a code asked for by the DM's LID, corrected", true, "483921", "483921", "15559876543@lid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb, br := newMainBridge(t, "")
			askIn := testDMChat
			if c.alt != "" {
				askIn = c.alt
			}
			br.AppendHistory(askIn, "main", "🔐 Please send me the verification code", history.Out)
			if c.code {
				tb.handleInbound(t.Context(), brain.Inbound{Inbound: chat.Inbound{JID: testDMChat, ChatAlt: c.alt, Sender: "15551234567", Text: "483920", Source: chat.SourceWhatsApp, ID: "3EB0OTP"}})
			}
			tb.handleChange(chat.Change{Kind: chat.ChangeEdit, Chat: testDMChat, ChatAlt: c.alt, Sender: "15551234567", Target: "3EB0OTP", Text: c.edit})

			if h := readHistory(t, br); len(h) != 1 || h[0].Type != history.Out {
				t.Errorf("history = %+v, want only the request", h)
			}
			if preface := capturedPrompt(t, br, brain.Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "next"}}); strings.Contains(preface, "4839") || strings.Contains(preface, c.edit) {
				t.Errorf("preface = %q, want no trace of the edit", preface)
			}
			if a := tb.activity.Recent(10, "main"); len(a) != 1 {
				t.Errorf("activity = %+v, want only the request", a)
			}
			var got struct{ Code string }
			data, err := os.ReadFile(filepath.Join(tb.cfgs.Get().DataDir, "otp", "main.json"))
			if err == nil {
				err = json.Unmarshal(data, &got)
			} else if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
			if err != nil || got.Code != c.handed {
				t.Errorf("the code handed over = %q (%v), want %q", got.Code, err, c.handed)
			}
			if rt := br.Snapshot(); rt.Running != nil || len(rt.Queue) > 0 || rt.Last != nil {
				t.Errorf("the edit reached the brain: %+v", rt)
			}
		})
	}
}

// The dashboard's activity feed follows the history: a deleted message
// leaves it, and a waiting one that was edited shows its new text.
func TestActivityFollowsChanges(t *testing.T) {
	tb, br := newMainBridge(t, "")
	startLongRun(t, tb, br, brain.Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "a long report", Source: chat.SourceWhatsApp}})
	wa := tb.whatsApp(t)
	wa.ProcessMessage(dmEvent("3EB0A", textMsg("my card number is 4111")))
	wa.ProcessMessage(dmEvent("3EB0B", textMsg("book Tuesday")))
	testutil.WaitFor(t, 5*time.Second, "both to wait", func() bool { return len(br.Snapshot().Queue) == 2 })
	wa.ProcessMessage(dmEvent("3EB0DEL", revokeMsg("3EB0A")))
	wa.ProcessMessage(dmEvent("3EB0EDIT", editMsg("3EB0B", "book Wednesday")))
	var texts []string
	for _, e := range tb.activity.Recent(10, "main") {
		texts = append(texts, e.Text)
	}
	slices.Sort(texts)
	if want := []string{"a long report", "book Wednesday"}; !slices.Equal(texts, want) {
		t.Errorf("activity = %q, want %q", texts, want)
	}
}
