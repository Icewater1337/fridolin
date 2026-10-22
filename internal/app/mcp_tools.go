package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"slices"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/skill"
)

// mcpCaller is the run a tool call comes from, as the bridge knows it: the
// run's token names the run (runTokens), and the brain's approved manifest
// says what its grants are now. Nothing in it comes from the model.
type mcpCaller struct {
	run runInfo
	// manifest is the brain's approved manifest as the call came in.
	manifest manifest.Manifest
	// running reports whether a brain is loaded and can take work.
	running func(id string) bool
}

// core reports whether the run is the core brain's own, not a task
// delegated to it: only then may it reach other brains.
func (c mcpCaller) core() bool { return c.manifest.IsCore() && !c.run.delegated() }

// granted lists the brains the run may use for kind, sorted: none unless
// it is a core run, and only brains that are loaded.
func (c mcpCaller) granted(kind string) []string {
	if !c.core() {
		return nil
	}
	var out []string
	for _, id := range c.manifest.GrantedTo(kind) {
		if c.running(id) {
			out = append(out, id)
		}
	}
	return out
}

// mayRead reports whether the run may read brain id's memories and skills:
// its own always, another's with a read grant.
func (c mcpCaller) mayRead(id string) bool {
	return id == c.run.Brain || c.core() && c.manifest.Granted(id, manifest.GrantRead) && c.running(id)
}

// newMCPTools is the registry of the tools a run's MCP server offers. A
// tool's Available decides, per run, whether the run has it at all; one it
// does not have is neither listed nor callable. Later tools register here
// too: the vault's vault_ls, vault_get, vault_put and vault_search with
// an Available that looks at the vault and the run's files grants.
func (b *App) newMCPTools() *mcp.Registry[mcpCaller] {
	r := &mcp.Registry[mcpCaller]{}
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "delegate",
		Available: func(c mcpCaller) bool { return len(c.granted(manifest.GrantDelegate)) > 0 },
		Describe:  b.describeDelegate,
		Call:      b.callDelegate,
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "list_brains",
		Available: func(c mcpCaller) bool { return c.core() && len(c.manifest.GrantedTo("")) > 0 },
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{Description: "List the other brains you may use, with their names and what you may do with each: delegate (hand it a task with the delegate tool), read (read its notes with read_memory), files (its vault files, with vault_ls, vault_get and vault_search). A brain that is not running cannot be used until it runs again."}
		},
		Call: b.callListBrains,
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:     "read_memory",
		Describe: describeReadMemory,
		Call:     b.callReadMemory,
	})
	b.registerVaultTools(r)
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "ask_user",
		Available: mcpCaller.canAsk,
		Describe:  b.describeAskUser,
		Call:      b.callAskUser,
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "request_approval",
		Available: mcpCaller.canAsk,
		Describe:  b.describeRequestApproval,
		Call:      b.callRequestApproval,
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "check_approval",
		Available: mcpCaller.canAsk,
		Describe:  describeCheckApproval,
		Call:      b.callCheckApproval,
	})
	b.registerFrideTools(r)
	b.registerNodeTools(r)
	b.registerWorkstationTools(r)
	return r
}

// mcpCall is a tool's function.
type mcpCall = func(context.Context, mcpCaller, json.RawMessage) (*mcp.CallResult, error)

// guarded is call behind the policy for a tool of risk: a write tool
// needs a run whose budget allows writing and no lockdown; an
// irreversible one, in addition, an approval_id argument naming an
// approved request_approval of the same brain, which the call uses up.
// Tools that act on the world register through it (the vault's
// vault_put); the other tools only read.
func (b *App) guarded(risk skill.Risk, call mcpCall) mcpCall {
	return func(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
		if risk.Rank() <= skill.RiskRead.Rank() {
			return call(ctx, c, raw)
		}
		if b.locked() {
			return mcp.ErrorResult("the bridge is in lockdown: only reading goes ahead. Do not try another way; say so in your reply."), nil
		}
		if risk == skill.RiskWrite && c.risk().Rank() < skill.RiskWrite.Rank() {
			return mcp.ErrorResult(fmt.Sprintf("this run's risk level is %s, which does not allow writing", c.risk())), nil
		}
		if risk == skill.RiskIrreversible {
			var args struct {
				ApprovalID string `json:"approval_id"`
			}
			_ = json.Unmarshal(raw, &args)
			if args.ApprovalID == "" {
				return mcp.ErrorResult("this tool takes an irreversible step: call request_approval first, and pass the approval_id it answers approved with"), nil
			}
			if err := b.useApproval(ctx, c, args.ApprovalID); err != nil {
				return mcp.ErrorResult(err.Error() + ". Do not take the step."), nil
			}
		}
		return call(ctx, c, raw)
	}
}

// schema is a JSON Schema object with props, of which required must be
// given, and nothing else.
func schema(props map[string]any, required ...string) json.RawMessage {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	data, _ := json.Marshal(s)
	return data
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// brainList names brains with their display names: "family (Family)".
func (c mcpCaller) brainList(ids []string, name func(string) string) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = id
		if n := name(id); n != "" && n != id {
			parts[i] += " (" + n + ")"
		}
	}
	return strings.Join(parts, ", ")
}

func (b *App) brainName(id string) string {
	if br := b.brainMgr.Get(id); br != nil {
		return br.Name()
	}
	return ""
}

func (b *App) describeDelegate(c mcpCaller) mcp.Tool {
	targets := c.granted(manifest.GrantDelegate)
	return mcp.Tool{
		Description: "Hand a bounded task to another brain and get its answer. That brain works on it with its own instructions, memory and files, which you do not see, so put everything it needs into the task. " +
			fmt.Sprintf("Waits up to timeout_seconds (default %d, at most %d); an answer that comes later arrives in this chat as a new message. ", int(defaultDelegateWait/time.Second), int(maxDelegateWait/time.Second)) +
			"Brains you may delegate to: " + c.brainList(targets, b.brainName) + ".",
		InputSchema: schema(map[string]any{
			"brain":           map[string]any{"type": "string", "enum": targets, "description": "The brain's id."},
			"task":            strProp("What the brain is to do, with all it needs to know. It cannot ask you back."),
			"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": int(maxDelegateWait / time.Second), "description": "How long to wait for the answer."},
		}, "brain", "task"),
	}
}

func (b *App) callDelegate(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var args struct {
		Brain          string `json:"brain"`
		Task           string `json:"task"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcp.ErrorResult("the arguments are {\"brain\": \"<id>\", \"task\": \"…\", \"timeout_seconds\": N}: " + err.Error()), nil
	}
	if strings.TrimSpace(args.Task) == "" {
		return mcp.ErrorResult("task is empty: say what the brain is to do"), nil
	}
	// Checked again here, not only by Available: the grant may have gone
	// since the run listed its tools.
	if !c.manifest.Granted(args.Brain, manifest.GrantDelegate) || !c.running(args.Brain) {
		return mcp.ErrorResult(fmt.Sprintf("you may not delegate to %q; you may delegate to: %s", args.Brain, c.brainList(c.granted(manifest.GrantDelegate), b.brainName))), nil
	}
	wait := defaultDelegateWait
	if args.TimeoutSeconds > 0 {
		wait = min(time.Duration(args.TimeoutSeconds)*time.Second, maxDelegateWait)
	}
	return b.delegate(ctx, c, args.Brain, args.Task, wait), nil
}

func (b *App) callListBrains(_ context.Context, c mcpCaller, _ json.RawMessage) (*mcp.CallResult, error) {
	type entry struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Grants  []string `json:"grants"`
		Running bool     `json:"running"`
	}
	out := []entry{}
	for _, id := range c.manifest.GrantedTo("") {
		out = append(out, entry{ID: id, Name: b.brainName(id), Grants: c.manifest.Normalized().Grants[id], Running: c.running(id)})
	}
	data, _ := json.MarshalIndent(map[string]any{"brains": out}, "", "  ")
	return mcp.TextResult(string(data)), nil
}

// mcpCallerFor is the caller a run's token stands for. run is the run id
// the MCP server was started with; it must be the token's, so that a
// server cannot be pointed at another run's token by mistake.
func (b *App) mcpCallerFor(token, run string) (mcpCaller, error) {
	info, ok := b.runTokens.runOf(token)
	br := b.brainMgr.Get(info.Brain)
	if !ok || br == nil {
		return mcpCaller{}, apperr.New(apperr.NotFound, "the bridge does not know this run's token (FRIDOLIN_BRAIN_TOKEN_FILE): it works only inside the run it was made for")
	}
	if run != "" && run != info.ID {
		return mcpCaller{}, apperr.New(apperr.Precondition, fmt.Sprintf("this token belongs to run %s, not %s", info.ID, run))
	}
	return mcpCaller{run: info, manifest: br.Manifest(), running: func(id string) bool { return b.brainMgr.Get(id) != nil }}, nil
}

// mcpToolsFor answers GET /brain/mcp/tools.
func (b *App) mcpToolsFor(_ context.Context, token, run string) (any, error) {
	c, err := b.mcpCallerFor(token, run)
	if err != nil {
		return nil, err
	}
	return map[string]any{"tools": b.mcpTools.List(c)}, nil
}

// mcpCall answers POST /brain/mcp/call.
func (b *App) mcpCall(ctx context.Context, token, run string, req httpapi.MCPCallRequest) (any, error) {
	c, err := b.mcpCallerFor(token, run)
	if err != nil {
		return nil, err
	}
	args := req.Arguments
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	res, err := b.mcpTools.Call(ctx, c, req.Name, args)
	b.auditToolCall(c, req.Name, res, err)
	if err != nil {
		if errors.Is(err, mcp.ErrUnknownTool) {
			return nil, apperr.New(apperr.NotFound, "this run has no tool "+req.Name)
		}
		return nil, err
	}
	return res, nil
}

// auditToolCall writes a tool call's line: which tool, and how it ended.
// Its arguments stay out of the log: they may hold what is not the log's.
func (b *App) auditToolCall(c mcpCaller, name string, res *mcp.CallResult, err error) {
	outcome := "ok"
	switch {
	case errors.Is(err, mcp.ErrUnknownTool):
		outcome = "refused: not this run's tool"
	case err != nil:
		outcome = "failed"
	case res != nil && res.IsError:
		outcome = "error"
		if len(res.Content) > 0 {
			outcome += ": " + res.Content[0].Text
		}
	case res != nil && len(res.Content) > 0:
		if word, _, _ := strings.Cut(res.Content[0].Text, ":"); slices.Contains([]string{"approved", "denied", "pending", "answered", "expired"}, word) {
			outcome = word
		}
	}
	e := audit.Entry{Kind: audit.ToolCall, Brain: c.run.Brain, Run: c.run.ID, Chat: c.run.Chat, Thread: c.run.Chat, Detail: name, Outcome: outcome}
	if p := c.run.Policy; p != nil {
		e.Thread, e.Skill, e.Risk = p.Thread, p.Skill, string(p.Risk)
	}
	b.audit.Append(e)
}
