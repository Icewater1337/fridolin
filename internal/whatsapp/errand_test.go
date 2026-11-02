package whatsapp

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/Icewater1337/fridolin/internal/chat"
)

// TestErrandsTakeTheDMsOfTheirPeople: a DM from someone allowed_numbers
// does not let in goes to the errands when they claim the sender, as
// text, with a line for what it held instead of text and nothing
// downloaded; one they do not claim is ignored as ever, and an allowed
// number's DM goes to the brains whatever the errands say.
func TestErrandsTakeTheDMsOfTheirPeople(t *testing.T) {
	t.Parallel()
	tw := newTestWhatsApp(t)
	var mu sync.Mutex
	var took []chat.Inbound
	var asked []string
	tw.errands = func(sender types.JID) bool {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, sender.User)
		return sender.User == otherNumber || sender.User == "15551234567"
	}
	tw.onErrand = func(_ context.Context, m chat.Inbound) {
		mu.Lock()
		defer mu.Unlock()
		took = append(took, m)
	}

	tw.handle(t, dmEventFrom(otherNumber, "3EB0E1", textMsg("Tuesday works")))
	voice := &waE2E.Message{AudioMessage: &waE2E.AudioMessage{Mimetype: proto.String("audio/ogg; codecs=opus"), URL: proto.String("https://mmg.whatsapp.net/x")}}
	tw.handle(t, dmEventFrom(otherNumber, "3EB0E2", voice))
	photo := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: proto.String("image/jpeg"), Caption: proto.String("the venue"), URL: proto.String("https://mmg.whatsapp.net/y")}}
	tw.handle(t, dmEventFrom(otherNumber, "3EB0E3", photo))
	tw.handle(t, dmEventFrom("15550001111", "3EB0E4", textMsg("who is this?"))) // personal-data:ignore (a fictional 555 number)
	tw.handle(t, dmEventFrom("15551234567", "3EB0E5", textMsg("hi from the owner")))

	mu.Lock()
	defer mu.Unlock()
	var texts []string
	for _, m := range took {
		if m.JID != otherNumber+"@s.whatsapp.net" || m.Sender != otherNumber || m.Source != chat.SourceWhatsApp || len(m.Media) != 0 {
			t.Errorf("the errands got %+v", m)
		}
		texts = append(texts, m.Text)
	}
	want := []string{"Tuesday works", "[a voice message, which Fridolin cannot listen to here]", "the venue\n[image not shown: Fridolin reads only text here]"}
	if !slices.Equal(texts, want) {
		t.Errorf("the errands got %q, want %q", texts, want)
	}
	if slices.Contains(asked, "15551234567") {
		t.Error("the errands were asked about an allowed number")
	}
	if m, ok := tw.next(time.Second); !ok || m.Text != "hi from the owner" {
		t.Errorf("the allowed number's DM: %+v %v", m, ok)
	}
	if !slices.Contains(tw.ignored, "15550001111@s.whatsapp.net") { // personal-data:ignore (a fictional 555 number)
		t.Errorf("the stranger was not ignored: %v", tw.ignored)
	}
}
