package main

import (
	"slices"

	"github.com/Icewater1337/fridolin/internal/trigger"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// Whether a group message addresses the bridge. One that does is
// "tagged" and gets a visible reply; the rest is chatter, which the brain
// reads without answering.

// groupRules are the parts of a brain's routing that decide what
// addresses the bridge in the brain's groups (see BrainRouting).
type groupRules struct {
	files    bool // a file counts as addressing the bridge
	forwards bool // a forwarded message does
}

// defaultGroupRules are what a group without a brain, or a brain that
// sets neither, goes by: files and forwards address the bridge, as they
// always did.
var defaultGroupRules = groupRules{files: true, forwards: true}

// groupAddressing is how a group message addresses the bridge.
type groupAddressing struct {
	tagged    bool // the bridge answers it
	mentioned bool // it @-mentions the bridge, whose mention is then stripped
	byCaption bool // the trigger is in an image's or document's caption, not in the text
	file      bool // it carries a file that counts as one (see attachmentOf)
}

// addressGroup decides whether msg, with the text text, addresses the
// bridge, whose own JIDs (number and LID) are own. A trigger in the text
// or in an image's or document's caption does, and so does an @-mention
// of the bridge or a quote-reply to one of its messages; a file and a
// forward do as rules say. Video captions do not count: they are the
// video's metadata, and a video is a file.
func addressGroup(msg *waE2E.Message, text string, triggers trigger.List, own []types.JID, rules groupRules) groupAddressing {
	var a groupAddressing
	_, a.file = attachmentOf(msg, "")
	caption := ""
	if img := msg.GetImageMessage(); img != nil {
		caption = img.GetCaption()
	} else if doc := msg.GetDocumentMessage(); doc != nil {
		caption = doc.GetCaption()
	}
	textMatches := triggers.Matches(text)
	a.byCaption = !textMatches && triggers.Matches(caption)

	forwarded, repliesToBridge := false, false
	for _, ci := range contextInfos(msg) {
		forwarded = forwarded || ci.GetIsForwarded() || ci.GetForwardingScore() > 0
		a.mentioned = a.mentioned || slices.ContainsFunc(ci.GetMentionedJID(), func(j string) bool { return isOwnJID(j, own) })
		repliesToBridge = repliesToBridge || (ci.GetQuotedMessage() != nil && isOwnJID(ci.GetParticipant(), own))
	}

	a.tagged = textMatches || a.byCaption || a.mentioned || repliesToBridge ||
		(rules.files && a.file) || (rules.forwards && forwarded)
	return a
}

// contextInfos returns the ContextInfo of each part of msg that can carry
// one. Mentions, quotes and the forwarded flag usually ride on the text,
// but some clients put them on the media message instead.
func contextInfos(msg *waE2E.Message) []*waE2E.ContextInfo {
	var out []*waE2E.ContextInfo
	for _, ci := range []*waE2E.ContextInfo{
		msg.GetExtendedTextMessage().GetContextInfo(),
		msg.GetImageMessage().GetContextInfo(),
		msg.GetVideoMessage().GetContextInfo(),
		msg.GetDocumentMessage().GetContextInfo(),
		msg.GetAudioMessage().GetContextInfo(),
	} {
		if ci != nil {
			out = append(out, ci)
		}
	}
	return out
}

// isOwnJID reports whether jid, as a message names it, is one of own, the
// bridge account's JIDs without a device.
func isOwnJID(jid string, own []types.JID) bool {
	parsed, err := types.ParseJID(jid)
	if err != nil || parsed.User == "" {
		return false
	}
	return slices.Contains(own, parsed.ToNonAD())
}
