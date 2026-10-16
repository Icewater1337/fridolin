package main

import "testing"

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
			if got := c.m.textWithQuote(); got != c.want {
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
			got := c.first.merge(c.next)
			if got.Text != c.wantText {
				t.Errorf("Text = %q, want %q", got.Text, c.wantText)
			}
			if got.Quoted != c.wantQuoted {
				t.Errorf("Quoted = %+v, want %+v", got.Quoted, c.wantQuoted)
			}
			if m := got.textWithQuote(); m != c.wantModel {
				t.Errorf("textWithQuote() = %q, want %q", m, c.wantModel)
			}
		})
	}
}
