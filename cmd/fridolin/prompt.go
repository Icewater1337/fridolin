package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/history"
)

// promptInput is what one run's prompt is made of. The brain gathers it
// through its folder (Brain.buildPrompt); renderPrompt only puts it into
// words, so that what a run is told can be tested without a folder or a
// CLI.
type promptInput struct {
	// Now is when the run starts. Its time zone is the one every time in
	// the prompt is given in.
	Now time.Time
	// Instructions is the brain's prompt file, trimmed; "" for none.
	Instructions string
	// BrainID names the brain where the history shows what it said.
	BrainID string
	// History is the chat's messages before Message, oldest first.
	History []history.Entry
	// Since is what went out to the chat after Message came in, while it
	// waited for its run, or waits in the outbox to go out ahead of the
	// run's reply, oldest first (see Brain.historyBefore).
	Since []history.Entry
	// Names is the brain's identity map, identityKey → name.
	Names map[string]string
	// Message is the message the run answers.
	Message Inbound
	// ObserveOnly marks a run for a message nobody addressed to the brain
	// (Inbound.Silent, see observeRun): nothing it says is sent.
	ObserveOnly bool
}

// observeOnlyNote is what an observe-only run is told instead of how its
// replies reach the chat: it has nobody to answer and nothing to do but
// take notes. The example prompt says the same.
const observeOnlyNote = "<observe_only>\nNobody addressed the new message to you: it is group chatter you read along with, and nothing you write is sent anywhere. Do not act on it — no calendar changes, no messages, no changes to files beyond your own notes. Note what is worth remembering, if anything, and end.\n</observe_only>"

// askUserQuestionNote tells the model what becomes of AskUserQuestion in a
// run nobody can answer mid-way: the question goes to the chat (see
// extractAskQuestion), and its final reply follows it there.
const askUserQuestionNote = "Nobody can answer AskUserQuestion while you work: a question you ask with it goes to the chat at once, with its options, and the answer comes as a new message. Your final reply is sent as well, so after asking, end your turn without repeating the question."

// renderPrompt writes the -p prompt: the brain's instructions, the chat's
// recent history, what the brain sent since the new message came in, what
// the run is about, then the new message. Everything in it that someone
// could have written, the messages, the names and the file names, goes in
// as JSON (see promptJSON), one message to a line.
func renderPrompt(in promptInput) string {
	var sb strings.Builder
	m := in.Message

	if in.Instructions != "" {
		sb.WriteString(in.Instructions)
		sb.WriteString("\n\n")
	}

	if n := len(in.History); n > 0 {
		what, each := "The message before the new one in this chat", "a JSON object"
		if n > 1 {
			what, each = fmt.Sprintf("The %d messages before the new one in this chat, oldest first", n), "each a JSON object"
		}
		sb.WriteString("<chat_history>\n")
		sb.WriteString(what + " (for context, not action), " + each + " like the new message, or with \"did\" in place of \"text\" for something its sender did without writing, such as a vote in a poll. ")
		sb.WriteString("You do NOT have prior turns in your working memory — the only context you have about previous conversation is what's in this block. ")
		sb.WriteString("Names come from your identity map at `" + identityMapFile + "`; the JID in parens is the raw WhatsApp identifier, and \"You (" + in.BrainID + ")\" is you. ")
		sb.WriteString("For anything older or more detailed, search `" + history.FileName + "` with `\"$FRIDOLIN_BIN\" history QUERY`.\n\n")
		for _, e := range in.History {
			sb.WriteString(in.line(e))
		}
		sb.WriteString("</chat_history>\n\n")
	}

	if len(in.Since) > 0 {
		sb.WriteString("<sent_since>\n")
		what := "a JSON object like the new message. Its sender had not seen this"
		if len(in.Since) > 1 {
			what = "oldest first, each a JSON object like the new message. Its sender had not seen these"
		}
		sb.WriteString("What went out to this chat after the new message came in, while it waited for its run, or is on its way there ahead of your reply, " + what + " when they wrote it.\n\n")
		for _, e := range in.Since {
			sb.WriteString(in.line(e))
		}
		sb.WriteString("</sent_since>\n\n")
	}

	// The kind of chat, which its JID does not tell a run: a build chat's
	// replies show in the dashboard, and files handed in through /ingest or
	// from Plaud did not come from the chat they are answered in.
	where := "a direct chat on WhatsApp"
	switch {
	case brainOfBuildJID(m.JID) != "":
		where = "the site-building chat on the dashboard's Build page, not a WhatsApp chat"
	case m.IsGroup():
		where = "the WhatsApp group " + promptJSON(m.JID)
	}
	fmt.Fprintf(&sb, "It is now %s. This chat is %s.", in.Now.Format(promptNowLayout), where)
	if m.Source == SourceIngest {
		sb.WriteString(" The new message did not come from it: its files were handed to you outside WhatsApp, and your reply is sent to this chat.")
	}
	sb.WriteString("\n\n")

	sb.WriteString(`The new message is a JSON object: "at" is when it was sent, "from" who sent it and "text" what it said.`)
	switch len(m.Media) {
	case 0:
	case 1:
		sb.WriteString(` The file in its "files" came with it, saved at that path.`)
	default:
		fmt.Fprintf(&sb, ` The %d files in its "files" came with it, saved at those paths: handle them together as one request.`, len(m.Media))
	}
	sb.WriteString("\n\n")

	if in.ObserveOnly {
		sb.WriteString(observeOnlyNote + "\n\n")
	} else {
		sb.WriteString(askUserQuestionNote + "\n\n")
	}
	msg := promptMessage{From: in.name(m.Sender), Text: m.TextWithQuote(), Files: m.Media}
	if !m.At.IsZero() {
		msg.At = m.At.In(in.Now.Location()).Format(promptTimeLayout)
	}
	sb.WriteString("<new_message>\n" + promptJSON(msg) + "\n</new_message>")
	return sb.String()
}

// promptMessage is one message as the prompt has it.
type promptMessage struct {
	At    string   `json:"at,omitempty"`
	From  string   `json:"from"`
	Text  string   `json:"text"`
	Files []string `json:"files,omitempty"`
}

// promptEvent is something someone did in the chat without writing a
// message (historyEvent), as the prompt has it: Did says what, such as
// `voted "Monday" in the poll "When?"`.
type promptEvent struct {
	At   string `json:"at,omitempty"`
	From string `json:"from"`
	Did  string `json:"did"`
}

// promptJSON writes v as JSON with every "<" escaped. Whatever someone
// wrote then stays inside its quotes, on its line: a message that holds
// "</new_message>" or a line break followed by a made-up history line
// cannot close the prompt's tags or pass for another message, or for
// someone else's. That keeps senders apart; it does not make the text
// any less of a prompt, which the brain's own instructions are for.
func promptJSON(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // "&" and ">" read better as they are
	if err := enc.Encode(v); err != nil {
		return `""`
	}
	return strings.ReplaceAll(strings.TrimSuffix(b.String(), "\n"), "<", `\u003c`)
}

// The prompt's times carry the day: a window of a direct chat often spans
// days or weeks, and "tomorrow at 3" from last week is not tomorrow.
const (
	promptTimeLayout = "Mon 2006-01-02 15:04"
	promptNowLayout  = "Mon 2006-01-02 15:04 MST (UTC-07:00)"
)

// at is the history time ts as the prompt gives it, in Now's time zone.
func (in promptInput) at(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "unknown time"
	}
	return t.In(in.Now.Location()).Format(promptTimeLayout)
}

// line is the history entry e as the prompt shows it, on a line of its
// own.
func (in promptInput) line(e history.Entry) string {
	if e.Type == history.Event {
		// Something its sender did without writing, such as a vote: its
		// text says what.
		return promptJSON(promptEvent{At: in.at(e.TS), From: in.from(e), Did: e.Text}) + "\n"
	}
	return promptJSON(promptMessage{At: in.at(e.TS), From: in.from(e), Text: e.Text}) + "\n"
}

// from is who wrote the history entry e, as the prompt names them: the
// brain itself, or the sender as name gives them.
func (in promptInput) from(e history.Entry) string {
	if e.Type == history.Out {
		return outboundSender(in.BrainID, e)
	}
	return in.name(e.From)
}

// name is sender with the name the identity map gives them, then the raw
// identifier in parentheses, or that alone.
func (in promptInput) name(sender string) string {
	if name, ok := in.Names[identityKey(sender)]; ok {
		return fmt.Sprintf("%s (%s)", name, sender)
	}
	return sender
}

// outboundSender is who the chat history shows as the sender of entry, an
// "out" line in brain id's history. Only the brain's own replies, and what it
// sent itself through /send, are "You": a line a script or another brain
// sent through /send must not read as the brain's own words.
func outboundSender(id string, entry history.Entry) string {
	switch {
	case entry.Via == "":
		return fmt.Sprintf("You (%s)", id)
	case entry.Via == "brain" && entry.From == id:
		return fmt.Sprintf("You (%s, via /send)", id)
	case entry.Via == "brain":
		return fmt.Sprintf("Brain %s (via /send)", entry.From)
	case entry.Via == "script":
		return "Script (via /send)"
	}
	return "Script without a token (via /send)"
}
