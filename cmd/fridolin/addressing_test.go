package main

import (
	"github.com/Icewater1337/fridolin/internal/whatsapp"
	"slices"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/Icewater1337/fridolin/internal/history"
)

// Quote-replying to one of the bridge's messages in a group addresses it:
// the most natural way to follow up gets an answer, not a silent run.
func TestGroupReplyToTheBridgeIsAnswered(t *testing.T) {
	tb, _ := newMainBridge(t, "")
	own := types.JID{User: otherNumber, Device: 12, Server: types.DefaultUserServer}
	wa := tb.whatsAppOffline(t, &store.Device{ID: &own, LIDs: bridgeLIDs{}})
	wa.ProcessMessage(groupEvent(replyMessage("and on Sunday?", "The pool opens at 9 on Saturday.")))
	waitFor(t, 10*time.Second, "the answer", func() bool { return len(tb.sentTo(testGroup)) > 0 })
}

// A brain can make files and forwards in its groups chatter: a photo is
// then not fetched, its caption is read without an answer, and a photo
// without one runs nothing.
func TestGroupFilesAndForwardsCanBeChatter(t *testing.T) {
	tb := newTestBridge(t, `, "otp_guard": true`)
	off := false
	tb.addFolderBrain(t, manifest.Manifest{ID: "family", Name: "Family", Routing: manifest.Routing{
		Groups: []string{testGroup}, AnswerFiles: &off, AnswerForwards: &off,
	}})
	br := tb.brainMgr.Get("family")
	own := types.JID{User: otherNumber, Device: 12, Server: types.DefaultUserServer}
	dl := &fakeDownloader{body: "jpeg"}
	wa := tb.whatsAppOffline(t, &store.Device{ID: &own, LIDs: bridgeLIDs{}}, func(opts *whatsapp.Options) { opts.Downloader = dl })

	wa.ProcessMessage(groupEvent(&waE2E.Message{ImageMessage: &waE2E.ImageMessage{}}))
	wa.ProcessMessage(groupEvent(&waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("the view from the hut")}}))
	wa.ProcessMessage(groupEvent(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: proto.String("a chain letter"), ContextInfo: &waE2E.ContextInfo{IsForwarded: proto.Bool(true)},
	}}))
	// Each arrives on a goroutine of its own, in any order.
	inbound := func() []string {
		var texts []string
		for _, e := range readHistory(t, br) {
			if e.Type == history.In {
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
	wa.ProcessMessage(groupEvent(&waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("@claude what is this?")}}))
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
