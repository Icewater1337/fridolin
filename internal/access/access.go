package access

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/trigger"
)

// Policy is a read-only projection of one configuration snapshot.
// A caller supplies the routing rules for the group being decided.
type Policy struct {
	AllowedNumbers, AllowedGroups []string
	AllowAnyDM                    bool
	GroupMembers                  map[string][]string
	Triggers                      trigger.List
	Files, Forwards               bool
}

type Decision struct {
	Allowed bool
	Ignore  string
	Silent  bool
	Text    string
}

func NumberAllowed(p Policy, number string) bool {
	if len(p.AllowedNumbers) == 0 {
		return p.AllowAnyDM
	}
	// No list entry is invalid UTF-8 (config.Resolve refuses one), so a
	// sender that is can match none, not even one its bytes were mangled into.
	if !utf8.ValidString(number) {
		return false
	}
	if n, err := chat.PhoneNumber(number); err == nil {
		number = n
	}
	return slices.Contains(chat.NumbersAsNumbers(p.AllowedNumbers), number)
}

func GroupAllowed(p Policy, group string) bool {
	return slices.Contains(p.AllowedGroups, group)
}

func GroupSenderAllowed(p Policy, group, number string, resolved, fromMe bool) bool {
	if fromMe {
		return true
	}
	members, listed := p.GroupMembers[group]
	switch {
	case listed && slices.Equal(members, []string{"*"}):
		return true
	case !listed && len(p.AllowedNumbers) == 0 && p.AllowAnyDM:
		return true
	case !resolved:
		return false
	case listed:
		if !utf8.ValidString(number) {
			return false
		}
		if n, err := chat.PhoneNumber(number); err == nil {
			number = n
		}
		return slices.Contains(chat.NumbersAsNumbers(members), number)
	default:
		return NumberAllowed(p, number)
	}
}

// Authorize runs before an adapter downloads media or applies edits.
// Unknown groups and DMs may be offered in the dashboard; refused members
// of an allowed group must not be offered as an ignored group.
func Authorize(p Policy, e chat.Envelope) Decision {
	if e.Group {
		if !GroupAllowed(p, e.Chat) {
			return Decision{Ignore: "group not allowed"}
		}
		if !GroupSenderAllowed(p, e.Chat, e.Sender, e.SenderResolved, e.FromMe) {
			return Decision{Ignore: "sender not admitted by group_members"}
		}
	} else {
		if e.FromMe {
			return Decision{Ignore: "own message"}
		}
		open := len(p.AllowedNumbers) == 0 && p.AllowAnyDM
		if !open && (!e.SenderResolved || !NumberAllowed(p, e.Sender)) {
			return Decision{Ignore: "number not allowed"}
		}
	}
	return Decision{Allowed: true}
}

// Decide applies the existing transport-independent access and text
// rules. Quote stays apart from Text; the adapter decides whether a message
// with no text, saved media or quote has anything to hand to intake.
func Decide(p Policy, e chat.Envelope) Decision {
	d := Authorize(p, e)
	if !d.Allowed {
		return d
	}
	text := e.Text
	if !e.Group {
		if e.Caption != "" {
			if text == "" {
				text = e.Caption
			} else {
				text = e.Caption + "\n\n" + text
			}
		}
		d.Text = text
		return d
	}
	a := AddressGroup(p, e)
	d.Silent = !a.Tagged
	if a.ByCaption {
		text = e.TriggerCaption
	}
	var mentions []string
	if a.Mentioned {
		mentions = e.OwnMentions
	}
	text = StripMentions(p.Triggers.Strip(text), mentions)
	if e.Caption != "" {
		caption := StripMentions(p.Triggers.Strip(e.Caption), mentions)
		if text == "" {
			text = caption
		} else if caption != text {
			text = caption + "\n\n" + text
		}
	}
	d.Text = text
	return d
}

type Addressing struct{ Tagged, Mentioned, ByCaption, File bool }

func AddressGroup(p Policy, e chat.Envelope) Addressing {
	textMatches := p.Triggers.Matches(e.Text)
	a := Addressing{File: e.HasFile, Mentioned: e.MentionsMe, ByCaption: !textMatches && p.Triggers.Matches(e.TriggerCaption)}
	a.Tagged = textMatches || a.ByCaption || a.Mentioned || e.RepliesToMe || (p.Files && a.File) || (p.Forwards && e.Forwarded)
	return a
}

// StripMentions removes complete mentions of the bridge's own phone
// or LID user parts, preserving the established trigger boundary rules.
func StripMentions(text string, users []string) string {
	for _, user := range users {
		if user == "" {
			continue
		}
		mention := "@" + user
		for from := 0; from < len(text); {
			i := strings.Index(text[from:], mention)
			if i < 0 {
				break
			}
			i += from
			end := i + len(mention)
			if (i > 0 && !trigger.BoundaryBefore(text, i)) || !trigger.BoundaryAt(text, end) {
				from = end
				continue
			}
			text = text[:i] + text[end:]
			from = i
		}
	}
	return strings.TrimSpace(text)
}
