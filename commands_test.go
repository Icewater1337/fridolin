package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

const (
	testDMChat = "15551234567@s.whatsapp.net"
	testGroup  = "120363000000000000@g.us"
	// otherNumber is a second person: another member of the group, or the
	// bridge's own account as a quote names it.
	otherNumber = "15559876543"
)

// newMainBridge is a test bridge with one folder brain, "main", that takes
// the DMs and, as the only brain, the allowed group too: one brain serving
// several chats, as in most setups.
func newMainBridge(t *testing.T, extra string) (*testBridge, *Brain) {
	t.Helper()
	tb := newTestBridge(t, `, "otp_guard": true`+extra)
	tb.addFolderBrain(t, BrainManifest{ID: "main", Name: "Main", Routing: BrainRouting{HandlesDMs: true}})
	br := tb.brainMgr.Get("main")
	if br == nil {
		t.Fatal("brain main is missing")
	}
	return tb, br
}

// whatsApp is a WhatsApp client without a connection that hands allowed
// messages and poll votes to the test bridge, like the real one. It
// cannot download an attachment, and sends what it tells a chat about one
// through the test bridge.
func (tb *testBridge) whatsApp() *WhatsAppClient {
	ctx, cancel := context.WithCancel(context.Background())
	wa := &WhatsAppClient{cfg: tb.cfgs.Get, store: tb.store, log: waLog.Noop, handler: tb.handleInbound, onVote: tb.recordVote, ctx: ctx, cancel: cancel}
	wa.media = wa.client // nil: every download fails with ErrClientIsNil
	wa.notify = func(jid, text string) { tb.dev.SendMessage(ctx, jid, text) }
	return wa
}

// whatsAppOffline is whatsApp with a whatsmeow client that never connected,
// which the group path needs: its group lookup fails at once, without
// reaching the network. dev is the bridge account's device store.
func (tb *testBridge) whatsAppOffline(dev *store.Device) *WhatsAppClient {
	wa := tb.whatsApp()
	wa.client = whatsmeow.NewClient(dev, waLog.Noop)
	wa.media = wa.client
	return wa
}

// sentTo returns what the bridge sent to jid, oldest first.
func (tb *testBridge) sentTo(jid string) []string {
	var out []string
	box := tb.dev.Outbox()
	for i := len(box) - 1; i >= 0; i-- {
		if box[i].JID == jid {
			out = append(out, box[i].Text)
		}
	}
	return out
}

// sentPrefix reports whether anything sent to jid starts with prefix.
func (tb *testBridge) sentPrefix(jid, prefix string) bool {
	for _, s := range tb.sentTo(jid) {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

func readHistory(t *testing.T, br *Brain) []historyEntry {
	t.Helper()
	f, err := os.Open(filepath.Join(br.Dir(), historyFileName))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []historyEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e historyEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("history line %q: %v", sc.Text(), err)
		}
		out = append(out, e)
	}
	return out
}

// replyEvent is a DM from the allowed number that quote-replies to one of
// the bridge's own messages.
func replyEvent(text, quoted string) *events.Message {
	chat := types.NewJID("15551234567", types.DefaultUserServer)
	return &events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "3EB0REPLY", Timestamp: time.Now()},
		Message: replyMessage(text, quoted),
	}
}

// replyMessage is text that quote-replies to a message of the bridge's.
func replyMessage(text, quoted string) *waE2E.Message {
	return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: proto.String(text),
		ContextInfo: &waE2E.ContextInfo{
			StanzaID:      proto.String("3EB0QUOTED"),
			Participant:   proto.String(otherNumber + "@s.whatsapp.net"),
			QuotedMessage: &waE2E.Message{Conversation: proto.String(quoted)},
		},
	}}
}

// groupEvent is msg, sent by the allowed number in the allowed group.
func groupEvent(msg *waE2E.Message) *events.Message {
	src := types.MessageSource{
		Chat:    types.NewJID("120363000000000000", types.GroupServer),
		Sender:  types.NewJID("15551234567", types.DefaultUserServer),
		IsGroup: true,
	}
	return &events.Message{Info: types.MessageInfo{MessageSource: src, ID: "3EB0GROUP", Timestamp: time.Now()}, Message: msg}
}

// startLongRun puts a long run for chat on the brain and waits until its
// process is up.
func startLongRun(t *testing.T, tb *testBridge, br *Brain, m Inbound) {
	t.Helper()
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "30000")
	tb.handleInbound(context.Background(), m)
	waitFor(t, 5*time.Second, "the run for "+m.JID, func() bool {
		r := br.Snapshot().Running
		return r != nil && r.PID > 0 && r.Chat == m.JID
	})
}

// A reply's quote reaches the model and the history exactly as the
// "[reply to …]" line it has always been.
func TestQuotedReplyReachesModelAndHistory(t *testing.T) {
	tb, br := newMainBridge(t, "")
	tb.whatsApp().handleMessage(replyEvent("yes, book it", "Lunch at 12?"))

	want := `[reply to ` + otherNumber + `: "Lunch at 12?"]` + "\nyes, book it"
	waitFor(t, 10*time.Second, "the answer", func() bool { return len(tb.sentTo(testDMChat)) > 0 })
	if got := tb.sentTo(testDMChat)[0]; !strings.HasSuffix(got, "says: "+want) {
		t.Errorf("the model was sent %q, want it to end in %q", got, want)
	}
	h := readHistory(t, br)
	if len(h) == 0 || h[0].Type != historyIn || h[0].Text != want {
		t.Errorf("history = %+v, want the reply line first", h)
	}
}

// Quote-replying to the bot's "send me the code" is how people answer it:
// the code must still be kept out of the history and the model.
func TestOTPGuardDropsQuotedCode(t *testing.T) {
	tb, br := newMainBridge(t, "")
	br.appendHistory(testDMChat, "main", "🔐 Please send me the verification code", historyOut)

	tb.whatsApp().handleMessage(replyEvent("483920", "🔐 Please send me the verification code"))

	otpFile := filepath.Join(tb.cfgs.Get().DataDir, "otp", "main.json")
	waitFor(t, 5*time.Second, "the code to be handed over", func() bool {
		_, err := os.Stat(otpFile)
		return err == nil
	})
	var got map[string]any
	data, _ := os.ReadFile(otpFile)
	if err := json.Unmarshal(data, &got); err != nil || got["code"] != "483920" {
		t.Errorf("otp file = %s (%v), want the bare code", data, err)
	}
	time.Sleep(200 * time.Millisecond)
	for _, e := range readHistory(t, br) {
		if strings.Contains(e.Text, "483920") {
			t.Errorf("the code reached the history: %+v", e)
		}
	}
	if rt := br.Snapshot(); rt.Running != nil || len(rt.Queue) > 0 || rt.Last != nil {
		t.Errorf("the code reached the brain: %+v", rt)
	}
}

// The junk filter judges what was typed: an untagged "ok" is skipped whether
// or not it quotes something, and real chatter keeps its quote in the history.
func TestJunkFilterIgnoresTheQuote(t *testing.T) {
	q := &Quote{From: otherNumber, Text: "Lunch at 12?"}
	cases := []struct {
		name string
		text string
		want string // the history line, "" when skipped
	}{
		{"one-word reply", "ok", ""},
		{"emoji reply", "👍", ""},
		{"real reply", "sounds good", `[reply to ` + otherNumber + `: "Lunch at 12?"]` + "\nsounds good"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb, br := newMainBridge(t, "")
			tb.handleInbound(context.Background(), Inbound{JID: testGroup, Sender: otherNumber, Text: c.text, Quoted: q, Silent: true, Source: sourceWhatsApp})
			if c.want != "" {
				waitFor(t, 5*time.Second, "the history line", func() bool { return len(readHistory(t, br)) > 0 })
			} else {
				time.Sleep(200 * time.Millisecond)
			}
			var got string
			if h := readHistory(t, br); len(h) > 0 {
				got = h[0].Text
			}
			if got != c.want {
				t.Errorf("history = %q, want %q", got, c.want)
			}
		})
	}
}

// A reply that is only the trigger ("@claude" on someone's message) asks
// about the quoted message. It has no text of its own, and must still reach
// the brain and be answered.
func TestTriggerOnlyReplyAsksAboutTheQuote(t *testing.T) {
	want := `[reply to ` + otherNumber + `: "Lunch?"]`
	cases := []struct {
		name string
		chat string
		send func(tb *testBridge)
	}{
		{"in a group, from WhatsApp", testGroup, func(tb *testBridge) {
			tb.whatsAppOffline(&store.Device{}).handleMessage(groupEvent(replyMessage("@claude", "Lunch?")))
		}},
		{"in a DM, from WhatsApp", testDMChat, func(tb *testBridge) {
			tb.whatsApp().handleMessage(replyEvent("", "Lunch?"))
		}},
		{"in a group, handed to the bridge", testGroup, func(tb *testBridge) {
			tb.handleInbound(context.Background(), Inbound{JID: testGroup, Sender: "15551234567",
				Quoted: &Quote{From: otherNumber, Text: "Lunch?"}, Source: sourceWhatsApp})
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb, br := newMainBridge(t, "")
			c.send(tb)
			waitFor(t, 10*time.Second, "the answer", func() bool { return len(tb.sentTo(c.chat)) > 0 })
			if got := tb.sentTo(c.chat)[0]; !strings.HasSuffix(got, "says: "+want) {
				t.Errorf("the model was sent %q, want it to end in %q", got, want)
			}
			if h := readHistory(t, br); len(h) == 0 || h[0].Type != historyIn || h[0].Text != want {
				t.Errorf("history = %+v, want the reply line first", h)
			}
		})
	}
}

// A file the bridge could not download is not lost without a word: the
// sender is asked to send it again, and its caption is still answered.
func TestFailedDownloadIsAnswered(t *testing.T) {
	photo := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("What is this?")}}
	cases := []struct {
		name string
		chat string
		send func(tb *testBridge)
	}{
		{"in a DM", testDMChat, func(tb *testBridge) {
			chat := types.NewJID("15551234567", types.DefaultUserServer)
			tb.whatsApp().handleMessage(&events.Message{
				Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "3EB0PHOTO", Timestamp: time.Now()},
				Message: photo,
			})
		}},
		{"in a group", testGroup, func(tb *testBridge) {
			tb.whatsAppOffline(&store.Device{}).handleMessage(groupEvent(photo))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb, _ := newMainBridge(t, "")
			c.send(tb)
			waitFor(t, 10*time.Second, "the notice and the answer", func() bool { return len(tb.sentTo(c.chat)) >= 2 })
			if !tb.sentPrefix(c.chat, "⚠️ I couldn't download that file. Please send it again.") {
				t.Errorf("sent %q, want the notice among it", tb.sentTo(c.chat))
			}
			answered := false
			for _, s := range tb.sentTo(c.chat) {
				answered = answered || strings.HasSuffix(s, "says: What is this?")
			}
			if !answered {
				t.Errorf("sent %q, want an answer to the caption", tb.sentTo(c.chat))
			}
		})
	}
}

// The dashboard shows a run or a waiting message as the model sees it, quote
// included: a reply that is only the trigger has no text of its own.
func TestPreviewShowsTheQuote(t *testing.T) {
	tb, br := newMainBridge(t, "")
	q := &Quote{From: otherNumber, Text: "Lunch?"}
	line := `[reply to ` + otherNumber + `: "Lunch?"]`
	startLongRun(t, tb, br, Inbound{JID: testDMChat, Sender: "15551234567", Text: "yes", Quoted: q, Source: sourceWhatsApp})
	tb.handleInbound(context.Background(), Inbound{JID: testGroup, Sender: "15551234567", Quoted: q, Source: sourceWhatsApp})

	rt := br.Snapshot()
	if rt.Running == nil || rt.Running.Preview != line+"\nyes" {
		t.Errorf("running = %+v, want the reply line and the text", rt.Running)
	}
	if len(rt.Queue) != 1 || rt.Queue[0].Preview != line {
		t.Errorf("queue = %+v, want the reply line", rt.Queue)
	}
}

// A command sent as a reply is still a command.
func TestQuotedCommandIsRecognised(t *testing.T) {
	t.Run("!help", func(t *testing.T) {
		tb, br := newMainBridge(t, "")
		tb.whatsApp().handleMessage(replyEvent("!help", "Here is your summary."))
		waitFor(t, 5*time.Second, "the help text", func() bool { return tb.sentPrefix(testDMChat, "🤖 *Fridolin*") })
		time.Sleep(200 * time.Millisecond)
		if h := readHistory(t, br); len(h) > 0 {
			t.Errorf("the command reached the brain: %+v", h)
		}
	})

	t.Run("!stop", func(t *testing.T) {
		tb, br := newMainBridge(t, "")
		startLongRun(t, tb, br, Inbound{JID: testDMChat, Sender: "15551234567", Text: "write a long report", Source: sourceWhatsApp})
		tb.whatsApp().handleMessage(replyEvent("!stop", workingNotice))
		// The confirmation goes out once the run has stopped, so wait for it
		// rather than for the run: the snapshot clears a moment earlier.
		waitFor(t, 10*time.Second, "the stop confirmation", func() bool { return tb.sentPrefix(testDMChat, "🛑") })
		if rt := br.Snapshot(); rt.Running != nil {
			t.Errorf("running = %+v after the stop confirmation, want nothing", rt.Running)
		}
	})
}

// bridgeLID is the LID of the bridge's own account (otherNumber) in tests:
// short, so it cannot be anyone's real one.
const bridgeLID = "424242"

// bridgeLIDs is the part of whatsmeow's store that maps the bridge's own
// number to its LID.
type bridgeLIDs struct{ store.LIDStore }

func (bridgeLIDs) GetLIDForPN(_ context.Context, pn types.JID) (types.JID, error) {
	if pn.User == otherNumber {
		return types.NewJID(bridgeLID, types.HiddenUserServer), nil
	}
	return types.EmptyJID, nil
}

// An @-mention addresses the bridge like the trigger keyword, commands
// included. WhatsApp writes the mention into the text as "@" and the
// number or LID, and the command must still be recognised.
func TestMentionAddressesTheBridge(t *testing.T) {
	own := types.JID{User: otherNumber, Device: 12, Server: types.DefaultUserServer}
	byNumber := types.NewJID(otherNumber, types.DefaultUserServer).String()
	byLID := types.NewJID(bridgeLID, types.HiddenUserServer).String()
	cases := []struct {
		name, text, mentioned string
		want                  string // part of the bridge's answer
	}{
		{"!help by number", "@" + otherNumber + " !help", byNumber, "🤖 *Fridolin*"},
		{"!help by LID", "@" + bridgeLID + " !help", byLID, "🤖 *Fridolin*"},
		{"a request", "@" + otherNumber + " what is on today?", byNumber, "15551234567 says: what is on today?"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb, _ := newMainBridge(t, "")
			wa := tb.whatsAppOffline(&store.Device{ID: &own, LIDs: bridgeLIDs{}})
			wa.handleMessage(groupEvent(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text:        proto.String(c.text),
				ContextInfo: &waE2E.ContextInfo{MentionedJID: []string{c.mentioned}},
			}}))
			waitFor(t, 10*time.Second, "the answer", func() bool { return len(tb.sentTo(testGroup)) > 0 })
			got := tb.sentTo(testGroup)[0]
			if !strings.Contains(got, c.want) {
				t.Errorf("answered %q, want it to contain %q", got, c.want)
			}
			if strings.Contains(got, "@"+otherNumber) || strings.Contains(got, "@"+bridgeLID) {
				t.Errorf("answered %q: the mention reached the model", got)
			}
		})
	}
}

// In a group, only a message addressed to the bridge is a command. The dev
// endpoint applies the WhatsApp client's trigger rules: the trigger is
// stripped, and an untagged group message arrives Silent.
func TestGroupCommandsMustBeAddressed(t *testing.T) {
	cases := []struct {
		name    string
		jid     string
		text    string
		command bool
	}{
		{"in a DM", testDMChat, "!help", true},
		{"tagged in a group", testGroup, "@claude !help", true},
		{"untagged in a group", testGroup, "!help", false},
		{"untagged in a group, upper case", testGroup, "!HELP", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb, br := newMainBridge(t, "")
			if w := tb.do("POST", "/api/dev/inbound", map[string]any{"jid": c.jid, "text": c.text}); w.Code != 202 {
				t.Fatalf("dev inbound: %d %s", w.Code, w.Body.String())
			}
			if c.command {
				waitFor(t, 5*time.Second, "the help text", func() bool { return tb.sentPrefix(c.jid, "🤖 *Fridolin*") })
				return
			}
			// Ordinary chatter: the brain reads it, nobody gets an answer.
			waitFor(t, 5*time.Second, "the message in the history", func() bool {
				h := readHistory(t, br)
				return len(h) > 0 && h[0].Text == c.text
			})
			waitFor(t, 5*time.Second, "the silent run", func() bool { return br.Snapshot().Last != nil })
			if sent := tb.sentTo(c.jid); len(sent) > 0 {
				t.Errorf("the bridge answered untagged chatter: %q", sent)
			}
		})
	}
}

// An untagged "!stop" in a group must not stop the run a member asked for.
func TestUntaggedStopInGroupIsChatter(t *testing.T) {
	tb, br := newMainBridge(t, "")
	startLongRun(t, tb, br, Inbound{JID: testGroup, Sender: "15551234567", Text: "summarise the thread", Source: sourceWhatsApp})

	tb.handleInbound(context.Background(), Inbound{JID: testGroup, Sender: otherNumber, Text: "!stop", Silent: true, Source: sourceWhatsApp})
	time.Sleep(300 * time.Millisecond)
	rt := br.Snapshot()
	if rt.Running == nil || rt.Running.Preview != "summarise the thread" {
		t.Errorf("running = %+v, want the tagged run still going", rt.Running)
	}
	if len(rt.Queue) != 1 || !rt.Queue[0].Silent || rt.Queue[0].Preview != "!stop" {
		t.Errorf("queue = %+v, want the untagged !stop waiting as chatter", rt.Queue)
	}
	if sent := tb.sentTo(testGroup); len(sent) > 0 {
		t.Errorf("the bridge answered untagged chatter: %q", sent)
	}
}

// A full queue points to !stop as the chat must send it, and says what it
// drops: only what that chat has waiting.
func TestQueueFullReplyNamesTheChatsStop(t *testing.T) {
	cases := []struct {
		name string
		next Inbound
		want string
	}{
		{"in a DM", Inbound{JID: testDMChat, Sender: "15551234567", Text: "dm 3"}, "send `!stop` to drop what this chat has waiting"},
		{"in a group", Inbound{JID: testGroup, Sender: otherNumber, Text: "group 1"}, "send `@claude !stop` to drop what this chat has waiting"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb, br := newMainBridge(t, `, "queue": {"max_pending": 1}`)
			startLongRun(t, tb, br, Inbound{JID: testDMChat, Sender: "15551234567", Text: "dm 1", Source: sourceWhatsApp})
			tb.handleInbound(context.Background(), Inbound{JID: testDMChat, Sender: "15551234567", Text: "dm 2", Source: sourceWhatsApp})
			c.next.Source = "whatsapp"
			tb.handleInbound(context.Background(), c.next)

			var full string
			for _, s := range tb.sentTo(c.next.JID) {
				if strings.Contains(s, "queue is full") {
					full = s
				}
			}
			if !strings.Contains(full, c.want) {
				t.Errorf("sent %q, want a queue-full reply with %q", tb.sentTo(c.next.JID), c.want)
			}
		})
	}
}

// queuePreviews lists what waits in the brain's queue.
func queuePreviews(br *Brain) []string {
	var out []string
	for _, q := range br.Snapshot().Queue {
		out = append(out, q.Preview)
	}
	return out
}

// !stop and !reset act only on the chat that sent them. One brain serves the
// DM and the group here, so the other chat's run and queue must survive.
func TestStopAndResetAreScopedToTheChat(t *testing.T) {
	const otherDM = otherNumber + "@s.whatsapp.net"
	cases := []struct {
		name        string
		chat, cmd   string
		wantRunning string   // preview of the run going afterwards
		wantQueue   []string // previews still waiting
		wantReply   string   // prefix of the answer to the chat
	}{
		{"!stop from the running chat", testDMChat, "!stop", "group 1", nil, "🛑 Stopped"},
		{"!stop from a chat with only a waiting message", testGroup, "!stop", "dm 1", []string{"dm 2"}, "🛑 Nothing was running for this chat; cleared"},
		{"!stop from a chat with nothing going", otherDM, "!stop", "dm 1", []string{"dm 2", "group 1"}, "Nothing was running for this chat."},
		{"!reset from another chat", testGroup, "!reset", "dm 1", []string{"dm 2"}, "✅ Cleared"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb, br := newMainBridge(t, "")
			ctx := context.Background()
			startLongRun(t, tb, br, Inbound{JID: testDMChat, Sender: "15551234567", Text: "dm 1", Source: sourceWhatsApp})
			pid := br.Snapshot().Running.PID
			tb.handleInbound(ctx, Inbound{JID: testDMChat, Sender: "15551234567", Text: "dm 2", Source: sourceWhatsApp})
			tb.handleInbound(ctx, Inbound{JID: testGroup, Sender: otherNumber, Text: "group 1", Source: sourceWhatsApp})
			if q := queuePreviews(br); len(q) != 2 {
				t.Fatalf("queue = %q, want dm 2 and group 1 waiting", q)
			}

			tb.handleInbound(ctx, Inbound{JID: c.chat, Sender: normalizeJID(c.chat), Text: c.cmd, Source: sourceWhatsApp})

			waitFor(t, 10*time.Second, "the run of "+c.wantRunning, func() bool {
				r := br.Snapshot().Running
				return r != nil && r.Preview == c.wantRunning && r.PID > 0
			})
			if c.wantRunning == "dm 1" && br.Snapshot().Running.PID != pid {
				t.Error("the run in flight was restarted")
			}
			if q := queuePreviews(br); strings.Join(q, "|") != strings.Join(c.wantQueue, "|") {
				t.Errorf("queue = %q, want %q", q, c.wantQueue)
			}
			if !tb.sentPrefix(c.chat, c.wantReply) {
				t.Errorf("sent %q to the chat, want %q", tb.sentTo(c.chat), c.wantReply)
			}
		})
	}
}

// !reset must not promise a fresh conversation, which it cannot give: a
// brain's next run still reads the recent chat history.
func TestResetReplySaysWhatItDoes(t *testing.T) {
	tb, _ := newMainBridge(t, "")
	ctx := context.Background()
	for _, cmd := range []string{"!reset", "!help"} {
		tb.handleInbound(ctx, Inbound{JID: testDMChat, Sender: "15551234567", Text: cmd, Source: sourceWhatsApp})
	}
	sent := tb.sentTo(testDMChat)
	if len(sent) != 2 {
		t.Fatalf("sent %q, want a reply to !reset and to !help", sent)
	}
	for _, s := range sent {
		if strings.Contains(s, "fresh") {
			t.Errorf("reply %q promises a fresh start", s)
		}
	}
	if !strings.Contains(sent[0], "history") {
		t.Errorf("reply %q does not say the chat history stays", sent[0])
	}
}

// A chat no brain answers has nothing to reset, which is not an error. Its
// next message gets "no brain is set up", so the reply does not promise that
// message sees the history.
func TestResetWithoutABrain(t *testing.T) {
	tb := newTestBridge(t, "")
	tb.handleInbound(context.Background(), Inbound{JID: testDMChat, Sender: "15551234567", Text: "!reset", Source: sourceWhatsApp})
	sent := tb.sentTo(testDMChat)
	if len(sent) != 1 || sent[0] != "✅ Cleared anything this chat had running or waiting." {
		t.Errorf("sent %q, want only that it cleared the chat", sent)
	}
}

// Brain runs keep nothing in the sessions and process_state tables: a run
// never resumes a session, and nothing read the PIDs. The tables stay, for
// an older bridge rolled back to.
func TestBrainRunsWriteNoSessionOrProcessRows(t *testing.T) {
	tb, br := newMainBridge(t, "")
	rows := func(when string) {
		t.Helper()
		for _, table := range []string{"sessions", "process_state"} {
			var n int
			if err := tb.store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
				t.Fatalf("%s: %v", table, err)
			}
			if n != 0 {
				t.Errorf("%s has %d rows %s, want none", table, n, when)
			}
		}
	}
	startLongRun(t, tb, br, Inbound{JID: testDMChat, Sender: "15551234567", Text: "dm 1", Source: sourceWhatsApp})
	rows("while a brain runs")
	tb.handleInbound(context.Background(), Inbound{JID: testDMChat, Sender: "15551234567", Text: "!reset", Source: sourceWhatsApp})
	waitFor(t, 10*time.Second, "the run to stop", func() bool { return br.Snapshot().Running == nil })
	rows("after !reset")
}

// In interrupt mode a request that has to wait for another chat is told so,
// like in queue mode; one that takes over its sender's own run is not.
func TestInterruptModeQueuedNotice(t *testing.T) {
	cases := []struct {
		name   string
		next   Inbound
		notice bool
	}{
		{"a request from another chat waits", Inbound{JID: testGroup, Sender: otherNumber, Text: "group question"}, true},
		{"a newer request replaces the sender's run", Inbound{JID: testDMChat, Sender: "15551234567", Text: "dm 2"}, false},
		{"untagged chatter is never told", Inbound{JID: testGroup, Sender: otherNumber, Text: "chatter here", Silent: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb, br := newMainBridge(t, `, "queue": {"mode": "interrupt"}`)
			startLongRun(t, tb, br, Inbound{JID: testDMChat, Sender: "15551234567", Text: "dm 1", Source: sourceWhatsApp})
			t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "10")
			c.next.Source = "whatsapp"
			tb.handleInbound(context.Background(), c.next)
			if got := tb.sentPrefix(c.next.JID, "⏳ Queued (#1)"); got != c.notice {
				t.Errorf("queued notice sent = %v, want %v (sent %q)", got, c.notice, tb.sentTo(c.next.JID))
			}
		})
	}
}

// WhatsApp may address a DM by the sender's LID while the run in flight came
// in under the phone number (a broadcast-list message turned into a DM, say).
// It is the same chat, so a !stop that arrives by LID stops that run and
// drops what waits behind it.
func TestStopByLIDReachesTheDMsRun(t *testing.T) {
	const lid = otherNumber + "@lid"
	tb, br := newMainBridge(t, "")
	ctx := context.Background()
	startLongRun(t, tb, br, Inbound{JID: testDMChat, Sender: "15551234567", Text: "dm 1", Source: sourceWhatsApp})
	tb.handleInbound(ctx, Inbound{JID: testDMChat, Sender: "15551234567", Text: "dm 2", Source: sourceWhatsApp})

	tb.handleInbound(ctx, Inbound{JID: lid, Sender: "15551234567", Text: "!stop", Source: sourceWhatsApp})
	if !tb.sentPrefix(lid, "🛑 Stopped") {
		t.Errorf("!stop answered %q, want that it stopped the DM's run", tb.sentTo(lid))
	}
	if rt := br.Snapshot(); rt.Running != nil || len(rt.Queue) != 0 {
		t.Errorf("the DM still has work after !stop: running %+v, queue %+v", rt.Running, rt.Queue)
	}
}
