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

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// dmEvent is a DM from the allowed number, with the message ID id.
func dmEvent(id string, msg *waE2E.Message) *events.Message {
	chat := types.NewJID("15551234567", types.DefaultUserServer)
	return &events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: types.MessageID(id), Timestamp: time.Now()},
		Message: msg,
	}
}

func textMsg(s string) *waE2E.Message { return &waE2E.Message{Conversation: proto.String(s)} }

// revokeMsg deletes the message id for everyone.
func revokeMsg(id string) *waE2E.Message {
	return &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: &waCommon.MessageKey{ID: proto.String(id)},
	}}
}

// editMsg gives the message id the text s.
func editMsg(id, s string) *waE2E.Message {
	return &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: &waCommon.MessageKey{ID: proto.String(id)}, EditedMessage: textMsg(s),
	}}
}

// historyTexts returns the text of the brain's history lines of type typ.
func historyTexts(t *testing.T, br *Brain, typ string) []string {
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
		startLongRun(t, tb, br, Inbound{JID: testDMChat, Sender: "15551234567", Text: "a long report", Source: sourceWhatsApp})
		wa := tb.whatsApp()
		wa.handleMessage(dmEvent("3EB0WAIT", textMsg("my card number is 4111")))
		waitFor(t, 5*time.Second, "the message to wait", func() bool { return len(br.Snapshot().Queue) == 1 })
		wa.handleMessage(dmEvent("3EB0DEL", revokeMsg("3EB0WAIT")))
		if q := br.Snapshot().Queue; len(q) != 0 {
			t.Errorf("queue = %+v, want the deleted message gone", q)
		}
		if got := historyTexts(t, br, historyIn); !slices.Equal(got, []string{"a long report"}) {
			t.Errorf("history holds %q, want the deleted message out", got)
		}
		if ev := historyTexts(t, br, historyEvent); len(ev) != 0 {
			t.Errorf("events = %q, want none for a message no run read", ev)
		}
	})

	t.Run("being answered", func(t *testing.T) {
		tb, br := newMainBridge(t, "")
		t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "30000")
		wa := tb.whatsApp()
		wa.handleMessage(dmEvent("3EB0RUN", textMsg("write it all down")))
		waitFor(t, 5*time.Second, "the run", func() bool { r := br.Snapshot().Running; return r != nil && r.PID > 0 })
		wa.handleMessage(dmEvent("3EB0DEL", revokeMsg("3EB0RUN")))
		waitFor(t, 10*time.Second, "the run to stop", func() bool { return br.Snapshot().Running == nil })
		time.Sleep(200 * time.Millisecond)
		if sent := tb.sentTo(testDMChat); len(sent) != 0 {
			t.Errorf("sent %q for a deleted message", sent)
		}
		if got := historyTexts(t, br, historyIn); len(got) != 0 {
			t.Errorf("history holds %q, want the deleted message out", got)
		}
		if ev := historyTexts(t, br, historyEvent); !slices.Equal(ev, []string{"deleted a message they had sent"}) {
			t.Errorf("events = %q, want the deletion noted", ev)
		}
	})

	t.Run("answered", func(t *testing.T) {
		tb, br := newMainBridge(t, "")
		wa := tb.whatsApp()
		wa.handleMessage(dmEvent("3EB0DONE", textMsg("what is my PIN again")))
		waitFor(t, 10*time.Second, "the answer", func() bool { return len(tb.sentTo(testDMChat)) == 1 })
		waitFor(t, 5*time.Second, "the answer in the history", func() bool { return len(historyTexts(t, br, historyOut)) == 1 })
		wa.handleMessage(dmEvent("3EB0DEL", revokeMsg("3EB0DONE")))
		if got := historyTexts(t, br, historyIn); len(got) != 0 {
			t.Errorf("history holds %q, want the deleted message out", got)
		}
		if ev := historyTexts(t, br, historyEvent); !slices.Equal(ev, []string{"deleted a message they had sent"}) {
			t.Errorf("events = %q, want the deletion noted", ev)
		}
		if out := historyTexts(t, br, historyOut); len(out) != 1 {
			t.Errorf("the brain's answer = %q, want it kept", out)
		}
	})

	t.Run("someone else's", func(t *testing.T) {
		tb, br := newMainBridge(t, "")
		startLongRun(t, tb, br, Inbound{JID: testGroup, Sender: "15551234567", Text: "a long report", Source: sourceWhatsApp})
		tb.handleInbound(t.Context(), Inbound{JID: testGroup, Sender: "15551234567", Text: "book Tuesday", Source: sourceWhatsApp, ID: "3EB0MINE"})
		tb.handleChange(messageChange{Kind: changeRevoke, Chat: testGroup, Sender: otherNumber, Target: "3EB0MINE"})
		if q := br.Snapshot().Queue; len(q) != 1 {
			t.Errorf("queue = %+v: another member's deletion took the message", q)
		}
		if got := historyTexts(t, br, historyIn); !slices.Contains(got, "book Tuesday") {
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
		startLongRun(t, tb, br, Inbound{JID: testDMChat, Sender: "15551234567", Text: "a long report", Source: sourceWhatsApp})
		wa := tb.whatsApp()
		wa.handleMessage(dmEvent("3EB0WAIT", textMsg("book Tuesday")))
		waitFor(t, 5*time.Second, "the message to wait", func() bool { return len(br.Snapshot().Queue) == 1 })
		wa.handleMessage(dmEvent("3EB0EDIT", editMsg("3EB0WAIT", "book Wednesday")))
		if q := br.Snapshot().Queue; len(q) != 1 || q[0].Preview != "book Wednesday" {
			t.Errorf("queue = %+v, want the edited text waiting", q)
		}
		if got := historyTexts(t, br, historyIn); !slices.Equal(got, []string{"a long report", "book Wednesday"}) {
			t.Errorf("history holds %q, want the edit in place", got)
		}
		t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "10")
		br.StopCurrent(false)
		waitFor(t, 10*time.Second, "the edited message's answer", func() bool { return tb.sentPrefix(testDMChat, "echo:") })
		if got := tb.sentTo(testDMChat); !strings.HasSuffix(got[len(got)-1], "15551234567 says: book Wednesday") {
			t.Errorf("answered %q, want the edited text", got)
		}
	})

	t.Run("answered", func(t *testing.T) {
		tb, br := newMainBridge(t, "")
		wa := tb.whatsApp()
		wa.handleMessage(dmEvent("3EB0DONE", textMsg("book Tuesday")))
		waitFor(t, 10*time.Second, "the answer", func() bool { return len(tb.sentTo(testDMChat)) == 1 })
		wa.handleMessage(dmEvent("3EB0EDIT", editMsg("3EB0DONE", "book Wednesday")))
		time.Sleep(300 * time.Millisecond)
		if sent := tb.sentTo(testDMChat); len(sent) != 1 {
			t.Errorf("sent %q: the edit started a run", sent)
		}
		if got := historyTexts(t, br, historyIn); !slices.Equal(got, []string{"book Tuesday"}) {
			t.Errorf("history holds %q, want what the run read", got)
		}
		if ev := historyTexts(t, br, historyEvent); !slices.Equal(ev, []string{`edited a message to: "book Wednesday"`}) {
			t.Errorf("events = %q, want the edit noted", ev)
		}
		if preface := br.buildPrompt(br.settings.Load(), Inbound{JID: testDMChat, Sender: "15551234567", Text: "next"}); !strings.Contains(preface, `"from":"15551234567","did":"edited a message to: \"book Wednesday\""`) {
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
			br.appendHistory(askIn, "main", "🔐 Please send me the verification code", historyOut)
			if c.code {
				tb.handleInbound(t.Context(), Inbound{JID: testDMChat, ChatAlt: c.alt, Sender: "15551234567", Text: "483920", Source: sourceWhatsApp, ID: "3EB0OTP"})
			}
			tb.handleChange(messageChange{Kind: changeEdit, Chat: testDMChat, ChatAlt: c.alt, Sender: "15551234567", Target: "3EB0OTP", Text: c.edit})

			if h := readHistory(t, br); len(h) != 1 || h[0].Type != historyOut {
				t.Errorf("history = %+v, want only the request", h)
			}
			if preface := br.buildPrompt(br.settings.Load(), Inbound{JID: testDMChat, Sender: "15551234567", Text: "next"}); strings.Contains(preface, "4839") || strings.Contains(preface, c.edit) {
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

// A deletion or an edit of a message the debouncer still holds reaches
// the brain with the batch: the deleted part is gone, the edited one has
// its new text.
func TestDebouncerAppliesChanges(t *testing.T) {
	d, f := newTestDebouncer(80*time.Millisecond, time.Second)
	base := Inbound{JID: testDMChat, Sender: "15551234567"}
	only := base
	only.Text, only.ID = "a secret", "A"
	d.Add(only)
	if !d.Change(messageChange{Kind: changeRevoke, Chat: testDMChat, Sender: "15551234567", Target: "A"}) {
		t.Fatal("the deletion did not find the message")
	}
	if d.Pending() != 0 {
		t.Errorf("%d batches wait, want the emptied one dropped", d.Pending())
	}

	first, second := base, base
	first.Text, first.ID = "book Tuesday", "B"
	second.Text, second.ID = "for four", "C"
	d.Add(first)
	d.Add(second)
	d.Change(messageChange{Kind: changeEdit, Chat: testDMChat, Sender: "15551234567", Target: "B", Text: "book Wednesday"})
	waitFor(t, 2*time.Second, "the flush", func() bool { return f.n() == 1 })
	if got := f.got[0]; got.Text != "book Wednesday\n\nfor four" {
		t.Errorf("flushed %q, want the edited batch", got.Text)
	}
}

// A view-once photo is not downloaded or kept, and the sender is told.
func TestViewOnceIsNotOpened(t *testing.T) {
	tw := newTestWhatsApp(t)
	dl := &fakeDownloader{body: "jpeg"}
	tw.media = dl
	evt := dmEvent("3EB0ONCE", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("just for you")}})
	evt.IsViewOnce = true
	tw.handle(t, evt)
	select {
	case n := <-tw.notices:
		if !strings.Contains(n, "view-once") {
			t.Errorf("notice %q, want one about view-once", n)
		}
	case <-time.After(2 * time.Second):
		t.Error("the sender was not told")
	}
	if m, ok := tw.next(100 * time.Millisecond); ok {
		t.Errorf("handed to a brain: %+v", m)
	}
	if dl.callCount() != 0 {
		t.Error("the photo was downloaded")
	}
}

// A location shared in a DM reaches the brain as a line it can read.
func TestLocationInADMReachesTheBrain(t *testing.T) {
	tw := newTestWhatsApp(t)
	tw.handle(t, dmEvent("3EB0LOC", &waE2E.Message{LocationMessage: &waE2E.LocationMessage{
		DegreesLatitude: proto.Float64(47.3769), DegreesLongitude: proto.Float64(8.5417),
	}}))
	m, ok := tw.next(2 * time.Second)
	if !ok || m.Text != "[location: 47.376900, 8.541700]" || m.ID != "3EB0LOC" {
		t.Errorf("got %+v (delivered %v), want the location as text", m, ok)
	}
}

// The dashboard's activity feed follows the history: a deleted message
// leaves it, and a waiting one that was edited shows its new text.
func TestActivityFollowsChanges(t *testing.T) {
	tb, br := newMainBridge(t, "")
	startLongRun(t, tb, br, Inbound{JID: testDMChat, Sender: "15551234567", Text: "a long report", Source: sourceWhatsApp})
	wa := tb.whatsApp()
	wa.handleMessage(dmEvent("3EB0A", textMsg("my card number is 4111")))
	wa.handleMessage(dmEvent("3EB0B", textMsg("book Tuesday")))
	waitFor(t, 5*time.Second, "both to wait", func() bool { return len(br.Snapshot().Queue) == 2 })
	wa.handleMessage(dmEvent("3EB0DEL", revokeMsg("3EB0A")))
	wa.handleMessage(dmEvent("3EB0EDIT", editMsg("3EB0B", "book Wednesday")))
	var texts []string
	for _, e := range tb.activity.Recent(10, "main") {
		texts = append(texts, e.Text)
	}
	slices.Sort(texts)
	if want := []string{"a long report", "book Wednesday"}; !slices.Equal(texts, want) {
		t.Errorf("activity = %q, want %q", texts, want)
	}
}

// A change to a message the history does not hold leaves the file alone:
// it is read, not copied, which a brain folder the bridge cannot write to
// shows.
func TestChangeOfAMessageTheHistoryLacksWritesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a folder whatever its mode")
	}
	_, br := newMainBridge(t, "")
	br.appendEntry(historyEntry{From: "15551234567", Chat: testDMChat, Text: "hi", Type: historyIn, ID: "3EB0A"})
	if err := os.Chmod(br.Dir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(br.Dir(), 0o700) })
	drop := func(historyEntry) *historyEntry { return nil }
	if n, err := br.rewriteHistory("3EB0B", func(e historyEntry) bool { return e.ID == "3EB0B" }, drop); n != 0 || err != nil {
		t.Errorf("rewriteHistory for an absent ID = %d, %v; want 0, nil without writing", n, err)
	}
	if _, err := br.rewriteHistory("3EB0A", func(e historyEntry) bool { return e.ID == "3EB0A" }, drop); err == nil {
		t.Errorf("rewriteHistory for a present ID wrote into a folder it cannot write to")
	}
}
