package toolcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
// answer. A hook that fails must not hold the session up, so it only says
// why on stderr.
func LiveTurnEnd(ctx context.Context, stdin io.Reader, t IO) int {
	var hook struct {
		Text string `json:"last_assistant_message"`
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
	var out struct{}
	if err := c.call(ctx, http.MethodPost, "/brain/live/turn-end", map[string]string{"text": hook.Text}, &out); err != nil {
		fmt.Fprintf(t.Stderr, "fridolin live-turn-end: %v\n", err)
		return 1
	}
	return 0
}
