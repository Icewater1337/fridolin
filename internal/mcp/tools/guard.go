package tools

import (
	"context"
	"encoding/json"
	"fmt"

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
	return func(ctx context.Context, c C, raw json.RawMessage) (*mcp.CallResult, error) {
		if risk.Rank() <= skill.RiskRead.Rank() {
			return call(ctx, c, raw)
		}
		if g.Locked() {
			return mcp.ErrorResult("the bridge is in lockdown: only reading goes ahead. Do not try another way; say so in your reply."), nil
		}
		if risk == skill.RiskWrite {
			if budget := g.Budget(c); budget.Rank() < skill.RiskWrite.Rank() {
				return mcp.ErrorResult(fmt.Sprintf("this run's risk level is %s, which does not allow writing", budget)), nil
			}
		}
		if risk == skill.RiskIrreversible {
			var args struct {
				ApprovalID string `json:"approval_id"`
			}
			_ = json.Unmarshal(raw, &args)
			if args.ApprovalID == "" {
				return mcp.ErrorResult("this tool takes an irreversible step: call request_approval first, and pass the approval_id it answers approved with"), nil
			}
			if err := g.UseApproval(ctx, c, args.ApprovalID); err != nil {
				return mcp.ErrorResult(err.Error() + ". Do not take the step."), nil
			}
		}
		return call(ctx, c, raw)
	}
}
