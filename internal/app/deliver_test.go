package app

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/devchat"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// pollRefuser is the dev messenger in front of a WhatsApp that is
// connected but refuses every poll, so that the outbox sends each as text.
type pollRefuser struct{ *devchat.Messenger }

func (pollRefuser) SendPoll(context.Context, string, string, []string, int, func(string)) (string, error) {
	return "", errors.New("polls are refused")
}

// outbound returns the brain's history lines of what it sent, by chat.
func outbound(t *testing.T, br *brain.Brain) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, e := range readHistory(t, br) {
		if e.Type == history.Out {
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
	t.Parallel()
	const poll = "[POLL]\nQuestion: Lunch?\n- Yes\n- No\n[/POLL]"
	const pollText = "📊 *Lunch?*\n1. Yes\n2. No"
	offline := func(tb *testBridge) Messenger {
		return newFlakyMessenger(func() int { return tb.cfgs.Get().MaxChunkSize }, false)
	}
	refusePolls := func(tb *testBridge) Messenger { return pollRefuser{tb.dev} }
	cases := []struct {
		name   string
		wa     func(tb *testBridge) Messenger // nil: the dev messenger
		silent bool
		res    claudecli.Result
		want   []string // the brain's outbound history in the chat
	}{
		{name: "a reply", res: claudecli.Result{Text: "Here you go."}, want: []string{"Here you go."}},
		{name: "a poll and text", res: claudecli.Result{Text: "See you there.\n" + poll}, want: []string{pollText + "\n\nSee you there."}},
		{name: "a poll sent as text", wa: refusePolls, res: claudecli.Result{Text: poll + "\nSee you there."}, want: []string{pollText + "\n\nSee you there."}},
		{name: "a poll alone", res: claudecli.Result{Text: poll}, want: []string{pollText}},
		{name: "WhatsApp is down", wa: offline, res: claudecli.Result{Text: "Here you go."}},
		{name: "a silent run", silent: true, res: claudecli.Result{Text: "Noted."}},
		{name: "an error", res: claudecli.Result{Err: errors.New("claude error: quota")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb, br := newMainBridge(t, "")
			if c.wa != nil {
				tb.wa = c.wa(tb)
			}
			tb.pipeline.Finished(br, brain.Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "hi", Silent: c.silent}}, c.res)
			if len(c.want) > 0 {
				testutil.WaitFor(t, 5*time.Second, "the reply in the history", func() bool { return len(outbound(t, br)[testDMChat]) > 0 })
			} else if f, ok := tb.wa.(*flakyMessenger); ok {
				testutil.WaitFor(t, 5*time.Second, "a failed attempt", func() bool {
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
	t.Parallel()
	tb, br := newObservingBridge(t, `, "queue": {"notify": false}`)
	ctx := context.Background()
	tb.handleInbound(ctx, brain.Inbound{Inbound: chat.Inbound{JID: testGroup, Sender: "15551234567", Text: "we meet at noon", Silent: true, Source: chat.SourceWhatsApp}})
	// The silent run ends with an answer before the DM comes in, so that
	// it is not stopped for the DM, and is delivered before the DM's run
	// starts: one worker runs the brain's messages in turn.
	testutil.WaitFor(t, 10*time.Second, "the silent run", func() bool { return br.Snapshot().Last != nil })
	if last := br.Snapshot().Last; last.Chat != testGroup || last.Outcome != "ok" {
		t.Fatalf("the silent run ended %+v, want it answered", last)
	}
	tb.handleInbound(ctx, brain.Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "hello", Source: chat.SourceWhatsApp}})
	testutil.WaitFor(t, 10*time.Second, "the DM's answer", func() bool { return len(tb.sentTo(testDMChat)) == 1 })
	// It goes into the history just after it was sent.
	testutil.WaitFor(t, 5*time.Second, "the DM's answer in the history", func() bool { return len(outbound(t, br)[testDMChat]) > 0 })

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
	// Serial: shortens workingNoticeAfter, a package variable every run
	// reads.
	prev := workingNoticeAfter
	workingNoticeAfter = 50 * time.Millisecond
	t.Cleanup(func() { workingNoticeAfter = prev })
	dm := brain.Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "book a table", Source: chat.SourceWhatsApp}}
	cases := []struct {
		name    string
		m       brain.Inbound
		refused bool // bridge.db refuses to add to the outbox
		offline bool // WhatsApp is down
		sent    bool // the question reached the chat and the history
		asked   bool // the run ended as one that asked
		notice  bool // the working notice reached the chat
	}{
		{name: "a DM", m: dm, sent: true, asked: true, notice: true},
		{name: "a DM, the outbox refusing it", m: dm, refused: true, notice: true},
		{name: "a DM while WhatsApp is down", m: dm, offline: true, asked: true},
		{name: "untagged group chatter", m: brain.Inbound{Inbound: chat.Inbound{JID: testGroup, Sender: "15551234567", Text: "we meet at noon", Silent: true, Source: chat.SourceWhatsApp}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb, br := newObservingBridge(t, "")
			setenv(t, "FRIDOLIN_FAKE_ASK", `{"questions":[{"question":"Which day?"}]}`)
			setenv(t, "FRIDOLIN_FAKE_SLEEP_MS", "300") // time for the working notice
			if c.refused {
				if _, err := testStoreSQL(t, tb.cfgs.Startup().DBPath).Exec(`CREATE TRIGGER refuse_outbox BEFORE INSERT ON outbox BEGIN SELECT RAISE(FAIL, 'disk full'); END`); err != nil {
					t.Fatal(err)
				}
			}
			chatJID := func() []string { return tb.sentTo(c.m.JID) }
			if c.offline {
				wa := newFlakyMessenger(func() int { return tb.cfgs.Get().MaxChunkSize }, false)
				tb.wa = wa
				chatJID = func() []string { return wa.sent(c.m.JID) }
			}
			tb.handleInbound(context.Background(), c.m)
			testutil.WaitFor(t, 10*time.Second, "the run", func() bool { return br.Snapshot().Last != nil })
			if c.sent {
				testutil.WaitFor(t, 5*time.Second, "the question in the history", func() bool { return len(outbound(t, br)[c.m.JID]) > 0 })
			}
			if c.offline {
				if sum, err := tb.store.OutboxSummary(context.Background(), time.Now()); err != nil || sum.Waiting == 0 {
					t.Errorf("outbox = %+v, %v; want the question waiting", sum, err)
				}
			}

			sent := slices.Contains(chatJID(), "❓ Which day?")
			recorded := slices.Contains(outbound(t, br)[c.m.JID], "❓ Which day?")
			if sent != c.sent || recorded != c.sent {
				t.Errorf("question sent %v, in the history %v; want %v", sent, recorded, c.sent)
			}
			if last := br.Snapshot().Last; (last.Outcome == "question") != c.asked {
				t.Errorf("the run ended %q, want it to have asked: %v", last.Outcome, c.asked)
			}
			if notice := slices.Contains(chatJID(), outboundmsg.WorkingNotice); notice != c.notice {
				t.Errorf("working notice sent %v, want %v", notice, c.notice)
			}
		})
	}
}

// A run that asks with AskUserQuestion and then answers anyway gets both to
// the chat: the question with its options first, then the reply, and the
// history holds both in that order.
func TestQuestionAndFinalReplyBothReachTheChat(t *testing.T) {
	t.Parallel()
	tb, br := newMainBridge(t, "")
	setenv(t, "FRIDOLIN_FAKE_ASK", `{"questions":[{"header":"Day","question":"Which day?","options":[{"label":"Monday"},{"label":"Tuesday"}]}]}`)
	tb.handleInbound(context.Background(), brain.Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "book a table", Source: chat.SourceWhatsApp}})
	testutil.WaitFor(t, 10*time.Second, "the reply", func() bool { return len(tb.sentTo(testDMChat)) == 2 })
	testutil.WaitFor(t, 5*time.Second, "the reply in the history", func() bool { return len(outbound(t, br)[testDMChat]) == 2 })

	sent := tb.sentTo(testDMChat)
	if sent[0] != "❓ Day: Which day?\n1. Monday\n2. Tuesday" || !strings.HasSuffix(sent[1], "says: book a table") {
		t.Errorf("sent %q, want the question with its options, then the reply", sent)
	}
	if got := outbound(t, br)[testDMChat]; !slices.Equal(got, sent) {
		t.Errorf("outbound history = %q, want what was sent, %q", got, sent)
	}
	if last := br.Snapshot().Last; last == nil || last.Outcome != "question" {
		t.Errorf("last run = %+v, want the outcome %q", last, "question")
	}
}

// A message that comes in while the run before it in the chat is still at
// work gets its run once that run's reply went out, and its prompt shows
// the reply as sent since it came in: its sender had not seen it. So does
// the answer to a question the run asked before it was over, which must
// learn what that run settled on in the end ("I booked Monday").
func TestQueuedRunSeesWhatWasSentWhileItWaited(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		env           map[string]string
		first, second string
		// asked is the question the first run sends before the second
		// message comes in, "" for none.
		asked string
	}{
		{"a reply to the message before", map[string]string{"FRIDOLIN_FAKE_SLEEP_MS": "700"}, "FIRST", "SECOND", ""},
		{"the reply after a question", map[string]string{"FRIDOLIN_FAKE_ASK_ON": "a table", "FRIDOLIN_FAKE_SLEEP_AFTER_ASK_MS": "700"}, "book a table", "Tuesday", "❓ Which day suits you?"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb, br := newMainBridge(t, `, "queue": {"notify": false}`)
			promptLog := filepath.Join(tb.home, "prompts.log")
			setenv(t, "FRIDOLIN_FAKE_PROMPT_LOG", promptLog)
			for k, v := range c.env {
				setenv(t, k, v)
			}
			ctx := context.Background()
			tb.handleInbound(ctx, brain.Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: c.first, Source: chat.SourceWhatsApp}})
			if c.asked == "" {
				testutil.WaitFor(t, 10*time.Second, "the first run", func() bool { return br.Snapshot().Running != nil })
			} else {
				// In the history, as the chat has it before anyone answers.
				testutil.WaitFor(t, 10*time.Second, "the question", func() bool { return slices.Contains(outbound(t, br)[testDMChat], c.asked) })
			}
			tb.handleInbound(ctx, brain.Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: c.second, Source: chat.SourceWhatsApp}})
			replies := func() []string {
				var out []string
				for _, s := range tb.sentTo(testDMChat) {
					if s != c.asked {
						out = append(out, s)
					}
				}
				return out
			}
			testutil.WaitFor(t, 10*time.Second, "both replies", func() bool { return len(replies()) == 2 })

			first := replies()[0]
			if !strings.HasSuffix(first, "says: "+c.first) {
				t.Fatalf("the first reply is %q, want the answer to %q", first, c.first)
			}
			var prompt string
			for msg, p := range loggedPrompts(t, promptLog) {
				if strings.HasSuffix(msg, "says: "+c.second) {
					prompt = p
				}
			}
			history, since := promptBlock(prompt, "chat_history"), promptBlock(prompt, "sent_since")
			if !strings.Contains(history, c.first) || !strings.Contains(history, c.asked) {
				t.Errorf("the history in the second prompt lacks %q or %q:\n%s", c.first, c.asked, prompt)
			}
			if !strings.Contains(since, first) || strings.Contains(history, first) {
				t.Errorf("the second prompt does not show the first reply as sent since it came in:\n%s", prompt)
			}
		})
	}
}
