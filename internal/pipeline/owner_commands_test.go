package pipeline

import (
	"context"
	"slices"
	"testing"

	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
)

// The owner's commands are taken in any case, and the past tense the
// owner types now and then means the command; other words are none.
func TestOwnerCommandOf(t *testing.T) {
	t.Parallel()
	for word, want := range map[string]string{
		"!approve": "!approve", "!APPROVE": "!approve", "!approved": "!approve", "!Approved": "!approve",
		"!denied": "!deny", "!revoked": "!revoke", "!join": "!join",
		"!approves": "", "approve": "", "!help": "", "": "",
	} {
		if got := ownerCommandOf(word); got != want {
			t.Errorf("ownerCommandOf(%q) = %q, want %q", word, got, want)
		}
	}
}

// What an owner command started runs after its reply is sent, so that
// what it tells the chat never overtakes the reply (#752).
func TestOwnerCommandThenRunsAfterTheReply(t *testing.T) {
	t.Parallel()
	var order []string
	p := &Workflow{
		Notice: func(_ context.Context, _, text string) error { order = append(order, "reply: "+text); return nil },
		Owner: func(brainruntime.Inbound, string, []string) (string, func()) {
			return "✅ Approved", func() { order = append(order, "then") }
		},
	}
	m := brainruntime.Inbound{Inbound: chat.Inbound{JID: "15551234567@s.whatsapp.net", Text: "!approve"}}
	if !p.handleCommand(context.Background(), m, Settings{}) {
		t.Fatal("!approve was not taken as a command")
	}
	if want := []string{"reply: ✅ Approved", "then"}; !slices.Equal(order, want) {
		t.Errorf("order %q, want %q", order, want)
	}
}
