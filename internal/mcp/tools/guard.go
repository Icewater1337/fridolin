package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/skill"
)

// Call is a tool's function: it runs the tool for c with the arguments
// object the client sent.
type Call[C any] func(ctx context.Context, c C, args json.RawMessage) (*mcp.CallResult, error)

// Guard is the policy behind the tools that act on the world.
type Guard[C any] interface {
	// Locked reports whether the bridge is in lockdown, in which only
	// reading goes ahead.
	Locked() bool
	// Budget is the highest risk c's run may take: its risk level.
	Budget(c C) skill.Risk
	// UseApproval checks that id names an approved request_approval c's
	// run may take, not used yet, and uses it up. Its error says, for the
	// run, why not.
	UseApproval(ctx context.Context, c C, id string) error
}

// Guarded is call behind g for a tool of risk: a write tool needs a run
// whose budget allows writing and no lockdown; an irreversible one, in
// addition, an approval_id argument that g takes (UseApproval). A tool
// that only reads runs as it is.
func Guarded[C any](g Guard[C], risk skill.Risk, call Call[C]) Call[C] {
	return guarded(g, risk, false, call)
}

// GuardedOutbound is Guarded for a tool that sends text off the machine:
// to someone else, to a vendor, or into work that may end up published.
// On top of Guarded's checks, none of its string arguments may hold what
// looks like a secret (audit.SecretIn), unless approval_id names an
// approval the run may take (UseApproval): a run that injected
// instructions talked into it cannot send a token it read. An
// irreversible tool's approval covers it.
func GuardedOutbound[C any](g Guard[C], risk skill.Risk, call Call[C]) Call[C] {
	return guarded(g, risk, true, call)
}

// ApprovalIDProp is the approval_id argument of an outbound tool
// (GuardedOutbound), for its input schema.
func ApprovalIDProp() map[string]any {
	return Str("Only when this tool refused a value that looks like a secret and the owner approved sending it: the approval_id request_approval answered approved with.")
}

// SecretRefused is the audit outcome of an outbound tool's call that a
// secret-like value stopped.
const SecretRefused = "refused: secret-like value"

func guarded[C any](g Guard[C], risk skill.Risk, outbound bool, call Call[C]) Call[C] {
	return func(ctx context.Context, c C, raw json.RawMessage) (*mcp.CallResult, error) {
		if risk.Rank() > skill.RiskRead.Rank() {
			if g.Locked() {
				return mcp.ErrorResult("the bridge is in lockdown: only reading goes ahead. Do not try another way; say so in your reply."), nil
			}
			if risk == skill.RiskWrite {
				if budget := g.Budget(c); budget.Rank() < skill.RiskWrite.Rank() {
					return mcp.ErrorResult(fmt.Sprintf("this run's risk level is %s, which does not allow writing", budget)), nil
				}
			}
		}
		id := approvalID(raw)
		if risk == skill.RiskIrreversible {
			if id == "" {
				return mcp.ErrorResult("this tool takes an irreversible step: call request_approval first, and pass the approval_id it answers approved with"), nil
			}
			if err := g.UseApproval(ctx, c, id); err != nil {
				return mcp.ErrorResult(err.Error() + ". Do not take the step."), nil
			}
		} else if outbound {
			if arg, kind := secretArg(raw); kind != "" {
				if id == "" {
					note(ctx).outcome = SecretRefused
					return mcp.ErrorResult(fmt.Sprintf("%s looks like a secret (%s), and this tool sends it off this machine: do not send it. "+
						"If the owner wants exactly this sent, ask them with request_approval, saying what goes to whom without the value itself, "+
						"and call again with the approval_id it answers approved with; otherwise leave the value out.", arg, kind)), nil
				}
				if err := g.UseApproval(ctx, c, id); err != nil {
					note(ctx).outcome = SecretRefused
					return mcp.ErrorResult(fmt.Sprintf("%s looks like a secret (%s), and %s. Do not send it.", arg, kind, err)), nil
				}
				note(ctx).approved = id
			}
		}
		return call(ctx, c, raw)
	}
}

// approvalID is the approval_id argument in raw, "" for none.
func approvalID(raw json.RawMessage) string {
	var args struct {
		ApprovalID string `json:"approval_id"`
	}
	_ = json.Unmarshal(raw, &args)
	return args.ApprovalID
}

// secretArg finds the first string argument in raw that looks like a
// secret (audit.SecretIn), in a nested list or object too: the argument,
// such as "the argument message", and the kind of secret; "", "" for
// none. Arguments are looked at in the order of their names.
func secretArg(raw json.RawMessage) (arg, kind string) {
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil {
		return "", ""
	}
	for _, name := range slices.Sorted(maps.Keys(args)) {
		if path, kind := secretIn(name, args[name]); kind != "" {
			return "the argument " + path, kind
		}
	}
	return "", ""
}

// secretIn is secretArg for the value v at path.
func secretIn(path string, v any) (string, string) {
	switch v := v.(type) {
	case string:
		if kind := audit.SecretIn(v); kind != "" {
			return path, kind
		}
	case []any:
		for i, e := range v {
			if p, kind := secretIn(path+"["+strconv.Itoa(i)+"]", e); kind != "" {
				return p, kind
			}
		}
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(v)) {
			if p, kind := secretIn(path+"."+k, v[k]); kind != "" {
				return p, kind
			}
		}
	}
	return "", ""
}

// callNote is what the guard tells Set.Call about a call for its audit
// line, through the call's context.
type callNote struct {
	// outcome, when set, is the line's outcome instead of the result's.
	outcome string
	// approved is the approval_id that let a secret-like value through.
	approved string
}

type noteKey struct{}

// note is the call's note, or one nobody reads when the call did not come
// through a Set.
func note(ctx context.Context) *callNote {
	if n, ok := ctx.Value(noteKey{}).(*callNote); ok {
		return n
	}
	return &callNote{}
}
