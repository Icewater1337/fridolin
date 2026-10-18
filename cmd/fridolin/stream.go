package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Icewater1337/fridolin/internal/textx"
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
	// Result takes the run's result once its event is in, an error result
	// too. The output may go on after it: a command the run left in the
	// background can hold it open.
	Result(res RunResult)
}

// maxStreamLine is the longest line of the CLI's output the parser reads.
// An event is far shorter, unless it carries a tool's large result, a file
// read whole say: such a line is skipped, and the run goes on to its
// result.
const maxStreamLine = 4 << 20

// parseStream reads the stream-json output of one run of the Claude CLI and
// returns the run's outcome, telling sink what it finds on the way. It does
// nothing else: writing the activity log and sending a question to the chat
// are for the sink to do (see Brain.readStream), so that the parser can be
// run on a recorded stream alone.
func parseStream(r io.Reader, sink StreamSink) RunResult {
	return parseAgentStream(r, sink, "claude")
}

// parseAgentStream shares line limits and terminal-result handling across both
// providers; codexStream translates Codex JSONL into the common events.
func parseAgentStream(r io.Reader, sink StreamSink, provider string) RunResult {
	in := bufio.NewReaderSize(r, 64<<10)
	p := streamParser{sink: sink, provider: provider}
	var buf []byte
	for {
		line, size, err := readStreamLine(in, buf)
		buf = line
		switch {
		case size > maxStreamLine:
			sink.Log(fmt.Sprintf("⚠️ Skipped a line of %d bytes, longer than the %d MiB the bridge reads", size, maxStreamLine>>20))
		case p.done:
			// A result is the run's last word. What comes after it
			// is read all the same, to the end: a CLI left with output
			// nobody reads would block writing it.
		default:
			p.event(line)
		}
		switch {
		case p.done && err != nil, err == io.EOF:
			return p.outcome()
		case errors.Is(err, os.ErrClosed):
			return RunResult{Err: errBrainInterrupted}
		case err != nil:
			return RunResult{Err: fmt.Errorf("reading %s output: %w", agentName(provider), err)}
		}
	}
}

// readStreamLine reads the next line of r into buf, without its newline,
// and returns it and its length. A line longer than maxStreamLine is read
// to its end, but not kept: size is more than maxStreamLine then, and line
// is only the buffer to reuse. err is io.EOF after the last line, which may
// lack a newline, or what r failed with.
func readStreamLine(r *bufio.Reader, buf []byte) (line []byte, size int, err error) {
	line = buf[:0]
	for {
		var chunk []byte
		chunk, err = r.ReadSlice('\n')
		size += len(chunk)
		if size <= maxStreamLine+1 { // +1 for the newline
			line = append(line, chunk...)
		}
		if err != bufio.ErrBufferFull {
			break
		}
	}
	if err == nil { // it ends in a newline
		size--
		if size <= maxStreamLine {
			line = line[:size]
		}
	}
	return line, size, err
}

// streamParser is what parseStream knows of a run so far.
type streamParser struct {
	provider string
	codex    codexStream
	sink     StreamSink
	done     bool            // the first result arrived
	result   string          // the first result's
	lastText string          // the last text the model wrote
	asked    bool            // a question went out
	err      error           // the run's error result
	unknown  map[string]bool // the event types logged as unknown
}

// event handles one line of the output. Lines that are no JSON object,
// warnings the CLI prints say, are left out.
func (p *streamParser) event(line []byte) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("{")) {
		return
	}
	var event streamEvent
	if p.provider == "codex" {
		event = p.codex.event(line)
		if event.Type == "" {
			return // Codex progress, retries, and malformed JSON carry no common event.
		}
	} else if err := json.Unmarshal(line, &event); err != nil {
		return
	}

	switch event.Type {
	case "assistant":
		if event.Message == nil {
			return
		}
		for _, content := range event.Message.Content {
			switch content.Type {
			case "text":
				if content.Text != "" {
					p.lastText = content.Text
					p.sink.Log("💬 " + textx.Truncate(content.Text, 2000))
				}
			case "tool_use":
				p.sink.Log("🔧 Tool: " + content.Name)
				if content.Name == "AskUserQuestion" {
					// Only a question on its way counts as asked.
					if question := extractAskQuestion(content.Input); question != "" && p.sink.Question(question) {
						p.asked = true
					}
				}
			}
		}

	case "result":
		p.done = true
		if event.IsError || event.Error != "" {
			errMsg := event.Error
			if errMsg == "" {
				errMsg = event.Result
			}
			if errMsg == "" {
				// The CLI reports some failures (authentication, quota) only
				// as its last message.
				errMsg = textx.Truncate(p.lastText, 300)
			}
			if errMsg == "" {
				errMsg = "(claude exited with an error but no message)"
			}
			p.sink.Log("❌ Error: " + errMsg)
			p.err = &cliError{msg: errMsg, provider: p.provider}
			p.sink.Result(p.outcome())
			return
		}
		p.result = event.Result
		p.sink.Log("✅ Result: " + textx.Truncate(p.result, 2000))
		p.sink.Result(p.outcome())

	case "user", "system":
		// Tool results, and the CLI's own notes (its settings, a
		// compaction): nothing the run says.

	default:
		// A kind of event the bridge does not know, from a newer CLI say.
		// The log names it once a run, so that a change in what the CLI
		// sends shows.
		if !p.unknown[event.Type] {
			if p.unknown == nil {
				p.unknown = map[string]bool{}
			}
			p.unknown[event.Type] = true
			p.sink.Log(fmt.Sprintf("❔ Event of a type the bridge does not read: %q", textx.Truncate(event.Type, 100)))
		}
	}
}

// outcome is the run's outcome once its output ended.
func (p *streamParser) outcome() RunResult {
	if p.err != nil {
		return RunResult{Err: p.err, Asked: p.asked}
	}
	// A -p run is stateless and nobody answers its AskUserQuestion, so the
	// model goes on after asking, often to an answer of its own ("I booked
	// Monday"), which the chat must get too. A run that only asked has said
	// what it had to say.
	if p.result == "" && !p.asked {
		if p.provider == "codex" && p.codex.err != "" {
			return RunResult{Err: &cliError{msg: p.codex.err, provider: p.provider}}
		}
		return RunResult{Err: errNoResult}
	}
	return RunResult{Text: p.result, Asked: p.asked}
}
