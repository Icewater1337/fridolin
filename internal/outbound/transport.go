package outbound

import "context"

// Transport is the send capability consumed by reply delivery.
// It has no pairing, lifecycle, configuration or persistence dependency.
type Transport interface {
	SendMessage(ctx context.Context, jid, text string) error
	SendPoll(ctx context.Context, jid, question string, options []string, maxSelectable int) (string, error)
}

// Link adds the connection observation used by the durable outbox.
// Connected is closed while the transport can send and replaced when it
// disconnects; reading it does not start or stop a connection.
type Link interface {
	Transport
	Connected() <-chan struct{}
}
