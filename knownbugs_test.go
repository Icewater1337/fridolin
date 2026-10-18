package main

import (
	"fmt"
	"strings"
	"testing"
)

// The tests in this file pin bugs that are known and left as they are while
// the code moves into packages: a move must not change them by accident,
// and the fix that changes one on purpose rewrites its test. One more is
// pinned elsewhere: the API golden files hold the status codes as each
// handler picks them.

// TestShutdownDropsTheMessagesItHolds: stopping the bridge, as SIGTERM and a
// restart do, drops the message a brain is answering and the messages
// waiting behind it. The chat that was told its message is queued hears
// nothing more, and since WhatsApp took the messages as delivered, nothing
// brings them back; only their lines in the history are left. A shutdown
// that lets the brains finish, and keeps what still waits for the next
// start, changes this.
func TestShutdownDropsTheMessagesItHolds(t *testing.T) {
	h := newHarness(t, "")
	dir := h.AddBrain(BrainManifest{ID: "main", Name: "Main", Routing: BrainRouting{HandlesDMs: true}})
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "30000")
	h.Inject(inbound{JID: harnessDM, Text: "first"})
	h.WaitFor("the first run", func() bool { return h.Running("main") })
	h.Inject(inbound{JID: harnessDM, Text: "second"})
	h.WaitSent(harnessDM, "Queued")
	h.Shutdown()

	s := h.scrubber()
	var sb strings.Builder
	sb.WriteString("sent:\n")
	for _, m := range h.Outbox() {
		fmt.Fprintf(&sb, "  %s %q\n", m.JID, s.text(m.Text))
	}
	sb.WriteString("history:\n")
	for _, l := range historyLines(t, dir) {
		fmt.Fprintf(&sb, "  %s\n", s.text(l))
	}
	assertGolden(t, "run/shutdown-drops.golden", []byte(sb.String()))
}
