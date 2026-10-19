package whatsapp

import (
	"slices"

	"github.com/Icewater1337/fridolin/internal/chat"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// groupEnvelope projects only message facts; authorization and addressing
// remain pure, and downloads/name lookups stay on the chat worker.
func groupEnvelope(msg *waE2E.Message, text string, own []types.JID) chat.Envelope {
	e := chat.Envelope{Group: true, Text: text}
	if att, ok := attachmentOf(msg, ""); ok {
		e.HasFile, e.Caption = true, att.caption
	}
	if img := msg.GetImageMessage(); img != nil {
		e.TriggerCaption = img.GetCaption()
	} else if doc := msg.GetDocumentMessage(); doc != nil {
		e.TriggerCaption = doc.GetCaption()
	}
	for _, ci := range contextInfos(msg) {
		e.Forwarded = e.Forwarded || ci.GetIsForwarded() || ci.GetForwardingScore() > 0
		e.MentionsMe = e.MentionsMe || slices.ContainsFunc(ci.GetMentionedJID(), func(j string) bool { return isOwnJID(j, own) })
		e.RepliesToMe = e.RepliesToMe || (ci.GetQuotedMessage() != nil && isOwnJID(ci.GetParticipant(), own))
	}
	for _, jid := range own {
		e.OwnMentions = append(e.OwnMentions, jid.User)
	}
	return e
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
