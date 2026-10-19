package main

import (
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/otp"
)

// OTPCatch is a code the guard caught, and the request it answers.
type OTPCatch struct {
	Code    string // the code, without spaces and dashes
	Typed   string // the message as it was typed
	Request otp.Request
}

// CatchesCode reports whether m is a one-time code that its chat asked for
// (see codeRequest), which must not be written to history or sent to the
// model. Only a message from the chat counts, from WhatsApp or the dev
// page: what /ingest and Plaud hand a brain comes from a program, and
// treating it as a code would let one plant a code for the script that
// waits for it.
func (br *Brain) CatchesCode(m Inbound, now time.Time) (OTPCatch, bool) {
	s := br.settings.Load()
	if !s.otpGuard || len(m.Media) > 0 || !fromChat(m) {
		return OTPCatch{}, false
	}
	code, ok := otp.Code(m.Text)
	if !ok {
		return OTPCatch{}, false
	}
	req, ok := otp.Requested(br.root, br.id, br.input.Chats(m.Inbound), s.otpMarkers, now)
	if !ok {
		return OTPCatch{}, false
	}
	return OTPCatch{Code: code, Typed: strings.TrimSpace(m.Text), Request: req}, true
}

// fromChat reports whether m came from a chat, from WhatsApp or the dev
// page, rather than being handed to a brain by a program (/ingest, Plaud,
// a dev message that names a brain).
func fromChat(m Inbound) bool { return m.Source != chat.SourceIngest && m.Brain == "" }

// AsksForCode reports whether text, going out to one of the brain's chats,
// asks for a one-time code that the guard would catch: the guard is on and
// text holds one of its markers.
func (br *Brain) AsksForCode(text string) bool {
	s := br.settings.Load()
	markers := s.otpMarkers
	if len(markers) == 0 {
		markers = otp.DefaultMarkers
	}
	return s.otpGuard && slices.ContainsFunc(markers, func(m string) bool { return otp.HasMarker(text, m) })
}
