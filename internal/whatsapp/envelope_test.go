package whatsapp

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
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
