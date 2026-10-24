package chat

import (
	"fmt"
	"strings"
	"time"
	"unicode"
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
	// Silent means untagged group chatter: the brain keeps it in its history
	// to keep up with the conversation, gives it an observe-only run only
	// when it observes with runs (observeRuns), and sends nothing back.
	Silent bool
	Source string // SourceWhatsApp, SourceIngest, SourceDev or SourceSchedule
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
	// GitHub, when set, asks for a GitHub token for the run that answers
	// the message (docs/GITHUB.md): a scheduled agent's grant.
	GitHub *GitHubGrant
}

// GitHubGrant is what a run may do on GitHub: the bridge mints an
// installation token of its GitHub App for exactly these repositories
// (owner/name) and permissions ("issues": "write"), hands it to the run
// as gh's login (GH_CONFIG_DIR) and revokes it when the run ends. No
// permissions is contents and metadata read.
type GitHubGrant struct {
	Repos       []string          `json:"repos"`
	Permissions map[string]string `json:"permissions,omitempty"`
}

// Where a message came from, Inbound.Source.
const (
	SourceWhatsApp = "whatsapp"
	// SourceIngest is files handed in outside WhatsApp, through /ingest or
	// the Plaud integration: never a command, and never interrupting a
	// request (an observe-only run gives way to it like to any message).
	SourceIngest = "ingest"
	// SourceDev is the dashboard's dev page (--no-whatsapp).
	SourceDev = "dev"
	// SourceDelegate is a task the core brain delegated to a sub-brain, on
	// a chat of its own (DelegateChatPrefix): what the run answers goes
	// back to the core brain's waiting run, never to a WhatsApp chat.
	SourceDelegate = "delegate"
	// SourceDelegateResult is the result of a delegated task that came
	// after the core brain stopped waiting for it, handed to the core
	// brain in the chat its run was for: its answer goes to that chat.
	SourceDelegateResult = "delegate-result"
	// SourceSchedule is a message the bridge hands a brain when one of
	// its schedules or standing intents falls due. Like SourceIngest it
	// is never a command and never interrupts a request.
	SourceSchedule = "schedule"
	// SourceApprovalResult is the owner's answer to a run's
	// request_approval or ask_user that came after the run stopped
	// waiting for it (or its expiry), handed to the brain in the chat it
	// was asked in.
	SourceApprovalResult = "approval-result"
)

// DelegateChatPrefix starts the JID of a delegated task's chat,
// "delegate:<run id of the core brain's run>": no WhatsApp chat has one.
const DelegateChatPrefix = "delegate:"

// IsDelegateChat reports whether jid is a delegated task's chat.
func IsDelegateChat(jid string) bool { return strings.HasPrefix(jid, DelegateChatPrefix) }

// HandedIn reports whether a program handed m to a brain rather than
// someone writing it in a chat: files through /ingest or Plaud, a task
// the core brain delegated or its late result, a schedule or standing
// intent that fell due. Such a message is never a
// command, and neither interrupts a run nor is replaced by a later one.
func (m Inbound) HandedIn() bool {
	switch m.Source {
	case SourceIngest, SourceDelegate, SourceDelegateResult, SourceSchedule, SourceApprovalResult:
		return true
	}
	return false
}

// Quote is the message a reply quotes.
type Quote struct {
	From string // phone number (or LID) of whoever wrote it
	Text string // its text, shortened
}

// IsGroup reports whether the message came from a group chat.
func (m Inbound) IsGroup() bool { return IsGroupJID(m.JID) }

// IsGroupJID reports whether jid is a group chat's, …@g.us.
func IsGroupJID(jid string) bool { return strings.HasSuffix(jid, "@g.us") }

// DMJID is the JID of the direct chat with a phone number, given as
// allowed_numbers has it (country code, no +).
func DMJID(number string) string { return number + "@s.whatsapp.net" }

// TextWithQuote is the text the model and the history see: a line naming the
// quoted message, then the message itself. The line's format is what the
// history has always held, and what `fridolin history` finds.
func (m Inbound) TextWithQuote() string {
	if m.Quoted == nil {
		return m.Text
	}
	line := fmt.Sprintf("[reply to %s: %q]", m.Quoted.From, m.Quoted.Text)
	if m.Text == "" {
		return line
	}
	return line + "\n" + m.Text
}

// Names reports whether c is about the message p.
func (c Change) Names(p Inbound) bool {
	sameChat := p.JID == c.Chat || (c.ChatAlt != "" && p.JID == c.ChatAlt) || (p.ChatAlt != "" && p.ChatAlt == c.Chat)
	return c.Target != "" && p.ID == c.Target && sameChat && p.Sender == c.Sender
}

// Change is an edit or a deletion of an earlier WhatsApp message,
// which Target names, in Chat (with ChatAlt as in Inbound).
type Change struct {
	Kind    string // ChangeEdit or ChangeRevoke
	Chat    string
	ChatAlt string
	Sender  string // who changed it, as Inbound.Sender
	Target  string // the ID of the message changed
	Text    string // an edit's new text, triggers stripped
}

// The kinds of Change.
const (
	ChangeEdit   = "edit"
	ChangeRevoke = "revoke"
)

// Merge folds the text, quote, files and silence of next into m.
func (m Inbound) Merge(next Inbound) Inbound {
	switch {
	case m.Text == "" && m.Quoted == nil:
		m.Text, m.Quoted = next.Text, next.Quoted
	case next.Text != "" || next.Quoted != nil:
		// Only the first quote stays apart. A later part's quote is written
		// into the text where it was, so the model still sees what each part
		// replies to; commands and the OTP guard ran before the merge.
		t := next.TextWithQuote()
		switch {
		case t == m.TextWithQuote():
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

// HistoryText is the text stored in conversation history: the message
// plus a line per attachment, so a later run can find the files again.
func HistoryText(text string, media []string) string {
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

// NormalizeJID strips the WhatsApp server suffix and a leading + from a JID,
// e.g. "15551234567@s.whatsapp.net" and "+15551234567" both become "15551234567".
// Spaces around the number and further +s in front of it go too, so that
// normalising the result again changes nothing: a number saved normalised
// still equals the normalised JID of its sender.
func NormalizeJID(jid string) string {
	if idx := strings.Index(jid, "@"); idx != -1 {
		jid = jid[:idx]
	}
	jid = strings.TrimLeftFunc(jid, func(r rune) bool { return r == '+' || unicode.IsSpace(r) })
	return strings.TrimRightFunc(jid, unicode.IsSpace)
}

// LinkState is the connection state shown on the dashboard.
type LinkState struct {
	// Mode: disabled (dev mode) or one of the mode constants in
	// waconn.go: connecting, pairing, qr-expired, connected,
	// disconnected, logged-out, replaced, outdated, banned, failed.
	Mode      string    `json:"mode"`
	OwnNumber string    `json:"own_number,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	Since     time.Time `json:"since"`
	QRCode    string    `json:"-"` // current pairing code; served as an image, never as text
	QRAt      time.Time `json:"-"`
}

// Group is a group the bridge account belongs to.
type Group struct {
	JID          string `json:"jid"`
	Name         string `json:"name"`
	Participants int    `json:"participants"`
}
