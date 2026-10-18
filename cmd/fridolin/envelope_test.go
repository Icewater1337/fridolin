package main

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/Icewater1337/fridolin/internal/chat"
)

func TestClassify(t *testing.T) {
	key := &waCommon.MessageKey{ID: proto.String("3EB0ORIGINAL")}
	edited := &waE2E.Message{Conversation: proto.String("book Wednesday")}
	cases := []struct {
		name     string
		evt      *events.Message
		kind     msgKind
		target   string
		editText string
	}{
		{"text", &events.Message{Message: &waE2E.Message{Conversation: proto.String("hi")}}, kindMessage, "", ""},
		{"a photo", &events.Message{Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{}}}, kindMessage, "", ""},
		{"a location", &events.Message{Message: &waE2E.Message{LocationMessage: &waE2E.LocationMessage{DegreesLatitude: proto.Float64(47.37)}}}, kindMessage, "", ""},
		{"a deletion", &events.Message{Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: key}}}, kindRevoke, "3EB0ORIGINAL", ""},
		{"an edit", &events.Message{IsEdit: true, Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: key, EditedMessage: edited}}}, kindEdit, "3EB0ORIGINAL", "book Wednesday"},
		{"another protocol message", &events.Message{Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_EPHEMERAL_SETTING.Enum()}}}, kindOther, "", ""},
		{"view once", &events.Message{IsViewOnce: true, Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{}}}, kindViewOnce, "", ""},
		{"a reaction", &events.Message{Message: &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Text: proto.String("👍")}}}, kindOther, "", ""},
		{"a sticker", &events.Message{Message: &waE2E.Message{StickerMessage: &waE2E.StickerMessage{}}}, kindOther, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, target, content := classify(c.evt)
			if kind != c.kind || target != c.target || extractText(content) != c.editText {
				t.Errorf("classify = %v, %q, %q; want %v, %q, %q", kind, target, extractText(content), c.kind, c.target, c.editText)
			}
		})
	}
}

// A location or contact card is read as a line of text: without one, a
// DM with nothing but a location got neither an answer nor an error.
func TestExtractTextOfLocationsAndContacts(t *testing.T) {
	vcard := "BEGIN:VCARD\nVERSION:3.0\nFN:Alex\nTEL;type=CELL;waid=15551234567:+1 555 123 4567\nEND:VCARD"
	cases := []struct {
		name string
		msg  *waE2E.Message
		want string
	}{
		{"location", &waE2E.Message{LocationMessage: &waE2E.LocationMessage{
			DegreesLatitude: proto.Float64(47.3769), DegreesLongitude: proto.Float64(8.5417),
			Name: proto.String("Main\nStation"), Address: proto.String("Station Square 1"),
		}}, "[location: 47.376900, 8.541700 — Main Station — Station Square 1]"},
		{"live location", &waE2E.Message{LiveLocationMessage: &waE2E.LiveLocationMessage{
			DegreesLatitude: proto.Float64(-33.8688), DegreesLongitude: proto.Float64(151.2093),
		}}, "[location: -33.868800, 151.209300]"},
		{"contact", &waE2E.Message{ContactMessage: &waE2E.ContactMessage{DisplayName: proto.String("Alex"), Vcard: proto.String(vcard)}},
			"[contact: Alex, +1 555 123 4567]"},
		{"contacts", &waE2E.Message{ContactsArrayMessage: &waE2E.ContactsArrayMessage{Contacts: []*waE2E.ContactMessage{
			{DisplayName: proto.String("Alex"), Vcard: proto.String(vcard)}, {DisplayName: proto.String("Sam")},
		}}}, "[contact: Alex, +1 555 123 4567]\n[contact: Sam]"},
		{"an empty card", &waE2E.Message{ContactMessage: &waE2E.ContactMessage{}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractText(c.msg); got != c.want {
				t.Errorf("extractText = %q, want %q", got, c.want)
			}
		})
	}
}

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
