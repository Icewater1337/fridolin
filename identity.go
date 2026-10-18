package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// Phone numbers and chats as people write them, and the one form the
// bridge compares: a number someone typed into config.json, the identity
// map or a /send request must match the sender WhatsApp reports however
// it was written.

// phoneNumberRE is a phone number as allowed_numbers holds it.
var phoneNumberRE = regexp.MustCompile(`^[0-9]{6,15}$`)

// phoneNumber is s as allowed_numbers holds it: digits with the country
// code. It takes the ways people write a number (a +, spaces, dashes,
// dots, brackets, a WhatsApp JID) and refuses anything else, a @lid JID
// too: its digits are WhatsApp's internal id for the sender, not a number.
func phoneNumber(s string) (string, error) {
	if _, server, ok := strings.Cut(strings.TrimSpace(s), "@"); ok && server != types.DefaultUserServer {
		return "", fmt.Errorf("%q holds no phone number: send the number with the country code, e.g. 15551234567", strings.TrimSpace(s))
	}
	n := strings.Map(func(r rune) rune {
		switch r {
		// A no-break space too, which a number copied from a contact
		// card can hold.
		case ' ', '\u00a0', '-', '.', '(', ')':
			return -1
		}
		return r
	}, normalizeJID(s))
	if !phoneNumberRE.MatchString(n) {
		return "", fmt.Errorf("%q is not a phone number: use digits with the country code, e.g. 15551234567", strings.TrimSpace(s))
	}
	return n, nil
}

// identityKey is the form a person is looked up by in a brain's identity
// map: the phone number for anything phoneNumber takes, so that a row
// written "+1 555 123 4567" names the sender 15551234567, and otherwise s
// normalised (a LID, say). The user part of a JID phoneNumber refuses is
// read as a number too when it is one: "0 00000@lid" would otherwise be
// the key "0 00000", which itself keys as 000000, so no sender's key
// could ever match it.
func identityKey(s string) string {
	if n, err := phoneNumber(s); err == nil {
		return n
	}
	k := normalizeJID(s)
	if n, err := phoneNumber(k); err == nil {
		return n
	}
	return k
}

// allowedNumbersAsNumbers is allowed_numbers with each entry in the form
// the bridge compares, digits only. An entry that is no phone number stays
// as written: it lets nobody in, and badAllowedNumbers names it.
func allowedNumbersAsNumbers(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		if n, err := phoneNumber(s); err == nil {
			out[i] = n
		} else {
			out[i] = s
		}
	}
	return out
}

// badAllowedNumbers lists the entries of allowed_numbers that are no phone
// number. config.json may be edited by hand, and such an entry silently
// lets nobody in.
func badAllowedNumbers(list []string) []string {
	var bad []string
	for _, s := range list {
		if _, err := phoneNumber(s); err != nil {
			bad = append(bad, s)
		}
	}
	return bad
}

// errNotAChat is why a chat a script named cannot be sent to.
var errNotAChat = errors.New("not a chat JID: use one such as 15551234567@s.whatsapp.net, a …@lid or 120363000000000000@g.us, or a phone number with the country code")

// chatJID parses the chat a script names, for /send: a WhatsApp JID, or a
// phone number written any way phoneNumber takes, for the direct chat with
// it. A bare number parses as a JID without a user, which whatsmeow fails
// to send to, so it is turned into the number's JID first. The result has
// no device part, the form inbound messages are recorded under.
func chatJID(s string) (types.JID, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "@") {
		n, err := phoneNumber(s)
		if err != nil {
			return types.JID{}, fmt.Errorf("%q is %w", s, errNotAChat)
		}
		return types.NewJID(n, types.DefaultUserServer), nil
	}
	jid, err := types.ParseJID(s)
	if err != nil || jid.User == "" {
		return types.JID{}, fmt.Errorf("%q is %w", s, errNotAChat)
	}
	switch jid.Server {
	case types.DefaultUserServer:
		if !phoneNumberRE.MatchString(jid.User) {
			return types.JID{}, fmt.Errorf("%q is %w", s, errNotAChat)
		}
	case types.GroupServer, types.HiddenUserServer, types.BroadcastServer, types.NewsletterServer:
		// Broadcast lists and channels are chats, which checkRecipient
		// refuses to send to.
	default:
		return types.JID{}, fmt.Errorf("%q is %w", s, errNotAChat)
	}
	return jid.ToNonAD(), nil
}

// ownerChat is the direct chat with the bridge's owner, as far as the
// bridge can tell: the first phone number in allowed_numbers. Replies to
// what did not come from a chat (an /ingest, a Plaud recording, a startup
// alert) go there when nothing else is set. "" when the list holds no
// number.
func ownerChat(cfg *Config) string {
	for _, n := range cfg.AllowedNumbers {
		if p, err := phoneNumber(n); err == nil {
			return userJID(p)
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
			log.Printf("could not look up the number of %s: %v", maskJID(jid.User, "", jid.Server), err)
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
