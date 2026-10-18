package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

func TestChatJID(t *testing.T) {
	cases := []struct {
		in, want string // want "" = refused
	}{
		{"15551234567@s.whatsapp.net", "15551234567@s.whatsapp.net"},
		{"15551234567", "15551234567@s.whatsapp.net"},
		{"+1 555 123-4567", "15551234567@s.whatsapp.net"},
		{" 15551234567@s.whatsapp.net ", "15551234567@s.whatsapp.net"},
		{"15551234567:12@s.whatsapp.net", "15551234567@s.whatsapp.net"},
		{"120363000000000000@g.us", "120363000000000000@g.us"},
		{bridgeLID + "@lid", bridgeLID + "@lid"},
		{"status@broadcast", "status@broadcast"}, // a chat, which /send then refuses
		{"12345", ""},
		{"call me", ""},
		{"@s.whatsapp.net", ""},
		{"alex@s.whatsapp.net", ""},
		{"15551234567@example.com", ""},
		{"", ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := chatJID(c.in)
			if c.want == "" {
				if err == nil {
					t.Errorf("chatJID(%q) = %v, want an error", c.in, got)
				}
				return
			}
			if err != nil || got.String() != c.want {
				t.Errorf("chatJID(%q) = %v, %v; want %s", c.in, got, err, c.want)
			}
		})
	}
}

func TestIdentityKey(t *testing.T) {
	cases := map[string]string{
		"15551234567":                    "15551234567",
		"+1 555 123 4567":                "15551234567",
		"+1 (555) 123-4567":              "15551234567",
		"+1\u00a0555\u00a0123\u00a04567": "15551234567", // no-break spaces
		"15551234567@s.whatsapp.net":     "15551234567",
		bridgeLID + "@lid":               bridgeLID,
		"Alex":                           "Alex",
	}
	for in, want := range cases {
		if got := identityKey(in); got != want {
			t.Errorf("identityKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// A number written into config.json by hand, the way people write it,
// lets its sender in; the running config holds it as digits.
func TestLoadConfigNormalisesAllowedNumbers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg, err := LoadConfig(writeConfig(t, `{"data_dir": "`+home+`/data", "allowed_numbers": ["+1 555 123-4567", "15559876543", "call me"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"15551234567", "15559876543", "call me"}; !slices.Equal(cfg.AllowedNumbers, want) {
		t.Errorf("allowed_numbers = %q, want %q", cfg.AllowedNumbers, want)
	}
	if !numberAllowed(cfg, "15551234567") {
		t.Error("the hand-written number is refused")
	}
	if got := badAllowedNumbers(cfg.AllowedNumbers); !slices.Equal(got, []string{"call me"}) {
		t.Errorf("badAllowedNumbers = %q, want the entry that is no number", got)
	}
}

// The Overview says when allowed_numbers holds an entry that lets nobody in.
func TestStatusWarnsAboutAnAllowedNumberThatIsNone(t *testing.T) {
	tb := newTestBridge(t, `, "allowed_numbers": ["15551234567", "my phone"]`)
	var st statusResponse
	if err := json.Unmarshal(tb.do("GET", "/api/status", nil).Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(st.Warnings, func(w string) bool { return strings.Contains(w, `"my phone"`) }) {
		t.Errorf("warnings = %q, want one naming the entry", st.Warnings)
	}
}

// /send takes a bare number for the DM with it, and records what it sent
// under the chat's JID, where the brain's next run looks; a chat that is
// none is the script's mistake, a 400, not a failed send.
func TestHTTPSendTakesANumber(t *testing.T) {
	tb := newTestBridge(t, "")
	tb.addFolderBrain(t, BrainManifest{ID: "p", Name: "P", Routing: BrainRouting{HandlesDMs: true}})
	br := tb.brainMgr.Get("p")
	send := func(jid string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8765/send", strings.NewReader(`{"jid":"`+jid+`","text":"from a script"}`))
		r.Host = "127.0.0.1:8765"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		tb.h.ServeHTTP(w, r)
		return w
	}
	if w := send("+1 555 123 4567"); w.Code != http.StatusOK {
		t.Fatalf("send to a number: %d %s", w.Code, w.Body)
	}
	if got := tb.sentTo(testDMChat); !slices.Equal(got, []string{"from a script"}) {
		t.Errorf("sent to %s: %q", testDMChat, got)
	}
	h := readHistory(t, br)
	if len(h) != 1 || h[0].Chat != testDMChat || h[0].Type != historyOut {
		t.Errorf("history = %+v, want one outbound line in %s", h, testDMChat)
	}
	for _, bad := range []string{"12345", "alex@s.whatsapp.net", "15551234567@example.com"} {
		if w := send(bad); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "not a chat") {
			t.Errorf("send to %q: %d %s, want 400", bad, w.Code, w.Body)
		}
	}
}

// personLID is the LID of the allowed number, 15551234567, in tests.
const personLID = "171717"

// personLIDs is whatsmeow's LID store as far as it knows the allowed
// number: its LID and back.
type personLIDs struct{ store.LIDStore }

func (personLIDs) GetPNForLID(_ context.Context, lid types.JID) (types.JID, error) {
	if lid.User == personLID {
		return types.NewJID("15551234567", types.DefaultUserServer), nil
	}
	return types.EmptyJID, nil
}

func (personLIDs) GetLIDForPN(_ context.Context, pn types.JID) (types.JID, error) {
	if pn.User == "15551234567" {
		return types.NewJID(personLID, types.HiddenUserServer), nil
	}
	return types.EmptyJID, nil
}

// /send to a person's LID goes to, and is recorded under, their number,
// the JID the chat's runs read its history by.
func TestHTTPSendToALIDUsesTheNumber(t *testing.T) {
	tb := newTestBridge(t, "")
	tb.addFolderBrain(t, BrainManifest{ID: "p", Name: "P", Routing: BrainRouting{HandlesDMs: true}})
	tb.lids = func() store.LIDStore { return personLIDs{} }
	r := httptest.NewRequest("POST", "http://127.0.0.1:8765/send", strings.NewReader(`{"jid":"`+personLID+`@lid","text":"from a script"}`))
	r.Host = "127.0.0.1:8765"
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	tb.h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("send to a LID: %d %s", w.Code, w.Body)
	}
	if got := tb.sentTo(testDMChat); !slices.Equal(got, []string{"from a script"}) {
		t.Errorf("sent to %s: %q", testDMChat, got)
	}
	h := readHistory(t, tb.brainMgr.Get("p"))
	if len(h) != 1 || h[0].Chat != testDMChat {
		t.Errorf("history = %+v, want the line under %s", h, testDMChat)
	}
}

func TestCanonicalUser(t *testing.T) {
	pn := types.NewJID("15551234567", types.DefaultUserServer)
	lid := types.NewJID(personLID, types.HiddenUserServer)
	stranger := types.NewJID("272727", types.HiddenUserServer)
	device := pn
	device.Device = 3
	cases := []struct {
		name     string
		jid, alt types.JID
		lids     store.LIDStore
		want     types.JID
	}{
		{"a number stays", pn, types.EmptyJID, nil, pn},
		{"without its device", device, types.EmptyJID, nil, pn},
		{"a LID with its number along", lid, device, nil, pn},
		{"a LID the store knows", lid, types.EmptyJID, personLIDs{}, pn},
		{"a LID nobody knows", stranger, types.EmptyJID, personLIDs{}, stranger},
		{"a LID without a store", lid, types.EmptyJID, nil, lid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := canonicalUser(context.Background(), c.lids, c.jid, c.alt); got != c.want {
				t.Errorf("canonicalUser(%v, %v) = %v, want %v", c.jid, c.alt, got, c.want)
			}
		})
	}
}

func TestOwnerChat(t *testing.T) {
	cases := []struct {
		numbers []string
		want    string
	}{
		{nil, ""},
		{[]string{"15551234567", "15559876543"}, "15551234567@s.whatsapp.net"},
		{[]string{"my phone", "15559876543"}, "15559876543@s.whatsapp.net"},
		{[]string{"my phone"}, ""},
	}
	for _, c := range cases {
		if got := ownerChat(&Config{AllowedNumbers: c.numbers}); got != c.want {
			t.Errorf("ownerChat(%q) = %q, want %q", c.numbers, got, c.want)
		}
	}
}

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

// A group member addressed by LID is known by their number, so the identity
// map names them; so is the author of a message a reply quotes.
func TestGroupSenderAndQuoteByNumber(t *testing.T) {
	tb, br := newMainBridge(t, "")
	if err := os.MkdirAll(filepath.Join(br.Dir(), "memories", "people"), 0o700); err != nil {
		t.Fatal(err)
	}
	idMap := "| WhatsApp Number | Person | Notes |\n| --- | --- | --- |\n| +1 555 123 4567 | Alex | |\n"
	if err := os.WriteFile(filepath.Join(br.Dir(), identityMapFile), []byte(idMap), 0o600); err != nil {
		t.Fatal(err)
	}
	own := types.JID{User: otherNumber, Device: 12, Server: types.DefaultUserServer}
	wa := tb.whatsAppOffline(&store.Device{ID: &own, LIDs: personLIDs{}})
	lid := types.NewJID(personLID, types.HiddenUserServer)
	evt := groupEvent(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: proto.String("@claude and you?"),
		ContextInfo: &waE2E.ContextInfo{
			StanzaID:      proto.String("3EB0QUOTED"),
			Participant:   proto.String(lid.String()),
			QuotedMessage: &waE2E.Message{Conversation: proto.String("I'm in")},
		},
	}})
	evt.Info.Sender = lid
	evt.Info.AddressingMode = types.AddressingModeLID
	wa.handleMessage(evt)
	waitFor(t, 10*time.Second, "the answer", func() bool { return len(tb.sentTo(testGroup)) > 0 })
	if got := tb.sentTo(testGroup)[0]; !strings.Contains(got, `15551234567 says: [reply to 15551234567: "I'm in"]`) {
		t.Errorf("answered %q, want the sender and the quote's author by number", got)
	}
	h := readHistory(t, br)
	if len(h) == 0 || h[0].From != "15551234567" {
		t.Fatalf("history = %+v, want the message from 15551234567", h)
	}
	// The next run's history names them from the identity map.
	if preface := br.composePreface([]string{testGroup}, true); !strings.Contains(preface, "Alex (15551234567)") {
		t.Errorf("preface = %q, want Alex named", preface)
	}
}

// History a DM wrote under its LID, before DMs were keyed by number, is
// still that chat's: the next run sees it.
func TestPrefaceReadsADMsLIDHistory(t *testing.T) {
	br := bareBrain(t)
	lid := types.NewJID(personLID, types.HiddenUserServer).String()
	br.appendHistory(lid, "15551234567", "OLD-UNDER-LID", historyIn)
	br.appendHistory(testDMChat, "15551234567", "NEW-UNDER-NUMBER", historyIn)
	br.appendHistory("15559876543@s.whatsapp.net", "15559876543", "SOMEONE-ELSE", historyIn)
	preface := br.composePreface(chatJIDs(Inbound{JID: testDMChat, ChatAlt: lid, Sender: "15551234567"}), false)
	if !strings.Contains(preface, "OLD-UNDER-LID") || !strings.Contains(preface, "NEW-UNDER-NUMBER") {
		t.Errorf("preface = %q, want both parts of the chat", preface)
	}
	if strings.Contains(preface, "SOMEONE-ELSE") {
		t.Errorf("preface = %q, holds another chat", preface)
	}
}
