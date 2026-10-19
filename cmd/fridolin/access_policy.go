package main

import (
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/trigger"
)

// accessPolicy is a read-only projection of one configuration snapshot.
// A caller supplies the routing rules for the group being decided.
type accessPolicy struct {
	AllowedNumbers, AllowedGroups []string
	AllowAnyDM                    bool
	GroupMembers                  map[string][]string
	Triggers                      trigger.List
	Files, Forwards               bool
}

type accessDecision struct {
	Allowed bool
	Ignore  string
	Silent  bool
	Text    string
}

func accessNumberAllowed(p accessPolicy, number string) bool {
	if len(p.AllowedNumbers) == 0 {
		return p.AllowAnyDM
	}
	if n, err := phoneNumber(number); err == nil {
		number = n
	}
	return slices.Contains(allowedNumbersAsNumbers(p.AllowedNumbers), number)
}

func accessGroupAllowed(p accessPolicy, group string) bool {
	return slices.Contains(p.AllowedGroups, group)
}

func accessGroupSenderAllowed(p accessPolicy, group, number string, resolved, fromMe bool) bool {
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
		if n, err := phoneNumber(number); err == nil {
			number = n
		}
		return slices.Contains(allowedNumbersAsNumbers(members), number)
	default:
		return accessNumberAllowed(p, number)
	}
}

// accessAuthorize runs before an adapter downloads media or applies edits.
// Unknown groups and DMs may be offered in the dashboard; refused members
// of an allowed group must not be offered as an ignored group.
func accessAuthorize(p accessPolicy, e chat.Envelope) accessDecision {
	if e.Group {
		if !accessGroupAllowed(p, e.Chat) {
			return accessDecision{Ignore: "group not allowed"}
		}
		if !accessGroupSenderAllowed(p, e.Chat, e.Sender, e.SenderResolved, e.FromMe) {
			return accessDecision{Ignore: "sender not admitted by group_members"}
		}
	} else {
		if e.FromMe {
			return accessDecision{Ignore: "own message"}
		}
		open := len(p.AllowedNumbers) == 0 && p.AllowAnyDM
		if !open && (!e.SenderResolved || !accessNumberAllowed(p, e.Sender)) {
			return accessDecision{Ignore: "number not allowed"}
		}
	}
	return accessDecision{Allowed: true}
}

// accessDecide applies the existing transport-independent access and text
// rules. Quote stays apart from Text; the adapter decides whether a message
// with no text, saved media or quote has anything to hand to intake.
func accessDecide(p accessPolicy, e chat.Envelope) accessDecision {
	d := accessAuthorize(p, e)
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
	a := accessAddress(p, e)
	d.Silent = !a.Tagged
	if a.ByCaption {
		text = e.TriggerCaption
	}
	var mentions []string
	if a.Mentioned {
		mentions = e.OwnMentions
	}
	text = accessStripMentions(p.Triggers.Strip(text), mentions)
	if e.Caption != "" {
		caption := accessStripMentions(p.Triggers.Strip(e.Caption), mentions)
		if text == "" {
			text = caption
		} else if caption != text {
			text = caption + "\n\n" + text
		}
	}
	d.Text = text
	return d
}

type accessAddressing struct{ Tagged, Mentioned, ByCaption, File bool }

func accessAddress(p accessPolicy, e chat.Envelope) accessAddressing {
	textMatches := p.Triggers.Matches(e.Text)
	a := accessAddressing{File: e.HasFile, Mentioned: e.MentionsMe, ByCaption: !textMatches && p.Triggers.Matches(e.TriggerCaption)}
	a.Tagged = textMatches || a.ByCaption || a.Mentioned || e.RepliesToMe || (p.Files && a.File) || (p.Forwards && e.Forwarded)
	return a
}

// accessStripMentions removes complete mentions of the bridge's own phone
// or LID user parts, preserving the established trigger boundary rules.
func accessStripMentions(text string, users []string) string {
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
