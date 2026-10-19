package main

import (
	"context"

	"github.com/Icewater1337/fridolin/internal/chat"
)

// Messenger is what the bridge needs from a chat network. The real one is
// WhatsApp; dev mode (--no-whatsapp) swaps in a fake that records outgoing
// messages, so the dashboard and brains can be tried without pairing a phone.
type Messenger interface {
	outboundLink
	Connect() error
	Disconnect()
	SendChunked(ctx context.Context, jid, text string) error
	State() chat.LinkState
	StartPairing() error
	JoinedGroups(ctx context.Context) ([]chat.Group, error)
}
