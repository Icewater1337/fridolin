package main

import (
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/Icewater1337/fridolin/internal/chat"
)

// A LID's digits are not a phone number, even when they happen to equal
// allowed_numbers. Only its resolved phone identity can match that list.
// An empty list with allow_any_dm still deliberately admits everyone.
func TestDMAccessPreservesSenderIdentity(t *testing.T) {
	const member = "15551234567"
	pn := types.NewJID(member, types.DefaultUserServer)
	collision := types.NewJID(member, types.HiddenUserServer)
	knownLID := types.NewJID(personLID, types.HiddenUserServer)
	unknown := types.NewJID("272727", types.HiddenUserServer)
	other := types.NewJID(otherNumber, types.DefaultUserServer)
	cases := []struct {
		name    string
		sender  types.JID
		alt     types.JID
		lids    store.LIDStore
		numbers []string
		anyDM   bool
		want    types.JID // empty means refused
	}{
		{name: "listed phone", sender: pn, numbers: []string{member}, want: pn},
		{name: "formatted listed phone", sender: pn, numbers: []string{"+1 555 123-4567"}, want: pn},
		{name: "unlisted phone", sender: other, numbers: []string{member}},
		{name: "open option does not widen a list", sender: other, numbers: []string{member}, anyDM: true},
		{name: "LID digits collide with listed phone", sender: collision, numbers: []string{member}},
		{name: "open option does not allow a LID collision", sender: collision, numbers: []string{member}, anyDM: true},
		{name: "LID with alternate listed phone", sender: knownLID, alt: pn, numbers: []string{member}, want: pn},
		{name: "LID with stored listed phone", sender: knownLID, lids: personLIDs{}, numbers: []string{member}, want: pn},
		{name: "alternate phone overrides colliding LID digits", sender: collision, alt: other, numbers: []string{member}},
		{name: "unknown LID", sender: unknown, lids: personLIDs{}, numbers: []string{member}},
		{name: "empty closed list", sender: pn},
		{name: "empty open list admits phone", sender: other, anyDM: true, want: other},
		{name: "empty open list admits unknown LID", sender: unknown, anyDM: true, want: unknown},
		{name: "empty open list admits colliding LID", sender: collision, anyDM: true, want: collision},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tw := newTestWhatsApp(t)
			cfg := tw.cfg()
			cfg.AllowedNumbers, cfg.AllowAnyDM = c.numbers, &c.anyDM
			reads := 0
			tw.cfg = func() *Config { reads++; return cfg }
			if c.lids != nil {
				tw.client = whatsmeow.NewClient(&store.Device{LIDs: c.lids}, waLog.Noop)
			}
			tw.handle(t, textEvent(types.MessageSource{Chat: c.sender, Sender: c.sender, SenderAlt: c.alt}, "hello"))
			select {
			case m := <-tw.inbound:
				if c.want.IsEmpty() {
					t.Errorf("sender reached the brain as %s, want refusal", m.JID)
				} else if m.JID != c.want.String() || m.Sender != c.want.User || m.Text != "hello" {
					t.Errorf("message = %+v, want canonical sender %s", m, c.want)
				}
			default:
				if !c.want.IsEmpty() {
					t.Errorf("sender %s was refused, want %s", c.sender, c.want)
				}
			}
			if reads != 1 {
				t.Errorf("read %d config snapshots, want one for the access decision", reads)
			}
			tw.mu.Lock()
			ignored := len(tw.ignored)
			tw.mu.Unlock()
			wantIgnored := 0
			if c.want.IsEmpty() {
				wantIgnored = 1
			}
			if ignored != wantIgnored {
				t.Errorf("ignored callbacks = %d, want %d", ignored, wantIgnored)
			}
		})
	}
}

func TestUnresolvedDMHasNoSideEffects(t *testing.T) {
	for name, msg := range map[string]*waE2E.Message{
		"text":   textMsg("hello"),
		"file":   photoMsg("look"),
		"edit":   editMsg("earlier", "replacement"),
		"revoke": revokeMsg("earlier"),
	} {
		t.Run(name, func(t *testing.T) {
			tw := newTestWhatsApp(t)
			dl := &fakeDownloader{body: "JPEG"}
			tw.media = dl
			changes := 0
			tw.onChange = func(chat.Change) { changes++ }
			lid := types.NewJID("15551234567", types.HiddenUserServer)
			evt := textEvent(types.MessageSource{Chat: lid, Sender: lid}, "")
			evt.Message = msg
			tw.handle(t, evt)
			if len(tw.inbound) != 0 || changes != 0 || dl.callCount() != 0 {
				t.Errorf("unresolved sender was read: inbound=%d, changes=%d, downloads=%d", len(tw.inbound), changes, dl.callCount())
			}
			tw.mu.Lock()
			defer tw.mu.Unlock()
			if len(tw.ignored) != 1 || tw.ignored[0] != lid.String() {
				t.Errorf("ignored = %v, want the unresolved sender offered once", tw.ignored)
			}
		})
	}
}
