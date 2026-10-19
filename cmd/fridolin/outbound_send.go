package main

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// errOffline marks a send that failed because WhatsApp is not connected or
// not paired: worth trying again later, and not the caller's mistake.
var errOffline = errors.New("WhatsApp is not connected")

// errBadRecipient marks a send to an address WhatsApp has no chat for.
var errBadRecipient = errors.New("not a chat WhatsApp can send to")

// partialSendError is a chunked send that failed after some chunks went
// out: sent holds those, as the chat shows them.
type partialSendError struct {
	Sent  []string
	Total int
	Err   error
}

func (e *partialSendError) Error() string {
	return fmt.Sprintf("sent %d of %d parts, then: %v", len(e.Sent), e.Total, e.Err)
}

func (e *partialSendError) Unwrap() error { return e.Err }

// sendChunks sends chunks one by one with send, pausing between them
// unless ctx ends. A failure after the first chunk is a *partialSendError
// naming what went out.
func sendChunks(ctx context.Context, chunks []string, pause time.Duration, send func(chunk string) error) error {
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
				return &partialSendError{Sent: chunks[:i], Total: len(chunks), Err: err}
			}
			return err
		}
	}
	return nil
}

// errRefusedRecipient marks a send the bridge refuses because of where it
// would go.
var errRefusedRecipient = errors.New("refusing to send to a Status, broadcast list or channel")
