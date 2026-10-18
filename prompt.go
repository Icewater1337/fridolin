package main

import (
	"fmt"
	"strings"
	"time"
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
	History []historyEntry
	// Since is what went out to the chat after Message came in, while it
	// waited for its run, or waits in the outbox to go out ahead of the
	// run's reply, oldest first (see Brain.historyBefore).
	Since []historyEntry
	// Names is the brain's identity map, identityKey → name.
	Names map[string]string
	// Message is the message the run answers.
	Message Inbound
}

// askUserQuestionNote tells the model what becomes of AskUserQuestion in a
// run nobody can answer mid-way: the question goes to the chat (see
// extractAskQuestion), and its final reply follows it there.
const askUserQuestionNote = "Nobody can answer AskUserQuestion while you work: a question you ask with it goes to the chat at once, with its options, and the answer comes as a new message. Your final reply is sent as well, so after asking, end your turn without repeating the question."

// renderPrompt writes the -p prompt: the brain's instructions, the chat's
// recent history, what the brain sent since the new message came in, the
// attachments, then the new message.
func renderPrompt(in promptInput) string {
	var sb strings.Builder
	m := in.Message

	if in.Instructions != "" {
		sb.WriteString(in.Instructions)
		sb.WriteString("\n\n")
	}

	if n := len(in.History); n > 0 {
		what := "The message before the new one in this chat"
		if n > 1 {
			what = fmt.Sprintf("The %d messages before the new one in this chat, oldest first", n)
		}
		sb.WriteString("<chat_history>\n")
		sb.WriteString(what + " (for context, not action). ")
		sb.WriteString("You do NOT have prior turns in your working memory — the only context you have about previous conversation is what's in this block. ")
		sb.WriteString("Names come from your identity map at `" + identityMapFile + "`; the JID in parens is the raw WhatsApp identifier. ")
		sb.WriteString("For anything older or more detailed, search `" + historyFileName + "` with `\"$FRIDOLIN_BIN\" history QUERY`.\n\n")
		for _, e := range in.History {
			sb.WriteString(in.line(e))
		}
		sb.WriteString("</chat_history>\n\n")
	}

	if len(in.Since) > 0 {
		sb.WriteString("<sent_since>\n")
		sb.WriteString("What went out to this chat after the new message came in, while it waited for its run, or is on its way there ahead of your reply, oldest first: its sender had not seen these when they wrote it.\n\n")
		for _, e := range in.Since {
			sb.WriteString(in.line(e))
		}
		sb.WriteString("</sent_since>\n\n")
	}

	switch len(m.Media) {
	case 0:
	case 1:
		fmt.Fprintf(&sb, "A file was shared via WhatsApp and saved to: %s\n\n", m.Media[0])
	default:
		fmt.Fprintf(&sb, "%d files were shared together and saved to:\n", len(m.Media))
		for _, p := range m.Media {
			sb.WriteString("- " + p + "\n")
		}
		sb.WriteString("Handle them together as one request.\n\n")
	}

	// The kind of chat, which its JID does not tell a run: a build chat's
	// replies show in the dashboard, and files handed in through /ingest or
	// from Plaud did not come from the chat they are answered in.
	where := "a direct chat on WhatsApp"
	switch {
	case brainOfBuildJID(m.JID) != "":
		where = "the site-building chat on the dashboard's Build page, not a WhatsApp chat"
	case m.IsGroup():
		where = "the WhatsApp group " + m.JID
	}
	fmt.Fprintf(&sb, "It is now %s. This chat is %s.", in.Now.Format(promptNowLayout), where)
	if m.Source == sourceIngest {
		sb.WriteString(" The new message did not come from it: its files were handed to you outside WhatsApp, and your reply is sent to this chat.")
	}
	sb.WriteString("\n\n")

	sb.WriteString(askUserQuestionNote + "\n\n")
	sb.WriteString("<new_message>\n")
	fmt.Fprintf(&sb, "%s says: %s\n", in.name(m.Sender), m.textWithQuote())
	sb.WriteString("</new_message>")
	return sb.String()
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
func (in promptInput) line(e historyEntry) string {
	// An event's text says what its sender did, after their name.
	sep := ": "
	if e.Type == historyEvent {
		sep = " "
	}
	return fmt.Sprintf("[%s] %s%s%s\n", in.at(e.TS), in.from(e), sep, e.Text)
}

// from is who wrote the history entry e, as the prompt names them: the
// brain itself, or the sender as name gives them.
func (in promptInput) from(e historyEntry) string {
	if e.Type == historyOut {
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
func outboundSender(id string, entry historyEntry) string {
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
