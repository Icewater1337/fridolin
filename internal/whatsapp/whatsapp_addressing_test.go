package whatsapp

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"

	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/Icewater1337/fridolin/internal/trigger"
)

func TestAddressGroup(t *testing.T) {
	ownPN := types.NewJID(otherNumber, types.DefaultUserServer)
	ownLID := types.NewJID(bridgeLID, types.HiddenUserServer)
	own := []types.JID{ownPN, ownLID}
	ownDevice := ownPN
	ownDevice.Device = 12
	member := types.NewJID("15551234567", types.DefaultUserServer).String()
	triggers := trigger.FromStrings([]string{"@claude"})

	text := func(s string, ci *waE2E.ContextInfo) *waE2E.Message {
		return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(s), ContextInfo: ci}}
	}
	quote := func(participant string) *waE2E.ContextInfo {
		return &waE2E.ContextInfo{StanzaID: proto.String("3EB0QUOTED"), Participant: proto.String(participant),
			QuotedMessage: &waE2E.Message{Conversation: proto.String("an earlier message")}}
	}
	image := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("look")}}
	none := testGroupRules{}

	cases := []struct {
		name  string
		msg   *waE2E.Message
		rules testGroupRules
		want  groupAddressing
	}{
		{"plain chatter", &waE2E.Message{Conversation: proto.String("see you at 8")}, defaultTestGroupRules, groupAddressing{}},
		{"the trigger", text("@claude what is on today?", nil), defaultTestGroupRules, groupAddressing{tagged: true}},
		{"the trigger in an image's caption", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("@claude what is this?")}},
			none, groupAddressing{tagged: true, byCaption: true, file: true}},
		{"a video's caption is no trigger", &waE2E.Message{VideoMessage: &waE2E.VideoMessage{Caption: proto.String("@claude")}},
			none, groupAddressing{file: true}},
		{"a GIF is no file", &waE2E.Message{VideoMessage: &waE2E.VideoMessage{GifPlayback: proto.Bool(true)}}, defaultTestGroupRules, groupAddressing{}},
		{"a mention by number", text("@"+otherNumber+" hi", &waE2E.ContextInfo{MentionedJID: []string{ownPN.String()}}),
			defaultTestGroupRules, groupAddressing{tagged: true, mentioned: true}},
		{"a mention by LID", text("@"+bridgeLID+" hi", &waE2E.ContextInfo{MentionedJID: []string{ownLID.String()}}),
			defaultTestGroupRules, groupAddressing{tagged: true, mentioned: true}},
		{"a mention on a photo", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{ContextInfo: &waE2E.ContextInfo{MentionedJID: []string{ownPN.String()}}}},
			none, groupAddressing{tagged: true, mentioned: true, file: true}},
		{"someone else mentioned", text("@15551234567 hi", &waE2E.ContextInfo{MentionedJID: []string{member}}), defaultTestGroupRules, groupAddressing{}},
		{"a reply to the bridge", text("and on Sunday?", quote(ownDevice.String())), none, groupAddressing{tagged: true}},
		{"a reply to the bridge's LID", text("and on Sunday?", quote(ownLID.String())), none, groupAddressing{tagged: true}},
		{"a photo replying to the bridge", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{ContextInfo: quote(ownPN.String())}},
			none, groupAddressing{tagged: true, file: true}},
		{"a reply to someone else", text("and on Sunday?", quote(member)), defaultTestGroupRules, groupAddressing{}},
		{"a forward", text("read this", &waE2E.ContextInfo{IsForwarded: proto.Bool(true)}), defaultTestGroupRules, groupAddressing{tagged: true}},
		{"a forward by score", text("read this", &waE2E.ContextInfo{ForwardingScore: proto.Uint32(3)}), defaultTestGroupRules, groupAddressing{tagged: true}},
		{"a forward, forwards off", text("read this", &waE2E.ContextInfo{IsForwarded: proto.Bool(true)}), testGroupRules{files: true}, groupAddressing{}},
		{"a photo", image, defaultTestGroupRules, groupAddressing{tagged: true, file: true}},
		{"a photo, files off", image, testGroupRules{forwards: true}, groupAddressing{file: true}},
		{"a voice note, files off", &waE2E.Message{AudioMessage: &waE2E.AudioMessage{}}, none, groupAddressing{file: true}},
		{"a document", &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("scan.pdf")}}, defaultTestGroupRules, groupAddressing{tagged: true, file: true}},
		{"a sticker", &waE2E.Message{StickerMessage: &waE2E.StickerMessage{}}, defaultTestGroupRules, groupAddressing{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := addressGroup(c.msg, extractText(c.msg), triggers, own, c.rules); got != c.want {
				t.Errorf("addressGroup = %+v, want %+v", got, c.want)
			}
		})
	}
}
