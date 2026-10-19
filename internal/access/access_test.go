package access

import (
	"testing"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/trigger"
)

func TestAccessDecision(t *testing.T) {
	const phone = "15551234567"
	const group = "120363000000000000@g.us"
	p := Policy{AllowedNumbers: []string{phone}, AllowedGroups: []string{group}, Triggers: trigger.FromStrings([]string{"bot"}), Files: true, Forwards: true}
	dm := chat.Envelope{Sender: phone, SenderResolved: true, Text: " hello "}
	grp := dm
	grp.Chat, grp.Group = group, true
	with := func(e chat.Envelope, f func(*chat.Envelope)) chat.Envelope { f(&e); return e }
	cases := []struct {
		name    string
		e       chat.Envelope
		allowed bool
		ignore  string
		silent  bool
		text    string
	}{
		{"phone DM keeps spacing", dm, true, "", false, " hello "},
		{"unlisted DM", with(dm, func(e *chat.Envelope) { e.Sender = "15559876543" }), false, "number not allowed", false, ""},
		{"unresolved collision", with(dm, func(e *chat.Envelope) { e.SenderResolved = false }), false, "number not allowed", false, ""},
		{"own DM", with(dm, func(e *chat.Envelope) { e.FromMe = true }), false, "own message", false, ""},
		{"DM caption merges", with(dm, func(e *chat.Envelope) { e.Caption = "photo" }), true, "", false, "photo\n\n hello "},
		{"DM caption alone", with(dm, func(e *chat.Envelope) { e.Caption = "photo"; e.Text = "" }), true, "", false, "photo"},
		{"group chatter trims", grp, true, "", true, "hello"},
		{"unknown group", with(grp, func(e *chat.Envelope) { e.Chat = "120363000000000001@g.us" }), false, "group not allowed", false, ""},
		{"group member denied", with(grp, func(e *chat.Envelope) { e.Sender = "15559876543" }), false, "sender not admitted by group_members", false, ""},
		{"group opaque identity denied", with(grp, func(e *chat.Envelope) { e.SenderResolved = false }), false, "sender not admitted by group_members", false, ""},
		{"operator group accepted", with(grp, func(e *chat.Envelope) { e.FromMe = true; e.SenderResolved = false }), true, "", true, "hello"},
		{"trigger stripped", with(grp, func(e *chat.Envelope) { e.Text = "bot hello" }), true, "", false, "hello"},
		{"caption trigger", with(grp, func(e *chat.Envelope) { e.Text = ""; e.TriggerCaption = "bot look"; e.Caption = "bot look" }), true, "", false, "look"},
		{"mention stripped", with(grp, func(e *chat.Envelope) {
			e.Text = "@" + phone + " hello"
			e.MentionsMe = true
			e.OwnMentions = []string{phone}
		}), true, "", false, "hello"},
		{"mention substring retained", with(grp, func(e *chat.Envelope) {
			e.Text = "@" + phone + "8 hello"
			e.MentionsMe = true
			e.OwnMentions = []string{"", phone}
		}), true, "", false, "@" + phone + "8 hello"},
		{"mention word prefix retained", with(grp, func(e *chat.Envelope) {
			e.Text = "x@" + phone + " hello"
			e.MentionsMe = true
			e.OwnMentions = []string{phone}
		}), true, "", false, "x@" + phone + " hello"},
		{"quote addresses without entering text", with(grp, func(e *chat.Envelope) { e.RepliesToMe = true; e.Quote = &chat.Quote{From: phone, Text: "old text"} }), true, "", false, "hello"},
		{"file addresses", with(grp, func(e *chat.Envelope) { e.HasFile = true }), true, "", false, "hello"},
		{"forward addresses", with(grp, func(e *chat.Envelope) { e.Forwarded = true }), true, "", false, "hello"},
		{"caption before text", with(grp, func(e *chat.Envelope) { e.Caption = "photo" }), true, "", true, "photo\n\nhello"},
		{"same caption not doubled", with(grp, func(e *chat.Envelope) { e.Caption = " hello " }), true, "", true, "hello"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Decide(p, c.e)
			want := Decision{Allowed: c.allowed, Ignore: c.ignore, Silent: c.silent, Text: c.text}
			if got != want {
				t.Errorf("Decide = %+v, want %+v", got, want)
			}
		})
	}
	for _, name := range []string{"empty closed", "empty open", "flag does not widen list", "wildcard group", "empty member list"} {
		t.Run(name, func(t *testing.T) {
			policy := p
			e := dm
			e.SenderResolved = false
			want := false
			switch name {
			case "empty closed":
				policy.AllowedNumbers = nil
			case "empty open":
				policy.AllowedNumbers = nil
				policy.AllowAnyDM = true
				want = true
			case "flag does not widen list":
				policy.AllowAnyDM = true
			case "wildcard group":
				e = grp
				e.SenderResolved = false
				policy.GroupMembers = map[string][]string{group: {"*"}}
				want = true
			case "empty member list":
				e = grp
				policy.GroupMembers = map[string][]string{group: {}}
			}
			if got := Decide(policy, e).Allowed; got != want {
				t.Errorf("Allowed = %v, want %v", got, want)
			}
		})
	}
}
