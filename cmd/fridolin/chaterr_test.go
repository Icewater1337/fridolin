package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

func TestChatErrorText(t *testing.T) {
	stderr := errors.New("claude exited: exit status 1 — stderr: open /Users/you/brains/main/secret: operation not permitted")
	cases := []struct {
		name string
		err  error
		inDM bool
		want string
	}{
		{"no error", nil, true, ""},
		{"stopped", claudecli.ErrInterrupted, true, ""},
		{"cancelled", fmt.Errorf("run: %w", context.Canceled), false, ""},
		{"stderr stays out of a DM", stderr, true, outboundmsg.ChatErrGeneric},
		{"stderr stays out of a group", stderr, false, outboundmsg.ChatErrGeneric},
		{"not logged in", &claudecli.Error{Message: "Invalid API key · Please run /login"}, false, outboundmsg.ChatErrNotSignedIn},
		{"auth in stderr", errors.New("claude exited: exit status 1 — stderr: Failed to authenticate. API Error: 401"), true, outboundmsg.ChatErrNotSignedIn},
		{"usage limit", &claudecli.Error{Message: "Claude AI usage limit reached|1760000000"}, false, outboundmsg.ChatErrLimit},
		{"rate limited", errors.New("API Error: 429 rate_limit_error"), true, outboundmsg.ChatErrLimit},
		{"deadline", fmt.Errorf("run: %w", context.DeadlineExceeded), false, outboundmsg.ChatErrTimeout},
		{"timed out", &claudecli.Error{Message: "Request timed out"}, true, outboundmsg.ChatErrTimeout},
		{"run_timeout_minutes", claudecli.TimedOut(30 * time.Minute), false, outboundmsg.ChatErrTimeout},
		// By the error, not by a word its text happens to hold.
		{"run timeout in other words", fmt.Errorf("%w after 90s", claudecli.ErrTimedOut), true, outboundmsg.ChatErrTimeout},
		{"sandbox", brain.ErrSandboxUnavailable, false, "❌ The sandbox could not be set up, so this message was not run (details in the bridge log)."},
		{"the CLI's own words in a DM", &claudecli.Error{Message: "Credit balance is too low"}, true, "❌ Claude: Credit balance is too low"},
		{"but not in a group", &claudecli.Error{Message: "Credit balance is too low"}, false, outboundmsg.ChatErrGeneric},
		{"no result", claudecli.ErrNoResult, true, outboundmsg.ChatErrGeneric},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := chatErrorText(c.err, c.inDM); got != c.want {
				t.Errorf("chatErrorText(%v, %v) = %q, want %q", c.err, c.inDM, got, c.want)
			}
		})
	}
}

// A run that fails tells the chat in a short message; what the CLI wrote
// to stderr goes to the log and the dashboard only.
func TestFailedRunPostsNoRawError(t *testing.T) {
	for _, chatJID := range []string{testDMChat, testGroup} {
		t.Run(chatJID, func(t *testing.T) {
			tb, br := newMainBridge(t, "")
			t.Setenv("FRIDOLIN_FAKE_STDERR_BYTES", "10")
			tb.handleInbound(context.Background(), brain.Inbound{Inbound: chat.Inbound{JID: chatJID, Sender: "15551234567", Text: "@claude fail", Source: chat.SourceWhatsApp}})
			testutil.WaitFor(t, 10*time.Second, "the error notice", func() bool { return len(tb.sentTo(chatJID)) > 0 })
			sent := tb.sentTo(chatJID)
			if len(sent) != 1 || sent[0] != outboundmsg.ChatErrGeneric {
				t.Errorf("sent %q, want only %q", sent, outboundmsg.ChatErrGeneric)
			}
			testutil.WaitFor(t, 5*time.Second, "the run's outcome", func() bool { return br.Snapshot().Last != nil })
			if last := br.Snapshot().Last; !strings.Contains(last.Error, "fatal: the last thing the CLI said") {
				t.Errorf("dashboard error = %q, want the CLI's stderr", last.Error)
			}
		})
	}
}
