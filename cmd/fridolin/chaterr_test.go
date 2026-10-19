package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
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
		{"stopped", errBrainInterrupted, true, ""},
		{"cancelled", fmt.Errorf("run: %w", context.Canceled), false, ""},
		{"stderr stays out of a DM", stderr, true, chatErrGeneric},
		{"stderr stays out of a group", stderr, false, chatErrGeneric},
		{"not logged in", &cliError{msg: "Invalid API key · Please run /login"}, false, chatErrNotSignedIn},
		{"auth in stderr", errors.New("claude exited: exit status 1 — stderr: Failed to authenticate. API Error: 401"), true, chatErrNotSignedIn},
		{"usage limit", &cliError{msg: "Claude AI usage limit reached|1760000000"}, false, chatErrLimit},
		{"rate limited", errors.New("API Error: 429 rate_limit_error"), true, chatErrLimit},
		{"deadline", fmt.Errorf("run: %w", context.DeadlineExceeded), false, chatErrTimeout},
		{"timed out", &cliError{msg: "Request timed out"}, true, chatErrTimeout},
		{"run_timeout_minutes", runTimedOut(30 * time.Minute), false, chatErrTimeout},
		// By the error, not by a word its text happens to hold.
		{"run timeout in other words", fmt.Errorf("%w after 90s", errRunTimedOut), true, chatErrTimeout},
		{"sandbox", errSandboxUnavailable, false, "❌ The sandbox could not be set up, so this message was not run (details in the bridge log)."},
		{"the CLI's own words in a DM", &cliError{msg: "Credit balance is too low"}, true, "❌ Claude: Credit balance is too low"},
		{"but not in a group", &cliError{msg: "Credit balance is too low"}, false, chatErrGeneric},
		{"no result", errNoResult, true, chatErrGeneric},
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
	for _, chat := range []string{testDMChat, testGroup} {
		t.Run(chat, func(t *testing.T) {
			tb, br := newMainBridge(t, "")
			t.Setenv("FRIDOLIN_FAKE_STDERR_BYTES", "10")
			tb.handleInbound(context.Background(), Inbound{JID: chat, Sender: "15551234567", Text: "@claude fail", Source: sourceWhatsApp})
			waitFor(t, 10*time.Second, "the error notice", func() bool { return len(tb.sentTo(chat)) > 0 })
			sent := tb.sentTo(chat)
			if len(sent) != 1 || sent[0] != chatErrGeneric {
				t.Errorf("sent %q, want only %q", sent, chatErrGeneric)
			}
			waitFor(t, 5*time.Second, "the run's outcome", func() bool { return br.Snapshot().Last != nil })
			if last := br.Snapshot().Last; !strings.Contains(last.Error, "fatal: the last thing the CLI said") {
				t.Errorf("dashboard error = %q, want the CLI's stderr", last.Error)
			}
		})
	}
}
