package toolcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/mcp"
)

// liveMCPInstructions go to the model of a live session with initialize.
const liveMCPInstructions = "Fridolin's own tools for this session, and its messages. Each message from the bridge arrives as a <channel source=\"fridolin\"> block holding the new message and what you need to answer it, exactly as a one-off run would be told. Answer in your final reply, as you would there: the bridge sends it to the chat when your turn ends. Nothing else you write reaches the chat. Which tools you have depends on your brain and what it is granted, and the bridge decides that: a tool you do not see here is not yours to use."

// liveEvent is a message for the session, as GET /brain/live/next has it
// (claudecli.LiveEvent), and Claude Code's channel notification takes it:
// "content" is the text, each "meta" key an attribute of the <channel> tag.
type liveEvent struct {
	Content string            `json:"content"`
	Meta    map[string]string `json:"meta,omitempty"`
}

// liveWait is how long one request for the next event waits at the bridge.
const liveWait = 25 * time.Second

// pumpLive asks the bridge for the session's events and passes each to out
// as a channel notification, until ctx ends. A bridge that cannot be
// reached is asked again after a second.
func pumpLive(ctx context.Context, c *brainClient, out chan<- mcp.Notification, logf func(string, ...any)) {
	for ctx.Err() == nil {
		ev, ok, err := nextLive(ctx, c)
		switch {
		case err != nil:
			if ctx.Err() == nil {
				logf("fridolin mcp: asking the bridge for the next message: %v", err)
				select {
				case <-time.After(time.Second):
				case <-ctx.Done():
				}
			}
		case ok:
			// Claude Code takes the text as "content" and each "meta" key as
			// an attribute of the <channel> tag.
			select {
			case out <- mcp.Notification{Method: "notifications/claude/channel", Params: ev}:
			case <-ctx.Done():
			}
		}
	}
}

// nextLive is one GET /brain/live/next; ok is false for 204, no event yet.
func nextLive(ctx context.Context, c *brainClient) (ev liveEvent, ok bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/brain/live/next?wait=%d", c.base, int(liveWait/time.Second)), nil)
	if err != nil {
		return ev, false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return ev, false, fmt.Errorf("the bridge is not reachable at %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return ev, false, nil
	case http.StatusOK:
		return ev, true, json.NewDecoder(io.LimitReader(resp.Body, mcp.MaxLine)).Decode(&ev)
	}
	return ev, false, fmt.Errorf("the bridge answered %s", resp.Status)
}

// LiveTurnEnd is `fridolin live-turn-end`, the Stop hook of a live session:
// Claude Code runs it when the model has finished a turn, with the hook's
// JSON on stdin, and it tells the bridge the turn's last text, which is the
// answer, and the last turn the model read, which says whose answer it is. A
// hook that fails must not hold the session up, so it only says why on
// stderr.
func LiveTurnEnd(ctx context.Context, stdin io.Reader, t IO) int {
	var hook struct {
		Text       string `json:"last_assistant_message"`
		Transcript string `json:"transcript_path"`
	}
	if err := json.NewDecoder(io.LimitReader(stdin, 16<<20)).Decode(&hook); err != nil {
		fmt.Fprintf(t.Stderr, "fridolin live-turn-end: reading the hook's input: %v\n", err)
		return 1
	}
	c, err := BrainClientFor(t)
	if err != nil {
		fmt.Fprintf(t.Stderr, "fridolin live-turn-end: %v\n", err)
		return 1
	}
	// Without a turn the bridge takes the text for late output: it still
	// reaches the chat, and never answers a message the model did not read.
	turn, err := lastLiveTurn(hook.Transcript)
	if err != nil {
		fmt.Fprintf(t.Stderr, "fridolin live-turn-end: reading the transcript: %v\n", err)
	}
	var out struct{}
	if err := c.call(ctx, http.MethodPost, "/brain/live/turn-end", map[string]string{"text": hook.Text, "turn": turn}, &out); err != nil {
		fmt.Fprintf(t.Stderr, "fridolin live-turn-end: %v\n", err)
		return 1
	}
	return 0
}

// liveTag is the opening of the <channel> block Claude Code wraps a message
// of the bridge's in, as the start of what the model is given. A message's
// own text cannot pass for it, nor can a tool's result, which is no text.
var liveTag = regexp.MustCompile(`^\s*<channel source="fridolin"[^>]*\sturn="([^"]*)"`)

// lastLiveTurn is the "turn" of the last message in the session's transcript
// that the model was given, "" without one. A message that comes while the
// model works is at first only a "queue-operation" line, which does not
// count: it becomes a "user" line when a turn starts with it, or a
// "queued_command" attachment when the running turn takes it in.
func lastLiveTurn(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var turn string
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if bytes.Contains(line, []byte("channel source=")) {
			if t, ok := liveTurnOf(line); ok {
				turn = t
			}
		}
		if err == io.EOF {
			return turn, nil
		}
		if err != nil {
			return turn, err
		}
	}
}

// liveTurnOf is the turn of a line of the transcript that gave the model a
// message of the bridge's. A line that does not parse is one being written.
func liveTurnOf(line []byte) (string, bool) {
	var rec struct {
		Type    string `json:"type"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Attachment struct {
			Type   string          `json:"type"`
			Prompt json.RawMessage `json:"prompt"`
		} `json:"attachment"`
	}
	if json.Unmarshal(line, &rec) != nil {
		return "", false
	}
	var given json.RawMessage
	switch {
	case rec.Type == "user":
		given = rec.Message.Content
	case rec.Type == "attachment" && rec.Attachment.Type == "queued_command":
		given = rec.Attachment.Prompt
	default:
		return "", false
	}
	var text string
	if json.Unmarshal(given, &text) != nil {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(given, &blocks) != nil {
			return "", false
		}
		var texts []string
		for _, b := range blocks {
			if b.Type == "text" {
				texts = append(texts, b.Text)
			}
		}
		text = strings.Join(texts, "\n")
	}
	m := liveTag.FindStringSubmatch(text)
	if m == nil {
		return "", false
	}
	return m[1], true
}
