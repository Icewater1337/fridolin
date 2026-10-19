package main

import (
	"context"
	"fmt"
	"github.com/Icewater1337/fridolin/internal/logx"
	"log"
	"strings"

	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"

	"github.com/Icewater1337/fridolin/internal/config"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"

	"github.com/Icewater1337/fridolin/internal/chat"
)

// Phone numbers and chats as people write them, and the one form the
// bridge compares: a number someone typed into config.json, the identity
// map or a /send request must match the sender WhatsApp reports however
// it was written.

// badAllowedNumbers lists the entries of allowed_numbers that are no phone
// number. config.json may be edited by hand, and such an entry silently
// lets nobody in.
func badAllowedNumbers(list []string) []string {
	var bad []string
	for _, s := range list {
		if _, err := chat.PhoneNumber(s); err != nil {
			bad = append(bad, s)
		}
	}
	return bad
}

// chatJID parses the chat a script names, for /send: a WhatsApp JID, or a
// phone number written any way phoneNumber takes, for the direct chat with
// it. A bare number parses as a JID without a user, which whatsmeow fails
// to send to, so it is turned into the number's JID first. The result has
// no device part, the form inbound messages are recorded under.
func chatJID(s string) (types.JID, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "@") {
		n, err := chat.PhoneNumber(s)
		if err != nil {
			return types.JID{}, fmt.Errorf("%q is %w", s, outboundmsg.ErrNotAChat)
		}
		return types.NewJID(n, types.DefaultUserServer), nil
	}
	jid, err := types.ParseJID(s)
	if err != nil || jid.User == "" {
		return types.JID{}, fmt.Errorf("%q is %w", s, outboundmsg.ErrNotAChat)
	}
	switch jid.Server {
	case types.DefaultUserServer:
		if !chat.IsPhoneNumber(jid.User) {
			return types.JID{}, fmt.Errorf("%q is %w", s, outboundmsg.ErrNotAChat)
		}
	case types.GroupServer, types.HiddenUserServer, types.BroadcastServer, types.NewsletterServer:
		// Broadcast lists and channels are chats, which checkRecipient
		// refuses to send to.
	default:
		return types.JID{}, fmt.Errorf("%q is %w", s, outboundmsg.ErrNotAChat)
	}
	return jid.ToNonAD(), nil
}

// ownerChat is the direct chat with the bridge's owner, as far as the
// bridge can tell: the first phone number in allowed_numbers. Replies to
// what did not come from a chat (an /ingest, a Plaud recording, a startup
// alert) go there when nothing else is set. "" when the list holds no
// number.
func ownerChat(cfg *config.File) string {
	for _, n := range cfg.AllowedNumbers {
		if p, err := chat.PhoneNumber(n); err == nil {
			return chat.DMJID(p)
		}
	}
	return ""
}

// canonicalUser is the one JID the bridge knows a person by: their phone
// number's, without a device. WhatsApp addresses people by LID more and
// more, in DMs and in groups alike. alt is the other form WhatsApp sent
// along (MessageSource.SenderAlt); without it, whatsmeow's LID store maps
// a LID to the number, which it learns from the traffic it sees. A LID
// neither knows stays a LID. lids may be nil.
func canonicalUser(ctx context.Context, lids store.LIDStore, jid, alt types.JID) types.JID {
	jid, alt = jid.ToNonAD(), alt.ToNonAD()
	if jid.Server != types.HiddenUserServer {
		return jid
	}
	if alt.Server == types.DefaultUserServer && alt.User != "" {
		return alt
	}
	if lids != nil {
		pn, err := lids.GetPNForLID(ctx, jid)
		if err == nil && pn.User != "" {
			return pn.ToNonAD()
		}
		if err != nil {
			// Masked: the log goes along with bug reports.
			log.Printf("could not look up the number of %s: %v", logx.MaskJID(jid.User, "", jid.Server), err)
		}
	}
	return jid
}

// lidOf is the LID of the person whose phone number's JID is pn, or the
// empty JID when it is not known. lids may be nil.
func lidOf(ctx context.Context, lids store.LIDStore, pn types.JID) types.JID {
	if lids == nil || pn.Server != types.DefaultUserServer {
		return types.EmptyJID
	}
	lid, err := lids.GetLIDForPN(ctx, pn.ToNonAD())
	if err != nil || lid.User == "" {
		return types.EmptyJID
	}
	return lid.ToNonAD()
}
