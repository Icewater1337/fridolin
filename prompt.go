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
	// Instructions is the brain's prompt file, trimmed; "" for none.
	Instructions string
	// BrainID names the brain where the history shows what it said.
	BrainID string
	// History is the chat's recent messages, oldest first.
	History []historyEntry
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
// recent history, the attachments, then the new message.
func renderPrompt(in promptInput) string {
	var sb strings.Builder
	m := in.Message
	isGroup := m.IsGroup()

	if in.Instructions != "" {
		sb.WriteString(in.Instructions)
		sb.WriteString("\n\n")
	}

	if len(in.History) > 0 {
		count := "10 messages"
		if isGroup {
			count = "20 messages"
		}
		sb.WriteString("<chat_history>\n")
		sb.WriteString("The following is the last " + count + " in this WhatsApp chat (for context, not action). ")
		sb.WriteString("You do NOT have prior turns in your working memory — the only context you have about previous conversation is what's in this block. ")
		sb.WriteString("Names come from your identity map at `" + identityMapFile + "`; the JID in parens is the raw WhatsApp identifier. ")
		sb.WriteString("For anything older or more detailed, search `" + historyFileName + "` with `\"$FRIDOLIN_BIN\" history QUERY`.\n\n")
		for _, e := range in.History {
			ts, _ := time.Parse(time.RFC3339, e.TS)
			// An event's text says what its sender did, after their name.
			sep := ": "
			if e.Type == historyEvent {
				sep = " "
			}
			fmt.Fprintf(&sb, "[%s] %s%s%s\n", ts.Format("15:04"), in.from(e), sep, e.Text)
		}
		sb.WriteString("</chat_history>\n\n")
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

	sb.WriteString(askUserQuestionNote + "\n\n")
	sb.WriteString("<new_message>\n")
	fmt.Fprintf(&sb, "%s says: %s\n", m.Sender, m.textWithQuote())
	sb.WriteString("</new_message>")
	return sb.String()
}

// from is who wrote the history entry e, as the prompt names them: the
// brain itself, a name from the identity map with the raw identifier, or
// that alone.
func (in promptInput) from(e historyEntry) string {
	if e.Type == historyOut {
		return outboundSender(in.BrainID, e)
	}
	if name, ok := in.Names[identityKey(e.From)]; ok {
		return fmt.Sprintf("%s (%s)", name, e.From)
	}
	return e.From
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
