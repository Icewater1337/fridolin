package main

import (
	"strings"
	"testing"
	"time"
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
			code, ok := otpCode(c.text)
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
			if got := hasMarker(c.text, c.marker); got != c.want {
				t.Errorf("hasMarker(%q, %q) = %v, want %v", c.text, c.marker, got, c.want)
			}
		})
	}
}

// A chat asked for a code when an outbound line there, written in the last
// otpWindow, holds a marker: the brain's own words, or a /send in the
// chat. Another chat's request, an old one, a person's own words and
// another brain's are none.
func TestCodeRequest(t *testing.T) {
	now := time.Now()
	ago := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	const lid = "15559876543@lid"
	ask := func(chat, from, via, text string, d time.Duration) historyEntry {
		return historyEntry{TS: ago(d), Chat: chat, From: from, Via: via, Text: text, Type: historyOut}
	}
	cases := []struct {
		name    string
		lines   []historyEntry
		chats   []string
		markers []string
		want    bool
	}{
		{"its own request", []historyEntry{ask(testDMChat, "a", "", "🔐 Send me the code", time.Minute)}, nil, nil, true},
		{"in another chat", []historyEntry{ask(testGroup, "a", "", "🔐 Send me the code", time.Minute)}, nil, nil, false},
		{"longer ago than the window", []historyEntry{ask(testDMChat, "a", "", "🔐 Send me the code", 11*time.Minute)}, nil, nil, false},
		{"a person's words", []historyEntry{{TS: ago(time.Minute), Chat: testDMChat, From: "15551234567", Text: "what is an OTP?", Type: historyIn}}, nil, nil, false},
		{"another brain's own words", []historyEntry{ask(testDMChat, "b", "", "🔐 Send me the code", time.Minute)}, nil, nil, false},
		{"a script's request", []historyEntry{ask(testDMChat, "script", "script", "your OTP please", time.Minute)}, nil, nil, true},
		{"a request without a token", []historyEntry{ask(testDMChat, "script", "api", "your OTP please", time.Minute)}, nil, nil, true},
		{"its own /send", []historyEntry{ask(testDMChat, "a", "brain", "your OTP please", time.Minute)}, nil, nil, true},
		{"no marker", []historyEntry{ask(testDMChat, "a", "", "here is your summary", time.Minute)}, nil, nil, false},
		{"a marker inside a word", []historyEntry{ask(testDMChat, "a", "", "hotpot tonight?", time.Minute)}, nil, nil, false},
		{"a bank's name by default", []historyEntry{ask(testDMChat, "a", "", "Please send the Examplebank code", time.Minute)}, nil, nil, false},
		{"a bank's name configured", []historyEntry{ask(testDMChat, "a", "", "Please send the Examplebank code", time.Minute)}, nil, []string{"Examplebank"}, true},
		{"followed by other words", []historyEntry{
			ask(testDMChat, "a", "", "🔐 Send me the code", 3*time.Minute),
			ask(testDMChat, "a", "", "Still waiting for it", 2*time.Minute),
			{TS: ago(time.Minute), Chat: testDMChat, From: "15551234567", Text: "one moment", Type: historyIn},
		}, nil, nil, true},
		{"behind a late message sent long ago", []historyEntry{
			ask(testDMChat, "a", "", "🔐 Send me the code", time.Minute),
			{TS: ago(3 * time.Hour), Chat: testDMChat, From: "15551234567", Text: "sent while offline", Type: historyIn},
		}, nil, nil, true},
		{"by the DM's LID", []historyEntry{ask(lid, "a", "", "🔐 Send me the code", time.Minute)}, []string{testDMChat, lid}, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			br := bareBrain(t)
			for _, e := range c.lines {
				br.appendEntry(e)
			}
			chats := c.chats
			if chats == nil {
				chats = []string{testDMChat}
			}
			req, ok := br.codeRequest(chats, c.markers, now)
			if ok != c.want {
				t.Fatalf("codeRequest = %v, want %v", ok, c.want)
			}
			if ok && (req.At.Before(now.Add(-otpWindow)) || req.At.After(now)) {
				t.Errorf("request at %v, want within the window", req.At)
			}
		})
	}
}
