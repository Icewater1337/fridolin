package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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

// StreamSink is what parseStream tells about a run's output while it reads
// it: the lines of the brain's activity log, the questions the model asks,
// and the result as soon as it is in.
type StreamSink interface {
	// Log takes one line of the activity log.
	Log(line string)
	// Question takes a question the model asked with AskUserQuestion, as
	// the chat would get it, and reports whether it is on its way: only
	// then does the run count as one that asked.
	Question(text string) bool
	// Result takes the run's result once its event is in. The output may
	// go on after it: a command the run left in the background can hold
	// it open.
	Result(res RunResult)
}

// parseStream reads the stream-json output of one run of the Claude CLI and
// returns the run's outcome, telling sink what it finds on the way. It does
// nothing else: writing the activity log and sending a question to the chat
// are for the sink to do (see Brain.readStream), so that the parser can be
// run on a recorded stream alone.
func parseStream(r io.Reader, sink StreamSink) RunResult {
	return parseAgentStream(r, sink, "claude")
}

func parseAgentStream(r io.Reader, sink StreamSink, provider string) RunResult {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)

	var finalResult, lastText string
	asked := false
	var codex codexStream

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}

		var event streamEvent
		if provider == "codex" {
			event = codex.event([]byte(line))
		} else if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		switch event.Type {
		case "assistant":
			if event.Message == nil {
				continue
			}
			for _, content := range event.Message.Content {
				switch content.Type {
				case "text":
					if content.Text != "" {
						lastText = content.Text
						sink.Log("💬 " + truncate(content.Text, 2000))
					}
				case "tool_use":
					sink.Log("🔧 Tool: " + content.Name)
					if content.Name == "AskUserQuestion" {
						// Only a question on its way counts as asked.
						if question := extractAskQuestion(content.Input); question != "" && sink.Question(question) {
							asked = true
						}
					}
				}
			}

		case "result":
			if event.IsError || event.Error != "" {
				errMsg := event.Error
				if errMsg == "" {
					errMsg = event.Result
				}
				if errMsg == "" {
					// The CLI reports some failures (authentication, quota) only
					// as its last message.
					errMsg = truncate(lastText, 300)
				}
				if errMsg == "" {
					errMsg = "(claude exited with an error but no message)"
				}
				sink.Log("❌ Error: " + errMsg)
				return RunResult{Err: &cliError{msg: errMsg, provider: provider}, Asked: asked}
			}
			finalResult = event.Result
			sink.Log("✅ Result: " + truncate(finalResult, 2000))
			sink.Result(RunResult{Text: finalResult, Asked: asked})
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		if errors.Is(err, os.ErrClosed) {
			return RunResult{Err: errBrainInterrupted}
		}
		return RunResult{Err: fmt.Errorf("reading %s output: %w", agentName(provider), err)}
	}

	// A -p run is stateless and nobody answers its AskUserQuestion, so the
	// model goes on after asking, often to an answer of its own ("I booked
	// Monday"), which the chat must get too. A run that only asked has said
	// what it had to say.
	if finalResult == "" && !asked {
		if provider == "codex" && codex.err != "" {
			return RunResult{Err: &cliError{msg: codex.err, provider: provider}}
		}
		return RunResult{Err: errNoResult}
	}

	return RunResult{Text: finalResult, Asked: asked}
}
