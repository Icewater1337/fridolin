package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
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

// TestIncomingChat pins which chats reach a brain. Status updates and
// channels never may: a reply there is published to every contact.
func TestIncomingChat(t *testing.T) {
	contact := mustJID(t, "15551234567@s.whatsapp.net")
	lid := mustJID(t, "15559876543@lid")
	group := mustJID(t, "120363000000000000@g.us")
	channel := mustJID(t, "120363000000000000@newsletter")

	cases := []struct {
		name string
		src  types.MessageSource
		want string // "" means dropped
	}{
		{"status update from a contact", types.MessageSource{Chat: types.StatusBroadcastJID, Sender: contact, IsGroup: true}, ""},
		{"own status update", types.MessageSource{Chat: types.StatusBroadcastJID, Sender: contact, IsGroup: true, IsFromMe: true}, ""},
		{"status update with a device suffix", types.MessageSource{Chat: types.JID{User: "status", Server: types.BroadcastServer, Device: 3}, Sender: contact}, ""},
		{"channel post", types.MessageSource{Chat: channel, Sender: channel}, ""},
		{"direct message", types.MessageSource{Chat: contact, Sender: contact}, "15551234567@s.whatsapp.net"},
		{"direct message by LID", types.MessageSource{Chat: lid, Sender: lid}, "15559876543@lid"},
		{"group message", types.MessageSource{Chat: group, Sender: contact, IsGroup: true}, "120363000000000000@g.us"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chat, ok := incomingChat(c.src)
			switch {
			case c.want == "" && ok:
				t.Errorf("incomingChat kept the message in %s, want it dropped", chat)
			case c.want != "" && !ok:
				t.Errorf("incomingChat dropped the message, want it in %s", c.want)
			case c.want != "" && chat.String() != c.want:
				t.Errorf("chat = %s, want %s", chat, c.want)
			}
		})
	}
}

// testWhatsApp is a WhatsAppClient without a whatsmeow connection. Anything
// that would reach WhatsApp (a download, a group lookup) fails or panics on
// the nil client and shows up in the log, so a test can tell that a message
// was dropped before that work.
type testWhatsApp struct {
	*WhatsAppClient
	inbound chan Inbound

	mu      sync.Mutex
	ignored []string // chat JIDs offered on the dashboard
	logs    []string // "LEVEL message"
}

// testWaLogger records what the client logs through whatsmeow's logger.
type testWaLogger struct{ tw *testWhatsApp }

func (l testWaLogger) add(level, msg string, args ...interface{}) {
	l.tw.mu.Lock()
	defer l.tw.mu.Unlock()
	l.tw.logs = append(l.tw.logs, level+" "+fmt.Sprintf(msg, args...))
}
func (l testWaLogger) Debugf(msg string, args ...interface{}) { l.add("DEBUG", msg, args...) }
func (l testWaLogger) Infof(msg string, args ...interface{})  { l.add("INFO", msg, args...) }
func (l testWaLogger) Warnf(msg string, args ...interface{})  { l.add("WARN", msg, args...) }
func (l testWaLogger) Errorf(msg string, args ...interface{}) { l.add("ERROR", msg, args...) }
func (l testWaLogger) Sub(string) waLog.Logger                { return l }

func newTestWhatsApp(t *testing.T) *testWhatsApp {
	t.Helper()
	cfg := &Config{AllowedNumbers: []string{"15551234567"}, MediaDir: t.TempDir(), MaxChunkSize: 4000}
	tw := &testWhatsApp{inbound: make(chan Inbound, 4)}
	tw.WhatsAppClient = &WhatsAppClient{
		cfg:     func() *Config { return cfg },
		log:     testWaLogger{tw},
		handler: func(_ context.Context, m Inbound) { tw.inbound <- m },
		onIgnored: func(_, jid, _, _, _ string) {
			tw.mu.Lock()
			defer tw.mu.Unlock()
			tw.ignored = append(tw.ignored, jid)
		},
	}
	return tw
}

// handle feeds one event to handleMessage, reporting a panic (the code
// reached for the missing WhatsApp connection) as a test failure.
func (tw *testWhatsApp) handle(t *testing.T, evt *events.Message) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("handleMessage reached for WhatsApp: %v", r)
		}
	}()
	tw.handleMessage(evt)
}

// next returns the Inbound handed to the brains, or false when none arrives
// within wait. The handler runs on its own goroutine.
func (tw *testWhatsApp) next(wait time.Duration) (Inbound, bool) {
	select {
	case m := <-tw.inbound:
		return m, true
	case <-time.After(wait):
		return Inbound{}, false
	}
}

func textEvent(src types.MessageSource, text string) *events.Message {
	return &events.Message{
		Info:    types.MessageInfo{MessageSource: src, ID: "3EB0TEST", PushName: "Alex", Timestamp: time.Now()},
		Message: &waE2E.Message{Conversation: proto.String(text)},
	}
}

func TestHandleMessageIgnoresStatusAndChannels(t *testing.T) {
	contact := mustJID(t, "15551234567@s.whatsapp.net")
	stranger := mustJID(t, "15559876543@s.whatsapp.net")
	channel := mustJID(t, "120363000000000000@newsletter")

	photo := textEvent(types.MessageSource{Chat: types.StatusBroadcastJID, Sender: contact, IsGroup: true}, "")
	photo.Message = &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("Beach day!")}}

	cases := []struct {
		name string
		evt  *events.Message
	}{
		{"text status from an allowed contact", textEvent(types.MessageSource{Chat: types.StatusBroadcastJID, Sender: contact, IsGroup: true}, "Beach day!")},
		{"photo status from an allowed contact", photo},
		{"status from someone not allowed", textEvent(types.MessageSource{Chat: types.StatusBroadcastJID, Sender: stranger, IsGroup: true}, "Beach day!")},
		{"channel post", textEvent(types.MessageSource{Chat: channel, Sender: channel}, "Weekly news")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tw := newTestWhatsApp(t)
			tw.handle(t, c.evt)
			if m, ok := tw.next(100 * time.Millisecond); ok {
				t.Errorf("message reached the brains: %+v", m)
			}
			tw.mu.Lock()
			defer tw.mu.Unlock()
			if len(tw.ignored) > 0 {
				t.Errorf("chat offered on the dashboard: %v", tw.ignored)
			}
			// One debug line, nothing louder: no download was attempted.
			if len(tw.logs) != 1 || !strings.HasPrefix(tw.logs[0], "DEBUG ") {
				t.Errorf("logged %q, want a single debug line", tw.logs)
			}
		})
	}

	// The same contact's direct message still gets through.
	tw := newTestWhatsApp(t)
	tw.handle(t, textEvent(types.MessageSource{Chat: contact, Sender: contact}, "hello"))
	m, ok := tw.next(2 * time.Second)
	if !ok || m.JID != "15551234567@s.whatsapp.net" || m.Text != "hello" {
		t.Errorf("direct message: got %+v (delivered %v)", m, ok)
	}
}
