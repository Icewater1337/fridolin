package app

import (
	"slices"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// A question a brain asks mid-run reaches the chat marked as a question,
// and the working notice as it is, whatever either starts with: the
// bridge used to tell the two apart by a leading ⏳.
func TestMidRunMessagesReachTheChatMarked(t *testing.T) {
	t.Parallel()
	const dm = "15551234567@s.whatsapp.net"
	tb := newTestBridge(t, "")
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A", Routing: manifest.Routing{HandlesDMs: true}})
	br := tb.brainMgr.Get("a")
	sink := brainHooksSink{tb.brainMgr.hooks}
	sink.Question(br, dm, "⏳ Wait for the delivery?")
	sink.Working(dm)
	// The question goes through the outbox, after the notice.
	testutil.WaitFor(t, 5*time.Second, "both messages", func() bool { return len(tb.sentTo(dm)) == 2 })
	sent := tb.sentTo(dm)
	slices.Sort(sent)
	if want := []string{"⏳ Working on it...", "❓ ⏳ Wait for the delivery?"}; !slices.Equal(sent, want) {
		t.Errorf("sent %q, want %q", sent, want)
	}
}
