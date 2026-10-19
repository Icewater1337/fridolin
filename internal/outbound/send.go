package outbound

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrOffline marks a send that failed because WhatsApp is not connected or
// not paired: worth trying again later, and not the caller's mistake.
var ErrOffline = errors.New("WhatsApp is not connected")

// ErrBadRecipient marks a send to an address WhatsApp has no chat for.
var ErrBadRecipient = errors.New("not a chat WhatsApp can send to")

// PartialSendError is a chunked send that failed after some chunks went
// out: sent holds those, as the chat shows them.
type PartialSendError struct {
	Sent  []string
	Total int
	Err   error
}

func (e *PartialSendError) Error() string {
	return fmt.Sprintf("sent %d of %d parts, then: %v", len(e.Sent), e.Total, e.Err)
}

func (e *PartialSendError) Unwrap() error { return e.Err }

// SendChunks sends chunks one by one with send, pausing between them
// unless ctx ends. A failure after the first chunk is a *PartialSendError
// naming what went out.
func SendChunks(ctx context.Context, chunks []string, pause time.Duration, send func(chunk string) error) error {
	for i, chunk := range chunks {
		err := ctx.Err()
		if i > 0 && pause > 0 && err == nil {
			select {
			case <-ctx.Done():
				err = ctx.Err()
			case <-time.After(pause):
			}
		}
		if err == nil {
			err = send(chunk)
		}
		if err != nil {
			if i > 0 {
				return &PartialSendError{Sent: chunks[:i], Total: len(chunks), Err: err}
			}
			return err
		}
	}
	return nil
}

// ErrRefusedRecipient marks a send the bridge refuses because of where it
// would go.
var ErrRefusedRecipient = errors.New("refusing to send to a Status, broadcast list or channel")

// ErrNotAChat is why a chat a script named cannot be sent to.
var ErrNotAChat = errors.New("not a chat JID: use one such as 15551234567@s.whatsapp.net, a …@lid or 120363000000000000@g.us, or a phone number with the country code")
