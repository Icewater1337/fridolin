package main

import (
	"os"

	"testing"

	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"google.golang.org/protobuf/proto"
)

func mustJID(t *testing.T, s string) types.JID {
	t.Helper()
	jid, err := types.ParseJID(s)
	if err != nil {
		t.Fatalf("parsing %q: %v", s, err)
	}
	return jid
}

func textEvent(src types.MessageSource, text string) *events.Message {
	return &events.Message{
		Info:    types.MessageInfo{MessageSource: src, ID: "3EB0TEST", PushName: "Alex", Timestamp: time.Now()},
		Message: &waE2E.Message{Conversation: proto.String(text)},
	}
}

// dirNames lists dir, hidden temp files included.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// bridgeAccount is the bridge's own WhatsApp account in the group tests.
var bridgeAccount = types.NewJID("5550100", types.DefaultUserServer)

// dmEventFrom is dmEvent from number.
func dmEventFrom(number, id string, msg *waE2E.Message) *events.Message {
	evt := dmEvent(id, msg)
	jid := types.NewJID(number, types.DefaultUserServer)
	evt.Info.Chat, evt.Info.Sender = jid, jid
	return evt
}

// photoMsg is a photo with caption.
func photoMsg(caption string) *waE2E.Message {
	return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: proto.String("image/jpeg"), Caption: proto.String(caption)}}
}
