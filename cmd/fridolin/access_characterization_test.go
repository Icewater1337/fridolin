package main

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/Icewater1337/fridolin/internal/trigger"
)

// Pin complete group decisions at the adapter boundary before moving policy:
// who reaches intake, the text and quote they carry, silence, downloads and
// ignored-chat side effects. The lower-level addressing/member tables cover
// the larger parsing and identity matrices independently.
func TestGroupAccessCharacterization(t *testing.T) {
	text := func(s string, ci *waE2E.ContextInfo) *waE2E.Message {
		return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(s), ContextInfo: ci}}
	}
	quote := func(who string) *waE2E.ContextInfo {
		return &waE2E.ContextInfo{StanzaID: proto.String("earlier"), Participant: proto.String(who), QuotedMessage: textMsg("quoted text")}
	}
	mention := func(who string) *waE2E.Message {
		return text("@"+who+" hello", &waE2E.ContextInfo{MentionedJID: []string{who + "@s.whatsapp.net"}})
	}
	forward := text("hello", &waE2E.ContextInfo{IsForwarded: proto.Bool(true)})
	none := groupRules{}
	cases := []struct {
		name       string
		msg        *waE2E.Message
		match      trigger.Match
		rules      *groupRules
		denyGroup  bool
		denyMember bool
		fromMe     bool
		want       bool
		text       string
		silent     bool
		media      int
		quoted     string
		ignored    int
	}{
		{name: "untagged chatter", msg: textMsg("hello"), want: true, text: "hello", silent: true},
		{name: "anywhere trigger", msg: textMsg("robot hello"), match: trigger.MatchAnywhere, want: true, text: "ro hello"},
		{name: "word trigger", msg: textMsg("hello bot"), match: trigger.MatchWord, want: true, text: "hello"},
		{name: "word does not match substring", msg: textMsg("robot hello"), match: trigger.MatchWord, want: true, text: "robot hello", silent: true},
		{name: "start trigger", msg: textMsg("bot hello"), match: trigger.MatchStart, want: true, text: "hello"},
		{name: "start does not match later", msg: textMsg("hello bot"), match: trigger.MatchStart, want: true, text: "hello bot", silent: true},
		{name: "trigger alone has nothing to read", msg: textMsg("bot")},
		{name: "caption trigger with files disabled", msg: photoMsg("bot look"), rules: &none, want: true, text: "look", media: 1},
		{name: "document caption trigger", msg: &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{Caption: proto.String("bot read"), FileName: proto.String("note.txt")}}, rules: &none, want: true, text: "read", media: 1},
		{name: "file addresses by default", msg: photoMsg("look"), want: true, text: "look", media: 1},
		{name: "caption stays silent with files disabled", msg: photoMsg("look"), rules: &none, want: true, text: "look", silent: true},
		{name: "empty photo with files disabled drops", msg: photoMsg(""), rules: &none},
		{name: "video caption is not a trigger", msg: &waE2E.Message{VideoMessage: &waE2E.VideoMessage{Caption: proto.String("bot look")}}, rules: &none, want: true, text: "look", silent: true},
		{name: "GIF is not a file or caption trigger", msg: &waE2E.Message{VideoMessage: &waE2E.VideoMessage{GifPlayback: proto.Bool(true), Caption: proto.String("bot look")}}},
		{name: "forward addresses by default", msg: forward, want: true, text: "hello"},
		{name: "forward can be chatter", msg: forward, rules: &none, want: true, text: "hello", silent: true},
		{name: "own mention stripped", msg: mention(bridgeAccount.User), want: true, text: "hello"},
		{name: "other mention retained as chatter", msg: mention(otherNumber), want: true, text: "@" + otherNumber + " hello", silent: true},
		{name: "reply to own message addresses", msg: text("hello", quote(bridgeAccount.String())), want: true, text: "hello", quoted: "quoted text"},
		{name: "reply to someone else stays silent", msg: text("hello", quote(otherNumber+"@s.whatsapp.net")), want: true, text: "hello", quoted: "quoted text", silent: true},
		{name: "trigger-only reply retains separate quote", msg: text("bot", quote(otherNumber+"@s.whatsapp.net")), want: true, quoted: "quoted text"},
		{name: "denied group text", msg: textMsg("bot hello"), denyGroup: true, ignored: 1},
		{name: "denied group file is not downloaded", msg: photoMsg("bot look"), denyGroup: true, ignored: 1},
		{name: "denied member text is not read", msg: textMsg("bot hello"), denyMember: true},
		{name: "denied member file is not downloaded", msg: photoMsg("bot look"), denyMember: true},
		{name: "operator may address a closed member list", msg: textMsg("bot hello"), denyMember: true, fromMe: true, want: true, text: "hello"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tw, dl := newGroupTestWhatsApp(t, []string{"15551234567"})
			cfg := tw.cfg()
			cfg.Triggers = trigger.List{{Text: "bot", Match: c.match}}
			if c.denyGroup {
				cfg.AllowedGroups = nil
			}
			if c.denyMember {
				cfg.GroupMembers = map[string][]string{testGroup: {}}
			}
			if c.rules != nil {
				tw.groupRules = whatsAppGroupRules(func(string) groupRules { return *c.rules })
			}
			lister := &fakeLister{info: "Group"}
			tw.lister = lister
			evt := groupMessage(types.NewJID("15551234567", types.DefaultUserServer), types.EmptyJID, c.msg)
			evt.Info.IsFromMe = c.fromMe
			tw.handle(t, evt)
			select {
			case m := <-tw.inbound:
				if !c.want {
					t.Fatalf("reached intake: %+v", m)
				}
				if m.Text != c.text || m.Silent != c.silent || len(m.Media) != c.media || m.JID != testGroup {
					t.Errorf("intake = %+v; want text=%q silent=%v media=%d", m, c.text, c.silent, c.media)
				}
				quoted := ""
				if m.Quoted != nil {
					quoted = m.Quoted.Text
				}
				if quoted != c.quoted {
					t.Errorf("quote = %q, want %q", quoted, c.quoted)
				}
			default:
				if c.want {
					t.Error("message did not reach intake")
				}
			}
			if got := dl.callCount(); got != c.media {
				t.Errorf("download calls = %d, want %d", got, c.media)
			}
			tw.mu.Lock()
			ignored := len(tw.ignored)
			tw.mu.Unlock()
			if ignored != c.ignored {
				t.Errorf("ignored callbacks = %d, want %d", ignored, c.ignored)
			}
			wantLookup := int32(0)
			if c.denyGroup {
				wantLookup = 1
			}
			if got := lister.infoCalls.Load(); got != wantLookup {
				t.Errorf("group name lookups = %d, want %d", got, wantLookup)
			}
		})
	}
}
