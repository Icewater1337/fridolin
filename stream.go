package main

import (
	"encoding/json"
	"strings"
)

// The Claude CLI, run with --output-format stream-json, prints one JSON
// event per line. These types decode the parts of those events the bridge
// reads: assistant text, tool uses (AskUserQuestion is forwarded to the
// chat) and the final result.

type streamEvent struct {
	Type      string     `json:"type"`
	Subtype   string     `json:"subtype"`
	IsError   bool       `json:"is_error"`
	Message   *streamMsg `json:"message"`
	Result    string     `json:"result"`
	SessionID string     `json:"session_id"`
	Error     string     `json:"error"`
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
	Question string `json:"question"`
	Header   string `json:"header"`
}

// extractAskQuestion pulls the question text out of an AskUserQuestion input.
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
		text := q.Question
		if q.Header != "" && q.Header != q.Question {
			text = q.Header + ": " + text
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n")
}
