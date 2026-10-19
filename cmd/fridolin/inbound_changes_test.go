package main

import (
	"testing"

	"github.com/Icewater1337/fridolin/internal/chat"
)

func TestApplyChange(t *testing.T) {
	const chatJID = "120363000000000000@g.us"
	a := Inbound{Inbound: chat.Inbound{JID: chatJID, Sender: "15551234567", Text: "book Tuesday", ID: "A", Media: []string{"/m/a.jpg"}}}
	b := Inbound{Inbound: chat.Inbound{JID: chatJID, Sender: "15551234567", Text: "for four", ID: "B"}}
	merged := a.merge(b)
	cases := []struct {
		name         string
		m            Inbound
		c            chat.Change
		found, keep  bool
		text         string
		media        []string
		changesFound bool
	}{
		{"delete the only part", a, chat.Change{Kind: chat.ChangeRevoke, Chat: chatJID, Sender: "15551234567", Target: "A"}, true, false, "", nil, true},
		{"delete one part of two", merged, chat.Change{Kind: chat.ChangeRevoke, Chat: chatJID, Sender: "15551234567", Target: "A"}, true, true, "for four", nil, true},
		{"edit one part of two", merged, chat.Change{Kind: chat.ChangeEdit, Chat: chatJID, Sender: "15551234567", Target: "A", Text: "book Wednesday"}, true, true, "book Wednesday\n\nfor four", []string{"/m/a.jpg"}, true},
		{"another sender's", merged, chat.Change{Kind: chat.ChangeRevoke, Chat: chatJID, Sender: "15559876543", Target: "A"}, false, true, "book Tuesday\n\nfor four", []string{"/m/a.jpg"}, false},
		{"another chat's", merged, chat.Change{Kind: chat.ChangeRevoke, Chat: "15551234567@s.whatsapp.net", Sender: "15551234567", Target: "A"}, false, true, "book Tuesday\n\nfor four", []string{"/m/a.jpg"}, false},
		{"another message", merged, chat.Change{Kind: chat.ChangeRevoke, Chat: chatJID, Sender: "15551234567", Target: "C"}, false, true, "book Tuesday\n\nfor four", []string{"/m/a.jpg"}, false},
		{"no ID", Inbound{Inbound: chat.Inbound{JID: chatJID, Sender: "15551234567", Text: "from /ingest"}}, chat.Change{Kind: chat.ChangeRevoke, Chat: chatJID, Sender: "15551234567"}, false, true, "from /ingest", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.m.changes(c.c); got != c.changesFound {
				t.Errorf("changes = %v, want %v", got, c.changesFound)
			}
			out, found, keep := c.m.applyChange(c.c)
			if found != c.found || keep != c.keep {
				t.Fatalf("applyChange found %v, keep %v; want %v, %v", found, keep, c.found, c.keep)
			}
			if keep && (out.Text != c.text || len(out.Media) != len(c.media)) {
				t.Errorf("applyChange = text %q, media %v; want %q, %v", out.Text, out.Media, c.text, c.media)
			}
		})
	}
}
