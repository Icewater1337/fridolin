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
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/chat"
	mediafiles "github.com/Icewater1337/fridolin/internal/media"
	"github.com/Icewater1337/fridolin/internal/trigger"
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
			chatJID, ok := incomingChat(c.src)
			switch {
			case c.want == "" && ok:
				t.Errorf("incomingChat kept the message in %s, want it dropped", chatJID)
			case c.want != "" && !ok:
				t.Errorf("incomingChat dropped the message, want it in %s", c.want)
			case c.want != "" && chatJID.String() != c.want:
				t.Errorf("chat = %s, want %s", chatJID, c.want)
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
	// Ends what the chats' workers still hold or run.
	t.Cleanup(tw.Disconnect)
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
// within wait.
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

// FuzzNumberAllowed: allowed_numbers lets a sender DM the bridge only when
// it lists the number: no prefix, no suffix, no other number, however the
// number was written on either side ("+1 555 123 4567" is 15551234567).
// allow_any_dm decides what an empty list means and never widens a list.
func FuzzNumberAllowed(f *testing.F) {
	const n = "15551234567"
	f.Add(n, n, false)
	f.Add(n, "", true)
	f.Add(n, "", false)
	f.Add(n[:len(n)-1], n+",15559876543", true) // a listed number cut short
	f.Add(n+"8", n, true)                       // and one made longer
	f.Add("+"+n, n, false)
	f.Add(n, "+1 555 123 4567", false) // a hand-written entry
	f.Add(n, "1 555 123 456", false)   // that is another number
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
		// Digits only for what is a phone number, else as written.
		canon := func(s string) string {
			if p, err := phoneNumber(s); err == nil {
				return p
			}
			return s
		}
		listed := slices.ContainsFunc(allowed, func(a string) bool { return canon(a) == canon(number) })
		if got != listed {
			t.Errorf("allowed_numbers %q: %q allowed: %v, listed: %v", allowed, number, got, listed)
		}
		other := !anyDM
		if numberAllowed(&Config{AllowedNumbers: allowed, AllowAnyDM: &other}, number) != got {
			t.Errorf("allowed_numbers %q: allow_any_dm changes whether %q is allowed", allowed, number)
		}
		for _, a := range allowed {
			p, err := phoneNumber(a)
			if err != nil {
				continue
			}
			// A sender, as WhatsApp reports one, in every form.
			for _, jid := range []string{p, "+" + p, p + "@s.whatsapp.net", "+" + p + "@s.whatsapp.net"} {
				if !numberAllowed(cfg, chat.NormalizeJID(jid)) {
					t.Errorf("allowed_numbers %q: %q is refused", allowed, jid)
				}
			}
			// A number one digit longer is someone else.
			if canon(p+"1") != canon(number) && numberAllowed(&Config{AllowedNumbers: []string{a}, AllowAnyDM: &anyDM}, p+"1") {
				t.Errorf("allowed_numbers [%q]: %q, a longer number, is allowed", a, p+"1")
			}
		}
	})
}

// fakeDownloader stands in for whatsmeow's media download. Like whatsmeow,
// it writes the body into the file with io.Copy; then it fails with err, or,
// with block set, waits for the context to end. With hold set, a download
// stalls before it writes anything, until the test closes hold or the
// context ends.
//
// With encrypted set, it does what whatsmeow does with a real attachment:
// the body arrives AES-CBC encrypted with a MAC after it, up to
// mediaCipherOverhead bytes larger, and is then decrypted in place.
type fakeDownloader struct {
	body      string
	encrypted bool
	err       error
	block     bool
	hold      chan struct{}

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
	if d.hold != nil {
		select {
		case <-d.hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
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

// An attachment reaches the brain as the path it was saved under, in the
// media folder's incoming folder, which no brain can read: the brain that
// takes the message moves it into its own inbox. Its download has a
// deadline, and disconnecting ends it.
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
		want := filepath.Join(tw.cfg().MediaDir, mediafiles.IncomingDir, "scan.pdf")
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
		if names := dirNames(t, filepath.Join(tw.cfg().MediaDir, mediafiles.IncomingDir)); len(names) != 0 {
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
				if !errors.Is(err, mediafiles.ErrTooLarge) {
					t.Errorf("err = %v, want errMediaTooLarge", err)
				}
				if names := dirNames(t, dir); len(names) != 0 {
					t.Errorf("left behind %q", names)
				}
				if dl.copied > limit+mediafiles.CipherOverhead {
					t.Errorf("the file took %d bytes, more than the limit allows", dl.copied)
				}
				if dl.sent > limit+mediafiles.CipherOverhead && !dl.stopped {
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

// A reply's quote names the quoted sender by number, as the identity map
// and the allowlist write it, and is left out when there is nothing to show.
func TestExtractQuote(t *testing.T) {
	ctx := func(participant, text string) *waE2E.ContextInfo {
		return &waE2E.ContextInfo{Participant: proto.String(participant), QuotedMessage: &waE2E.Message{Conversation: proto.String(text)}}
	}
	cases := []struct {
		name string
		info *waE2E.ContextInfo
		want *chat.Quote
	}{
		{"phone number", ctx("15559876543@s.whatsapp.net", "Lunch?"), &chat.Quote{From: "15559876543", Text: "Lunch?"}},
		{"lid", ctx("15559876543@lid", "Lunch?"), &chat.Quote{From: "15559876543", Text: "Lunch?"}},
		{"no sender", ctx("", "Lunch?"), nil},
		{"no text", ctx("15559876543@s.whatsapp.net", ""), nil},
		{"no context", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractQuote(c.info)
			if (got == nil) != (c.want == nil) || got != nil && *got != *c.want {
				t.Errorf("extractQuote = %+v, want %+v", got, c.want)
			}
		})
	}
	long := strings.Repeat("é", 150)
	if q := extractQuote(ctx("15559876543@s.whatsapp.net", long)); q == nil || len(q.Text) > 200+len("…") || !utf8.ValidString(q.Text) {
		t.Errorf("a long quote is %q, want it cut to 200 bytes", q)
	}
}

// bridgeAccount is the bridge's own WhatsApp account in the group tests.
var bridgeAccount = types.NewJID("5550100", types.DefaultUserServer)

// newGroupTestWhatsApp is newTestWhatsApp with the allowed group
// testGroup, whose group_members entry is members (none for nil), the
// trigger @claude, the bridge's account known so that a mention of it
// addresses the bridge, and a download that counts its calls.
func newGroupTestWhatsApp(t *testing.T, members []string) (*testWhatsApp, *fakeDownloader) {
	t.Helper()
	tw := newTestWhatsApp(t)
	cfg := tw.cfg()
	cfg.AllowedGroups = []string{testGroup}
	cfg.Triggers = trigger.FromStrings([]string{"@claude"})
	if members != nil {
		cfg.GroupMembers = map[string][]string{testGroup: members}
	}
	tw.setOwn(bridgeAccount, types.EmptyJID)
	dl := &fakeDownloader{body: "JPEG"}
	tw.media = dl
	return tw, dl
}

// groupMessage is msg in testGroup, from sender, and the sender's other
// JID alt as WhatsApp sends it along (none when empty).
func groupMessage(sender, alt types.JID, msg *waE2E.Message) *events.Message {
	src := types.MessageSource{Chat: types.NewJID("120363000000000000", types.GroupServer), Sender: sender, SenderAlt: alt, IsGroup: true}
	return &events.Message{Info: types.MessageInfo{MessageSource: src, ID: "3EB0GROUP", PushName: "Sam", Timestamp: time.Now()}, Message: msg}
}

// The ways a group message addresses the bridge, and one that does not.
var (
	groupTrigger = func() *waE2E.Message { return &waE2E.Message{Conversation: proto.String("@claude what's up")} }
	groupMention = func() *waE2E.Message {
		return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text:        proto.String("@" + bridgeAccount.User + " what's up"),
			ContextInfo: &waE2E.ContextInfo{MentionedJID: []string{bridgeAccount.String()}},
		}}
	}
	groupFile = func() *waE2E.Message {
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: proto.String("image/jpeg"), Caption: proto.String("look")}}
	}
	groupForward = func() *waE2E.Message {
		return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String("read this"), ContextInfo: &waE2E.ContextInfo{IsForwarded: proto.Bool(true)},
		}}
	}
	groupChatter = func() *waE2E.Message { return &waE2E.Message{Conversation: proto.String("lunch at noon?")} }
	groupStop    = func() *waE2E.Message { return &waE2E.Message{Conversation: proto.String("@claude !stop")} }
)

// A member group_members does not admit reaches no brain, however their
// message addresses the bridge: no run, no command, no download, no edit
// of what is waiting, and the group, which is allowed, is not offered on
// the dashboard again. The log names them masked.
func TestGroupNonMemberIsNotRead(t *testing.T) {
	stranger := types.NewJID(otherNumber, types.DefaultUserServer)
	cases := []struct {
		name string
		msg  *waE2E.Message
	}{
		{"trigger", groupTrigger()},
		{"mention", groupMention()},
		{"file", groupFile()},
		{"forward", groupForward()},
		{"untagged", groupChatter()},
		{"!stop", groupStop()},
		{"edit", editMsg("3EB0EARLIER", "@claude now do this")},
		{"deletion", revokeMsg("3EB0EARLIER")},
	}
	entries := []struct {
		name    string
		members []string
	}{{"no entry", nil}, {"a list", []string{"15551234567"}}, {"an empty list", []string{}}}
	for _, e := range entries {
		for _, c := range cases {
			t.Run(e.name+", "+c.name, func(t *testing.T) {
				logged := captureLog(t)
				tw, dl := newGroupTestWhatsApp(t, e.members)
				var changes []chat.Change
				tw.onChange = func(c chat.Change) { changes = append(changes, c) }
				tw.handle(t, groupMessage(stranger, types.EmptyJID, c.msg))
				if m, ok := tw.next(100 * time.Millisecond); ok {
					t.Errorf("reached the brains: %+v", m)
				}
				if len(changes) > 0 {
					t.Errorf("changed a message: %+v", changes)
				}
				if n := dl.callCount(); n != 0 {
					t.Errorf("downloaded the file %d times", n)
				}
				select {
				case n := <-tw.notices:
					t.Errorf("told the group %q", n)
				default:
				}
				tw.mu.Lock()
				defer tw.mu.Unlock()
				if len(tw.ignored) > 0 {
					t.Errorf("offered on the dashboard: %v", tw.ignored)
				}
				got := strings.Join(logged(), "\n")
				if !strings.Contains(got, "by "+maskPhone(otherNumber)+": ignored (not admitted by group_members)") || strings.Contains(got, otherNumber) {
					t.Errorf("log %q, want the ignore with the sender masked", got)
				}
			})
		}
	}
}

// Who group_members admits, allowed_numbers for a group without an entry,
// reaches the brain under their phone number, also when WhatsApp names
// them by LID: through the number it sends along, or the LID store's. A
// LID nothing maps to a number gets in only where everyone does.
func TestGroupMemberReachesTheBrain(t *testing.T) {
	member := types.NewJID("15551234567", types.DefaultUserServer)
	lid := types.NewJID(personLID, types.HiddenUserServer)
	unknown := types.NewJID("272727", types.HiddenUserServer)
	stranger := types.NewJID(otherNumber, types.DefaultUserServer)
	cases := []struct {
		name    string
		members []string
		numbers []string // allowed_numbers; nil keeps 15551234567
		anyDM   bool
		sender  types.JID
		alt     types.JID
		lids    store.LIDStore
		fromMe  bool
		want    string // Inbound.Sender; "" means not handed over
	}{
		{name: "listed by number", members: []string{"15551234567"}, sender: member, want: "15551234567"},
		{name: "listed, by LID with the number along", members: []string{"15551234567"}, sender: lid, alt: member, want: "15551234567"},
		{name: "listed, by LID the store knows", members: []string{"15551234567"}, sender: lid, lids: personLIDs{}, want: "15551234567"},
		{name: "listed, by LID nobody knows", members: []string{"272727"}, sender: unknown, lids: personLIDs{}},
		{name: "everyone, by LID nobody knows", members: []string{anyMember}, sender: unknown, lids: personLIDs{}, want: "272727"},
		{name: "everyone, a stranger", members: []string{anyMember}, sender: stranger, want: otherNumber},
		{name: "no entry, in allowed_numbers", sender: member, want: "15551234567"},
		{name: "no entry, by LID the store knows", sender: lid, lids: personLIDs{}, want: "15551234567"},
		{name: "no entry, a stranger", sender: stranger},
		{name: "no entry, a LID nobody knows", numbers: []string{"272727"}, sender: unknown, lids: personLIDs{}},
		{name: "no entry, anyone may DM", numbers: []string{}, anyDM: true, sender: stranger, want: otherNumber},
		{name: "the bridge's own account, left off the list", members: []string{"15551234567"}, sender: bridgeAccount, fromMe: true, want: bridgeAccount.User},
		{name: "the bridge's own account, nobody allowed", numbers: []string{}, sender: bridgeAccount, fromMe: true, want: bridgeAccount.User},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tw, _ := newGroupTestWhatsApp(t, c.members)
			cfg := tw.cfg()
			if c.numbers != nil {
				cfg.AllowedNumbers = c.numbers
			}
			cfg.AllowAnyDM = &c.anyDM
			if c.lids != nil {
				tw.client = whatsmeow.NewClient(&store.Device{LIDs: c.lids}, waLog.Noop)
			}
			evt := groupMessage(c.sender, c.alt, groupTrigger())
			evt.Info.IsFromMe = c.fromMe
			tw.handle(t, evt)
			wait := 100 * time.Millisecond
			if c.want != "" {
				wait = 2 * time.Second
			}
			m, ok := tw.next(wait)
			switch {
			case c.want == "" && ok:
				t.Errorf("reached the brains: %+v", m)
			case c.want != "" && !ok:
				t.Errorf("did not reach the brains, want it from %s", c.want)
			case ok && (m.Sender != c.want || m.JID != testGroup || m.Text != "what's up" || m.Silent):
				t.Errorf("got %+v, want an addressed message in %s from %s", m, testGroup, c.want)
			}
		})
	}
}

// What an admitted member writes goes on as before: untagged chatter to
// the history, a file that addresses the bridge downloaded, and an
// addressed !stop to the bridge as the command.
func TestGroupMemberMessagesGoOn(t *testing.T) {
	member := types.NewJID("15551234567", types.DefaultUserServer)
	t.Run("untagged", func(t *testing.T) {
		tw, _ := newGroupTestWhatsApp(t, []string{"15551234567"})
		tw.handle(t, groupMessage(member, types.EmptyJID, groupChatter()))
		if m, ok := tw.next(2 * time.Second); !ok || !m.Silent || m.Sender != "15551234567" {
			t.Errorf("got %+v (delivered %v), want silent chatter from 15551234567", m, ok)
		}
	})
	t.Run("file", func(t *testing.T) {
		tw, dl := newGroupTestWhatsApp(t, []string{"15551234567"})
		tw.handle(t, groupMessage(member, types.EmptyJID, groupFile()))
		if m, ok := tw.next(2 * time.Second); !ok || len(m.Media) != 1 || m.Silent {
			t.Errorf("got %+v (delivered %v), want the photo", m, ok)
		}
		if dl.callCount() != 1 {
			t.Errorf("downloaded %d times, want once", dl.callCount())
		}
	})
	t.Run("!stop", func(t *testing.T) {
		tw, _ := newGroupTestWhatsApp(t, []string{"15551234567"})
		tw.handle(t, groupMessage(member, types.EmptyJID, groupStop()))
		if m, ok := tw.next(2 * time.Second); !ok || m.Text != "!stop" || m.Silent {
			t.Errorf("got %+v (delivered %v), want the command", m, ok)
		}
	})
}

// The log names a refused sender WhatsApp knows by no number with the
// server of the JID that names them, masked like the number would be.
func TestGroupRefusalNamesTheSendersServer(t *testing.T) {
	cases := []struct {
		name   string
		sender types.JID
		want   string
	}{
		{"by number", types.NewJID(otherNumber, types.DefaultUserServer), maskPhone(otherNumber)},
		{"by a LID nobody knows", types.NewJID("272727", types.HiddenUserServer), "…27@lid"},
		{"by a hosted account", types.NewJID("272727", types.HostedServer), "…27@hosted"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logged := captureLog(t)
			tw, _ := newGroupTestWhatsApp(t, []string{"15551234567"})
			tw.client = whatsmeow.NewClient(&store.Device{LIDs: personLIDs{}}, waLog.Noop)
			tw.handle(t, groupMessage(c.sender, types.EmptyJID, groupTrigger()))
			if m, ok := tw.next(100 * time.Millisecond); ok {
				t.Errorf("reached the brains: %+v", m)
			}
			if got := strings.Join(logged(), "\n"); !strings.Contains(got, "by "+c.want+": ignored (not admitted by group_members)") {
				t.Errorf("log %q, want the sender as %s", got, c.want)
			}
		})
	}
}

// A non-member's reaction or sticker would not have been read from a
// member either, so its refusal goes to the debug log, not to the main
// one, where a busy group would fill it.
func TestGroupNonMemberReactionIsQuiet(t *testing.T) {
	stranger := types.NewJID(otherNumber, types.DefaultUserServer)
	for name, msg := range map[string]*waE2E.Message{
		"reaction": {ReactionMessage: &waE2E.ReactionMessage{Text: proto.String("👍")}},
		"sticker":  {StickerMessage: &waE2E.StickerMessage{}},
	} {
		t.Run(name, func(t *testing.T) {
			logged := captureLog(t)
			tw, _ := newGroupTestWhatsApp(t, []string{"15551234567"})
			tw.handle(t, groupMessage(stranger, types.EmptyJID, msg))
			if m, ok := tw.next(100 * time.Millisecond); ok {
				t.Errorf("reached the brains: %+v", m)
			}
			if got := strings.Join(logged(), "\n"); strings.Contains(got, "not admitted") {
				t.Errorf("logged %q, want it in the debug log only", got)
			}
			tw.mu.Lock()
			defer tw.mu.Unlock()
			if !slices.ContainsFunc(tw.logs, func(l string) bool {
				return strings.HasPrefix(l, "DEBUG ") && strings.Contains(l, "by "+maskPhone(otherNumber)+": ignored (not admitted by group_members")
			}) {
				t.Errorf("debug log %q, want the refusal, masked", tw.logs)
			}
		})
	}
}

// allowed_groups and group_members are read from one config: with an edit
// in between, a message must not pass under the new allowed_groups and the
// old members when neither config admits it. Here the first config allows
// no group but would let anyone in, and the second allows the group but
// only the owner.
func TestGroupMessageReadsOneConfig(t *testing.T) {
	tw, _ := newGroupTestWhatsApp(t, nil)
	before := &Config{AllowedNumbers: []string{}, AllowAnyDM: proto.Bool(true), MediaDir: t.TempDir(), MaxChunkSize: 4000}
	after := tw.cfg()
	var reads atomic.Int32
	tw.cfg = func() *Config {
		if reads.Add(1) == 1 {
			return before
		}
		return after
	}
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("handleGroupMessage reached for WhatsApp: %v", r)
		}
	}()
	tw.handleGroupMessage(groupMessage(types.NewJID(otherNumber, types.DefaultUserServer), types.EmptyJID, groupTrigger()))
	if m, ok := tw.next(100 * time.Millisecond); ok {
		t.Errorf("a stranger reached the brains under two configs: %+v", m)
	}
}

// dmEventFrom is dmEvent from number.
func dmEventFrom(number, id string, msg *waE2E.Message) *events.Message {
	evt := dmEvent(id, msg)
	jid := types.NewJID(number, types.DefaultUserServer)
	evt.Info.Chat, evt.Info.Sender = jid, jid
	return evt
}

// Whoever writes without being allowed is in the log masked, as a group
// member group_members does not admit is; the dashboard, which offers to
// allow them, still gets their number.
func TestIgnoredSendersAreMaskedInTheLog(t *testing.T) {
	stranger := types.NewJID(otherNumber, types.DefaultUserServer)
	unknown := types.NewJID("272727", types.HiddenUserServer)
	byLID := dmEvent("3EB0LID", textMsg("hi"))
	byLID.Info.Chat, byLID.Info.Sender = unknown, unknown
	cases := []struct {
		name   string
		evt    *events.Message
		log    []string
		sender string // as the dashboard is told, and nowhere in the log
	}{
		{"a DM", dmEventFrom(otherNumber, "3EB0DM", textMsg("hi")), []string{"DM from " + maskPhone(otherNumber) + ": ignored (not in allowed_numbers)"}, otherNumber},
		{"a DM by a LID nobody knows", byLID, []string{"DM: could not resolve …27@lid to a phone number", "DM from …27@lid: ignored (not in allowed_numbers)"}, "272727"},
		{"a group", groupMessage(stranger, types.EmptyJID, groupChatter()), []string{"by " + maskPhone(otherNumber) + ": ignored (not in allowed_groups)"}, otherNumber},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logged := captureLog(t)
			tw := newTestWhatsApp(t)
			var senders []string
			tw.onIgnored = func(_, _, _, sender, _ string) { senders = append(senders, sender) }
			tw.handle(t, c.evt)
			got := strings.Join(logged(), "\n")
			for _, want := range c.log {
				if !strings.Contains(got, want) {
					t.Errorf("log %q, want %q", got, want)
				}
			}
			if strings.Contains(got, c.sender) {
				t.Errorf("log %q names %s", got, c.sender)
			}
			if !slices.Equal(senders, []string{c.sender}) {
				t.Errorf("the dashboard was told of %v, want %s", senders, c.sender)
			}
		})
	}
}

// feed hands evts to the event handler one after another, on a goroutine
// of its own as whatsmeow does, so that an event handler that blocks
// shows up as a message that does not arrive rather than as a test that
// hangs.
func (tw *testWhatsApp) feed(evts ...*events.Message) {
	go func() {
		for _, evt := range evts {
			tw.eventHandler(evt)
		}
	}()
}

// ignoredGroupNames records the names of the groups tw offers the
// dashboard, as they come.
func ignoredGroupNames(tw *testWhatsApp) func() []string {
	var mu sync.Mutex
	var names []string
	tw.onIgnored = func(_, _, name, _, _ string) {
		mu.Lock()
		defer mu.Unlock()
		names = append(names, name)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(names)
	}
}

// A group the bridge does not allow is named in the log and on the
// dashboard, but however much it sends, WhatsApp is asked for its name at
// most once per groupNameTTL.
func TestIgnoredGroupNameIsLookedUpOnce(t *testing.T) {
	logged := captureLog(t)
	tw := newTestWhatsApp(t)
	fl := &fakeLister{info: "Family"}
	tw.lister = fl
	offered := ignoredGroupNames(tw)
	stranger := types.NewJID(otherNumber, types.DefaultUserServer)
	var evts []*events.Message
	for range 5 {
		evts = append(evts, groupMessage(stranger, types.EmptyJID, groupChatter()))
	}
	tw.feed(evts...)
	waitFor(t, 2*time.Second, "the five messages", func() bool { return len(offered()) == 5 })
	if n := fl.infoCalls.Load(); n != 1 {
		t.Errorf("WhatsApp was asked for the name %d times, want once", n)
	}
	for _, name := range offered() {
		if name != "Family" {
			t.Errorf("offered as %q, want Family", name)
		}
	}
	got := strings.Join(logged(), "\n")
	if !strings.Contains(got, `("Family") by `+maskPhone(otherNumber)+": ignored (not in allowed_groups)") {
		t.Errorf("log %q, want the group's name and the sender masked", got)
	}
}

// An allowed group's name is only ever read from what the bridge knows,
// for the log line: no message of the group asks WhatsApp for it.
func TestAllowedGroupNameIsNeverLookedUp(t *testing.T) {
	logged := captureLog(t)
	tw, _ := newGroupTestWhatsApp(t, []string{"15551234567"})
	fl := &fakeLister{info: "Family"}
	tw.lister = fl
	member := types.NewJID("15551234567", types.DefaultUserServer)
	arrive := func(n int) {
		t.Helper()
		for range n {
			if _, ok := tw.next(2 * time.Second); !ok {
				t.Fatal("a message did not reach the brains")
			}
		}
	}
	tw.feed(groupMessage(member, types.EmptyJID, groupChatter()))
	arrive(1)
	// Before, the log names the group by its JID alone.
	if got := strings.Join(logged(), "\n"); !strings.Contains(got, testGroup+" by 15551234567: processing") {
		t.Errorf("log %q, want the group by its JID", got)
	}
	// The group is named once a rename (or the group list) told its name.
	tw.eventHandler(&events.GroupInfo{JID: types.NewJID("120363000000000000", types.GroupServer), Name: &types.GroupName{Name: "Family"}})
	tw.feed(groupMessage(member, types.EmptyJID, groupTrigger()), groupMessage(member, types.EmptyJID, groupChatter()))
	arrive(2)
	if n := fl.infoCalls.Load(); n != 0 {
		t.Errorf("WhatsApp was asked for the name %d times, want never", n)
	}
	if got := strings.Join(logged(), "\n"); !strings.Contains(got, testGroup+` ("Family") by 15551234567: processing`) {
		t.Errorf("log %q, want the name the event told", got)
	}
}

// ── intake: each chat in order, nothing slow on the event goroutine ──────

// photoMsg is a photo with caption.
func photoMsg(caption string) *waE2E.Message {
	return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: proto.String("image/jpeg"), Caption: proto.String(caption)}}
}

// arrivals records what the client hands the bridge, in order: messages
// by ID, and edits and deletions by kind and the message they change.
type arrivals struct {
	mu   sync.Mutex
	seen []string
	msgs []Inbound
}

func (a *arrivals) list() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.seen)
}

func (a *arrivals) messages() []Inbound {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.msgs)
}

// recordArrivals points tw's handler and onChange at a new arrivals. The
// handler takes its time with the message slow (none for ""), as the
// bridge's may: what does not wait for it to return overtakes it.
func recordArrivals(tw *testWhatsApp, slow string) *arrivals {
	a := &arrivals{}
	tw.handler = func(_ context.Context, m Inbound) {
		if m.ID == slow {
			time.Sleep(50 * time.Millisecond)
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		a.seen = append(a.seen, m.ID)
		a.msgs = append(a.msgs, m)
	}
	tw.onChange = func(c chat.Change) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.seen = append(a.seen, c.Kind+" "+c.Target)
	}
	return a
}

// A chat's messages, edits and deletions reach the bridge in the order
// they came, each once the one before was handed over: a photo whose
// download takes a while and "what is this?" after it, a !stop after what
// it is to stop, an edit after the message it edits.
func TestChatEventsKeepTheirOrder(t *testing.T) {
	member := types.NewJID("15551234567", types.DefaultUserServer)
	inGroup := func(id string, msg *waE2E.Message) *events.Message {
		evt := groupMessage(member, types.EmptyJID, msg)
		evt.Info.ID = types.MessageID(id)
		return evt
	}
	cases := []struct {
		name string
		evts []*events.Message
		want []string // the first is slow to hand over
	}{
		{"a photo, then text", []*events.Message{dmEvent("3EB0PHOTO", photoMsg("look")), dmEvent("3EB0TEXT", textMsg("what is this?"))}, []string{"3EB0PHOTO", "3EB0TEXT"}},
		{"a message, then !stop", []*events.Message{dmEvent("3EB0ASK", textMsg("book Tuesday")), dmEvent("3EB0STOP", textMsg("!stop"))}, []string{"3EB0ASK", "3EB0STOP"}},
		{"a message, then its edit", []*events.Message{dmEvent("3EB0ASK", textMsg("book Tuesday")), dmEvent("3EB0EDIT", editMsg("3EB0ASK", "book Wednesday"))}, []string{"3EB0ASK", "edit 3EB0ASK"}},
		{"a message, then its deletion", []*events.Message{dmEvent("3EB0ASK", textMsg("book Tuesday")), dmEvent("3EB0DEL", revokeMsg("3EB0ASK"))}, []string{"3EB0ASK", "revoke 3EB0ASK"}},
		{"a group photo, then text", []*events.Message{inGroup("3EB0PHOTO", groupFile()), inGroup("3EB0TEXT", groupTrigger())}, []string{"3EB0PHOTO", "3EB0TEXT"}},
		{"a group message, then !stop", []*events.Message{inGroup("3EB0ASK", groupTrigger()), inGroup("3EB0STOP", groupStop())}, []string{"3EB0ASK", "3EB0STOP"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tw, dl := newGroupTestWhatsApp(t, []string{"15551234567"})
			dl.hold = make(chan struct{})
			a := recordArrivals(tw, c.want[0])
			tw.feed(c.evts...)
			// A download stalls until the rest of the chat came in.
			time.AfterFunc(100*time.Millisecond, func() { close(dl.hold) })
			waitFor(t, 2*time.Second, "both to arrive", func() bool { return len(a.list()) == len(c.want) })
			if got := a.list(); !slices.Equal(got, c.want) {
				t.Errorf("arrived as %v, want %v", got, c.want)
			}
		})
	}
}

// The debouncer merges a burst in the order the bridge got it, so a photo
// and the question about it reach the brain as one message, the photo
// first.
func TestPhotoThenQuestionMergePhotoFirst(t *testing.T) {
	tw := newTestWhatsApp(t)
	dl := &fakeDownloader{body: "JPEG", hold: make(chan struct{})}
	tw.media = dl
	merged := make(chan Inbound, 1)
	deb := NewDebouncer(func() time.Duration { return 200 * time.Millisecond }, func() time.Duration { return 0 }, func(m Inbound) { merged <- m })
	recordArrivals(tw, "3EB0PHOTO") // for the handler that takes its time
	slow := tw.handler
	tw.handler = func(ctx context.Context, m Inbound) {
		slow(ctx, m)
		deb.Add(m)
	}
	tw.feed(dmEvent("3EB0PHOTO", photoMsg("look")), dmEvent("3EB0TEXT", textMsg("what is this?")))
	time.AfterFunc(100*time.Millisecond, func() { close(dl.hold) })
	select {
	case m := <-merged:
		if m.Text != "look\n\nwhat is this?" || len(m.Media) != 1 || len(m.parts) != 2 || m.parts[0].ID != "3EB0PHOTO" {
			t.Errorf("merged %q with %v from %d parts, want the photo and its caption first", m.Text, m.Media, len(m.parts))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("nothing reached the brain")
	}
}

// A download that takes its time holds up only its own chat: another
// chat's message goes on meanwhile.
func TestSlowDownloadHoldsUpOnlyItsChat(t *testing.T) {
	tw := newTestWhatsApp(t)
	tw.cfg().AllowedNumbers = []string{"15551234567", otherNumber}
	dl := &fakeDownloader{body: "JPEG", hold: make(chan struct{})}
	tw.media = dl
	a := recordArrivals(tw, "")
	tw.feed(dmEvent("3EB0PHOTO", photoMsg("look")), dmEventFrom(otherNumber, "3EB0OTHER", textMsg("hello")))
	waitFor(t, time.Second, "the other chat's message", func() bool { return slices.Contains(a.list(), "3EB0OTHER") })
	if got := a.list(); slices.Contains(got, "3EB0PHOTO") {
		t.Errorf("arrived %v while the download stalled", got)
	}
	close(dl.hold)
	waitFor(t, 2*time.Second, "the photo", func() bool { return slices.Contains(a.list(), "3EB0PHOTO") })
}

// A group lookup that hangs holds up only the group that asked for it: a
// direct message goes on meanwhile, and so do the events after it.
func TestHangingGroupLookupDoesNotDelayADM(t *testing.T) {
	tw := newTestWhatsApp(t)
	fl := &fakeLister{hang: true}
	tw.lister = fl
	a := recordArrivals(tw, "")
	stranger := types.NewJID(otherNumber, types.DefaultUserServer)
	start := time.Now()
	tw.feed(groupMessage(stranger, types.EmptyJID, groupChatter()), dmEvent("3EB0DM", textMsg("hello")))
	waitFor(t, time.Second, "the DM", func() bool { return slices.Contains(a.list(), "3EB0DM") })
	if waited := time.Since(start); waited >= groupNameTimeout {
		t.Errorf("the DM took %v, as long as the lookup", waited)
	}
	if fl.infoCalls.Load() != 1 {
		t.Errorf("%d lookups, want the one that hangs", fl.infoCalls.Load())
	}
}

// A group that floods the bridge fills only its own queue: what does not
// fit is dropped, with one line in the log, and the group's lookup is
// still the one.
func TestFloodingGroupIsDropped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logged := captureLog(t)
		tw := newTestWhatsApp(t)
		fl := &fakeLister{hang: true}
		tw.lister = fl
		offered := ignoredGroupNames(tw)
		stranger := types.NewJID(otherNumber, types.DefaultUserServer)
		tw.eventHandler(groupMessage(stranger, types.EmptyJID, groupChatter()))
		synctest.Wait() // the first message's lookup hangs, its queue empty
		for range chatQueueLimit + 49 {
			tw.eventHandler(groupMessage(stranger, types.EmptyJID, groupChatter()))
		}
		synctest.Wait()
		if lines := logged(); len(lines) != 1 || !strings.Contains(lines[0], testGroup+" sent more than") {
			t.Fatalf("logged %q, want one line about the drops", lines)
		}
		time.Sleep(groupNameTimeout)
		synctest.Wait()
		if n := len(offered()); n != chatQueueLimit+1 {
			t.Errorf("%d messages were handled, want %d", n, chatQueueLimit+1)
		}
		if n := fl.infoCalls.Load(); n != 1 {
			t.Errorf("WhatsApp was asked for the name %d times, want once", n)
		}
		if got := strings.Join(logged(), "\n"); strings.Contains(got, otherNumber) {
			t.Errorf("log %q names the sender", got)
		}
	})
}

// Disconnecting ends the download running and refuses what comes after,
// but first hands on every message the chats' queues hold: the one whose
// download it cut off with its caption, without the file and without a
// word to the sender.
func TestDisconnectHandsOnWhatIsQueued(t *testing.T) {
	t.Run("every chat's, in order", func(t *testing.T) {
		tw := newTestWhatsApp(t)
		tw.cfg().AllowedNumbers = []string{"15551234567", otherNumber}
		dl := &fakeDownloader{body: "JPEG", hold: make(chan struct{})} // never released
		tw.media = dl
		a := recordArrivals(tw, "")
		tw.feed(
			dmEvent("3EB0PHOTO", photoMsg("look")),
			dmEvent("3EB0A", textMsg("one")),
			dmEvent("3EB0B", textMsg("two")),
			dmEventFrom(otherNumber, "3EB0OTHER", textMsg("hello")),
			dmEvent("3EB0C", textMsg("three")),
		)
		waitFor(t, time.Second, "the download to start", func() bool { return dl.callCount() == 1 })
		waitFor(t, time.Second, "the other chat's message", func() bool { return slices.Contains(a.list(), "3EB0OTHER") })
		waitFor(t, time.Second, "the chat's messages to be queued", func() bool {
			tw.intake.mu.Lock()
			defer tw.intake.mu.Unlock()
			c := tw.intake.chats[testDMChat]
			return c != nil && len(c.jobs) == 3
		})
		start := time.Now()
		tw.Disconnect()
		if took := time.Since(start); took >= intakeDrainWarning {
			t.Errorf("Disconnect took %v", took)
		}
		if got, want := a.list(), []string{"3EB0OTHER", "3EB0PHOTO", "3EB0A", "3EB0B", "3EB0C"}; !slices.Equal(got, want) {
			t.Fatalf("handed on %v by the time Disconnect returned, want %v", got, want)
		}
		photo := a.messages()[1]
		if photo.Text != "look" || len(photo.Media) != 0 {
			t.Errorf("the photo went on as %+v, want its caption without the file", photo)
		}
		select {
		case n := <-tw.notices:
			t.Errorf("told the sender %q while shutting down", n)
		case <-time.After(100 * time.Millisecond):
		}
		tw.eventHandler(dmEvent("3EB0LATE", textMsg("late")))
		time.Sleep(50 * time.Millisecond)
		if slices.Contains(a.list(), "3EB0LATE") {
			t.Error("a message after Disconnect was handed on")
		}
	})

	// What the workers hand on then still reads the map between LIDs and
	// numbers, the polls sent and their secrets, which the SQL stores no
	// longer answer under the context Disconnect ended: a member WhatsApp
	// names by LID alone and a vote, each behind the download it cut off,
	// are still handed on, and not taken for a stranger's.
	member := types.NewJID(personLID, types.HiddenUserServer)
	key := []byte("0123456789abcdef0123456789abcdef")
	cases := []struct {
		name string
		evts func(t *testing.T) []*events.Message // the first downloads
		want []string
	}{
		{"a group member by LID", func(*testing.T) []*events.Message {
			photo := groupMessage(types.NewJID("15551234567", types.DefaultUserServer), types.EmptyJID, groupFile())
			photo.Info.ID = "3EB0PHOTO"
			evt := groupMessage(member, types.EmptyJID, groupTrigger())
			evt.Info.ID = "3EB0LID"
			return []*events.Message{photo, evt}
		}, []string{"3EB0PHOTO", "3EB0LID"}},
		{"a DM by LID", func(*testing.T) []*events.Message {
			photo, evt := dmEvent("3EB0PHOTO", photoMsg("look")), dmEvent("3EB0LID", textMsg("hello"))
			photo.Info.Chat, photo.Info.Sender = member, member
			evt.Info.Chat, evt.Info.Sender = member, member
			return []*events.Message{photo, evt}
		}, []string{"3EB0PHOTO", "3EB0LID"}},
		{"a poll vote", func(t *testing.T) []*events.Message {
			voter := types.NewJID("15551234567", types.DefaultUserServer)
			return []*events.Message{dmEvent("3EB0PHOTO", photoMsg("look")), pollVoteEvent(t, key, voter, "3EB0POLL", "Tuesday 3pm")}
		}, []string{"3EB0PHOTO", "vote Tuesday 3pm"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tw, dl := newGroupTestWhatsApp(t, []string{"15551234567"})
			dl.hold = make(chan struct{}) // never released
			tw.client = whatsmeow.NewClient(&store.Device{LIDs: storedLIDs{}, MsgSecrets: pollSecrets{key: key}}, waLog.Noop)
			tw.store = openTestStore(t, filepath.Join(t.TempDir(), "bridge.db"))
			if err := tw.store.SavePoll("3EB0POLL", testDMChat, "main", "When should we meet?", []string{"Monday 2pm", "Tuesday 3pm"}); err != nil {
				t.Fatal(err)
			}
			a := recordArrivals(tw, "")
			tw.onVote = func(v pollVote) {
				a.mu.Lock()
				defer a.mu.Unlock()
				a.seen = append(a.seen, "vote "+strings.Join(v.Selected, ","))
			}
			evts := c.evts(t)
			chatJID, _ := tw.queueChat(evts[1].Info.MessageSource)
			tw.feed(evts...)
			waitFor(t, time.Second, "the download to start", func() bool { return dl.callCount() == 1 })
			waitFor(t, time.Second, "the next to be queued behind it", func() bool {
				tw.intake.mu.Lock()
				defer tw.intake.mu.Unlock()
				c := tw.intake.chats[chatJID]
				return c != nil && len(c.jobs) == 1
			})
			tw.Disconnect()
			if got := a.list(); !slices.Equal(got, c.want) {
				t.Errorf("handed on %v by the time Disconnect returned, want %v", got, c.want)
			}
			tw.mu.Lock()
			defer tw.mu.Unlock()
			if len(tw.ignored) > 0 {
				t.Errorf("offered %v on the dashboard as strangers", tw.ignored)
			}
		})
	}
}

// Shutdown must keep the store open until every accepted event has been
// handed over. A slow first handler warns after five seconds, but does
// not discard the rest of its chat or let Disconnect return early.
func TestDisconnectDrainsPastTheWarning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logged := captureLog(t)
		tw := newTestWhatsApp(t)
		stuck := make(chan struct{})
		delivered := make(chan string, 3)
		tw.handler = func(_ context.Context, m Inbound) {
			if m.ID == "3EB0A" {
				<-stuck
			}
			delivered <- m.ID
		}
		for _, id := range []string{"3EB0A", "3EB0B", "3EB0C"} {
			tw.eventHandler(dmEvent(id, textMsg("hello")))
		}
		synctest.Wait()
		done := make(chan struct{})
		go func() { tw.Disconnect(); close(done) }()
		synctest.Wait()
		time.Sleep(intakeDrainWarning)
		synctest.Wait()
		select {
		case <-done:
			t.Error("Disconnect returned while an accepted worker still used the handler")
		default:
		}
		if got := strings.Join(logged(), "\n"); !strings.Contains(got, "still draining accepted messages after 5s") {
			t.Errorf("log %q, want the slow-drain warning", got)
		}
		close(stuck)
		synctest.Wait()
		<-done
		close(delivered)
		var got []string
		for id := range delivered {
			got = append(got, id)
		}
		if want := []string{"3EB0A", "3EB0B", "3EB0C"}; !slices.Equal(got, want) {
			t.Errorf("handed over %v by Disconnect's return, want %v", got, want)
		}
	})
}

// A direct message from a person WhatsApp names by LID goes on the same
// queue as one by their number, when it says which that is.
func TestQueueChat(t *testing.T) {
	pn := mustJID(t, "15551234567@s.whatsapp.net")
	lid := mustJID(t, "15559876543@lid")
	group := mustJID(t, testGroup)
	cases := []struct {
		name string
		src  types.MessageSource
		want string // "" for none
	}{
		{"by number", types.MessageSource{Chat: pn, Sender: pn}, testDMChat},
		{"by LID, the number along", types.MessageSource{Chat: lid, Sender: lid, SenderAlt: pn}, testDMChat},
		{"by LID from a second device", types.MessageSource{Chat: lid, Sender: mustJID(t, "15559876543:3@lid"), SenderAlt: pn}, testDMChat},
		{"by LID alone", types.MessageSource{Chat: lid, Sender: lid}, "15559876543@lid"},
		{"own message in the chat", types.MessageSource{Chat: lid, Sender: bridgeAccount, SenderAlt: pn, IsFromMe: true}, "15559876543@lid"},
		{"group", types.MessageSource{Chat: group, Sender: lid, SenderAlt: pn, IsGroup: true}, testGroup},
		{"broadcast list", types.MessageSource{Chat: mustJID(t, "1700000000@broadcast"), Sender: pn, IsGroup: true}, testDMChat},
		{"status", types.MessageSource{Chat: types.StatusBroadcastJID, Sender: pn}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tw := newTestWhatsApp(t)
			got, ok := tw.queueChat(c.src)
			if !ok {
				got = ""
			}
			if got != c.want {
				t.Errorf("queueChat = %q, want %q", got, c.want)
			}
		})
	}
}

// At most maxDownloads attachments are downloaded at once, all chats
// together; the others wait for a slot, and go on once one is free.
func TestDownloadsAreBoundedAcrossChats(t *testing.T) {
	tw, dl := newGroupTestWhatsApp(t, nil)
	dl.hold = make(chan struct{})
	a := recordArrivals(tw, "")
	member := types.NewJID("15551234567", types.DefaultUserServer)
	var evts []*events.Message
	for i := range maxDownloads + 2 {
		group := types.NewJID(fmt.Sprintf("1203630000000000%02d", 10+i), types.GroupServer)
		tw.cfg().AllowedGroups = append(tw.cfg().AllowedGroups, group.String())
		evt := groupMessage(member, types.EmptyJID, groupFile())
		evt.Info.Chat = group
		evts = append(evts, evt)
	}
	tw.feed(evts...)
	waitFor(t, time.Second, "the downloads to start", func() bool { return dl.callCount() >= maxDownloads })
	time.Sleep(50 * time.Millisecond)
	if n := dl.callCount(); n != maxDownloads {
		t.Errorf("%d downloads at once, want %d", n, maxDownloads)
	}
	close(dl.hold)
	waitFor(t, 2*time.Second, "every photo", func() bool { return len(a.list()) == len(evts) })
	for _, m := range a.messages() {
		if len(m.Media) != 1 {
			t.Errorf("%s arrived without its photo: %+v", m.JID, m)
		}
	}
}

// A file over max_media_mb is refused before any download, so it takes no
// slot: its sender is told at once, while other chats hold every slot.
func TestOversizedFileDoesNotWaitForASlot(t *testing.T) {
	tw, dl := newGroupTestWhatsApp(t, nil)
	dl.hold = make(chan struct{})
	defer close(dl.hold)
	member := types.NewJID("15551234567", types.DefaultUserServer)
	var evts []*events.Message
	for i := range maxDownloads {
		group := types.NewJID(fmt.Sprintf("1203630000000000%02d", 10+i), types.GroupServer)
		tw.cfg().AllowedGroups = append(tw.cfg().AllowedGroups, group.String())
		evt := groupMessage(member, types.EmptyJID, groupFile())
		evt.Info.Chat = group
		evts = append(evts, evt)
	}
	tw.feed(evts...)
	waitFor(t, time.Second, "every slot to be taken", func() bool { return dl.callCount() == maxDownloads })
	tw.feed(dmEvent("3EB0BIG", &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
		FileName: proto.String("backup.zip"), FileLength: proto.Uint64(101 << 20),
	}}))
	select {
	case n := <-tw.notices:
		if want := testDMChat + ": ⚠️ That file is over the 100 MB limit, so I skipped it."; n != want {
			t.Errorf("notice %q, want %q", n, want)
		}
	case <-time.After(time.Second):
		t.Error("the sender was not told while the slots were taken")
	}
}

// WhatsApp may omit SenderAlt after learning a contact's LID mapping.
// Those messages must share the phone-number queue so text and edits
// cannot overtake an earlier photo addressed only by LID.
func TestStoredLIDMessagesShareThePhoneQueue(t *testing.T) {
	for _, edit := range []bool{false, true} {
		name := "text"
		if edit {
			name = "edit"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tw := newTestWhatsApp(t)
				tw.client = whatsmeow.NewClient(&store.Device{LIDs: personLIDs{}}, waLog.Noop)
				dl := &fakeDownloader{body: "JPEG", hold: make(chan struct{})}
				tw.media = dl
				a := recordArrivals(tw, "")
				first := dmEvent("3EB0PHOTO", photoMsg("look"))
				first.Info.Chat = types.NewJID(personLID, types.HiddenUserServer)
				first.Info.Sender = first.Info.Chat
				tw.eventHandler(first)
				synctest.Wait()
				second := dmEvent("3EB0TEXT", textMsg("what is this?"))
				want := []string{"3EB0PHOTO", "3EB0TEXT"}
				if edit {
					second = dmEvent("3EB0EDIT", editMsg("3EB0PHOTO", "new caption"))
					want[1] = "edit 3EB0PHOTO"
				}
				tw.eventHandler(second)
				synctest.Wait()
				if got := a.list(); len(got) != 0 {
					t.Errorf("later event overtook the LID photo: %v", got)
				}
				close(dl.hold)
				synctest.Wait()
				if got := a.list(); !slices.Equal(got, want) {
					t.Errorf("arrived %v, want %v", got, want)
				}
			})
		})
	}
}
