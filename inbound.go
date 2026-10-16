package main

import (
	"fmt"
	"strings"
	"time"
)

// Inbound is one message on its way to a brain: from WhatsApp, from the
// /ingest endpoint, or injected in dev mode. Several WhatsApp messages sent in
// quick succession (an album, a caption plus files) are merged into one
// Inbound by the debouncer before they reach a brain.
type Inbound struct {
	JID    string   // chat: the DM partner's JID or the group JID
	Sender string   // phone number (or LID) of the person who wrote it
	Text   string   // message text as typed, triggers already stripped
	Media  []string // downloaded attachments, in arrival order
	// Brain is the id of the brain the message was handed to (/ingest, Plaud,
	// a dev message that names one). It wins over routing by chat: such a
	// message answers in JID, which may be a chat that belongs to another
	// brain. Empty routes by JID.
	Brain string
	// Quoted is the message this one replies to. It stays out of Text so
	// commands and the OTP guard see only what was typed: "!stop" or a
	// one-time code sent as a reply must still be recognised.
	Quoted *Quote
	// Silent means untagged group chatter: the brain reads it to keep up with
	// the conversation but sends nothing back.
	Silent bool
	Source string // "whatsapp", "ingest" or "dev"
	At     time.Time
}

// Quote is the message a reply quotes.
type Quote struct {
	From string // phone number (or LID) of whoever wrote it
	Text string // its text, shortened
}

// IsGroup reports whether the message came from a group chat.
func (m Inbound) IsGroup() bool { return strings.HasSuffix(m.JID, "@g.us") }

// textWithQuote is the text the model and the history see: a line naming the
// quoted message, then the message itself. The line's format is what the
// history has always held, and what tools/history_search.py finds.
func (m Inbound) textWithQuote() string {
	if m.Quoted == nil {
		return m.Text
	}
	line := fmt.Sprintf("[reply to %s: %q]", m.Quoted.From, m.Quoted.Text)
	if m.Text == "" {
		return line
	}
	return line + "\n" + m.Text
}

// merge folds a later message into m. The batch is tagged if any part was:
// a file followed by "@claude what is this?" must get an answer.
func (m Inbound) merge(next Inbound) Inbound {
	switch {
	case m.Text == "" && m.Quoted == nil:
		m.Text, m.Quoted = next.Text, next.Quoted
	case next.Text != "" || next.Quoted != nil:
		// Only the first quote stays apart. A later part's quote is written
		// into the text where it was, so the model still sees what each part
		// replies to; commands and the OTP guard ran before the merge.
		t := next.textWithQuote()
		switch {
		case t == m.textWithQuote():
			// The same part twice: nothing to add.
		case m.Text == "":
			m.Text = t
		default:
			m.Text += "\n\n" + t
		}
	}
	m.Media = append(append([]string(nil), m.Media...), next.Media...)
	m.Silent = m.Silent && next.Silent
	return m
}

// historyTextWithMedia is the text stored in conversation history: the message
// plus a line per attachment, so a later run can find the files again.
func historyTextWithMedia(text string, media []string) string {
	if len(media) == 0 {
		return text
	}
	var sb strings.Builder
	sb.WriteString(text)
	for _, p := range media {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("[attachment: " + p + "]")
	}
	return sb.String()
}
