package main

import (
	"fmt"
	"strings"

	"fridolin/internal/textx"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
)

// What kind of message whatsmeow handed over, before any policy. Most are
// something to read; an edit or a deletion changes an earlier one.

// msgKind is what a WhatsApp message is to the bridge.
type msgKind int

const (
	// kindMessage is something to read: text, a file, a location, a
	// contact card, or a reply.
	kindMessage msgKind = iota
	// kindEdit replaces the text of an earlier message.
	kindEdit
	// kindRevoke deletes an earlier message for everyone.
	kindRevoke
	// kindViewOnce is a photo, video or voice note its sender meant to be
	// seen once. The bridge would keep it in the media folder for good and
	// hand it to the model, so it is not opened.
	kindViewOnce
	// kindOther is nothing the bridge reads: a reaction, a sticker, a
	// protocol message other than an edit or a deletion.
	kindOther
)

// classify says what evt is. For an edit or a deletion, target is the ID
// of the message it changes, and for an edit, content is the new message.
// whatsmeow has already unwrapped an edit's and a view-once message's
// wrappers (events.Message.UnwrapRaw): an edit arrives as the protocol
// message inside, a view-once message as the media with IsViewOnce set.
func classify(evt *events.Message) (kind msgKind, target string, content *waE2E.Message) {
	msg := evt.Message
	if pm := msg.GetProtocolMessage(); pm != nil {
		switch pm.GetType() {
		case waE2E.ProtocolMessage_REVOKE:
			return kindRevoke, pm.GetKey().GetID(), nil
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			return kindEdit, pm.GetKey().GetID(), pm.GetEditedMessage()
		}
		return kindOther, "", nil
	}
	if evt.IsViewOnce {
		return kindViewOnce, "", nil
	}
	if _, file := attachmentOf(msg, ""); file || extractText(msg) != "" || quoteOf(msg) != nil {
		return kindMessage, "", nil
	}
	return kindOther, "", nil
}

// locationText is a shared location as the model reads it.
func locationText(lat, lng float64, name, address, url string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[location: %.6f, %.6f", lat, lng)
	for _, s := range []string{name, address, url} {
		if s = strings.TrimSpace(s); s != "" {
			sb.WriteString(" — " + singleLine(s, 200))
		}
	}
	sb.WriteString("]")
	return sb.String()
}

// contactText is a shared contact card as the model reads it: the name
// and the numbers in the vCard.
func contactText(name, vcard string) string {
	parts := []string{}
	if name = strings.TrimSpace(name); name != "" {
		parts = append(parts, singleLine(name, 200))
	}
	for _, line := range strings.Split(vcard, "\n") {
		line = strings.TrimSpace(line)
		field, value, ok := strings.Cut(line, ":")
		if !ok || !strings.HasPrefix(strings.ToUpper(field), "TEL") {
			continue
		}
		// TEL;type=CELL;waid=15551234567:+1 555 123 4567
		if value = strings.TrimSpace(value); value != "" {
			parts = append(parts, singleLine(value, 40))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "[contact: " + strings.Join(parts, ", ") + "]"
}

// singleLine is s on one line, its runs of space and line breaks made one
// space, at most n bytes: a name or an address the sender chose must not
// break the "[…]" line it goes in.
func singleLine(s string, n int) string {
	return textx.Truncate(strings.Join(strings.Fields(s), " "), n)
}
