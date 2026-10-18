package main

import (
	"slices"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/trigger"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
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
	none := groupRules{}

	cases := []struct {
		name  string
		msg   *waE2E.Message
		rules groupRules
		want  groupAddressing
	}{
		{"plain chatter", &waE2E.Message{Conversation: proto.String("see you at 8")}, defaultGroupRules, groupAddressing{}},
		{"the trigger", text("@claude what is on today?", nil), defaultGroupRules, groupAddressing{tagged: true}},
		{"the trigger in an image's caption", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("@claude what is this?")}},
			none, groupAddressing{tagged: true, byCaption: true, file: true}},
		{"a video's caption is no trigger", &waE2E.Message{VideoMessage: &waE2E.VideoMessage{Caption: proto.String("@claude")}},
			none, groupAddressing{file: true}},
		{"a GIF is no file", &waE2E.Message{VideoMessage: &waE2E.VideoMessage{GifPlayback: proto.Bool(true)}}, defaultGroupRules, groupAddressing{}},
		{"a mention by number", text("@"+otherNumber+" hi", &waE2E.ContextInfo{MentionedJID: []string{ownPN.String()}}),
			defaultGroupRules, groupAddressing{tagged: true, mentioned: true}},
		{"a mention by LID", text("@"+bridgeLID+" hi", &waE2E.ContextInfo{MentionedJID: []string{ownLID.String()}}),
			defaultGroupRules, groupAddressing{tagged: true, mentioned: true}},
		{"a mention on a photo", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{ContextInfo: &waE2E.ContextInfo{MentionedJID: []string{ownPN.String()}}}},
			none, groupAddressing{tagged: true, mentioned: true, file: true}},
		{"someone else mentioned", text("@15551234567 hi", &waE2E.ContextInfo{MentionedJID: []string{member}}), defaultGroupRules, groupAddressing{}},
		{"a reply to the bridge", text("and on Sunday?", quote(ownDevice.String())), none, groupAddressing{tagged: true}},
		{"a reply to the bridge's LID", text("and on Sunday?", quote(ownLID.String())), none, groupAddressing{tagged: true}},
		{"a photo replying to the bridge", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{ContextInfo: quote(ownPN.String())}},
			none, groupAddressing{tagged: true, file: true}},
		{"a reply to someone else", text("and on Sunday?", quote(member)), defaultGroupRules, groupAddressing{}},
		{"a forward", text("read this", &waE2E.ContextInfo{IsForwarded: proto.Bool(true)}), defaultGroupRules, groupAddressing{tagged: true}},
		{"a forward by score", text("read this", &waE2E.ContextInfo{ForwardingScore: proto.Uint32(3)}), defaultGroupRules, groupAddressing{tagged: true}},
		{"a forward, forwards off", text("read this", &waE2E.ContextInfo{IsForwarded: proto.Bool(true)}), groupRules{files: true}, groupAddressing{}},
		{"a photo", image, defaultGroupRules, groupAddressing{tagged: true, file: true}},
		{"a photo, files off", image, groupRules{forwards: true}, groupAddressing{file: true}},
		{"a voice note, files off", &waE2E.Message{AudioMessage: &waE2E.AudioMessage{}}, none, groupAddressing{file: true}},
		{"a document", &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("scan.pdf")}}, defaultGroupRules, groupAddressing{tagged: true, file: true}},
		{"a sticker", &waE2E.Message{StickerMessage: &waE2E.StickerMessage{}}, defaultGroupRules, groupAddressing{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := addressGroup(c.msg, extractText(c.msg), triggers, own, c.rules); got != c.want {
				t.Errorf("addressGroup = %+v, want %+v", got, c.want)
			}
		})
	}
}

// Quote-replying to one of the bridge's messages in a group addresses it:
// the most natural way to follow up gets an answer, not a silent run.
func TestGroupReplyToTheBridgeIsAnswered(t *testing.T) {
	tb, _ := newMainBridge(t, "")
	own := types.JID{User: otherNumber, Device: 12, Server: types.DefaultUserServer}
	wa := tb.whatsAppOffline(&store.Device{ID: &own, LIDs: bridgeLIDs{}})
	wa.handleMessage(groupEvent(replyMessage("and on Sunday?", "The pool opens at 9 on Saturday.")))
	waitFor(t, 10*time.Second, "the answer", func() bool { return len(tb.sentTo(testGroup)) > 0 })
}

// A brain can make files and forwards in its groups chatter: a photo is
// then not fetched, its caption is read without an answer, and a photo
// without one runs nothing.
func TestGroupFilesAndForwardsCanBeChatter(t *testing.T) {
	tb := newTestBridge(t, `, "otp_guard": true`)
	off := false
	tb.addFolderBrain(t, BrainManifest{ID: "family", Name: "Family", Routing: BrainRouting{
		Groups: []string{testGroup}, AnswerFiles: &off, AnswerForwards: &off,
	}})
	br := tb.brainMgr.Get("family")
	own := types.JID{User: otherNumber, Device: 12, Server: types.DefaultUserServer}
	wa := tb.whatsAppOffline(&store.Device{ID: &own, LIDs: bridgeLIDs{}})
	dl := &fakeDownloader{body: "jpeg"}
	wa.media = dl

	wa.handleMessage(groupEvent(&waE2E.Message{ImageMessage: &waE2E.ImageMessage{}}))
	wa.handleMessage(groupEvent(&waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("the view from the hut")}}))
	wa.handleMessage(groupEvent(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: proto.String("a chain letter"), ContextInfo: &waE2E.ContextInfo{IsForwarded: proto.Bool(true)},
	}}))
	// Each arrives on a goroutine of its own, in any order.
	inbound := func() []string {
		var texts []string
		for _, e := range readHistory(t, br) {
			if e.Type == historyIn {
				texts = append(texts, e.Text)
			}
		}
		slices.Sort(texts)
		return texts
	}
	waitFor(t, 10*time.Second, "the chatter in the history", func() bool { return len(inbound()) == 2 })
	time.Sleep(300 * time.Millisecond)
	if got, want := inbound(), []string{"a chain letter", "the view from the hut"}; !slices.Equal(got, want) {
		t.Errorf("history holds %q, want %q: the caption and the forward, no attachment", got, want)
	}
	if n := dl.callCount(); n != 0 {
		t.Errorf("%d downloads, want none", n)
	}
	if sent := tb.sentTo(testGroup); len(sent) > 0 {
		t.Errorf("answered chatter: %q", sent)
	}

	// The trigger still addresses the bridge, a file along with it.
	wa.handleMessage(groupEvent(&waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("@claude what is this?")}}))
	waitFor(t, 10*time.Second, "the answer", func() bool { return len(tb.sentTo(testGroup)) > 0 })
	if n := dl.callCount(); n != 1 {
		t.Errorf("%d downloads, want the addressed photo's", n)
	}

	// Dev mode goes by the same rule.
	if w := tb.do("POST", "/api/dev/inbound", map[string]any{"jid": testGroup, "sender": "15551234567", "text": "", "media": []string{"/tmp/x.jpg"}}); w.Code != 202 {
		t.Fatalf("dev inbound: %d %s", w.Code, w.Body)
	}
	time.Sleep(300 * time.Millisecond)
	if sent := tb.sentTo(testGroup); len(sent) != 1 {
		t.Errorf("dev mode answered an untagged file: %q", sent)
	}
}
