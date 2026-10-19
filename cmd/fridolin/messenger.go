package main

import (
	"context"

	"github.com/Icewater1337/fridolin/internal/chat"
)

// Messenger is what the bridge needs from a chat network. The real one is
// WhatsApp; dev mode (--no-whatsapp) swaps in a fake that records outgoing
// messages, so the dashboard and brains can be tried without pairing a phone.
type Messenger interface {
	Connect() error
	Disconnect()
	SendMessage(ctx context.Context, jid, text string) error
	SendChunked(ctx context.Context, jid, text string) error
	SendPoll(ctx context.Context, jid, question string, options []string, maxSelectable int) (string, error)
	State() chat.LinkState
	// Connected returns a channel that is closed while the messenger can
	// send: at once when it is connected, else when it next connects.
	Connected() <-chan struct{}
	StartPairing() error
	JoinedGroups(ctx context.Context) ([]chat.Group, error)
}
