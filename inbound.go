package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// Inbound is one message on its way to a brain: from WhatsApp, from the
// /ingest endpoint, or injected in dev mode. Several WhatsApp messages sent in
// quick succession (an album, a caption plus files) are merged into one
// Inbound by the debouncer before they reach a brain.
type Inbound struct {
	// JID is the chat: the group's JID, or for a DM the partner's phone
	// number's JID (their LID's when the number is not known).
	JID string
	// ChatAlt is the DM's other JID, the partner's LID, when known: what
	// the chat may have gone by before, in the history and in hand-offs.
	ChatAlt string
	Sender  string   // phone number (or LID) of the person who wrote it
	Text    string   // message text as typed, triggers already stripped
	Media   []string // downloaded attachments, in arrival order
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
	Source string // sourceWhatsApp, sourceIngest or sourceDev
	At     time.Time
	// ID is the WhatsApp message's ID, "" for one from elsewhere. An edit
	// or a deletion on WhatsApp names the message it changes by it.
	ID string
	// Done, when set, hears how the message ended once its brain took it:
	// nil after a run that answered it, else why not (the run failed or
	// was stopped, or the message was dropped before it ran). A refused
	// Submit calls nothing. Only a hand-off that skips the debouncer sets
	// it (Bridge.submitHandOff); a merged message keeps the first one's.
	Done func(error)

	// parts are the messages this one was merged from, oldest first, when
	// there were several (see merge), so that an edit or a deletion of one
	// of them can redo the merge (see applyChange). Empty for one message.
	parts []Inbound
}

// Where a message came from, Inbound.Source.
const (
	sourceWhatsApp = "whatsapp"
	// sourceIngest is files handed in outside WhatsApp, through /ingest or
	// the Plaud integration: never a command, and never interrupting.
	sourceIngest = "ingest"
	// sourceDev is the dashboard's dev page (--no-whatsapp).
	sourceDev = "dev"
)

// Quote is the message a reply quotes.
type Quote struct {
	From string // phone number (or LID) of whoever wrote it
	Text string // its text, shortened
}

// IsGroup reports whether the message came from a group chat.
func (m Inbound) IsGroup() bool { return isGroupJID(m.JID) }

// isGroupJID reports whether jid is a group chat's, …@g.us.
func isGroupJID(jid string) bool { return strings.HasSuffix(jid, "@"+types.GroupServer) }

// userJID is the JID of the direct chat with a phone number, given as
// allowed_numbers has it (country code, no +).
func userJID(number string) string { return number + "@" + types.DefaultUserServer }

// textWithQuote is the text the model and the history see: a line naming the
// quoted message, then the message itself. The line's format is what the
// history has always held, and what `fridolin history` finds.
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
	parts := append(m.ownParts(), next.ownParts()...)
	m = m.mergeContent(next)
	m.parts = parts
	return m
}

// ownParts returns the messages m is made of: its parts, or m itself.
func (m Inbound) ownParts() []Inbound {
	if len(m.parts) > 0 {
		return append([]Inbound(nil), m.parts...)
	}
	m.parts = nil
	return []Inbound{m}
}

// changes reports whether c changes one of m's parts: the message c
// names, in the same chat, from the same sender. A client can put any ID
// in an edit or a deletion, and one group member must not rewrite or
// cancel what another asked for; so a group admin's deletion of someone
// else's message changes nothing here either.
func (m Inbound) changes(c messageChange) bool {
	return slices.ContainsFunc(m.ownParts(), c.names)
}

// names reports whether c is about the message p.
func (c messageChange) names(p Inbound) bool {
	sameChat := p.JID == c.Chat || (c.ChatAlt != "" && p.JID == c.ChatAlt) || (p.ChatAlt != "" && p.ChatAlt == c.Chat)
	return c.Target != "" && p.ID == c.Target && sameChat && p.Sender == c.Sender
}

// remerge merges parts again, as the debouncer did, into one message.
func remerge(parts []Inbound) Inbound {
	m := parts[0]
	for _, p := range parts[1:] {
		m = m.merge(p)
	}
	return m
}

// applyChange applies c to m: the part c names is deleted, or gets its
// new text. It reports whether m holds that part, and keep is false when
// nothing is left of m.
func (m Inbound) applyChange(c messageChange) (out Inbound, found, keep bool) {
	parts := m.ownParts()
	i := slices.IndexFunc(parts, c.names)
	if i < 0 {
		return m, false, true
	}
	switch c.Kind {
	case changeRevoke:
		parts = append(parts[:i], parts[i+1:]...)
	case changeEdit:
		parts[i].Text = c.Text
	}
	if len(parts) == 0 {
		return Inbound{}, true, false
	}
	return remerge(parts), true, true
}

// messageChange is an edit or a deletion of an earlier WhatsApp message,
// which Target names, in Chat (with ChatAlt as in Inbound).
type messageChange struct {
	Kind    string // changeEdit or changeRevoke
	Chat    string
	ChatAlt string
	Sender  string // who changed it, as Inbound.Sender
	Target  string // the ID of the message changed
	Text    string // an edit's new text, triggers stripped
}

// The kinds of messageChange.
const (
	changeEdit   = "edit"
	changeRevoke = "revoke"
)

// mergeContent folds the text, quote, files and silence of next into m.
func (m Inbound) mergeContent(next Inbound) Inbound {
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
