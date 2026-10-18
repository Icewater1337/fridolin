package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// pollRefuser is the dev messenger in front of a WhatsApp that is
// connected but refuses every poll, so that the outbox sends each as text.
type pollRefuser struct{ *devMessenger }

func (pollRefuser) SendPoll(context.Context, string, string, []string, int) (string, error) {
	return "", errors.New("polls are refused")
}

// outbound returns the brain's history lines of what it sent, by chat.
func outbound(t *testing.T, br *Brain) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, e := range readHistory(t, br) {
		if e.Type == historyOut {
			if e.From != br.ID() {
				t.Errorf("outbound line %+v is not from brain %s", e, br.ID())
			}
			out[e.Chat] = append(out[e.Chat], e.Text)
		}
	}
	return out
}

// A brain's history holds what its replies put in the chat, written once
// all of it went out: each poll as its question and numbered options,
// whether WhatsApp took it as a poll or not, then the text, never the
// [POLL] markup. A reply still waiting for WhatsApp is not there yet, nor
// is a silent run's or an error the bridge reports, so later runs and the
// OTP guard do not take them for something the brain said.
func TestRepliesAreRecordedAsSent(t *testing.T) {
	const poll = "[POLL]\nQuestion: Lunch?\n- Yes\n- No\n[/POLL]"
	const pollText = "📊 *Lunch?*\n1. Yes\n2. No"
	offline := func(tb *testBridge) Messenger { return newFlakyMessenger(tb.cfgs.Get, false) }
	refusePolls := func(tb *testBridge) Messenger { return pollRefuser{tb.dev} }
	cases := []struct {
		name   string
		wa     func(tb *testBridge) Messenger // nil: the dev messenger
		silent bool
		res    RunResult
		want   []string // the brain's outbound history in the chat
	}{
		{name: "a reply", res: RunResult{Text: "Here you go."}, want: []string{"Here you go."}},
		{name: "a poll and text", res: RunResult{Text: "See you there.\n" + poll}, want: []string{pollText + "\n\nSee you there."}},
		{name: "a poll sent as text", wa: refusePolls, res: RunResult{Text: poll + "\nSee you there."}, want: []string{pollText + "\n\nSee you there."}},
		{name: "a poll alone", res: RunResult{Text: poll}, want: []string{pollText}},
		{name: "WhatsApp is down", wa: offline, res: RunResult{Text: "Here you go."}},
		{name: "a silent run", silent: true, res: RunResult{Text: "Noted."}},
		{name: "an error", res: RunResult{Err: errors.New("claude error: quota")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb, br := newMainBridge(t, "")
			if c.wa != nil {
				tb.wa = c.wa(tb)
			}
			tb.deliver(br, Inbound{JID: testDMChat, Sender: "15551234567", Text: "hi", Silent: c.silent}, c.res)
			if len(c.want) > 0 {
				waitFor(t, 5*time.Second, "the reply in the history", func() bool { return len(outbound(t, br)[testDMChat]) > 0 })
			} else if f, ok := tb.wa.(*flakyMessenger); ok {
				waitFor(t, 5*time.Second, "a failed attempt", func() bool {
					f.mu.Lock()
					defer f.mu.Unlock()
					return f.tries > 0
				})
			}
			if got := outbound(t, br)[testDMChat]; strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Errorf("outbound history = %q, want %q", got, c.want)
			}
		})
	}
}

// A run writes nothing to the history itself: what it answered goes there
// when it was sent. An untagged group message's run is never sent, so its
// answer is not in the history, while the DM's is.
func TestSilentRunLeavesNoReplyInTheHistory(t *testing.T) {
	tb, br := newMainBridge(t, `, "queue": {"notify": false}`)
	ctx := context.Background()
	tb.handleInbound(ctx, Inbound{JID: testGroup, Sender: "15551234567", Text: "we meet at noon", Silent: true, Source: sourceWhatsApp})
	// The silent run ends with an answer before the DM comes in, so that
	// it is not stopped for the DM, and is delivered before the DM's run
	// starts: one worker runs the brain's messages in turn.
	waitFor(t, 10*time.Second, "the silent run", func() bool { return br.Snapshot().Last != nil })
	if last := br.Snapshot().Last; last.Chat != testGroup || last.Outcome != outcomeOK {
		t.Fatalf("the silent run ended %+v, want it answered", last)
	}
	tb.handleInbound(ctx, Inbound{JID: testDMChat, Sender: "15551234567", Text: "hello", Source: sourceWhatsApp})
	waitFor(t, 10*time.Second, "the DM's answer", func() bool { return len(tb.sentTo(testDMChat)) == 1 })
	// It goes into the history just after it was sent.
	waitFor(t, 5*time.Second, "the DM's answer in the history", func() bool { return len(outbound(t, br)[testDMChat]) > 0 })

	out := outbound(t, br)
	if len(out[testGroup]) != 0 {
		t.Errorf("the silent run's answer is in the history: %q", out[testGroup])
	}
	if got := out[testDMChat]; len(got) != 1 || got[0] != tb.sentTo(testDMChat)[0] {
		t.Errorf("the DM's history = %q, want what was sent, %q", got, tb.sentTo(testDMChat))
	}
	if sent := tb.sentTo(testGroup); len(sent) != 0 {
		t.Errorf("sent %q to the group", sent)
	}
}

// What a run says before it ends reaches the chat only when the message was
// addressed to the brain: a silent run sends no working notice and asks no
// question, and the dashboard does not show it as having asked. A question
// goes into the history as the brain's once it went out, so that the run
// for the answer sees what it answers; while WhatsApp is down it waits in
// the outbox, and the run has asked. One the outbox could not take is on
// its way to nobody, and the run did not ask.
func TestMidRunMessagesOnlyForAddressedMessages(t *testing.T) {
	prev := workingNoticeAfter
	workingNoticeAfter = 50 * time.Millisecond
	t.Cleanup(func() { workingNoticeAfter = prev })
	dm := Inbound{JID: testDMChat, Sender: "15551234567", Text: "book a table", Source: sourceWhatsApp}
	cases := []struct {
		name    string
		m       Inbound
		refused bool // bridge.db refuses to add to the outbox
		offline bool // WhatsApp is down
		sent    bool // the question reached the chat and the history
		asked   bool // the run ended as one that asked
		notice  bool // the working notice reached the chat
	}{
		{name: "a DM", m: dm, sent: true, asked: true, notice: true},
		{name: "a DM, the outbox refusing it", m: dm, refused: true, notice: true},
		{name: "a DM while WhatsApp is down", m: dm, offline: true, asked: true},
		{name: "untagged group chatter", m: Inbound{JID: testGroup, Sender: "15551234567", Text: "we meet at noon", Silent: true, Source: sourceWhatsApp}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb, br := newMainBridge(t, "")
			t.Setenv("FRIDOLIN_FAKE_ASK", `{"questions":[{"question":"Which day?"}]}`)
			t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "300") // time for the working notice
			if c.refused {
				if _, err := tb.store.db.Exec(`CREATE TRIGGER refuse_outbox BEFORE INSERT ON outbox BEGIN SELECT RAISE(FAIL, 'disk full'); END`); err != nil {
					t.Fatal(err)
				}
			}
			chat := func() []string { return tb.sentTo(c.m.JID) }
			if c.offline {
				wa := newFlakyMessenger(tb.cfgs.Get, false)
				tb.wa = wa
				chat = func() []string { return wa.sent(c.m.JID) }
			}
			tb.handleInbound(context.Background(), c.m)
			waitFor(t, 10*time.Second, "the run", func() bool { return br.Snapshot().Last != nil })
			if c.sent {
				waitFor(t, 5*time.Second, "the question in the history", func() bool { return len(outbound(t, br)[c.m.JID]) > 0 })
			}
			if c.offline {
				if sum, err := tb.store.OutboxSummary(context.Background(), time.Now()); err != nil || sum.Waiting == 0 {
					t.Errorf("outbox = %+v, %v; want the question waiting", sum, err)
				}
			}

			sent := slices.Contains(chat(), "❓ Which day?")
			recorded := slices.Contains(outbound(t, br)[c.m.JID], "❓ Which day?")
			if sent != c.sent || recorded != c.sent {
				t.Errorf("question sent %v, in the history %v; want %v", sent, recorded, c.sent)
			}
			if last := br.Snapshot().Last; (last.Outcome == outcomeQuestion) != c.asked {
				t.Errorf("the run ended %q, want it to have asked: %v", last.Outcome, c.asked)
			}
			if notice := slices.Contains(chat(), workingNotice); notice != c.notice {
				t.Errorf("working notice sent %v, want %v", notice, c.notice)
			}
		})
	}
}
