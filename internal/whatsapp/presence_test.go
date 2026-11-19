package whatsapp

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/testutil"
	"github.com/Icewater1337/fridolin/internal/trigger"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// fakePresencer records the presence, chat states and receipts the
// client sends, one line each.
type fakePresencer struct {
	mu   sync.Mutex
	sent []string
}

func (f *fakePresencer) add(line string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, line)
}

func (f *fakePresencer) SendPresence(_ context.Context, state types.Presence) error {
	f.add("presence " + string(state))
	return nil
}

func (f *fakePresencer) SendChatPresence(_ context.Context, jid types.JID, state types.ChatPresence, media types.ChatPresenceMedia) error {
	f.add(fmt.Sprintf("chat %s %s%s", jid, state, media))
	return nil
}

func (f *fakePresencer) MarkRead(_ context.Context, ids []types.MessageID, _ time.Time, chat, sender types.JID, kind ...types.ReceiptType) error {
	f.add(fmt.Sprintf("%s %s from %s: %v", kind[0], chat, sender, ids))
	return nil
}

func (f *fakePresencer) lines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

// newPresenceTestWhatsApp is newTestWhatsApp with whatsapp.presence on
// and a fake for what goes to WhatsApp.
func newPresenceTestWhatsApp(t *testing.T) (*testWhatsApp, *fakePresencer) {
	t.Helper()
	tw := newTestWhatsApp(t)
	tw.cfg().Presence = true
	f := &fakePresencer{}
	tw.presence = f
	return tw, f
}

// Connecting shows the bridge online, which WhatsApp wants before a chat
// shows it typing, unless whatsapp.presence is off.
func TestConnectingShowsTheBridgeOnline(t *testing.T) {
	for _, on := range []bool{true, false} {
		t.Run(fmt.Sprint("presence ", on), func(t *testing.T) {
			tw, f := newPresenceTestWhatsApp(t)
			tw.cfg().Presence = on
			tw.client = whatsmeow.NewClient(&store.Device{}, waLog.Noop) // the account Connected reads
			tw.connectionEvent(&events.Connected{})
			if on {
				testutil.WaitFor(t, 2*time.Second, "the presence", func() bool { return len(f.lines()) > 0 })
				if got := f.lines(); !slices.Equal(got, []string{"presence available"}) || !tw.shownOnline.Load() {
					t.Errorf("sent %q, shown online %v", got, tw.shownOnline.Load())
				}
				return
			}
			time.Sleep(50 * time.Millisecond)
			if got := f.lines(); len(got) != 0 {
				t.Errorf("sent %q with presence off", got)
			}
		})
	}
}

// Typing goes to a person's or a group's chat, and shows the bridge
// online first when it is not yet; a broadcast list, a Status or a
// channel gets none.
func TestTyping(t *testing.T) {
	tw, f := newPresenceTestWhatsApp(t)
	ctx := context.Background()
	lidChat := types.NewJID(personLID, types.HiddenUserServer).String()
	for _, step := range []struct {
		jid string
		on  bool
	}{{testDMChat, true}, {testDMChat, false}, {testGroup, true}, {lidChat, true}} {
		if err := tw.Typing(ctx, step.jid, step.on); err != nil {
			t.Fatalf("typing in %s: %v", step.jid, err)
		}
	}
	want := []string{
		"presence available",
		"chat " + testDMChat + " composing",
		"chat " + testDMChat + " paused",
		"chat " + testGroup + " composing",
		"chat " + lidChat + " composing",
	}
	if got := f.lines(); !slices.Equal(got, want) {
		t.Errorf("sent %q, want %q", got, want)
	}
	for _, jid := range []string{"status@broadcast", "1700000000@broadcast", "120363000000000000@newsletter", "not a jid@"} {
		if err := tw.Typing(ctx, jid, true); err == nil {
			t.Errorf("typing in %s: no error", jid)
		}
	}
	if got := f.lines(); len(got) != len(want) {
		t.Errorf("sent %q after the refused chats", got[len(want):])
	}
}

// The messages handed to the brains are marked read as WhatsApp
// addressed them: a DM by the LID it came from, a group's by its sender,
// one receipt per sender. Played receipts go only for voice notes, and
// none for a message the client did not hand on, or one of another chat.
func TestReceiptsForHandledMessages(t *testing.T) {
	tw, f := newPresenceTestWhatsApp(t)
	cfg := tw.cfg()
	cfg.AllowedGroups = []string{testGroup}
	cfg.Triggers = trigger.FromStrings([]string{"@claude"})
	tw.setOwn(bridgeAccount, types.EmptyJID)
	ctx := context.Background()

	// A DM by LID, with the number WhatsApp sent along: the bridge has it
	// in the number's chat.
	lid := types.NewJID(personLID, types.HiddenUserServer)
	pn := types.NewJID("15551234567", types.DefaultUserServer)
	dm := textEvent(types.MessageSource{Chat: lid, Sender: lid, SenderAlt: pn}, "hello")
	dm.Info.ID = "DM1"
	voice := textEvent(types.MessageSource{Chat: lid, Sender: lid, SenderAlt: pn}, "")
	voice.Info.ID = "DM2"
	voice.Message = &waE2E.Message{AudioMessage: &waE2E.AudioMessage{PTT: proto.Bool(true), Mimetype: proto.String("audio/ogg; codecs=opus")}}
	tw.media = &fakeDownloader{body: "OGG"}
	for _, evt := range []*events.Message{dm, voice} {
		tw.handle(t, evt)
		if m, ok := tw.next(time.Second); !ok || m.JID != testDMChat {
			t.Fatalf("the DM reached the brains as %+v %v", m, ok)
		}
	}

	// Two members of the group, one by LID.
	member := types.NewJID("15551234567", types.DefaultUserServer)
	otherLID := types.NewJID("434343", types.HiddenUserServer)
	cfg.GroupMembers = map[string][]string{testGroup: {anyMember}}
	for i, sender := range []types.JID{member, otherLID, member} {
		evt := groupMessage(sender, types.EmptyJID, groupTrigger())
		evt.Info.ID = types.MessageID(fmt.Sprintf("G%d", i+1))
		tw.handle(t, evt)
		if _, ok := tw.next(time.Second); !ok {
			t.Fatalf("group message %d did not reach the brains", i+1)
		}
	}
	// The bridge's own account writing in the group gets no receipt.
	own := groupMessage(member, types.EmptyJID, groupTrigger())
	own.Info.ID, own.Info.IsFromMe = "G4", true
	tw.handle(t, own)
	tw.next(time.Second)

	for _, call := range []struct {
		played bool
		jid    string
		ids    []string
	}{
		{false, testDMChat, []string{"DM1", "DM2", "UNKNOWN"}},
		{true, testDMChat, []string{"DM1", "DM2"}},
		{false, testGroup, []string{"G1", "G2", "G3", "G4"}},
		{true, testGroup, []string{"G1"}},
		{false, testGroup, []string{"DM1"}}, // another chat's
	} {
		mark := tw.MarkRead
		if call.played {
			mark = tw.MarkPlayed
		}
		if err := mark(ctx, call.jid, call.ids); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		"read " + lid.String() + " from : [DM1 DM2]",
		"played " + lid.String() + " from : [DM2]",
		"read " + testGroup + " from " + member.String() + ": [G1 G3]",
		"read " + testGroup + " from " + otherLID.String() + ": [G2]",
	}
	if got := f.lines(); !slices.Equal(got, want) {
		t.Errorf("sent\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The client remembers the last handledKept messages, the oldest going
// first, and a message handed on twice once.
func TestHandledMessagesAreBounded(t *testing.T) {
	var h handledMessages
	for i := range handledKept + 2 {
		h.add("chat", types.MessageID(fmt.Sprint(i)), handledMessage{chat: types.NewJID("1", types.DefaultUserServer)})
	}
	h.add("chat", types.MessageID(fmt.Sprint(handledKept+1)), handledMessage{chat: types.NewJID("1", types.DefaultUserServer)})
	if len(h.byKey) != handledKept {
		t.Errorf("kept %d, want %d", len(h.byKey), handledKept)
	}
	got := h.receipts("chat", []string{"0", "1", "2", fmt.Sprint(handledKept + 1)}, false)
	if len(got) != 1 || !slices.Equal(got[0].ids, []types.MessageID{"2", types.MessageID(fmt.Sprint(handledKept + 1))}) {
		t.Errorf("receipts = %+v, want the two oldest gone", got)
	}
	h.add("chat", "", handledMessage{})
	if _, ok := h.byKey[handledKey("chat", "")]; ok {
		t.Error("kept a message without an id")
	}
}
