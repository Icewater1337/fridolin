package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// errOffline marks a send that failed because WhatsApp is not connected or
// not paired: worth trying again later, and not the caller's mistake.
var errOffline = errors.New("WhatsApp is not connected")

// errBadRecipient marks a send to an address WhatsApp has no chat for.
var errBadRecipient = errors.New("not a chat WhatsApp can send to")

// classifySendErr marks err, from whatsmeow, with errOffline or
// errBadRecipient where it is one, so callers can tell them apart without
// knowing whatsmeow.
func classifySendErr(err error) error {
	switch {
	case errors.Is(err, whatsmeow.ErrNotConnected), errors.Is(err, whatsmeow.ErrNotLoggedIn):
		return fmt.Errorf("%w: %w", errOffline, err)
	case errors.Is(err, whatsmeow.ErrUnknownServer):
		return fmt.Errorf("%w: %w", errBadRecipient, err)
	}
	return err
}

// partialSendError is a chunked send that failed after some chunks went
// out: sent holds those, as the chat shows them.
type partialSendError struct {
	sent  []string
	total int
	err   error
}

func (e *partialSendError) Error() string {
	return fmt.Sprintf("sent %d of %d parts, then: %v", len(e.sent), e.total, e.err)
}

func (e *partialSendError) Unwrap() error { return e.err }

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
				return &partialSendError{sent: chunks[:i], total: len(chunks), err: err}
			}
			return err
		}
	}
	return nil
}

// errRefusedRecipient marks a send the bridge refuses because of where it
// would go.
var errRefusedRecipient = errors.New("refusing to send to a Status, broadcast list or channel")

// checkRecipient refuses Status, broadcast-list and channel addresses. A send
// to status@broadcast posts this account's own Status to every contact, and
// one to a newsletter posts in a public channel the account runs. whatsmeow
// cannot send to other broadcast lists yet; once it can, a send would reach
// every member. Nothing the bridge says belongs there, whoever asked for it:
// a brain's reply, its reply_chat, or a script calling /send.
func checkRecipient(jid types.JID) error {
	if jid.Server == types.BroadcastServer || jid.Server == types.NewsletterServer {
		return fmt.Errorf("%w: %s", errRefusedRecipient, jid)
	}
	return nil
}

// parseRecipient parses the JID of a chat to send to and refuses the ones
// checkRecipient refuses.
func parseRecipient(jid string) (types.JID, error) {
	recipient, err := types.ParseJID(jid)
	if err != nil {
		return types.JID{}, fmt.Errorf("parsing JID %q: %w", jid, err)
	}
	if err := checkRecipient(recipient); err != nil {
		return types.JID{}, err
	}
	return recipient, nil
}
