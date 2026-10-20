package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/mcp"
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
			return mcp.Tool{Description: "List the other brains you may use, with their names and what you may do with each: delegate (hand it a task with the delegate tool), read (read its notes with read_memory), files (its vault files, later). A brain that is not running cannot be used until it runs again."}
		},
		Call: b.callListBrains,
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:     "read_memory",
		Describe: describeReadMemory,
		Call:     b.callReadMemory,
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name: "ask_user",
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{Description: "Ask the person you work for a question and wait for the answer. Not available yet: ask in your reply instead.", InputSchema: schema(map[string]any{"question": strProp("The question.")}, "question")}
		},
		Call: notYet("ask_user", "ask in your final reply instead; the answer comes as a new message"),
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name: "request_approval",
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{Description: "Ask the person you work for to approve an action before you take it. Not available yet: do not take irreversible actions; describe them in your reply instead.", InputSchema: schema(map[string]any{"action": strProp("What you want to do, in one or two sentences.")}, "action")}
		},
		Call: notYet("request_approval", "do not take the action; describe it in your reply and let the person decide"),
	})
	return r
}

// notYet is a tool that is listed but not there yet (ask_user and
// request_approval, which the policy step fills in).
func notYet(name, instead string) func(context.Context, mcpCaller, json.RawMessage) (*mcp.CallResult, error) {
	return func(context.Context, mcpCaller, json.RawMessage) (*mcp.CallResult, error) {
		return mcp.ErrorResult(name + " is not available yet: " + instead + "."), nil
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
	if err != nil {
		if errors.Is(err, mcp.ErrUnknownTool) {
			return nil, apperr.New(apperr.NotFound, "this run has no tool "+req.Name)
		}
		return nil, err
	}
	return res, nil
}
