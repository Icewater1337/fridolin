package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

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
