package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/mcp/tools"
	"github.com/Icewater1337/fridolin/internal/textx"
)

// catch_up is the live session's way back into a chat after a restart or
// a compaction (docs/BRAINS.md, "Live session"): by default only counts,
// as the whatsapp-claude-plugin's does, so that opening a session reads
// nothing it does not need; read: true shows the end of the chat.
func (b *App) registerLiveTools(r *tools.Set[mcpCaller]) {
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "catch_up",
		Available: func(c mcpCaller) bool { return c.run.Live },
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Recover this chat's context in a live session: how many messages came in since you last answered, and how many things happened that were no message (a vote in a poll). Counts only, no text. With read: true, also the last 10 lines of the chat, oldest first. Use it after you lost the thread, such as after your context was compacted.",
				InputSchema: tools.Schema(map[string]any{
					"read": map[string]any{"type": "boolean", "description": "Also show the last 10 lines of the chat."},
				}),
			}
		},
		Call: b.callCatchUp,
	})
}

func (b *App) callCatchUp(_ context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var args struct {
		Read bool `json:"read"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcp.ErrorResult(`the arguments are {"read": false}: ` + err.Error()), nil
	}
	br := b.brainMgr.Get(c.run.Brain)
	if br == nil {
		return mcp.ErrorResult("this brain is not running"), nil
	}
	entries := br.Backlog(c.run.Message.JID, 200)
	unanswered, events := 0, 0
	for i := len(entries) - 1; i >= 0 && entries[i].Type != history.Out; i-- {
		switch entries[i].Type {
		case history.In:
			unanswered++
		case history.Event:
			events++
		}
	}
	out := fmt.Sprintf("%d message(s) came in since you last answered in this chat, and %d event(s) that were no message.", unanswered, events)
	if args.Read {
		from := max(0, len(entries)-10)
		for _, e := range entries[from:] {
			out += fmt.Sprintf("\n[%s] %s (%s): %s", e.TS, e.From, e.Type, strings.ReplaceAll(textx.Truncate(e.Text, 300), "\n", " "))
		}
	}
	return mcp.TextResult(out), nil
}
