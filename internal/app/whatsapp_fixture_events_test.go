package app

import (
	"os"

	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"

	"go.mau.fi/whatsmeow/types"

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

// photoMsg is a photo with caption.
func photoMsg(caption string) *waE2E.Message {
	return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: proto.String("image/jpeg"), Caption: proto.String(caption)}}
}
