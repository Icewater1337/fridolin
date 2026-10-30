package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/mcp/tools"
	"github.com/Icewater1337/fridolin/internal/skill"
)

// mcpCaller is the run a tool call comes from: what every tool knows of
// it (tools.Caller: its brain, what the brain's manifest grants, whether
// it is a core run) and the run as the bridge started it, whose chat,
// policy and message the bridge's own tools read.
type mcpCaller struct {
	tools.Caller
	run runInfo
}

// newMCPCaller is the caller of run, whose brain's approved manifest is m;
// running reports whether a brain is loaded.
func newMCPCaller(run runInfo, m manifest.Manifest, running func(id string) bool) mcpCaller {
	return mcpCaller{
		Caller: tools.Caller{Brain: run.Brain, Delegated: run.delegated(), Manifest: m, Running: running},
		run:    run,
	}
}

// mcpGuard is the policy as the tools that act on the world see it: the
// lockdown, the run's risk level and the approvals it may take.
type mcpGuard struct{ b *App }

func (g mcpGuard) Locked() bool                  { return g.b.locked() }
func (g mcpGuard) Budget(c mcpCaller) skill.Risk { return c.risk() }
func (g mcpGuard) UseApproval(ctx context.Context, c mcpCaller, id string) error {
	return g.b.useApproval(ctx, c, id)
}

// newMCPTools is the set of the tools a run's MCP server offers. A
// tool's Available decides, per run, whether the run has it at all; one it
// does not have is neither listed nor callable. Each feature registers
// its own tools here; those that act on the world go through the policy
// (mcpGuard), and every call is written to the audit log.
func (b *App) newMCPTools() *tools.Set[mcpCaller] {
	r := tools.NewSet[mcpCaller](mcpGuard{b}, b.auditToolCall)
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "delegate",
		Available: func(c mcpCaller) bool { return len(c.Granted(manifest.GrantDelegate)) > 0 },
		Describe:  b.describeDelegate,
		Call:      b.callDelegate,
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "list_brains",
		Available: func(c mcpCaller) bool { return c.Core() && len(c.Manifest.GrantedTo("")) > 0 },
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
	b.registerNodeExecTools(r)
	b.registerWorkstationTools(r)
	b.registerConnectionTools(r)
	b.registerPlaudTools(r)
	return r
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
	return newMCPCaller(info, br.Manifest(), func(id string) bool { return b.brainMgr.Get(id) != nil }), nil
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
	res, err := b.mcpTools.Call(ctx, c, req.Name, req.Arguments)
	if err != nil {
		if errors.Is(err, mcp.ErrUnknownTool) {
			return nil, apperr.New(apperr.NotFound, "this run has no tool "+req.Name)
		}
		return nil, err
	}
	return res, nil
}

// auditToolCall writes a tool call's line: which tool, and how it ended
// (tools.Set.Call). Its arguments stay out of the log: they may hold what
// is not the log's.
func (b *App) auditToolCall(c mcpCaller, name, outcome string) {
	e := audit.Entry{Kind: audit.ToolCall, Brain: c.run.Brain, Run: c.run.ID, Chat: c.run.Chat, Thread: c.run.Chat, Detail: name, Outcome: outcome}
	if p := c.run.Policy; p != nil {
		e.Thread, e.Skill, e.Risk = p.Thread, p.Skill, string(p.Risk)
	}
	b.audit.Append(e)
}
