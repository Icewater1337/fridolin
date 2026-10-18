package main

import (
	"log"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/otp"
)

// otpCatch is a code the guard caught, and the request it answers.
type otpCatch struct {
	Code    string // the code, without spaces and dashes
	Typed   string // the message as it was typed
	Request otp.Request
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
	code, ok := otp.Code(m.Text)
	if !ok {
		return otpCatch{}, false
	}
	req, ok := otp.Requested(br.root, br.id, chatJIDs(m), s.otpMarkers, now)
	if !ok {
		return otpCatch{}, false
	}
	return otpCatch{Code: code, Typed: strings.TrimSpace(m.Text), Request: req}, true
}

// fromChat reports whether m came from a chat, from WhatsApp or the dev
// page, rather than being handed to a brain by a program (/ingest, Plaud,
// a dev message that names a brain).
func fromChat(m Inbound) bool { return m.Source != chat.SourceIngest && m.Brain == "" }

// asksForCode reports whether text, going out to one of the brain's chats,
// asks for a one-time code that the guard would catch: the guard is on and
// text holds one of its markers.
func (br *Brain) asksForCode(text string) bool {
	s := br.settings.Load()
	markers := s.otpMarkers
	if len(markers) == 0 {
		markers = otp.DefaultMarkers
	}
	return s.otpGuard && slices.ContainsFunc(markers, func(m string) bool { return otp.HasMarker(text, m) })
}

// handOffCode hands a code the OTP guard caught in m to the local script
// that asked for it: it is written to <data_dir>/otp/<brain>.json,
// readable only by this user, until it expires (otpHandoffTTL). Brains
// cannot read it (the sandbox denies the data folder), and it never
// reaches the history or the model. A newer code for the brain replaces
// it.
func (b *Bridge) handOffCode(br *Brain, m Inbound, c otpCatch) {
	now := time.Now()
	h := otp.Record{
		Brain: br.ID(), Chat: m.JID, Sender: m.Sender, Code: c.Code, Typed: c.Typed,
		Received: now.Format(time.RFC3339), ReceivedUnixMS: now.UnixMilli(),
		Request: c.Request.ID, RequestedUnixMS: c.Request.At.UnixMilli(),
		ExpiresUnixMS: now.Add(otp.HandoffTTL).UnixMilli(),
	}
	if err := b.otp.Write(filepath.Join(b.cfgs.Get().DataDir, "otp"), h); err != nil {
		log.Printf("brain %s: handing over a one-time code: %v", br.ID(), err)
	}
}
