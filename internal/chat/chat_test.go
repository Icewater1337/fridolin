package chat

import (
	"strings"
	"testing"
)

func TestTextWithQuote(t *testing.T) {
	q := &Quote{From: "15559876543", Text: `Lunch at "12"?`}
	cases := []struct {
		name string
		m    Inbound
		want string
	}{
		{"no quote", Inbound{Text: "hello"}, "hello"},
		{"quote and text", Inbound{Text: "yes", Quoted: q}, `[reply to 15559876543: "Lunch at \"12\"?"]` + "\nyes"},
		// A group reply that was only the trigger: the quote is the content.
		{"quote only", Inbound{Quoted: q}, `[reply to 15559876543: "Lunch at \"12\"?"]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.m.TextWithQuote(); got != c.want {
				t.Errorf("textWithQuote() = %q, want %q", got, c.want)
			}
		})
	}
}

// Merging keeps the text the model sees the same as when each part carried
// its own "[reply to …]" line, and keeps the first quote apart.
func TestMergeKeepsQuotes(t *testing.T) {
	q1 := &Quote{From: "15559876543", Text: "Lunch?"}
	q2 := &Quote{From: "15551234567", Text: "Dinner?"}
	cases := []struct {
		name       string
		first      Inbound
		next       Inbound
		wantText   string
		wantQuoted *Quote
		wantModel  string
	}{
		{"plain parts", Inbound{Text: "a"}, Inbound{Text: "b"}, "a\n\nb", nil, "a\n\nb"},
		{"file, then a reply", Inbound{Media: []string{"/m/1.jpg"}}, Inbound{Text: "what is this?", Quoted: q1}, "what is this?", q1,
			"[reply to 15559876543: \"Lunch?\"]\nwhat is this?"},
		{"reply, then plain text", Inbound{Text: "yes", Quoted: q1}, Inbound{Text: "at 12"}, "yes\n\nat 12", q1,
			"[reply to 15559876543: \"Lunch?\"]\nyes\n\nat 12"},
		{"plain text, then a reply", Inbound{Text: "hi"}, Inbound{Text: "yes", Quoted: q1}, "hi\n\n[reply to 15559876543: \"Lunch?\"]\nyes", nil,
			"hi\n\n[reply to 15559876543: \"Lunch?\"]\nyes"},
		{"two replies", Inbound{Text: "yes", Quoted: q1}, Inbound{Text: "no", Quoted: q2}, "yes\n\n[reply to 15551234567: \"Dinner?\"]\nno", q1,
			"[reply to 15559876543: \"Lunch?\"]\nyes\n\n[reply to 15551234567: \"Dinner?\"]\nno"},
		{"the same reply twice", Inbound{Text: "yes", Quoted: q1}, Inbound{Text: "yes", Quoted: q1}, "yes", q1,
			"[reply to 15559876543: \"Lunch?\"]\nyes"},
		{"trigger-only reply, then text", Inbound{Quoted: q1}, Inbound{Text: "and this"}, "and this", q1,
			"[reply to 15559876543: \"Lunch?\"]\nand this"},
		{"the same trigger-only reply twice", Inbound{Quoted: q1}, Inbound{Quoted: q1}, "", q1,
			"[reply to 15559876543: \"Lunch?\"]"},
		{"trigger-only reply, then another", Inbound{Quoted: q1}, Inbound{Quoted: q2}, "[reply to 15551234567: \"Dinner?\"]", q1,
			"[reply to 15559876543: \"Lunch?\"]\n[reply to 15551234567: \"Dinner?\"]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.first.Merge(c.next)
			if got.Text != c.wantText {
				t.Errorf("Text = %q, want %q", got.Text, c.wantText)
			}
			if got.Quoted != c.wantQuoted {
				t.Errorf("Quoted = %+v, want %+v", got.Quoted, c.wantQuoted)
			}
			if m := got.TextWithQuote(); m != c.wantModel {
				t.Errorf("textWithQuote() = %q, want %q", m, c.wantModel)
			}
		})
	}
}

// Group chats are told apart, and DM chats built, by the server names
// whatsmeow uses.
func TestJIDHelpers(t *testing.T) {
	for jid, want := range map[string]bool{
		"120363000000000000@g.us":    true,
		"15551234567@s.whatsapp.net": false,
		"15551234567@lid":            false,
		"status@broadcast":           false,
		"g.us":                       false,
		"":                           false,
	} {
		if got := IsGroupJID(jid); got != want {
			t.Errorf("isGroupJID(%q) = %v, want %v", jid, got, want)
		}
		if got := (Inbound{JID: jid}).IsGroup(); got != want {
			t.Errorf("Inbound{JID: %q}.IsGroup() = %v, want %v", jid, got, want)
		}
	}
	if got := DMJID("15551234567"); got != "15551234567@s.whatsapp.net" {
		t.Errorf("userJID = %q", got)
	}
}

func TestNormalizeJID(t *testing.T) {
	cases := map[string]string{
		"15551234567@s.whatsapp.net": "15551234567",
		"+15551234567":               "15551234567",
		"120363000000000000@g.us":    "120363000000000000",
		"15551234567":                "15551234567",
		"++15551234567":              "15551234567",
		" + 15551234567 ":            "15551234567",
	}
	for in, want := range cases {
		if got := NormalizeJID(in); got != want {
			t.Errorf("normalizeJID(%q) = %q, want %q", in, got, want)
		}
	}
}

// FuzzNormalizeJID: a normalised JID is what allowed_numbers and the
// identity map hold, and what a sender is compared with. It has no server
// and no space around it, it is part of the input, normalising it again
// changes nothing, and every way of writing a number gives the number.
func FuzzNormalizeJID(f *testing.F) {
	for _, s := range []string{"15551234567@s.whatsapp.net", "+15551234567", "120363000000000000@g.us", "15551234567", "15551234567:12@s.whatsapp.net", "", "@", "+", "a@b@c", "+@s.whatsapp.net"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, jid string) {
		n := NormalizeJID(jid)
		if strings.Contains(n, "@") {
			t.Errorf("normalizeJID(%q) = %q, which still has a server", jid, n)
		}
		if n != strings.TrimSpace(n) {
			t.Errorf("normalizeJID(%q) = %q, not trimmed", jid, n)
		}
		if !strings.Contains(jid, n) {
			t.Errorf("normalizeJID(%q) = %q, which is not part of it", jid, n)
		}
		if again := NormalizeJID(n); again != n {
			t.Errorf("normalizeJID(%q) = %q, but normalising that gives %q", jid, n, again)
		}
		digits := strings.Map(func(r rune) rune {
			if '0' <= r && r <= '9' {
				return r
			}
			return -1
		}, jid)
		if digits == "" {
			return
		}
		for _, form := range []string{digits, "+" + digits, digits + "@s.whatsapp.net", "+" + digits + "@s.whatsapp.net", digits + "@g.us"} {
			if got := NormalizeJID(form); got != digits {
				t.Errorf("normalizeJID(%q) = %q, want %q", form, got, digits)
			}
		}
	})
}
