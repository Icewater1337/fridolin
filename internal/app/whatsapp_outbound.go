package app

import (
	"errors"
	"fmt"

	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// classifySendErr marks err, from whatsmeow, with errOffline or
// errBadRecipient where it is one, so callers can tell them apart without
// knowing whatsmeow.
func classifySendErr(err error) error {
	switch {
	case errors.Is(err, whatsmeow.ErrNotConnected), errors.Is(err, whatsmeow.ErrNotLoggedIn):
		return fmt.Errorf("%w: %w", outboundmsg.ErrOffline, err)
	case errors.Is(err, whatsmeow.ErrUnknownServer):
		return fmt.Errorf("%w: %w", outboundmsg.ErrBadRecipient, err)
	}
	return err
}

// checkRecipient refuses Status, broadcast-list and channel addresses. A send
// to status@broadcast posts this account's own Status to every contact, and
// one to a newsletter posts in a public channel the account runs. whatsmeow
// cannot send to other broadcast lists yet; once it can, a send would reach
// every member. Nothing the bridge says belongs there, whoever asked for it:
// a brain's reply, its reply_chat, or a script calling /send.
func checkRecipient(jid types.JID) error {
	if jid.Server == types.BroadcastServer || jid.Server == types.NewsletterServer {
		return fmt.Errorf("%w: %s", outboundmsg.ErrRefusedRecipient, jid)
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
