package brain

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// TestStreamLogIsRotatedAndNamesEachRun: a full activity log is moved to
// claude-stream.1.log when the next run starts, and each run's part of it
// starts with the chat, the sender, the process and the model, in a line
// RunHeadingPID reads back.
func TestStreamLogIsRotatedAndNamesEachRun(t *testing.T) {
	mgr, d, home := queueTestManager(t, 10, "")
	settings := *mgr.Get("a").settings.Load()
	settings.Model = "sonnet"
	mgr.Get("a").settings.Store(&settings)
	logPath := filepath.Join(home, "a", StreamLogName)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(streamLogMax); err != nil { // sparse: takes no room
		t.Fatal(err)
	}
	f.Close()
	if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 10*time.Second, "the run", func() bool { return d.count() == 1 })
	if fi, err := os.Stat(filepath.Join(home, "a", StreamLogOld)); err != nil || fi.Size() != streamLogMax {
		t.Errorf("rotated log: %v, %v; want the full one", fi, err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(string(data), "\n")
	if len(data) > 4096 || !strings.Contains(first, "Run for 15551234567@s.whatsapp.net from 15551234567, pid ") || !strings.Contains(first, "model sonnet") {
		t.Errorf("new log starts %q (%d bytes), want the run named", first, len(data))
	}
	// The Build page finds the run's part of the log by this heading.
	if pid, ok := RunHeadingPID(first); !ok || pid <= 0 {
		t.Errorf("RunHeadingPID(%q) = %d, %v; want the run's pid", first, pid, ok)
	}
}

// A message's history line is written once the brain takes the message,
// and says when it was sent: not when the debouncer let it through. A
// message the brain turns away leaves no line, or the sender's retry, or
// the brain it is routed to instead, would put it in the history twice.
func TestHistoryLineIsWrittenOnceTakenAtItsOwnTime(t *testing.T) {
	sent := time.Now().Add(-time.Hour).Truncate(time.Second)
	cases := []struct {
		name  string
		setup func(t *testing.T, br *Brain)
		m     Inbound
		err   error
		want  []string // the history lines' text and time, "text@ts"
	}{
		{"a message, at its time", nil, Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "hi", At: sent}},
			nil, []string{"hi@" + sent.Format(time.RFC3339)}},
		{"each part at its own time", nil, Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "photo", At: sent}}.Merge(
			Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "what is it?", At: sent.Add(5 * time.Second)}}),
			nil, []string{"photo@" + sent.Format(time.RFC3339), "what is it?@" + sent.Add(5*time.Second).Format(time.RFC3339)}},
		{"turned away by a full queue", func(t *testing.T, br *Brain) {
			if _, _, err := br.Submit(dm("long task")); err != nil {
				t.Fatal(err)
			}
			testutil.WaitFor(t, 5*time.Second, "the first run", func() bool { r := br.Snapshot().Running; return r != nil && r.PID > 0 })
			if _, _, err := br.Submit(Inbound{Inbound: chat.Inbound{JID: testGroup, Sender: otherNumber, Text: "waiting"}}); err != nil {
				t.Fatal(err)
			}
		}, Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "one more", At: sent}}, ErrQueueFull, []string{"long task", "waiting"}},
		{"chatter that finds no place", func(t *testing.T, br *Brain) {
			// b observes its group with runs, and its one place is taken.
			if _, _, err := br.Submit(Inbound{Inbound: chat.Inbound{JID: "120363000000000000@g.us", Sender: otherNumber, Text: "long task"}}); err != nil {
				t.Fatal(err)
			}
			testutil.WaitFor(t, 5*time.Second, "the first run", func() bool { r := br.Snapshot().Running; return r != nil && r.PID > 0 })
			if _, _, err := br.Submit(Inbound{Inbound: chat.Inbound{JID: "120363000000000000@g.us", Sender: otherNumber, Text: "waiting"}}); err != nil {
				t.Fatal(err)
			}
		}, Inbound{Inbound: chat.Inbound{JID: "120363000000000000@g.us", Sender: "15551234567", Text: "chatter", At: sent, Silent: true}},
			nil, []string{"long task", "waiting", "chatter@" + sent.Format(time.RFC3339)}},
		{"turned away by a retired brain", func(t *testing.T, br *Brain) {
			br.mu.Lock()
			br.retired = true
			br.mu.Unlock()
		}, Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "too late", At: sent}}, ErrRetired, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mgr, _, _ := queueTestManager(t, 30000, "")
			for _, b := range mgr.Brains() {
				settings := *b.settings.Load()
				settings.MaxPending = 1
				b.settings.Store(&settings)
			}
			br := mgr.Get("a")
			if c.m.IsGroup() {
				br = mgr.Get("b")
			}
			if c.setup != nil {
				c.setup(t, br)
			}
			if _, _, err := br.Submit(c.m); !errors.Is(err, c.err) {
				t.Fatalf("Submit: %v, want %v", err, c.err)
			}
			var got []string
			for _, e := range readHistory(t, br) {
				if e.TS == sent.Format(time.RFC3339) || e.TS == sent.Add(5*time.Second).Format(time.RFC3339) {
					got = append(got, e.Text+"@"+e.TS)
				} else {
					got = append(got, e.Text)
				}
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("history = %q, want %q", got, c.want)
			}
		})
	}
}
