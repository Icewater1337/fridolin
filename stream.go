package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The Claude CLI, run with --output-format stream-json, prints one JSON
// event per line. These types decode the parts of those events the bridge
// reads: assistant text, tool uses (AskUserQuestion is forwarded to the
// chat) and the final result.

type streamEvent struct {
	Type    string     `json:"type"`
	Subtype string     `json:"subtype"`
	IsError bool       `json:"is_error"`
	Message *streamMsg `json:"message"`
	Result  string     `json:"result"`
	Error   string     `json:"error"`
}

type streamMsg struct {
	Content []streamContent `json:"content"`
}

type streamContent struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type askInput struct {
	Questions []askQuestion `json:"questions"`
}

type askQuestion struct {
	Question    string      `json:"question"`
	Header      string      `json:"header"`
	Options     []askOption `json:"options"`
	MultiSelect bool        `json:"multiSelect"`
}

type askOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// extractAskQuestion turns an AskUserQuestion input into the message the
// chat gets: each question with its header and its options numbered, so
// that a reply can name one. Nobody answers the tool call in a -p run; the
// answer arrives as the chat's next message. A question that is blank is
// left out, and so is a blank option: an empty message, or an empty
// choice, asks nothing.
func extractAskQuestion(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var ask askInput
	if err := json.Unmarshal(raw, &ask); err != nil {
		return ""
	}
	var parts []string
	for _, q := range ask.Questions {
		text, header := strings.TrimSpace(q.Question), strings.TrimSpace(q.Header)
		if text == "" {
			continue
		}
		if header != "" && header != text {
			text = header + ": " + text
		}
		n := 0
		for _, o := range q.Options {
			label, desc := strings.TrimSpace(o.Label), strings.TrimSpace(o.Description)
			if label == "" {
				continue
			}
			n++
			text += fmt.Sprintf("\n%d. %s", n, label)
			if desc != "" {
				text += " – " + desc
			}
		}
		if n > 1 && q.MultiSelect {
			text += "\n(You can pick more than one.)"
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n\n")
}
