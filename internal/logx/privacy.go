package logx

import (
	"regexp"
	"slices"
	"strings"
)

// jidExpr is a WhatsApp JID or LID: its user part, with a device or
// creator suffix, then the server.
const jidExpr = `([0-9]+)((?:[-.:][0-9]+)*)@(s\.whatsapp\.net|g\.us|lid)`

// redactPattern finds a JID or LID, a number in international format, or
// an email address.
var redactPattern = regexp.MustCompile(jidExpr + `\b|\+[0-9]{7,15}\b|[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)

// trailingJID finds a JID at the end of what redactPattern took for an
// email address: one right after a name, "brain-120363…@g.us" say, which
// the address's pattern matches from the name's start.
var trailingJID = regexp.MustCompile(jidExpr + `$`)

// maskJID masks a JID's user part and the suffix after it: a phone
// number's down to its country code and last two digits, without the
// device, a group's or a LID's down to the last two digits of the two
// together, which hides the number of whoever made an old-style group.
func MaskJID(user, suffix, server string) string {
	if server == "s.whatsapp.net" {
		return MaskPhone(user) + "@" + server
	}
	return MaskDigits(user+suffix, 0) + "@" + server
}

// isWhatsAppServer reports whether domain is one of WhatsApp's JID servers.
func isWhatsAppServer(domain string) bool {
	return domain == "s.whatsapp.net" || domain == "g.us" || domain == "lid"
}

// maskPhone keeps a phone number's country code and its last two digits.
func MaskPhone(n string) string {
	return MaskDigits(n, countryCodeLen(n))
}

// maskDigits keeps the first keep and the last two characters of n, or
// nothing when that would leave fewer than four of them masked.
func MaskDigits(n string, keep int) string {
	if len(n) < keep+2+4 {
		return "…"
	}
	return n[:keep] + "…" + n[len(n)-2:]
}

// countryCodeLen is how many leading digits of an international number
// are its country calling code (ITU-T E.164): 1 and 7 stand alone, the
// codes below take two digits, every other code three.
func countryCodeLen(n string) int {
	if n == "" {
		return 0
	}
	if n[0] == '1' || n[0] == '7' {
		return 1
	}
	if len(n) >= 2 && slices.Contains(twoDigitCountryCodes, n[:2]) {
		return 2
	}
	return 3
}

var twoDigitCountryCodes = []string{
	"20", "27",
	"30", "31", "32", "33", "34", "36", "39",
	"40", "41", "43", "44", "45", "46", "47", "48", "49",
	"51", "52", "53", "54", "55", "56", "57", "58",
	"60", "61", "62", "63", "64", "65", "66",
	"81", "82", "84", "86",
	"90", "91", "92", "93", "94", "95", "98",
}

// redactIdentifiers masks identifiers with the caller's email policy.
func RedactIdentifiers(s string, maskEmail func(string) string) string {
	return redactPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := redactPattern.FindStringSubmatch(m)
		switch {
		case sub[3] != "":
			return MaskJID(sub[1], sub[2], sub[3])
		case strings.HasPrefix(m, "+"):
			return "+" + MaskPhone(m[1:])
		case isWhatsAppServer(m[strings.LastIndex(m, "@")+1:]):
			// No address: a JID after a name, or no JID either
			// ("digits@g.us", say).
			if j := trailingJID.FindStringSubmatch(m); j != nil {
				return strings.TrimSuffix(m, j[0]) + MaskJID(j[1], j[2], j[3])
			}
			return m
		}
		return maskEmail(m)
	})
}
