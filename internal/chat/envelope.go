package chat

import "time"

// Envelope is a transport's description of a message before access policy
// strips addressing text. It contains no transport or runtime objects.
type Envelope struct {
	Chat, Sender, PushName string
	Group, FromMe          bool
	// SenderResolved distinguishes a phone identity from an opaque WhatsApp
	// LID. An unresolved identity must not match a configured phone by digits.
	SenderResolved bool
	Text, Caption  string
	// TriggerCaption is only an image or document caption. Video captions
	// remain content but do not address the bridge on their own.
	TriggerCaption                              string
	HasFile, MentionsMe, Forwarded, RepliesToMe bool
	OwnMentions                                 []string
	Quote                                       *Quote
	At                                          time.Time
}
