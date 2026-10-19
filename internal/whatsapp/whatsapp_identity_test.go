package whatsapp

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// A DM is one chat under the sender's number, whether WhatsApp addressed it
// by number or by LID, and it remembers the LID, under which older history
// may be.
func TestDMIsKeyedByNumber(t *testing.T) {
	pn := types.NewJID("15551234567", types.DefaultUserServer)
	lid := types.NewJID(personLID, types.HiddenUserServer)
	cases := []struct {
		name string
		src  types.MessageSource
		lids store.LIDStore
	}{
		{"by LID, the number along", types.MessageSource{Chat: lid, Sender: lid, SenderAlt: pn}, nil},
		{"by LID, the number in the store", types.MessageSource{Chat: lid, Sender: lid}, personLIDs{}},
		{"by number, the LID in the store", types.MessageSource{Chat: pn, Sender: pn}, personLIDs{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tw := newTestWhatsApp(t)
			if c.lids != nil {
				tw.client = whatsmeow.NewClient(&store.Device{LIDs: c.lids}, waLog.Noop)
			}
			tw.handle(t, textEvent(c.src, "hello"))
			m, ok := tw.next(2 * time.Second)
			if !ok || m.JID != pn.String() || m.ChatAlt != lid.String() || m.Sender != "15551234567" {
				t.Errorf("got %+v (delivered %v), want chat %s, alt %s, sender 15551234567", m, ok, pn, lid)
			}
		})
	}
}

// A DM from a LID nothing maps to a number is checked and offered on the
// dashboard as that LID, as before group senders were resolved by the same
// helper.
func TestDMFromAnUnknownLID(t *testing.T) {
	unknown := types.NewJID("272727", types.HiddenUserServer)
	tw := newTestWhatsApp(t)
	tw.client = whatsmeow.NewClient(&store.Device{LIDs: personLIDs{}}, waLog.Noop)
	tw.handle(t, textEvent(types.MessageSource{Chat: unknown, Sender: unknown}, "hello"))
	if m, ok := tw.next(100 * time.Millisecond); ok {
		t.Errorf("reached the brains: %+v", m)
	}
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if len(tw.ignored) != 1 || tw.ignored[0] != unknown.String() {
		t.Errorf("dashboard was offered %v, want the LID's chat", tw.ignored)
	}
}
