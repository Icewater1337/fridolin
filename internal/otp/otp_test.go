package otp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/history"
)

// A code is recognised as people send one: digits, in groups or not, or
// upper-case letters and digits; nothing that merely holds one.
func TestOTPCode(t *testing.T) {
	cases := []struct {
		text, code string
		ok         bool
	}{
		{"483920", "483920", true},
		{" 483920 ", "483920", true},
		{"483 920", "483920", true},
		{"483-920", "483920", true},
		{"48 39 20", "483920", true},
		{"1234", "1234", true},
		{"12345678", "12345678", true},
		{"2024", "2024", true}, // a year too, which is why the chat must have asked
		{"G-483920", "G483920", true},
		{"AB12CD", "AB12CD", true},
		{"123", "", false},
		{"123456789", "", false},
		{"483  920", "", false},
		{"483 920 sorry", "", false},
		{"code 123456", "", false},
		{"ab12cd", "", false},
		{"ABCDEF", "", false},
		{"A1BCDE", "", false},
		{strings.Repeat("7", 12), "", false}, // twelve digits, a phone number perhaps
		{"+41 79 123", "", false},
		{"12:30", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		t.Run(c.text, func(t *testing.T) {
			code, ok := Code(c.text)
			if ok != c.ok || (ok && code != c.code) {
				t.Errorf("otpCode(%q) = %q, %v; want %q, %v", c.text, code, ok, c.code, c.ok)
			}
		})
	}
}

// Markers match in any letter case, as whole words where they begin or
// end with a letter or digit.
func TestHasMarker(t *testing.T) {
	cases := []struct {
		text, marker string
		want         bool
	}{
		{"Your OTP is on its way", "OTP", true},
		{"your otp: please", "OTP", true},
		{"OTP", "OTP", true},
		{"a hotpot for dinner", "OTP", false},
		{"2FA please", "2FA", true},
		{"pin 12FA", "2FA", false},
		{"🔐 the code please", "🔐", true},
		{"🔐code", "🔐", true},
		{"Send me the one-time code", "one-time code", true},
		{"VERIFICATION CODE?", "verification code", true},
		{"the verification codes", "verification code", false},
		{"Examplebank 2FA: reply with the code", "Examplebank 2FA", true},
		{"İstanbul OTP", "OTP", true},
		{"anything", "", false},
	}
	for _, c := range cases {
		t.Run(c.text+"/"+c.marker, func(t *testing.T) {
			if got := HasMarker(c.text, c.marker); got != c.want {
				t.Errorf("hasMarker(%q, %q) = %v, want %v", c.text, c.marker, got, c.want)
			}
		})
	}
}

// A chat asked for a code when an outbound line there, written in the last
// otpWindow, holds a marker: the brain's own words, or a /send in the
// chat. Another chat's request, an old one, a person's own words and
// another brain's are none. A script's request with an id is the one a
// code answers, even when the brain's own words with a marker came after
// it: its script waits for a code that names it.
func TestCodeRequest(t *testing.T) {
	now := time.Now()
	ago := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	const lid = "15559876543@lid"
	ask := func(chat, from, via, text string, d time.Duration) history.Entry {
		return history.Entry{TS: ago(d), Chat: chat, From: from, Via: via, Text: text, Type: history.Out}
	}
	// askFor is a script's request in the DM, which /send answered with id.
	askFor := func(id, text string, d time.Duration) history.Entry {
		e := ask("15551234567@s.whatsapp.net", "script", "script", text, d)
		e.OTPRequest = id
		return e
	}
	cases := []struct {
		name    string
		lines   []history.Entry
		chats   []string
		markers []string
		want    bool
		id      string // the request's id, "" for the brain's own words
	}{
		{"its own request", []history.Entry{ask("15551234567@s.whatsapp.net", "a", "", "🔐 Send me the code", time.Minute)}, nil, nil, true, ""},
		{"in another chat", []history.Entry{ask("120363000000000000@g.us", "a", "", "🔐 Send me the code", time.Minute)}, nil, nil, false, ""},
		{"longer ago than the window", []history.Entry{ask("15551234567@s.whatsapp.net", "a", "", "🔐 Send me the code", 11*time.Minute)}, nil, nil, false, ""},
		{"a person's words", []history.Entry{{TS: ago(time.Minute), Chat: "15551234567@s.whatsapp.net", From: "15551234567", Text: "what is an OTP?", Type: history.In}}, nil, nil, false, ""},
		{"another brain's own words", []history.Entry{ask("15551234567@s.whatsapp.net", "b", "", "🔐 Send me the code", time.Minute)}, nil, nil, false, ""},
		{"a script's request", []history.Entry{ask("15551234567@s.whatsapp.net", "script", "script", "your OTP please", time.Minute)}, nil, nil, true, ""},
		{"a request without a token", []history.Entry{ask("15551234567@s.whatsapp.net", "script", "api", "your OTP please", time.Minute)}, nil, nil, true, ""},
		{"its own /send", []history.Entry{ask("15551234567@s.whatsapp.net", "a", "brain", "your OTP please", time.Minute)}, nil, nil, true, ""},
		{"no marker", []history.Entry{ask("15551234567@s.whatsapp.net", "a", "", "here is your summary", time.Minute)}, nil, nil, false, ""},
		{"a marker inside a word", []history.Entry{ask("15551234567@s.whatsapp.net", "a", "", "hotpot tonight?", time.Minute)}, nil, nil, false, ""},
		{"a bank's name by default", []history.Entry{ask("15551234567@s.whatsapp.net", "a", "", "Please send the Examplebank code", time.Minute)}, nil, nil, false, ""},
		{"a bank's name configured", []history.Entry{ask("15551234567@s.whatsapp.net", "a", "", "Please send the Examplebank code", time.Minute)}, nil, []string{"Examplebank"}, true, ""},
		{"followed by other words", []history.Entry{
			ask("15551234567@s.whatsapp.net", "a", "", "🔐 Send me the code", 3*time.Minute),
			ask("15551234567@s.whatsapp.net", "a", "", "Still waiting for it", 2*time.Minute),
			{TS: ago(time.Minute), Chat: "15551234567@s.whatsapp.net", From: "15551234567", Text: "one moment", Type: history.In},
		}, nil, nil, true, ""},
		{"behind a late message sent long ago", []history.Entry{
			ask("15551234567@s.whatsapp.net", "a", "", "🔐 Send me the code", time.Minute),
			{TS: ago(3 * time.Hour), Chat: "15551234567@s.whatsapp.net", From: "15551234567", Text: "sent while offline", Type: history.In},
		}, nil, nil, true, ""},
		{"by the DM's LID", []history.Entry{ask(lid, "a", "", "🔐 Send me the code", time.Minute)}, []string{"15551234567@s.whatsapp.net", lid}, nil, true, ""},
		{"a script's request names its id", []history.Entry{askFor("abc123", "your OTP please", time.Minute)}, nil, nil, true, "abc123"},
		{"a script's request, then the brain's own words", []history.Entry{
			askFor("abc123", "your OTP please", 2*time.Minute),
			ask("15551234567@s.whatsapp.net", "a", "", "That is your Examplebank login asking for the 2FA code", time.Minute),
		}, nil, nil, true, "abc123"},
		{"the newest of two scripts' requests", []history.Entry{
			askFor("abc123", "your OTP please", 3*time.Minute),
			askFor("def456", "your OTP please", 2*time.Minute),
			ask("15551234567@s.whatsapp.net", "a", "", "🔐 Send me the code", time.Minute),
		}, nil, nil, true, "def456"},
		{"the brain's own words after a script's request ran out", []history.Entry{
			askFor("abc123", "your OTP please", 11*time.Minute),
			ask("15551234567@s.whatsapp.net", "a", "", "🔐 Send me the code", time.Minute),
		}, nil, nil, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { root.Close() })
			for _, e := range c.lines {
				if _, err := history.Append(root, e); err != nil {
					t.Fatal(err)
				}
			}
			chats := c.chats
			if chats == nil {
				chats = []string{"15551234567@s.whatsapp.net"}
			}
			req, ok := Requested(root, "a", chats, c.markers, now)
			if ok != c.want {
				t.Fatalf("codeRequest = %v, want %v", ok, c.want)
			}
			if ok && (req.At.Before(now.Add(-Window)) || req.At.After(now)) {
				t.Errorf("request at %v, want within the window", req.At)
			}
			if req.ID != c.id {
				t.Errorf("request id %q, want %q", req.ID, c.id)
			}
			for _, e := range c.lines {
				if c.id != "" && e.OTPRequest == c.id && req.At.Format(time.RFC3339) != e.TS {
					t.Errorf("request at %v, want %s, when the script asked", req.At, e.TS)
				}
			}
		})
	}
}

// A caught code waits in its hand-off file until it expires, and is then
// taken away, unless a newer code replaced it; one a script removed stays
// removed.
func TestOTPHandoffExpires(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "otp")
	path := filepath.Join(dir, "a.json")
	// Each received a millisecond after the one before.
	received := time.Now().UnixMilli()
	handoff := func(code string, ttl time.Duration) Record {
		received++
		return Record{Brain: "a", Code: code, ReceivedUnixMS: received, ExpiresUnixMS: time.Now().Add(ttl).UnixMilli()}
	}
	read := func() string {
		var h Record
		data, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		_ = json.Unmarshal(data, &h)
		return h.Code
	}
	var o Handoff
	t.Cleanup(o.Stop)

	if err := o.Write(dir, handoff("111111", 150*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if read() != "111111" {
		t.Fatal("the code was not handed over")
	}
	waitForOTP(t, 5*time.Second, "the code to expire", func() bool { return read() == "" })

	// A newer code is not taken away at the older one's expiry.
	older := handoff("222222", time.Hour)
	if err := o.Write(dir, older); err != nil {
		t.Fatal(err)
	}
	if err := o.Write(dir, handoff("333333", time.Hour)); err != nil {
		t.Fatal(err)
	}
	o.expire(path, older.ReceivedUnixMS)
	if read() != "333333" {
		t.Errorf("the newer code went at the older one's expiry: %q", read())
	}

	// Picked up and removed by the script, it stays removed.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	o.expire(path, older.ReceivedUnixMS)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a removed hand-off came back (%v)", err)
	}
}

// At startup the hand-off files of codes that expired while the bridge was
// stopped are removed, and those still valid kept.
func TestSweepOTPHandoffs(t *testing.T) {
	now := time.Now()
	old := now.Add(-time.Hour)
	cases := []struct {
		name  string
		data  string
		mtime time.Time
		kept  bool
	}{
		{"expired", fmt.Sprintf(`{"code":"1234","expires_unix_ms":%d}`, now.Add(-time.Second).UnixMilli()), now, false},
		{"still valid", fmt.Sprintf(`{"code":"1234","expires_unix_ms":%d}`, now.Add(time.Minute).UnixMilli()), old, true},
		{"from before expiry, old", `{"code":"1234"}`, old, false},
		{"from before expiry, recent", `{"code":"1234"}`, now, true},
		{"no JSON, old", `nonsense`, old, false},
	}
	dir := t.TempDir()
	for i, c := range cases {
		p := filepath.Join(dir, fmt.Sprintf("b%d.json", i))
		if err := os.WriteFile(p, []byte(c.data), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, c.mtime, c.mtime); err != nil {
			t.Fatal(err)
		}
	}
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, old, old); err != nil {
		t.Fatal(err)
	}
	Sweep(dir, now)
	for i, c := range cases {
		_, err := os.Stat(filepath.Join(dir, fmt.Sprintf("b%d.json", i)))
		if kept := err == nil; kept != c.kept {
			t.Errorf("%s: kept %v, want %v", c.name, kept, c.kept)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("a file that is no hand-off went: %v", err)
	}
}

// waitForOTP polls cond until it holds or the timeout passes.
func waitForOTP(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
