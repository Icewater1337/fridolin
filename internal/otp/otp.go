package otp

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
// instead (see Handoff.Write). A message is caught when it is nothing but a
// code (Code) and its chat asked for one within Window (Requested).

// Window is how long after a chat was asked for a code a code that
// arrives there is caught. Codes expire within minutes; past this, a bare
// number in the chat is a year, a price or a PIN for the brain to read.
var Window = 10 * time.Minute

// DefaultMarkers are the phrases that mark an outbound message as a
// request for a one-time code, each matched in any letter case and as
// whole words (see HasMarker). Add your own (a bank's name, say) with
// otp_markers, which replaces them.
var DefaultMarkers = []string{"🔐", "OTP", "one-time code", "2FA", "verification code"}

var (
	// otpDigitsRe is digits, maybe in groups split by single spaces or
	// dashes, as codes are read out and written: "483 920", "48-39-20".
	otpDigitsRe = regexp.MustCompile(`^[0-9]+(?:[ -][0-9]+)*$`)
	// otpMixedRe is upper-case letters and digits, maybe split by dashes:
	// "G-483920", "AB12CD".
	otpMixedRe = regexp.MustCompile(`^[A-Z0-9]+(?:-[A-Z0-9]+)*$`)
)

// Code returns the code text is, when text is nothing but one: 4 to 8
// digits, or 6 to 10 upper-case letters and digits, at least two of them
// digits, as in "G-483920". The code is text without its spaces and
// dashes.
func Code(text string) (code string, ok bool) {
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

// HasMarker reports whether text holds marker in any letter case. Where the
// marker begins or ends with a letter or a digit, a word must begin or end
// there too, so that "OTP" is not found in "hotpot", nor "2FA" in "12FA".
func HasMarker(text, marker string) bool {
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

// Request is the outbound line that asked a chat for a code.
type Request struct {
	// ID is what /send answered the request with (history.Entry.OTPRequest),
	// "" when the brain asked in its own words.
	ID string
	At time.Time // when it was written
}

// otpRequestBudget is how far back from the end of the history the guard
// looks for a request: far more than Window of messages takes.
const otpRequestBudget = 1 << 20

// Requested returns the request for a code in the chat that goes by
// chats which was written in the Window before now: an outbound line
// there, the brain's own or one sent on its behalf through /send, that
// holds one of markers (the built-in ones for none). Another brain's own
// words in this history do not count. Of several, it is the newest that
// came through /send with an id, whose script waits for the code that
// names it, even when the brain's own words held a marker after it ("the
// bank asks for your 2FA code"); without one, the newest of all. The
// history is read back from its end, and only as far as the lines the
// bridge stamps when it writes them (all but a message's own, which has
// the time it was sent) are recent.
func Requested(root *os.Root, brainID string, chats, markers []string, now time.Time) (req Request, ok bool) {
	if len(markers) == 0 {
		markers = DefaultMarkers
	}
	since := now.Add(-Window)
	history.Scan(root, brainID, otpRequestBudget, nil, func(e history.Entry) bool {
		at, err := time.Parse(time.RFC3339, e.TS)
		if err != nil {
			return true
		}
		if e.Type != history.In && at.Before(since) {
			return false // older than the window, and everything before it too
		}
		if e.Type != history.Out || (e.From != brainID && e.Via == "") || !slices.Contains(chats, e.Chat) || at.Before(since) {
			return true
		}
		if !slices.ContainsFunc(markers, func(m string) bool { return HasMarker(e.Text, m) }) {
			return true
		}
		if e.OTPRequest != "" || !ok {
			req, ok = Request{ID: e.OTPRequest, At: at}, true
		}
		// A request with an id ends the search; one without goes on
		// looking for one further back.
		return e.OTPRequest == ""
	})
	return req, ok
}

// NewRequestID names a request for a code made through /send, which
// the hand-off of the code that answers it repeats.
func NewRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// HandoffTTL is how long a caught code waits in its hand-off file for
// the script that asked for it. Codes expire within minutes; the file does
// not hold one for longer than that.
var HandoffTTL = 10 * time.Minute

// Record is the hand-off file, <data_dir>/otp/<brain>.json, for local
// scripts: docs/CONFIGURATION.md is its contract.
type Record struct {
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

// Handoff writes the hand-off files and takes each away again once it
// expires, unless a newer code replaced it meanwhile or the script that
// picked the code up removed it first.
type Handoff struct {
	mu     sync.Mutex
	timers map[string]*time.Timer // by file
}

// Write puts h into dir's file for its brain, readable by this user only,
// and has it removed at its expiry.
func (o *Handoff) Write(dir string, h Record) error {
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
func (o *Handoff) expire(path string, receivedMS int64) {
	o.expireAgain(path, receivedMS, 0)
}

// expireRetry is how soon a removal that failed is first tried again:
// on Windows a file cannot be removed while a script has it open to read,
// as it may at the very moment the code expires.
const expireRetry = 20 * time.Millisecond

// expireAgain is expire after it failed with a wait of retry before, which
// doubles, up to a minute, until the file goes, holds a newer code or the
// bridge stops.
func (o *Handoff) expireAgain(path string, receivedMS int64, retry time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var h Record
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &h) != nil || h.ReceivedUnixMS != receivedMS {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		if retry == 0 {
			log.Printf("otp: removing an expired code: %v; trying again", err)
		}
		retry = min(max(2*retry, expireRetry), time.Minute)
		if o.timers != nil {
			if t := o.timers[path]; t != nil {
				t.Stop()
			}
			o.timers[path] = time.AfterFunc(retry, func() { o.expireAgain(path, receivedMS, retry) })
		}
		return
	}
	delete(o.timers, path)
}

// Take hands over the code that answers request, once: the hand-off file
// in dir whose Request it is, unless it expired by now, is removed and its
// record returned. ok is false when no such code is waiting, whether the
// request is unknown, not answered yet, expired or taken already.
func (o *Handoff) Take(dir, request string, now time.Time) (h Record, ok bool, err error) {
	if request == "" {
		return Record{}, false, nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, &h) != nil || h.Request != request {
			continue
		}
		if h.ExpiresUnixMS > 0 && now.UnixMilli() >= h.ExpiresUnixMS {
			return Record{}, false, nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return Record{}, false, err
		}
		if t := o.timers[path]; t != nil {
			t.Stop()
			delete(o.timers, path)
		}
		return h, true, nil
	}
	return Record{}, false, nil
}

// Stop ends the expiry timers, when the bridge stops; the next start's
// sweep removes what expired in between.
func (o *Handoff) Stop() {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, t := range o.timers {
		t.Stop()
	}
	o.timers = nil
}

// Sweep removes the hand-off files in dir that expired by now,
// left by a bridge that stopped before their time: one whose expiry has
// passed, or for one written before files had an expiry, one older than
// HandoffTTL.
func Sweep(dir string, now time.Time) {
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
		var h Record
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		expired := now.Sub(info.ModTime()) > HandoffTTL
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
