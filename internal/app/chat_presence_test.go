package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/devchat"
	"github.com/Icewater1337/fridolin/internal/manifest"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/testutil"
	"github.com/Icewater1337/fridolin/internal/whatsapp"
)

// fakePresencer records the presence, chat states and receipts a
// WhatsApp client without a connection sends, one line each.
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

func (f *fakePresencer) SendChatPresence(_ context.Context, jid types.JID, state types.ChatPresence, _ types.ChatPresenceMedia) error {
	f.add(fmt.Sprintf("%s %s", state, jid))
	return nil
}

func (f *fakePresencer) MarkRead(_ context.Context, ids []types.MessageID, _ time.Time, chat, _ types.JID, kind ...types.ReceiptType) error {
	f.add(fmt.Sprintf("%s %s %v", kind[0], chat, ids))
	return nil
}

func (f *fakePresencer) lines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

// presenceVia is the dev messenger with the chat states and receipts of
// wa, a WhatsApp client without a connection.
type presenceVia struct {
	*devchat.Messenger
	wa *whatsapp.Client
}

func (p presenceVia) Typing(ctx context.Context, jid string, on bool) error {
	return p.wa.Typing(ctx, jid, on)
}

func (p presenceVia) MarkRead(ctx context.Context, jid string, ids []string) error {
	return p.wa.MarkRead(ctx, jid, ids)
}

func (p presenceVia) MarkPlayed(ctx context.Context, jid string, ids []string) error {
	return p.wa.MarkPlayed(ctx, jid, ids)
}

// plainMessenger is a messenger without chat states and receipts.
type plainMessenger struct{ Messenger }

// A DM that reaches a brain is marked read once the brain took it; the
// chat shows typing while the run works, the bridge shown online first;
// and a voice note is marked played once its run answered.
func TestTypingAndReceiptsThroughWhatsApp(t *testing.T) {
	t.Parallel()
	tb, _ := newMainBridge(t, "")
	f := &fakePresencer{}
	wa := tb.whatsAppOffline(t, &store.Device{}, func(opts *whatsapp.Options) {
		opts.Presence = f
		opts.Downloader = &fakeDownloader{body: "OggS"}
	})
	tb.wa = presenceVia{Messenger: tb.dev, wa: wa}

	wa.ProcessMessage(dmEvent("3EB0TEXT", textMsg("hello")))
	testutil.WaitFor(t, 10*time.Second, "the run's end", func() bool { return slices.Contains(f.lines(), "paused "+testDMChat) })
	testutil.WaitFor(t, 5*time.Second, "the read receipt", func() bool { return slices.Contains(f.lines(), "read "+testDMChat+" [3EB0TEXT]") })
	got := f.lines()
	online, typing, paused := slices.Index(got, "presence available"), slices.Index(got, "composing "+testDMChat), slices.Index(got, "paused "+testDMChat)
	if online < 0 || typing < online || paused < typing {
		t.Errorf("sent %q, want online, then typing, then paused", got)
	}
	testutil.WaitFor(t, 5*time.Second, "the answer", func() bool { return len(tb.sentTo(testDMChat)) > 0 })

	voice := &waE2E.Message{AudioMessage: &waE2E.AudioMessage{PTT: proto.Bool(true), Mimetype: proto.String("audio/ogg; codecs=opus")}}
	wa.ProcessMessage(dmEvent("3EB0VOICE", voice))
	testutil.WaitFor(t, 10*time.Second, "the played receipt", func() bool { return slices.Contains(f.lines(), "played "+testDMChat+" [3EB0VOICE]") })
	if n := strings.Count(strings.Join(f.lines(), "\n"), "played "); n != 1 {
		t.Errorf("%d played receipts, want the voice note's: %q", n, f.lines())
	}
}

// Typing and receipts are for messages someone wrote to a brain: not for
// chatter it only reads, nor for files handed in, nor while
// whatsapp.presence or the brain's presence is off. A message without a
// WhatsApp id (the dev page's) shows typing, and is marked read nowhere.
func TestPresenceIsForMessagesToABrain(t *testing.T) {
	t.Parallel()
	off := false
	dm := chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "hello", Source: chat.SourceWhatsApp, ID: "3EB0A"}
	cases := []struct {
		name     string
		extra    string
		brain    manifest.Manifest
		m        chat.Inbound
		want     []devchat.Signal
		observes bool
	}{
		{name: "a DM", m: dm, want: []devchat.Signal{
			{JID: testDMChat, Kind: devchat.SignalRead, IDs: []string{"3EB0A"}},
			{JID: testDMChat, Kind: devchat.SignalTyping},
			{JID: testDMChat, Kind: devchat.SignalPaused},
		}},
		{name: "a dev-page message", m: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "hello", Source: chat.SourceDev}, want: []devchat.Signal{
			{JID: testDMChat, Kind: devchat.SignalTyping},
			{JID: testDMChat, Kind: devchat.SignalPaused},
		}},
		{name: "whatsapp.presence off", extra: `, "whatsapp": {"presence": false}`, m: dm},
		{name: "the brain's presence off", brain: manifest.Manifest{Presence: &off}, m: dm},
		{name: "untagged chatter", observes: true, m: chat.Inbound{JID: testGroup, Sender: "15551234567", Text: "lunch at noon?", Silent: true, Source: chat.SourceWhatsApp, ID: "3EB0B"}},
		{name: "a file handed in", m: chat.Inbound{JID: testDMChat, Brain: "main", Sender: "plaud", Text: "a recording", Source: chat.SourceIngest}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if c.observes {
				c.brain.Observe = manifest.ObserveRun
			}
			// The only brain, main, is the group's too.
			tb, br := newMainBridgeWith(t, c.extra, c.brain)
			tb.handleInbound(context.Background(), brain.Inbound{Inbound: c.m})
			testutil.WaitFor(t, 10*time.Second, "the run", func() bool { return br.Snapshot().Last != nil })
			if len(c.want) > 0 {
				testutil.WaitFor(t, 5*time.Second, "the signals", func() bool { return len(tb.dev.Signals()) >= len(c.want) })
			} else {
				time.Sleep(200 * time.Millisecond)
			}
			got := tb.dev.Signals()
			// The receipt goes out on its own, beside the run.
			slices.SortStableFunc(got, func(a, b devchat.Signal) int {
				return strings.Compare(fmt.Sprint(a.Kind != devchat.SignalRead), fmt.Sprint(b.Kind != devchat.SignalRead))
			})
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Errorf("signals = %+v, want %+v", got, c.want)
			}
		})
	}
}

// The lease gate and the build chats pass chat states and receipts on to
// the messenger: none once the lease is lost, and none for a build chat,
// which is no WhatsApp chat.
func TestPresenceThroughTheMessengersWrappers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dev := devchat.New(nil)
	lost := false
	gate := leaseGate{Messenger: dev, lost: func() bool { return lost }}
	site := siteChatMessenger{Messenger: gate}
	var p outboundmsg.Presence = site
	for _, jid := range []string{testDMChat, buildChatJID("main", 1)} {
		if err := errors.Join(p.Typing(ctx, jid, true), p.MarkRead(ctx, jid, []string{"A"}), p.MarkPlayed(ctx, jid, []string{"A"})); err != nil {
			t.Fatal(err)
		}
	}
	want := []devchat.Signal{{JID: testDMChat, Kind: devchat.SignalTyping}, {JID: testDMChat, Kind: devchat.SignalRead, IDs: []string{"A"}}, {JID: testDMChat, Kind: devchat.SignalPlayed, IDs: []string{"A"}}}
	if got := dev.Signals(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("signals = %+v, want %+v", got, want)
	}
	lost = true
	for _, err := range []error{p.Typing(ctx, testDMChat, false), p.MarkRead(ctx, testDMChat, nil), p.MarkPlayed(ctx, testDMChat, nil)} {
		if !errors.Is(err, errLeaseLost) {
			t.Errorf("after the lease was lost: %v", err)
		}
	}
	// A messenger without presence shows nothing, and says nothing failed.
	plain := plainMessenger{devchat.New(nil)}
	bare := leaseGate{Messenger: plain, lost: func() bool { return true }}
	if err := errors.Join(bare.Typing(ctx, testDMChat, true), bare.MarkRead(ctx, testDMChat, nil), bare.MarkPlayed(ctx, testDMChat, nil)); err != nil {
		t.Errorf("a messenger without presence: %v", err)
	}
	if err := errors.Join(siteChatMessenger{Messenger: plain}.Typing(ctx, testDMChat, true)); err != nil {
		t.Errorf("a site chat without presence: %v", err)
	}
}
