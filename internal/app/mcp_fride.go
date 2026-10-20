package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/skill"
)

// The core brain's FrIDE tools (PLAN.md, Workstream E): coding work goes
// to FrIDE on a node, never into a repository the core brain edits itself.
// A core run has them while nodes are on; a sub-brain and a delegated run
// never do.

// frideTools reports whether the run has the FrIDE tools.
func (b *App) frideTools(c mcpCaller) bool { return c.core() && b.nodesEnabled }

func threadProp() map[string]any {
	return map[string]any{"type": "integer", "minimum": 1, "description": "The FrIDE thread's number, as fride_task returned it and FrIDE's messages in the chat name it (FrIDE #3 is 3)."}
}

// registerFrideTools registers them behind M5's policy (App.guarded):
// fride_task, fride_reply, fride_move and fride_close change work on a
// node and so are write, refused in a lockdown and to a read budget;
// fride_status only reads.
func (b *App) registerFrideTools(r *mcp.Registry[mcpCaller]) {
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "fride_task",
		Available: b.frideTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Hand coding work to FrIDE, the coding IDE on one of the owner's machines: anything that changes code (a feature, a fix, a refactor) goes here, never into a repository you edit yourself. FrIDE's orchestrator decides how to do it, may ask back, and reports in this chat; its questions and news arrive here as messages naming the thread (FrIDE #3). Returns the thread number. If no machine can take it now, it waits and starts when one can.",
				InputSchema: schema(map[string]any{
					"message": strProp("What to build or change, in full: the person's request with everything FrIDE needs. FrIDE cannot see this chat."),
					"repos":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The repositories to work in, by folder name, when the person named them."},
					"node":    strProp("The machine to run it on: a node's name, id or tag (such as macos or gpu), when the person asked for one; leave it out to let fridolin pick."),
				}, "message"),
			}
		},
		Call: b.guarded(skill.RiskWrite, b.callFrideTask),
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "fride_status",
		Available: b.frideTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "What FrIDE says about a thread now: its status, its task and branch, what it asks, its recent messages. Without a thread, lists every FrIDE thread.",
				InputSchema: schema(map[string]any{"thread": threadProp()}),
			}
		},
		Call: b.callFrideStatus,
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "fride_reply",
		Available: b.frideTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Pass the person's answer to FrIDE's question, or more instructions for its work, to a FrIDE thread. Use it when the person answers a FrIDE message in this chat.",
				InputSchema: schema(map[string]any{"thread": threadProp(), "text": strProp("The answer or instruction, in the person's words.")}, "thread", "text"),
			}
		},
		Call: b.guarded(skill.RiskWrite, b.callFrideReply),
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "fride_move",
		Available: b.frideTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Move a FrIDE thread's task to another machine: its agents stop, the task travels with its branches, uncommitted work and conversation, and continues there. Only when the person asks for it.",
				InputSchema: schema(map[string]any{"thread": threadProp(), "node": strProp("The machine to move it to: a node's name, id or tag.")}, "thread", "node"),
			}
		},
		Call: b.guarded(skill.RiskWrite, b.callFrideMove),
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "fride_close",
		Available: b.frideTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Close a FrIDE thread's task when the person says the work is finished: FrIDE writes its rationale, stops its agents and stack, and its workspace is archived into the vault and removed from the machine. The branch stays in the repositories. Work still waiting for a machine is dropped.",
				InputSchema: schema(map[string]any{"thread": threadProp()}, "thread"),
			}
		},
		Call: b.guarded(skill.RiskWrite, b.callFrideClose),
	})
}

// frideArgs reads a FrIDE tool's arguments.
type frideArgs struct {
	Message string   `json:"message"`
	Repos   []string `json:"repos"`
	Node    string   `json:"node"`
	Thread  int64    `json:"thread"`
	Text    string   `json:"text"`
}

func readFrideArgs(raw json.RawMessage, needThread bool) (frideArgs, *mcp.CallResult) {
	var a frideArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, mcp.ErrorResult("unreadable arguments: " + err.Error())
	}
	if needThread && a.Thread <= 0 {
		return a, mcp.ErrorResult("thread is missing: the number of the FrIDE thread (FrIDE #3 is 3)")
	}
	a.Node = strings.TrimSpace(a.Node)
	return a, nil
}

func (b *App) callFrideTask(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	a, bad := readFrideArgs(raw, false)
	if bad != nil {
		return bad, nil
	}
	if strings.TrimSpace(a.Message) == "" {
		return mcp.ErrorResult("message is empty: say what FrIDE is to build or change"), nil
	}
	// Policy (PLAN.md, M5): fride_task is a write action, checked by
	// App.guarded (registerFrideTools) before anything leaves the core: a
	// lockdown or a run whose budget is read gets its error result.
	chatJID := c.run.Chat
	if chatJID == "" || chat.IsDelegateChat(chatJID) {
		return mcp.ErrorResult("this run answers no chat, so FrIDE's questions and news would have nowhere to go"), nil
	}
	t, placed, _, err := b.fride.newWork(ctx, chatJID, c.run.Brain, a.Message, a.Repos, a.Node)
	if err != nil {
		return mcp.ErrorResult("recording the work failed: " + err.Error()), nil
	}
	if !placed {
		return mcp.TextResult(fmt.Sprintf("FrIDE #%d waits: no machine with FrIDE can take it now. The chat has been told, and hears when it starts; you need not repeat it.", t.ID)), nil
	}
	return mcp.TextResult(fmt.Sprintf("Handed to FrIDE on %s as FrIDE #%d (FrIDE's status: %s). Its questions and news will arrive in this chat as messages naming FrIDE #%d.", b.fride.nodeName(t.Node), t.ID, orDefault(t.FrideStatus, "queued"), t.ID)), nil
}

func (b *App) callFrideStatus(ctx context.Context, _ mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	a, bad := readFrideArgs(raw, false)
	if bad != nil {
		return bad, nil
	}
	var out any
	if a.Thread <= 0 {
		all, err := b.store.FrideThreads(ctx, "")
		if err != nil {
			return mcp.ErrorResult(err.Error()), nil
		}
		list := []map[string]any{}
		for _, t := range all {
			e := map[string]any{"thread": t.ID, "state": t.State, "message": truncateRunes(t.Message, 200)}
			for k, v := range map[string]string{"node": t.Node, "task": t.TaskID, "branch": t.Branch, "fride_status": t.FrideStatus, "archive": t.Archive} {
				if v != "" {
					e[k] = v
				}
			}
			if t.Node != "" {
				e["node"] = b.fride.nodeName(t.Node)
			}
			list = append(list, e)
		}
		out = map[string]any{"threads": list}
	} else {
		t, err := b.fride.thread(ctx, a.Thread)
		if err != nil {
			return mcp.ErrorResult(err.Error()), nil
		}
		out = b.fride.status(ctx, t)
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	return mcp.TextResult(string(data)), nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (b *App) callFrideReply(ctx context.Context, _ mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	a, bad := readFrideArgs(raw, true)
	if bad != nil {
		return bad, nil
	}
	if strings.TrimSpace(a.Text) == "" {
		return mcp.ErrorResult("text is empty"), nil
	}
	text, err := b.fride.reply(ctx, a.Thread, a.Text)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	return mcp.TextResult(text), nil
}

func (b *App) callFrideMove(ctx context.Context, _ mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	a, bad := readFrideArgs(raw, true)
	if bad != nil {
		return bad, nil
	}
	if a.Node == "" {
		return mcp.ErrorResult("node is missing: the machine to move it to"), nil
	}
	text, err := b.fride.move(ctx, a.Thread, a.Node)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	return mcp.TextResult(text), nil
}

func (b *App) callFrideClose(ctx context.Context, _ mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	a, bad := readFrideArgs(raw, true)
	if bad != nil {
		return bad, nil
	}
	text, err := b.fride.close(ctx, a.Thread)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	return mcp.TextResult(text), nil
}
