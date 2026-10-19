package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/chat"
	prompttext "github.com/Icewater1337/fridolin/internal/prompt"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

func TestObserveRuns(t *testing.T) {
	chatter := brain.Inbound{Inbound: chat.Inbound{JID: testGroup, Sender: "15551234567", Text: "we meet at noon", Silent: true}}
	junk := brain.Inbound{Inbound: chat.Inbound{JID: testGroup, Sender: "15551234567", Text: "ok", Silent: true}}
	file := brain.Inbound{Inbound: chat.Inbound{JID: testGroup, Sender: "15551234567", Media: []string{"/m/1.jpg"}, Silent: true}}
	cases := []struct {
		name string
		mode string
		m    brain.Inbound
		want bool
	}{
		{"chatter, default", "", chatter, false},
		{"chatter, history", manifest.ObserveHistory, chatter, false},
		{"chatter, run", manifest.ObserveRun, chatter, true},
		{"junk, run", manifest.ObserveRun, junk, false},
		{"a file, run", manifest.ObserveRun, file, true},
		{"a file, history", manifest.ObserveHistory, file, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := observeRuns(c.mode, c.m); got != c.want {
				t.Errorf("observeRuns(%q, %q) = %v, want %v", c.mode, c.m.Text, got, c.want)
			}
		})
	}
}

// Untagged group chatter goes into the brain's history, and gets a run of
// its own only when the brain observes with runs. Either way nothing is
// sent back.
func TestUntaggedChatterRunsOnlyWhenObserved(t *testing.T) {
	cases := []struct {
		observe string
		runs    int // Claude runs for the chatter and the DM after it
	}{
		{"", 1},
		{manifest.ObserveHistory, 1},
		{manifest.ObserveRun, 2},
	}
	for _, c := range cases {
		t.Run("observe "+c.observe, func(t *testing.T) {
			tb, br := newMainBridgeWith(t, `, "queue": {"notify": false}`, manifest.Manifest{Observe: c.observe})
			argsLog := filepath.Join(tb.home, "args.log")
			t.Setenv("FRIDOLIN_FAKE_ARGS_LOG", argsLog)
			ctx := context.Background()
			tb.handleInbound(ctx, brain.Inbound{Inbound: chat.Inbound{JID: testGroup, Sender: otherNumber, Text: "we meet at noon", Silent: true, Source: chat.SourceWhatsApp}})
			testutil.WaitFor(t, 5*time.Second, "the chatter in the history", func() bool { return len(readHistory(t, br)) == 1 })
			if c.runs > 1 {
				// The DM would stop the observe-only run, see
				// TestAddressedWorkGoesAheadOfObserveRuns.
				testutil.WaitFor(t, 5*time.Second, "the observe-only run", func() bool { return br.Snapshot().Last != nil })
			}
			tb.handleInbound(ctx, brain.Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "hello", Source: chat.SourceWhatsApp}})
			testutil.WaitFor(t, 10*time.Second, "the DM's answer", func() bool { return len(tb.sentTo(testDMChat)) == 1 })

			data, _ := os.ReadFile(argsLog)
			if runs := strings.Count(string(data), "\n"); runs != c.runs {
				t.Errorf("%d Claude runs, want %d", runs, c.runs)
			}
			var chatter []string
			for _, e := range readHistory(t, br) {
				if e.Chat == testGroup {
					chatter = append(chatter, e.Type+": "+e.Text)
				}
			}
			if strings.Join(chatter, "|") != "in: we meet at noon" {
				t.Errorf("the group's history = %q, want only the chatter", chatter)
			}
			if sent := tb.sentTo(testGroup); len(sent) > 0 {
				t.Errorf("sent %q to the group", sent)
			}
		})
	}
}

// Work addressed to a brain does not wait for observe-only runs: it stops
// the one in flight, whose message is in the history already, and goes
// ahead of the chatter that waits for one.
func TestAddressedWorkGoesAheadOfObserveRuns(t *testing.T) {
	t.Run("the run in flight stops", func(t *testing.T) {
		tb, br := newObservingBridge(t, `, "queue": {"notify": false}`)
		startLongRun(t, tb, br, brain.Inbound{Inbound: chat.Inbound{JID: testGroup, Sender: otherNumber, Text: "long chatter", Silent: true, Source: chat.SourceWhatsApp}})
		t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "10")
		tb.handleInbound(context.Background(), brain.Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "hello", Source: chat.SourceWhatsApp}})
		// The observe-only run would sleep for 30 seconds.
		testutil.WaitFor(t, 10*time.Second, "the DM's answer", func() bool { return len(tb.sentTo(testDMChat)) == 1 })
		if sent := tb.sentTo(testGroup); len(sent) > 0 {
			t.Errorf("sent %q to the group", sent)
		}
	})
	t.Run("the queue", func(t *testing.T) {
		tb, br := newObservingBridge(t, "")
		startLongRun(t, tb, br, brain.Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "long task", Source: chat.SourceWhatsApp}})
		for _, g := range []string{"120363000000000001@g.us", "120363000000000002@g.us"} {
			if _, _, err := br.Submit(brain.Inbound{Inbound: chat.Inbound{JID: g, Sender: otherNumber, Text: "chatter in " + g, Silent: true}}); err != nil {
				t.Fatal(err)
			}
		}
		pos, busy, err := br.Submit(brain.Inbound{Inbound: chat.Inbound{JID: testGroup, Sender: otherNumber, Text: "a question"}})
		if err != nil || pos != 1 || !busy {
			t.Errorf("Submit = #%d, busy %v, %v; want #1 behind the run in flight", pos, busy, err)
		}
		var queued []string
		for _, q := range br.Snapshot().Queue {
			queued = append(queued, q.Preview)
		}
		want := "a question|chatter in 120363000000000001@g.us|chatter in 120363000000000002@g.us"
		if strings.Join(queued, "|") != want {
			t.Errorf("queue = %q, want %q", queued, want)
		}
		if r := br.Snapshot().Running; r == nil || r.Preview != "long task" {
			t.Errorf("running = %+v, want the DM's run untouched", r)
		}
	})
}

// A run for untagged chatter is told so: its prompt marks it observe-only
// and says nothing about questions reaching the chat. A run for a message
// addressed to the brain in the same group is not marked.
func TestObserveRunsAreToldSo(t *testing.T) {
	tb, br := newObservingBridge(t, "")
	promptLog := filepath.Join(tb.home, "prompts.log")
	t.Setenv("FRIDOLIN_FAKE_PROMPT_LOG", promptLog)
	ctx := context.Background()
	tb.handleInbound(ctx, brain.Inbound{Inbound: chat.Inbound{JID: testGroup, Sender: otherNumber, Text: "CHATTER", Silent: true, Source: chat.SourceWhatsApp}})
	testutil.WaitFor(t, 5*time.Second, "the observe-only run", func() bool { return br.Snapshot().Last != nil })
	tb.handleInbound(ctx, brain.Inbound{Inbound: chat.Inbound{JID: testGroup, Sender: otherNumber, Text: "REQUEST", Source: chat.SourceWhatsApp}})
	testutil.WaitFor(t, 10*time.Second, "the answer", func() bool { return len(tb.sentTo(testGroup)) == 1 })

	prompts := loggedPrompts(t, promptLog)
	cases := []struct {
		msg     string
		observe bool
	}{
		{"CHATTER", true},
		{"REQUEST", false},
	}
	for _, c := range cases {
		t.Run(c.msg, func(t *testing.T) {
			p := prompts[otherNumber+" says: "+c.msg]
			if p == "" {
				t.Fatalf("no prompt for %s in %q", c.msg, prompts)
			}
			if got := strings.Contains(p, prompttext.ObserveOnlyNote); got != c.observe {
				t.Errorf("marked observe-only: %v, want %v:\n%s", got, c.observe, p)
			}
			if got := strings.Contains(p, prompttext.AskUserQuestionNote); got == c.observe {
				t.Errorf("told about AskUserQuestion: %v, want %v", got, !c.observe)
			}
		})
	}
}
