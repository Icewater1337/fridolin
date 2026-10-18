package main

import (
	"context"
	"errors"
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
