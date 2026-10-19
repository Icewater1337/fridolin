package main

import "context"

// outboundTransport is the send capability consumed by reply delivery.
// It has no pairing, lifecycle, configuration or persistence dependency.
type outboundTransport interface {
	SendMessage(ctx context.Context, jid, text string) error
	SendPoll(ctx context.Context, jid, question string, options []string, maxSelectable int) (string, error)
}

// outboundLink adds the connection observation used by the durable outbox.
// Connected is closed while the transport can send and replaced when it
// disconnects; reading it does not start or stop a connection.
type outboundLink interface {
	outboundTransport
	Connected() <-chan struct{}
}
