package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/cbcutil"
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
// was dropped before that work. A test that wants a download to work sets
// media to a fakeDownloader.
type testWhatsApp struct {
	*WhatsAppClient
	inbound chan Inbound

	mu      sync.Mutex
	ignored []string // chat JIDs offered on the dashboard
	logs    []string // "LEVEL message"
	notices chan string
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
	tw := &testWhatsApp{inbound: make(chan Inbound, 4), notices: make(chan string, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tw.WhatsAppClient = &WhatsAppClient{
		cfg:     func() *Config { return cfg },
		log:     testWaLogger{tw},
		ctx:     ctx,
		cancel:  cancel,
		notify:  func(jid, text string) { tw.notices <- jid + ": " + text },
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

// FuzzSanitizeMediaName: whatever name the sender gives an attachment, the
// file is saved in the media directory, under a name that is not hidden,
// keeps to one line and is short enough to save; and so is every name
// placeMedia falls back to when that one is taken.
func FuzzSanitizeMediaName(f *testing.F) {
	for _, s := range []string{
		"scan.pdf", "../../etc/passwd", "/etc/passwd", ".bashrc", "..", "", "/", "a\nb.pdf", "a\r\tb.pdf",
		"a\x00b.pdf", "a\u2028b\u2029c.pdf", "\n\n", "Rechnung März 🧾.pdf", "x/.", "./..", ".../...",
		strings.Repeat("ä", 150) + ".pdf", strings.Repeat("x", 100) + "." + strings.Repeat("y", 100), "\xff\xfe.\xc3",
	} {
		f.Add(s)
	}
	const dir = "/media"
	f.Fuzz(func(t *testing.T, name string) {
		got := sanitizeMediaName(name)
		if len(got) > maxMediaNameBytes {
			t.Errorf("sanitizeMediaName(%q) = %q, %d bytes, more than %d", name, got, len(got), maxMediaNameBytes)
		}
		for _, i := range []int{1, 2, lastMediaCandidate - 1, lastMediaCandidate} {
			c := mediaNameCandidate(got, i)
			if filepath.Dir(filepath.Join(dir, c)) != dir || strings.HasPrefix(c, ".") {
				t.Errorf("%q, from %q, is not a visible file in the media directory", c, name)
			}
			if strings.IndexFunc(c, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' }) >= 0 {
				t.Errorf("%q, from %q, has a control character or a line break", c, name)
			}
			if !utf8.ValidString(c) {
				t.Errorf("%q, from %q, is not valid UTF-8", c, name)
			}
			if len(c) > 255 {
				t.Errorf("%q, from %q, is longer than a file name may be", c, name)
			}
		}
	})
}

// FuzzNumberAllowed: allowed_numbers lets a sender DM the bridge only when
// it lists the number exactly: no prefix, no suffix, no other number.
// allow_any_dm decides what an empty list means and never widens a list.
// A listed number gets in however the JID it came from was written.
func FuzzNumberAllowed(f *testing.F) {
	const n = "15551234567"
	f.Add(n, n, false)
	f.Add(n, "", true)
	f.Add(n, "", false)
	f.Add(n[:len(n)-1], n+",15559876543", true) // a listed number cut short
	f.Add(n+"8", n, true)                       // and one made longer
	f.Add("+"+n, n, false)
	f.Add("", ",", false)
	f.Fuzz(func(t *testing.T, number, list string, anyDM bool) {
		var allowed []string
		if list != "" {
			allowed = strings.Split(list, ",")
		}
		cfg := &Config{AllowedNumbers: allowed, AllowAnyDM: &anyDM}
		got := numberAllowed(cfg, number)
		if len(allowed) == 0 {
			if got != anyDM {
				t.Errorf("with no allowed_numbers and allow_any_dm %v, %q allowed: %v", anyDM, number, got)
			}
			return
		}
		if listed := slices.Contains(allowed, number); got != listed {
			t.Errorf("allowed_numbers %q: %q allowed: %v, listed: %v", allowed, number, got, listed)
		}
		other := !anyDM
		if numberAllowed(&Config{AllowedNumbers: allowed, AllowAnyDM: &other}, number) != got {
			t.Errorf("allowed_numbers %q: allow_any_dm changes whether %q is allowed", allowed, number)
		}
		for _, n := range allowed {
			// The dashboard saves numbers normalised.
			if n != normalizeJID(n) {
				continue
			}
			for _, jid := range []string{n, "+" + n, n + "@s.whatsapp.net", "+" + n + "@s.whatsapp.net"} {
				if !numberAllowed(cfg, normalizeJID(jid)) {
					t.Errorf("allowed_numbers %q: %q is refused", allowed, jid)
				}
			}
		}
	})
}

// fakeDownloader stands in for whatsmeow's media download. Like whatsmeow,
// it writes the body into the file with io.Copy; then it fails with err, or,
// with block set, waits for the context to end.
//
// With encrypted set, it does what whatsmeow does with a real attachment:
// the body arrives AES-CBC encrypted with a MAC after it, up to
// mediaCipherOverhead bytes larger, and is then decrypted in place.
type fakeDownloader struct {
	body      string
	encrypted bool
	err       error
	block     bool

	mu      sync.Mutex
	calls   int
	ctx     context.Context // of the last call
	sent    int64           // bytes the download carried
	copied  int64           // bytes the file took
	stopped bool            // the context ended while copying
}

// fakeMediaKey and fakeMediaIV encrypt the fake's attachments; whatsmeow
// derives both from the media key in the message.
var (
	fakeMediaKey = bytes.Repeat([]byte{7}, 32)
	fakeMediaIV  = bytes.Repeat([]byte{9}, 16)
)

func (d *fakeDownloader) DownloadToFile(ctx context.Context, _ whatsmeow.DownloadableMessage, f whatsmeow.File) error {
	wire := []byte(d.body)
	if d.encrypted {
		enc, err := cbcutil.Encrypt(fakeMediaKey, fakeMediaIV, wire[:len(wire):len(wire)])
		if err != nil {
			return err
		}
		wire = append(enc, make([]byte, fakeMACLength)...) // whatsmeow checks the MAC; the fake does not
	}
	d.mu.Lock()
	d.calls++
	d.ctx = ctx
	d.sent = int64(len(wire))
	d.mu.Unlock()
	n, err := io.Copy(f, io.TeeReader(bytes.NewReader(wire), io.Discard))
	d.mu.Lock()
	d.copied, d.stopped = n, ctx.Err() != nil
	d.mu.Unlock()
	if err != nil {
		return err
	}
	if d.encrypted {
		if err := decryptInPlace(f, n); err != nil {
			return err
		}
	}
	if d.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return d.err
}

// fakeMACLength is the length of whatsmeow's media MAC.
const fakeMACLength = 10

// decryptInPlace turns the n encrypted bytes in f back into the attachment
// the way whatsmeow's DownloadToFile does: it reads the MAC off the end,
// truncates it away, and decrypts the rest in place, which writes over the
// file with WriteAt and truncates off the padding.
func decryptInPlace(f whatsmeow.File, n int64) error {
	mac := make([]byte, fakeMACLength)
	if _, err := f.ReadAt(mac, n-fakeMACLength); err != nil {
		return err
	}
	if err := f.Truncate(n - fakeMACLength); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := cbcutil.DecryptFile(fakeMediaKey, fakeMediaIV, f); err != nil {
		return err
	}
	_, err := f.Seek(0, io.SeekStart)
	return err
}

func (d *fakeDownloader) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
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

// All four kinds of attachment go through one path; the name a file is
// saved under comes from one table of MIME types, and each kind passes on
// the size its message declares.
func TestAttachmentOf(t *testing.T) {
	cases := []struct {
		name          string
		msg           *waE2E.Message
		kind, saveAs  string
		caption       string
		size          uint64
		noAttachments bool
	}{
		{name: "jpeg photo", msg: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: proto.String("image/jpeg"), Caption: proto.String("look"), FileLength: proto.Uint64(1)}}, kind: "image", saveAs: "3EB0TEST.jpg", caption: "look", size: 1},
		{name: "png photo", msg: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: proto.String("image/png")}}, kind: "image", saveAs: "3EB0TEST.png"},
		{name: "photo of no known type", msg: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{}}, kind: "image", saveAs: "3EB0TEST.jpg"},
		{name: "named document", msg: &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("Q3 report.pdf"), Mimetype: proto.String("application/pdf"), Caption: proto.String("numbers"), FileLength: proto.Uint64(2)}}, kind: "document", saveAs: "Q3 report.pdf", caption: "numbers", size: 2},
		{name: "unnamed spreadsheet", msg: &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{Mimetype: proto.String("application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")}}, kind: "document", saveAs: "3EB0TEST.xlsx"},
		{name: "unnamed document of no known type", msg: &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{Mimetype: proto.String("application/x-unknown")}}, kind: "document", saveAs: "3EB0TEST.bin"},
		{name: "voice note", msg: &waE2E.Message{AudioMessage: &waE2E.AudioMessage{Mimetype: proto.String("audio/ogg; codecs=opus"), FileLength: proto.Uint64(3)}}, kind: "audio", saveAs: "3EB0TEST.ogg", size: 3},
		{name: "m4a audio", msg: &waE2E.Message{AudioMessage: &waE2E.AudioMessage{Mimetype: proto.String("audio/mp4")}}, kind: "audio", saveAs: "3EB0TEST.m4a"},
		{name: "mp3 audio", msg: &waE2E.Message{AudioMessage: &waE2E.AudioMessage{Mimetype: proto.String("Audio/MPEG")}}, kind: "audio", saveAs: "3EB0TEST.mp3"},
		{name: "mp4 video", msg: &waE2E.Message{VideoMessage: &waE2E.VideoMessage{Mimetype: proto.String("video/mp4"), Caption: proto.String("clip"), FileLength: proto.Uint64(4)}}, kind: "video", saveAs: "3EB0TEST.mp4", caption: "clip", size: 4},
		{name: "quicktime video", msg: &waE2E.Message{VideoMessage: &waE2E.VideoMessage{Mimetype: proto.String("video/quicktime")}}, kind: "video", saveAs: "3EB0TEST.mov"},
		{name: "webm video with parameters", msg: &waE2E.Message{VideoMessage: &waE2E.VideoMessage{Mimetype: proto.String("video/webm; codecs=vp9")}}, kind: "video", saveAs: "3EB0TEST.webm"},
		{name: "GIF reaction", msg: &waE2E.Message{VideoMessage: &waE2E.VideoMessage{GifPlayback: proto.Bool(true)}}, noAttachments: true},
		{name: "text", msg: &waE2E.Message{Conversation: proto.String("hi")}, noAttachments: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			att, ok := attachmentOf(c.msg, "3EB0TEST")
			if ok == c.noAttachments {
				t.Fatalf("attachment found = %v, want %v", ok, !c.noAttachments)
			}
			if !ok {
				return
			}
			if att.kind != c.kind || att.name != c.saveAs || att.caption != c.caption || att.size != c.size || att.media == nil {
				t.Errorf("got kind %q, name %q, caption %q, size %d, media %v; want %q, %q, %q, %d", att.kind, att.name, att.caption, att.size, att.media, c.kind, c.saveAs, c.caption, c.size)
			}
		})
	}
}

// saveAttachment streams the download into the media directory and names it
// only when it is complete. It never replaces or follows what is already
// there, and a failed download leaves nothing behind.
func TestSaveAttachment(t *testing.T) {
	doc := attachment{kind: "document", media: &waE2E.DocumentMessage{}, name: "scan.pdf"}

	t.Run("saved under its name", func(t *testing.T) {
		dir := t.TempDir()
		path, err := saveAttachment(context.Background(), &fakeDownloader{body: "%PDF-1.7"}, dir, doc, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(dir, "scan.pdf"); path != want {
			t.Errorf("saved as %q, want %q", path, want)
		}
		if got, _ := os.ReadFile(path); string(got) != "%PDF-1.7" {
			t.Errorf("content %q", got)
		}
		if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("mode %v (%v), want 0600", st.Mode().Perm(), err)
		}
		if names := dirNames(t, dir); len(names) != 1 {
			t.Errorf("media dir holds %q, want only the saved file", names)
		}
	})

	t.Run("sanitised name", func(t *testing.T) {
		dir := t.TempDir()
		path, err := saveAttachment(context.Background(), &fakeDownloader{body: "x"}, dir, attachment{kind: "document", media: &waE2E.DocumentMessage{}, name: "../a\nb.pdf"}, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(dir, "ab.pdf"); path != want {
			t.Errorf("saved as %q, want %q", path, want)
		}
	})

	t.Run("never replaces or follows", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "scan.pdf"), []byte("first"), 0o600); err != nil {
			t.Fatal(err)
		}
		// A dangling symlink, as a brain could leave in the media directory.
		outside := filepath.Join(t.TempDir(), "outside.pdf")
		if err := os.Symlink(outside, filepath.Join(dir, "scan-2.pdf")); err != nil {
			t.Fatal(err)
		}
		path, err := saveAttachment(context.Background(), &fakeDownloader{body: "second"}, dir, doc, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(dir, "scan-3.pdf"); path != want {
			t.Errorf("saved as %q, want %q", path, want)
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "scan.pdf")); string(got) != "first" {
			t.Errorf("the existing scan.pdf now holds %q", got)
		}
		if _, err := os.Lstat(outside); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the symlink was followed: %v", err)
		}
		if got, _ := os.ReadFile(path); string(got) != "second" {
			t.Errorf("content %q", got)
		}
	})

	failures := []struct {
		name string
		dl   *fakeDownloader
		ctx  func() (context.Context, context.CancelFunc)
		want error
	}{
		{"download fails", &fakeDownloader{body: "half", err: whatsmeow.ErrMediaDownloadFailedWith404}, nil, whatsmeow.ErrMediaDownloadFailedWith404},
		{"timeout", &fakeDownloader{body: "half", block: true}, func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 10*time.Millisecond)
		}, context.DeadlineExceeded},
	}
	for _, c := range failures {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			ctx := context.Background()
			if c.ctx != nil {
				var cancel context.CancelFunc
				ctx, cancel = c.ctx()
				defer cancel()
			}
			path, err := saveAttachment(ctx, c.dl, dir, doc, 1<<20)
			if !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
			if path != "" {
				t.Errorf("path = %q, want none", path)
			}
			if names := dirNames(t, dir); len(names) != 0 {
				t.Errorf("left behind %q", names)
			}
		})
	}
}

// An attachment reaches the brain as the path it was saved under. Its
// download has a deadline, and disconnecting ends it.
func TestHandleMessageSavesAttachment(t *testing.T) {
	contact := mustJID(t, "15551234567@s.whatsapp.net")
	docEvent := func() *events.Message {
		evt := textEvent(types.MessageSource{Chat: contact, Sender: contact}, "")
		evt.Message = &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			FileName: proto.String("scan.pdf"), Caption: proto.String("file this"),
		}}
		return evt
	}

	t.Run("saved and handed over", func(t *testing.T) {
		tw := newTestWhatsApp(t)
		dl := &fakeDownloader{body: "%PDF-1.7"}
		tw.media = dl
		start := time.Now()
		tw.handle(t, docEvent())
		m, ok := tw.next(2 * time.Second)
		want := filepath.Join(tw.cfg().MediaDir, "scan.pdf")
		if !ok || m.Text != "file this" || len(m.Media) != 1 || m.Media[0] != want {
			t.Fatalf("got %+v (delivered %v), want the caption and %s", m, ok, want)
		}
		deadline, ok := dl.ctx.Deadline()
		if !ok || deadline.After(start.Add(mediaDownloadTimeout+time.Second)) {
			t.Errorf("download deadline %v (set %v), want within %v", deadline, ok, mediaDownloadTimeout)
		}
	})

	t.Run("disconnect ends the download", func(t *testing.T) {
		tw := newTestWhatsApp(t)
		dl := &fakeDownloader{body: "half", block: true}
		tw.media = dl
		done := make(chan struct{})
		go func() {
			defer close(done)
			tw.handle(t, docEvent())
		}()
		waitFor(t, 2*time.Second, "the download to start", func() bool { return dl.callCount() == 1 })
		tw.Disconnect()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("the download kept running after Disconnect")
		}
		m, ok := tw.next(2 * time.Second)
		if !ok || m.Text != "file this" || len(m.Media) != 0 {
			t.Errorf("got %+v (delivered %v), want the caption without the file", m, ok)
		}
		if names := dirNames(t, tw.cfg().MediaDir); len(names) != 0 {
			t.Errorf("left behind %q", names)
		}
		select {
		case n := <-tw.notices:
			t.Errorf("told the sender %q while shutting down", n)
		case <-time.After(100 * time.Millisecond):
		}
	})

	// A file that cannot be downloaded is not dropped without a word: sent
	// without a caption, it would otherwise get no reply at all.
	t.Run("download fails", func(t *testing.T) {
		tw := newTestWhatsApp(t)
		tw.media = &fakeDownloader{err: whatsmeow.ErrMediaDownloadFailedWith410}
		evt := textEvent(types.MessageSource{Chat: contact, Sender: contact}, "")
		evt.Message = &waE2E.Message{AudioMessage: &waE2E.AudioMessage{Mimetype: proto.String("audio/ogg; codecs=opus")}}
		tw.handle(t, evt)
		select {
		case n := <-tw.notices:
			if want := "15551234567@s.whatsapp.net: ⚠️ I couldn't download that file. Please send it again."; n != want {
				t.Errorf("notice %q, want %q", n, want)
			}
		case <-time.After(2 * time.Second):
			t.Error("the sender was not told")
		}
		if m, ok := tw.next(100 * time.Millisecond); ok {
			t.Errorf("an empty message reached the brains: %+v", m)
		}
	})
}

// A file over the limit is never saved. One that declares its size is
// refused before anything is downloaded; one that understates it is cut off
// once it passes the limit, so a lying sender cannot fill the disk.
func TestSaveAttachmentSizeLimit(t *testing.T) {
	// A multiple of the AES block size, as every limit in MB is, so that a
	// file at the limit needs all of mediaCipherOverhead while encrypted.
	const limit = 1024
	cases := []struct {
		name      string
		declared  uint64
		body      int  // the attachment's real size
		encrypted bool // downloaded the way whatsmeow downloads it
		saved     bool
		calls     int
	}{
		{name: "declared and real size at the limit", declared: limit, body: limit, saved: true, calls: 1},
		{name: "no declared size, under the limit", body: limit - 1, saved: true, calls: 1},
		{name: "declares more than the limit", declared: limit + 1},
		{name: "declares far more than the limit", declared: 2 << 30},
		{name: "understates by a byte", declared: 10, body: limit + 1, calls: 1},
		{name: "understates by a lot", declared: 10, body: 50 * limit, calls: 1},
		{name: "no declared size, over the limit", body: 50 * limit, calls: 1},

		// Encrypted, a file is up to mediaCipherOverhead bytes larger while it
		// downloads; it must still fit when it is within the limit.
		{name: "encrypted, one byte", body: 1, encrypted: true, saved: true, calls: 1},
		{name: "encrypted, just under the limit", body: limit - 8, encrypted: true, saved: true, calls: 1},
		{name: "encrypted, at the limit", body: limit, encrypted: true, saved: true, calls: 1},
		{name: "encrypted, a byte over the limit", body: limit + 1, encrypted: true, calls: 1},
		{name: "encrypted, just over the limit", body: limit + 8, encrypted: true, calls: 1},
		{name: "encrypted, a block over the limit", body: limit + 16, encrypted: true, calls: 1},
		{name: "encrypted, far over the limit", body: 50 * limit, encrypted: true, calls: 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			dl := &fakeDownloader{body: strings.Repeat("x", c.body), encrypted: c.encrypted}
			att := attachment{kind: "video", media: &waE2E.VideoMessage{}, name: "clip.mp4", size: c.declared}
			path, err := saveAttachment(context.Background(), dl, dir, att, limit)
			if dl.callCount() != c.calls {
				t.Errorf("downloaded %d times, want %d", dl.callCount(), c.calls)
			}
			if !c.saved {
				if !errors.Is(err, errMediaTooLarge) {
					t.Errorf("err = %v, want errMediaTooLarge", err)
				}
				if names := dirNames(t, dir); len(names) != 0 {
					t.Errorf("left behind %q", names)
				}
				if dl.copied > limit+mediaCipherOverhead {
					t.Errorf("the file took %d bytes, more than the limit allows", dl.copied)
				}
				if dl.sent > limit+mediaCipherOverhead && !dl.stopped {
					t.Error("the download was not cancelled when the file passed the cap")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != dl.body {
				t.Errorf("saved %d bytes (%v), want the %d sent, unchanged", len(got), err, c.body)
			}
		})
	}
}

// cappedFile refuses every way whatsmeow could grow the file past the cap,
// and allows everything up to it. A refusal counts as a cancellation, which
// whatsmeow does not retry on its next server.
func TestCappedFile(t *testing.T) {
	ops := []struct {
		name string
		op   func(f whatsmeow.File) error
		over bool
	}{
		{"write up to the cap", func(f whatsmeow.File) error { _, err := f.Write(make([]byte, 10)); return err }, false},
		{"write past the cap", func(f whatsmeow.File) error { _, err := f.Write(make([]byte, 11)); return err }, true},
		{"write after a seek past the cap", func(f whatsmeow.File) error {
			if _, err := f.Seek(8, io.SeekStart); err != nil {
				return err
			}
			_, err := f.Write(make([]byte, 3))
			return err
		}, true},
		{"write at the end of the cap", func(f whatsmeow.File) error { _, err := f.WriteAt(make([]byte, 2), 8); return err }, false},
		{"write at past the cap", func(f whatsmeow.File) error { _, err := f.WriteAt(make([]byte, 3), 8); return err }, true},
		{"truncate to the cap", func(f whatsmeow.File) error { return f.Truncate(10) }, false},
		{"truncate past the cap", func(f whatsmeow.File) error { return f.Truncate(11) }, true},
		{"copy past the cap", func(f whatsmeow.File) error {
			_, err := io.Copy(f, io.TeeReader(strings.NewReader(strings.Repeat("x", 11)), io.Discard))
			return err
		}, true},
	}
	for _, c := range ops {
		t.Run(c.name, func(t *testing.T) {
			tmp, err := os.CreateTemp(t.TempDir(), "capped")
			if err != nil {
				t.Fatal(err)
			}
			defer tmp.Close()
			stopped := false
			f := &cappedFile{f: tmp, limit: 10, stop: func() { stopped = true }}
			err = c.op(f)
			if c.over != errors.Is(err, errMediaTooLarge) || c.over != f.over || c.over != stopped {
				t.Errorf("err %v, over %v, stopped %v; want refused = %v", err, f.over, stopped, c.over)
			}
			if c.over && !errors.Is(err, context.Canceled) {
				t.Errorf("err %v, want it to count as context.Canceled", err)
			}
			if !c.over && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if st, _ := tmp.Stat(); st.Size() > 10 {
				t.Errorf("the file grew to %d bytes", st.Size())
			}
		})
	}
}

// An attachment over max_media_mb is skipped without a download; the
// sender is told, in a DM and in a group, and the caption still reaches the
// brain.
func TestHandleMessageSkipsOversizedAttachment(t *testing.T) {
	contact := mustJID(t, testDMChat)
	backup := &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
		FileName: proto.String("backup.zip"), Caption: proto.String("keep this"),
		FileLength: proto.Uint64(101 << 20),
	}}
	cases := []struct {
		name, chat string
		evt        func(tw *testWhatsApp) *events.Message
	}{
		{"DM", testDMChat, func(*testWhatsApp) *events.Message {
			evt := textEvent(types.MessageSource{Chat: contact, Sender: contact}, "")
			evt.Message = backup
			return evt
		}},
		{"group", testGroup, func(tw *testWhatsApp) *events.Message {
			tw.cfg().AllowedGroups = []string{testGroup}
			// The group path looks the group up; a client that never
			// connected fails that at once, without the network.
			tw.client = whatsmeow.NewClient(&store.Device{}, waLog.Noop)
			return groupEvent(backup)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tw := newTestWhatsApp(t)
			dl := &fakeDownloader{body: "x"}
			tw.media = dl
			tw.handle(t, c.evt(tw))

			m, ok := tw.next(2 * time.Second)
			if !ok || m.JID != c.chat || m.Text != "keep this" || len(m.Media) != 0 || m.Silent {
				t.Errorf("got %+v (delivered %v), want the caption without the file", m, ok)
			}
			select {
			case n := <-tw.notices:
				if want := c.chat + ": ⚠️ That file is over the 100 MB limit, so I skipped it."; n != want {
					t.Errorf("notice %q, want %q", n, want)
				}
			case <-time.After(2 * time.Second):
				t.Error("the sender was not told")
			}
			if dl.callCount() != 0 {
				t.Error("the file was downloaded anyway")
			}
			if names := dirNames(t, tw.cfg().MediaDir); len(names) != 0 {
				t.Errorf("left behind %q", names)
			}
		})
	}
}
