package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// workArgs reads a workstation tool's arguments.
type workArgs struct {
	Prompt       string `json:"prompt"`
	Node         string `json:"node"`
	Folder       string `json:"folder"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	UseCoreLogin bool   `json:"use_core_login"`
	Session      int64  `json:"session"`
	Terminal     int64  `json:"terminal"`
	Text         string `json:"text"`
	Enter        *bool  `json:"enter"`
	WaitSeconds  *int   `json:"wait_seconds"`
}

func readWorkArgs(raw json.RawMessage) (workArgs, *mcp.CallResult) {
	var a workArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, mcp.ErrorResult("unreadable arguments: " + err.Error())
	}
	a.Node, a.Provider, a.Model = strings.TrimSpace(a.Node), strings.TrimSpace(a.Provider), strings.TrimSpace(a.Model)
	return a, nil
}

func (b *App) callAgentRun(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	a, bad := readWorkArgs(raw)
	if bad != nil {
		return bad, nil
	}
	if strings.TrimSpace(a.Prompt) == "" {
		return mcp.ErrorResult("prompt is empty: say what the session is to do"), nil
	}
	chatJID := b.newsChat(c)
	if chatJID == "" {
		return mcp.ErrorResult("this run answers no chat, so the session's questions and news would have nowhere to go"), nil
	}
	if err := (nodeproto.AgentStartParams{Folder: "/x", Prompt: a.Prompt, Provider: a.Provider, Model: a.Model}).Check(); err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	w, s, err := b.work.startAgent(ctx, c, chatJID, a.Node, a.Folder, a.Prompt, a.Provider, a.Model, a.UseCoreLogin)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	login := "the node's own login"
	if s.CoreLogin {
		login = "the core's login"
	}
	return mcp.TextResult(fmt.Sprintf("Started Agent #%d on %s in %s (%s, %s, sandbox %s). Its questions, steps to approve and its end arrive in %s as messages naming Agent #%d; you need not wait for it.",
		w.N, b.work.nodeName(w.Node), w.Folder, s.Provider, login, s.Sandbox, newsWhere(c, chatJID), w.N)), nil
}

// agentView is what node_agent_status shows of a session.
func agentView(n int64, node string, s nodeproto.AgentSession) map[string]any {
	v := map[string]any{"session": n, "node": node, "folder": s.Folder, "provider": s.Provider, "state": s.State, "sandbox": s.Sandbox, "turns": s.Turns, "started": s.Started}
	for k, x := range map[string]string{"ask": s.Ask, "question": s.Question, "summary": s.Summary, "error": s.Error, "diff_stat": s.DiffStat, "model": s.Model} {
		if x != "" {
			v[k] = x
		}
	}
	if len(s.Changed) > 0 {
		v["changed"] = s.Changed
	}
	if s.Ended != nil {
		v["ended"] = s.Ended
	}
	if len(s.Events) > 0 {
		ev := make([]map[string]any, len(s.Events))
		for i, e := range s.Events {
			ev[i] = map[string]any{"seq": e.Seq, "kind": e.Kind, "text": truncateRunes(e.Text, 2000)}
		}
		v["events"] = ev
	}
	return v
}

func (b *App) callAgentStatus(ctx context.Context, _ mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	a, bad := readWorkArgs(raw)
	if bad != nil {
		return bad, nil
	}
	if a.Session <= 0 {
		return jsonResult(map[string]any{"sessions": b.work.agentList()}), nil
	}
	w, err := b.work.agent(a.Session)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	b.work.mu.Lock()
	after := w.Seen
	b.work.mu.Unlock()
	s, err := b.nodes.AgentStatus(ctx, w.Node, nodeproto.AgentRef{ID: w.ID, After: after})
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	if n := len(s.Events); n > 0 {
		b.work.mu.Lock()
		w.Seen = s.Events[n-1].Seq
		b.work.mu.Unlock()
	}
	return jsonResult(agentView(w.N, b.work.nodeName(w.Node), s)), nil
}

func (b *App) callAgentReply(ctx context.Context, _ mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	a, bad := readWorkArgs(raw)
	if bad != nil {
		return bad, nil
	}
	if strings.TrimSpace(a.Text) == "" {
		return mcp.ErrorResult("text is empty"), nil
	}
	if len(a.Text) > nodeproto.MaxAgentPrompt {
		return mcp.ErrorResult(fmt.Sprintf("text is longer than %d bytes", nodeproto.MaxAgentPrompt)), nil
	}
	w, err := b.work.agent(a.Session)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	b.work.mu.Lock()
	pending := w.Approval
	b.work.mu.Unlock()
	if pending != "" {
		return mcp.ErrorResult(fmt.Sprintf("Agent #%d waits for the person's vote on its approval poll; it is answered there, not here", w.N)), nil
	}
	// Allow stays false: a permission or an irreversible step is the
	// owner's vote on its poll (workRuntime.decided), never the brain's.
	s, err := b.nodes.AgentReply(ctx, w.Node, nodeproto.AgentReplyParams{ID: w.ID, Text: a.Text})
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	return mcp.TextResult(fmt.Sprintf("Passed on to Agent #%d (now %s).", w.N, s.State)), nil
}

func (b *App) callAgentStop(ctx context.Context, _ mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	a, bad := readWorkArgs(raw)
	if bad != nil {
		return bad, nil
	}
	w, err := b.work.agent(a.Session)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	s, err := b.work.stopAgent(ctx, w, "stopped by the core brain")
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	s.Events = nil
	return jsonResult(agentView(w.N, b.work.nodeName(w.Node), s)), nil
}
