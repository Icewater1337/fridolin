package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Icewater1337/fridolin/internal/chat"
)

// Phone numbers and chats as people write them, and the one form the
// bridge compares: a number someone typed into config.json, the identity
// map or a /send request must match the sender WhatsApp reports however
// it was written.

// phoneNumberRE is a phone number as allowed_numbers holds it.
var phoneNumberRE = regexp.MustCompile(`^[0-9]{6,15}$`)

// canonicalPhoneNumber reports whether s is already a valid phone number.
func canonicalPhoneNumber(s string) bool { return phoneNumberRE.MatchString(s) }

// phoneNumber is s as allowed_numbers holds it: digits with the country
// code. It takes the ways people write a number (a +, spaces, dashes,
// dots, brackets, a WhatsApp JID) and refuses anything else, a @lid JID
// too: its digits are WhatsApp's internal id for the sender, not a number.
func phoneNumber(s string) (string, error) {
	if _, server, ok := strings.Cut(strings.TrimSpace(s), "@"); ok && server != "s.whatsapp.net" {
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
	}, chat.NormalizeJID(s))
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
	k := chat.NormalizeJID(s)
	if n, err := phoneNumber(k); err == nil {
		return n
	}
	return k
}
