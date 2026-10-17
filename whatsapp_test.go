package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

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
// channels never may: a reply there is published to every contact. A
// broadcast list's message belongs in the DM with its sender.
func TestIncomingChat(t *testing.T) {
	contact := mustJID(t, "15551234567@s.whatsapp.net")
	lid := mustJID(t, "15559876543@lid")
	group := mustJID(t, "120363000000000000@g.us")
	channel := mustJID(t, "120363000000000000@newsletter")
	list := mustJID(t, "1700000000@broadcast")

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
		{"broadcast list from a contact", types.MessageSource{Chat: list, Sender: contact, IsGroup: true}, "15551234567@s.whatsapp.net"},
		{"broadcast list from a contact's second device", types.MessageSource{Chat: list, Sender: mustJID(t, "15551234567:2@s.whatsapp.net"), IsGroup: true}, "15551234567@s.whatsapp.net"},
		{"broadcast list from a LID", types.MessageSource{Chat: list, Sender: lid, IsGroup: true}, "15559876543@lid"},
		{"own broadcast list message", types.MessageSource{Chat: list, Sender: contact, IsGroup: true, IsFromMe: true}, ""},
		{"broadcast list without a sender", types.MessageSource{Chat: list, IsGroup: true}, ""},
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
		{"own broadcast list message", textEvent(types.MessageSource{Chat: mustJID(t, "1700000000@broadcast"), Sender: contact, IsGroup: true, IsFromMe: true}, "Party on Friday")},
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

// A contact's broadcast-list message is a DM from them: it is answered in
// their direct chat, never on the list.
func TestHandleMessageBroadcastListIsDM(t *testing.T) {
	list := mustJID(t, "1700000000@broadcast")

	t.Run("allowed sender", func(t *testing.T) {
		tw := newTestWhatsApp(t)
		evt := textEvent(types.MessageSource{Chat: list, Sender: mustJID(t, "15551234567:2@s.whatsapp.net"), IsGroup: true}, "Party on Friday")
		tw.handle(t, evt)
		m, ok := tw.next(2 * time.Second)
		if !ok || m.JID != "15551234567@s.whatsapp.net" || m.Sender != "15551234567" || m.Text != "Party on Friday" {
			t.Errorf("got %+v (delivered %v), want a DM from 15551234567", m, ok)
		}
		if evt.Info.Chat != list {
			t.Errorf("whatsmeow's event was changed: chat is now %s", evt.Info.Chat)
		}
	})

	t.Run("sender not allowed", func(t *testing.T) {
		tw := newTestWhatsApp(t)
		tw.handle(t, textEvent(types.MessageSource{Chat: list, Sender: mustJID(t, "15559876543@s.whatsapp.net"), IsGroup: true}, "Party on Friday"))
		if m, ok := tw.next(100 * time.Millisecond); ok {
			t.Errorf("message reached the brains: %+v", m)
		}
		tw.mu.Lock()
		defer tw.mu.Unlock()
		if len(tw.ignored) != 1 || tw.ignored[0] != "15559876543@s.whatsapp.net" {
			t.Errorf("dashboard was offered %v, want the sender's DM chat", tw.ignored)
		}
	})
}

// Every way the messenger sends refuses Status, broadcast lists and
// channels before it reaches WhatsApp, whatever chat a brain or script
// names.
func TestSendRefusesStatusBroadcastAndChannels(t *testing.T) {
	tw := newTestWhatsApp(t)
	ctx := context.Background()
	sends := map[string]func(jid string) error{
		"SendMessage": func(jid string) error { return tw.SendMessage(ctx, jid, "hello") },
		"SendChunked": func(jid string) error { return tw.SendChunked(ctx, jid, strings.Repeat("hello ", 2000)) },
		"SendPoll": func(jid string) error {
			_, err := tw.SendPoll(ctx, jid, "Lunch?", []string{"Yes", "No"}, 1)
			return err
		},
		"SendReaction": func(jid string) error { return tw.SendReaction(ctx, jid, "3EB0TEST", "👍") },
	}
	for _, jid := range []string{"status@broadcast", "status:3@broadcast", "1700000000@broadcast", "broadcast", "120363000000000000@newsletter"} {
		for name, send := range sends {
			t.Run(name+" to "+jid, func(t *testing.T) {
				err := func() (err error) {
					defer func() {
						if r := recover(); r != nil {
							err = fmt.Errorf("reached for WhatsApp: %v", r)
						}
					}()
					return send(jid)
				}()
				if !errors.Is(err, errRefusedRecipient) {
					t.Errorf("err = %v, want a refusal", err)
				}
			})
		}
	}
}

func TestCheckRecipient(t *testing.T) {
	cases := []struct {
		jid    string
		refuse bool
	}{
		{"status@broadcast", true},
		{"1700000000@broadcast", true},
		{"120363000000000000@newsletter", true},
		{"15551234567@s.whatsapp.net", false},
		{"15559876543@lid", false},
		{"120363000000000000@g.us", false},
	}
	for _, c := range cases {
		t.Run(c.jid, func(t *testing.T) {
			err := checkRecipient(mustJID(t, c.jid))
			if got := errors.Is(err, errRefusedRecipient); got != c.refuse {
				t.Errorf("refused = %v (%v), want %v", got, err, c.refuse)
			}
		})
	}
}

// A WhatsApp @-mention is "@" and the number (or LID) in the text. The
// bridge's own mentions go, like a trigger keyword; anything else stays.
func TestStripMentions(t *testing.T) {
	own := []types.JID{
		types.NewJID("15559876543", types.DefaultUserServer),
		types.NewJID("424242", types.HiddenUserServer), // short, so it cannot be anyone's real LID
	}
	cases := []struct {
		name, text, want string
	}{
		{"by number, before a command", "@15559876543 !stop", "!stop"},
		{"by LID", "@424242 !stop", "!stop"},
		{"twice", "@15559876543 @424242 !help", "!help"},
		{"in the middle", "what does @15559876543 think?", "what does  think?"},
		{"someone else", "@15551234567 can you check?", "@15551234567 can you check?"},
		{"a longer number", "@155598765430 hi", "@155598765430 hi"},
		{"part of an address", "write to x@15559876543", "write to x@15559876543"},
		{"only the mention", " @15559876543 ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripMentions(c.text, own); got != c.want {
				t.Errorf("stripMentions(%q) = %q, want %q", c.text, got, c.want)
			}
		})
	}
	if got := stripMentions(" @15559876543 hi ", nil); got != "@15559876543 hi" {
		t.Errorf("without accounts: %q, want the text trimmed and kept", got)
	}
}

// Attachment names come from the sender. Whatever they send, the saved name
// stays in the media directory, is not hidden, keeps to one line in the
// prompt and the history, and is short enough to be saved at all.
func TestSanitizeMediaName(t *testing.T) {
	long := strings.Repeat("ä", 150) + ".pdf" // 304 bytes
	cases := []struct {
		name, in, want string
	}{
		{"plain", "scan.pdf", "scan.pdf"},
		{"directories", "../../etc/passwd", "passwd"},
		{"absolute", "/etc/passwd", "passwd"},
		{"hidden", ".bashrc", "bashrc"},
		{"dot dot", "..", "file"},
		{"empty", "", "file"},
		{"root", "/", "file"},
		{"newline", "a\nb.pdf", "ab.pdf"},
		{"carriage return and tab", "a\r\tb.pdf", "ab.pdf"},
		{"NUL", "a\x00b.pdf", "ab.pdf"},
		{"line separator", "a\u2028b\u2029c.pdf", "abc.pdf"},
		{"only control characters", "\n\n", "file"},
		{"unicode kept", "Rechnung März 🧾.pdf", "Rechnung März 🧾.pdf"},
		{"too long", long, strings.Repeat("ä", 56) + "….pdf"},
		{"too long, no real extension", strings.Repeat("x", 100) + "." + strings.Repeat("y", 100), strings.Repeat("x", 100) + "." + strings.Repeat("y", 16) + "…"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sanitizeMediaName(c.in)
			if got != c.want {
				t.Errorf("sanitizeMediaName(%q) = %q, want %q", c.in, got, c.want)
			}
			if len(got) > maxMediaNameBytes {
				t.Errorf("%d bytes, more than %d", len(got), maxMediaNameBytes)
			}
			if !utf8.ValidString(got) {
				t.Errorf("%q is not valid UTF-8", got)
			}
		})
	}
}

// safeMediaPath never hands out a name that is taken, even by a dangling
// symlink: writing there would follow the link out of the media directory.
func TestSafeMediaPathNeverReusesAName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "scan.pdf"), []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), filepath.Join(dir, "scan-2.pdf")); err != nil {
		t.Fatal(err)
	}
	if got, want := safeMediaPath(dir, "scan.pdf"), filepath.Join(dir, "scan-3.pdf"); got != want {
		t.Errorf("safeMediaPath = %q, want %q", got, want)
	}
	if got, want := safeMediaPath(dir, "a\nb.pdf"), filepath.Join(dir, "ab.pdf"); got != want {
		t.Errorf("safeMediaPath sanitises: got %q, want %q", got, want)
	}
}
