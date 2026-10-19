package whatsapp

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

// A WhatsApp @-mention is "@" and the number (or LID) in the text. The
// bridge's own mentions go, like a trigger keyword; anything else stays.
func TestStripMentions(t *testing.T) {
	own := []types.JID{
		types.NewJID("15559876543", types.DefaultUserServer),
		types.NewJID("424242", types.HiddenUserServer), // short, so it cannot be anyone's real LID
	}
	cases := []struct {
		name, text, want string
	}{
		{"by number, before a command", "@15559876543 !stop", "!stop"},
		{"by LID", "@424242 !stop", "!stop"},
		{"twice", "@15559876543 @424242 !help", "!help"},
		{"in the middle", "what does @15559876543 think?", "what does  think?"},
		{"someone else", "@15551234567 can you check?", "@15551234567 can you check?"},
		{"a longer number", "@155598765430 hi", "@155598765430 hi"},
		{"part of an address", "write to x@15559876543", "write to x@15559876543"},
		{"only the mention", " @15559876543 ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripMentions(c.text, own); got != c.want {
				t.Errorf("stripMentions(%q) = %q, want %q", c.text, got, c.want)
			}
		})
	}
	if got := stripMentions(" @15559876543 hi ", nil); got != "@15559876543 hi" {
		t.Errorf("without accounts: %q, want the text trimmed and kept", got)
	}
}
