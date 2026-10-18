package main

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The OTP guard. A local script that logs in somewhere for you asks a chat
// for the one-time code through /send, or the brain asks for one itself.
// The code that comes back must not reach the model, nor the history every
// later run reads: it is caught on its way in and handed to the script
// instead (see handOffCode). A message is caught when it is nothing but a
// code (otpCode) and its chat asked for one within otpWindow (codeRequest).

// otpWindow is how long after a chat was asked for a code a code that
// arrives there is caught. Codes expire within minutes; past this, a bare
// number in the chat is a year, a price or a PIN for the brain to read.
var otpWindow = 10 * time.Minute

// defaultOTPMarkers are the phrases that mark an outbound message as a
// request for a one-time code, each matched in any letter case and as
// whole words (see hasMarker). Add your own (a bank's name, say) with
// otp_markers, which replaces them.
var defaultOTPMarkers = []string{"🔐", "OTP", "one-time code", "2FA", "verification code"}

var (
	// otpDigitsRe is digits, maybe in groups split by single spaces or
	// dashes, as codes are read out and written: "483 920", "48-39-20".
	otpDigitsRe = regexp.MustCompile(`^[0-9]+(?:[ -][0-9]+)*$`)
	// otpMixedRe is upper-case letters and digits, maybe split by dashes:
	// "G-483920", "AB12CD".
	otpMixedRe = regexp.MustCompile(`^[A-Z0-9]+(?:-[A-Z0-9]+)*$`)
)

// otpCode returns the code text is, when text is nothing but one: 4 to 8
// digits, or 6 to 10 upper-case letters and digits, at least two of them
// digits, as in "G-483920". The code is text without its spaces and
// dashes.
func otpCode(text string) (code string, ok bool) {
	text = strings.TrimSpace(text)
	switch {
	case otpDigitsRe.MatchString(text):
		code = strings.NewReplacer(" ", "", "-", "").Replace(text)
		return code, len(code) >= 4 && len(code) <= 8
	case otpMixedRe.MatchString(text):
		code = strings.ReplaceAll(text, "-", "")
		digits := 0
		for _, r := range code {
			if r >= '0' && r <= '9' {
				digits++
			}
		}
		// A letter at least: twelve digits are no code, a phone number
		// perhaps.
		return code, len(code) >= 6 && len(code) <= 10 && digits >= 2 && digits < len(code)
	}
	return "", false
}

// hasMarker reports whether text holds marker in any letter case. Where the
// marker begins or ends with a letter or a digit, a word must begin or end
// there too, so that "OTP" is not found in "hotpot", nor "2FA" in "12FA".
func hasMarker(text, marker string) bool {
	if marker == "" {
		return false
	}
	t, m := strings.ToLower(text), strings.ToLower(marker)
	first, _ := utf8.DecodeRuneInString(m)
	last, _ := utf8.DecodeLastRuneInString(m)
	inWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	for from := 0; from < len(t); {
		i := strings.Index(t[from:], m)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(m)
		before, _ := utf8.DecodeLastRuneInString(t[:start])
		after, _ := utf8.DecodeRuneInString(t[end:])
		if !(inWord(first) && start > 0 && inWord(before)) && !(inWord(last) && end < len(t) && inWord(after)) {
			return true
		}
		_, size := utf8.DecodeRuneInString(t[start:])
		from = start + size
	}
	return false
}

// otpRequest is the outbound line that asked a chat for a code.
type otpRequest struct {
	At time.Time // when it was written
}

// otpRequestBudget is how far back from the end of the history the guard
// looks for a request: far more than otpWindow of messages takes.
const otpRequestBudget = 1 << 20

// codeRequest returns the newest request for a code in the chat that goes
// by chats which was written in the otpWindow before now: an outbound line
// there, the brain's own or one sent on its behalf through /send, that
// holds one of markers (the built-in ones for none). Another brain's own
// words in this history do not count. The history is read back from its
// end, and only as far as the lines the bridge stamps when it writes them
// (all but a message's own, which has the time it was sent) are recent.
func (br *Brain) codeRequest(chats, markers []string, now time.Time) (req otpRequest, ok bool) {
	if len(markers) == 0 {
		markers = defaultOTPMarkers
	}
	since := now.Add(-otpWindow)
	br.scanHistory(otpRequestBudget, func(line []byte) bool {
		var e historyEntry
		if json.Unmarshal(line, &e) != nil {
			return true
		}
		at, err := time.Parse(time.RFC3339, e.TS)
		if err != nil {
			return true
		}
		if e.Type != historyIn && at.Before(since) {
			return false // older than the window, and everything before it too
		}
		if e.Type != historyOut || (e.From != br.id && e.Via == "") || !slices.Contains(chats, e.Chat) || at.Before(since) {
			return true
		}
		if slices.ContainsFunc(markers, func(m string) bool { return hasMarker(e.Text, m) }) {
			req, ok = otpRequest{At: at}, true
			return false
		}
		return true
	})
	return req, ok
}

// otpCatch is a code the guard caught, and the request it answers.
type otpCatch struct {
	Code    string // the code, without spaces and dashes
	Typed   string // the message as it was typed
	Request otpRequest
}

// catchesCode reports whether m is a one-time code that its chat asked for
// (see codeRequest), which must not be written to history or sent to the
// model. Only a message from the chat counts, from WhatsApp or the dev
// page: what /ingest and Plaud hand a brain comes from a program, and
// treating it as a code would let one plant a code for the script that
// waits for it.
func (br *Brain) catchesCode(m Inbound, now time.Time) (otpCatch, bool) {
	s := br.settings.Load()
	if !s.otpGuard || len(m.Media) > 0 || !fromChat(m) {
		return otpCatch{}, false
	}
	code, ok := otpCode(m.Text)
	if !ok {
		return otpCatch{}, false
	}
	req, ok := br.codeRequest(chatJIDs(m), s.otpMarkers, now)
	if !ok {
		return otpCatch{}, false
	}
	return otpCatch{Code: code, Typed: strings.TrimSpace(m.Text), Request: req}, true
}

// fromChat reports whether m came from a chat, from WhatsApp or the dev
// page, rather than being handed to a brain by a program (/ingest, Plaud,
// a dev message that names a brain).
func fromChat(m Inbound) bool { return m.Source != sourceIngest && m.Brain == "" }
