package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/history"
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
	// ID is what /send answered the request with (historyEntry.OTPRequest),
	// "" when the brain asked in its own words.
	ID string
	At time.Time // when it was written
}

// otpRequestBudget is how far back from the end of the history the guard
// looks for a request: far more than otpWindow of messages takes.
const otpRequestBudget = 1 << 20

// codeRequest returns the request for a code in the chat that goes by
// chats which was written in the otpWindow before now: an outbound line
// there, the brain's own or one sent on its behalf through /send, that
// holds one of markers (the built-in ones for none). Another brain's own
// words in this history do not count. Of several, it is the newest that
// came through /send with an id, whose script waits for the code that
// names it, even when the brain's own words held a marker after it ("the
// bank asks for your 2FA code"); without one, the newest of all. The
// history is read back from its end, and only as far as the lines the
// bridge stamps when it writes them (all but a message's own, which has
// the time it was sent) are recent.
func (br *Brain) codeRequest(chats, markers []string, now time.Time) (req otpRequest, ok bool) {
	if len(markers) == 0 {
		markers = defaultOTPMarkers
	}
	since := now.Add(-otpWindow)
	br.scanHistory(otpRequestBudget, nil, func(e history.Entry) bool {
		at, err := time.Parse(time.RFC3339, e.TS)
		if err != nil {
			return true
		}
		if e.Type != history.In && at.Before(since) {
			return false // older than the window, and everything before it too
		}
		if e.Type != history.Out || (e.From != br.id && e.Via == "") || !slices.Contains(chats, e.Chat) || at.Before(since) {
			return true
		}
		if !slices.ContainsFunc(markers, func(m string) bool { return hasMarker(e.Text, m) }) {
			return true
		}
		if e.OTPRequest != "" || !ok {
			req, ok = otpRequest{ID: e.OTPRequest, At: at}, true
		}
		// A request with an id ends the search; one without goes on
		// looking for one further back.
		return e.OTPRequest == ""
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

// asksForCode reports whether text, going out to one of the brain's chats,
// asks for a one-time code that the guard would catch: the guard is on and
// text holds one of its markers.
func (br *Brain) asksForCode(text string) bool {
	s := br.settings.Load()
	markers := s.otpMarkers
	if len(markers) == 0 {
		markers = defaultOTPMarkers
	}
	return s.otpGuard && slices.ContainsFunc(markers, func(m string) bool { return hasMarker(text, m) })
}

// newOTPRequestID names a request for a code made through /send, which
// the hand-off of the code that answers it repeats.
func newOTPRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// otpHandoffTTL is how long a caught code waits in its hand-off file for
// the script that asked for it. Codes expire within minutes; the file does
// not hold one for longer than that.
var otpHandoffTTL = 10 * time.Minute

// otpHandoff is the hand-off file, <data_dir>/otp/<brain>.json, for local
// scripts: docs/CONFIGURATION.md is its contract.
type otpHandoff struct {
	Brain  string `json:"brain"`
	Chat   string `json:"chat"`
	Sender string `json:"sender"`
	Code   string `json:"code"`
	Typed  string `json:"typed"`
	// Received is local time with offset, like the history; the unix
	// times are what scripts compare with: they need no time zone
	// (received ends in "Z" on a host in UTC, which Python before 3.11
	// cannot read) and tell apart two codes in one second.
	Received       string `json:"received"`
	ReceivedUnixMS int64  `json:"received_unix_ms"`
	// Request is the id /send answered the code request with, "" when
	// the brain asked in its own words; RequestedUnixMS is when the
	// request went out.
	Request         string `json:"request"`
	RequestedUnixMS int64  `json:"requested_unix_ms"`
	ExpiresUnixMS   int64  `json:"expires_unix_ms"`
}

// otpHandoffs writes the hand-off files and takes each away again once it
// expires, unless a newer code replaced it meanwhile or the script that
// picked the code up removed it first.
type otpHandoffs struct {
	mu     sync.Mutex
	timers map[string]*time.Timer // by file
}

// write puts h into dir's file for its brain, readable by this user only,
// and has it removed at its expiry.
func (o *otpHandoffs) write(dir string, h otpHandoff) error {
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, h.Brain+".json")
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := fsx.WriteFileAtomic(path, data, 0o600, false); err != nil {
		return err
	}
	if o.timers == nil {
		o.timers = map[string]*time.Timer{}
	}
	if t := o.timers[path]; t != nil {
		t.Stop()
	}
	o.timers[path] = time.AfterFunc(time.Until(time.UnixMilli(h.ExpiresUnixMS)), func() { o.expire(path, h.ReceivedUnixMS) })
	return nil
}

// expire removes the file at path if it still holds the code received at
// receivedMS.
func (o *otpHandoffs) expire(path string, receivedMS int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var h otpHandoff
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &h) != nil || h.ReceivedUnixMS != receivedMS {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("otp: removing an expired code: %v", err)
	}
	delete(o.timers, path)
}

// stop ends the expiry timers, when the bridge stops; the next start's
// sweep removes what expired in between.
func (o *otpHandoffs) stop() {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, t := range o.timers {
		t.Stop()
	}
	o.timers = nil
}

// sweepOTPHandoffs removes the hand-off files in dir that expired by now,
// left by a bridge that stopped before their time: one whose expiry has
// passed, or for one written before files had an expiry, one older than
// otpHandoffTTL.
func sweepOTPHandoffs(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		var h otpHandoff
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		expired := now.Sub(info.ModTime()) > otpHandoffTTL
		if json.Unmarshal(data, &h) == nil && h.ExpiresUnixMS > 0 {
			expired = now.UnixMilli() >= h.ExpiresUnixMS
		}
		if expired {
			if err := os.Remove(path); err != nil {
				log.Printf("otp: removing an expired code: %v", err)
			}
		}
	}
}

// handOffCode hands a code the OTP guard caught in m to the local script
// that asked for it: it is written to <data_dir>/otp/<brain>.json,
// readable only by this user, until it expires (otpHandoffTTL). Brains
// cannot read it (the sandbox denies the data folder), and it never
// reaches the history or the model. A newer code for the brain replaces
// it.
func (b *Bridge) handOffCode(br *Brain, m Inbound, c otpCatch) {
	now := time.Now()
	h := otpHandoff{
		Brain: br.ID(), Chat: m.JID, Sender: m.Sender, Code: c.Code, Typed: c.Typed,
		Received: now.Format(time.RFC3339), ReceivedUnixMS: now.UnixMilli(),
		Request: c.Request.ID, RequestedUnixMS: c.Request.At.UnixMilli(),
		ExpiresUnixMS: now.Add(otpHandoffTTL).UnixMilli(),
	}
	if err := b.otp.write(filepath.Join(b.cfgs.Get().DataDir, "otp"), h); err != nil {
		log.Printf("brain %s: handing over a one-time code: %v", br.ID(), err)
	}
}
