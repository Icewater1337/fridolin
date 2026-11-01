package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/housekeeping"
	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/mcp/tools"
	"github.com/Icewater1337/fridolin/internal/sched"
	"github.com/Icewater1337/fridolin/internal/skill"
)

// The core brain's ops tools (docs/BRAINS.md, "The core brain and its
// grants"): what a maintenance agent needs to see how the system stands
// and to act on it as the owner does on the dashboard. system_status is
// one snapshot, built from what the dashboard's pages compute;
// schedule_run, run_stop and housekeeping_run do what the Schedules
// page's Run now, a brain's Stop and the Housekeeping card's Run now do. A
// core run has them; a sub-brain and a delegated run never do.

// opsTools reports whether the run has the ops tools.
func (b *App) opsTools(c mcpCaller) bool { return c.Core() }

// registerOpsTools registers them: the three that act start or stop work
// and so are write, refused in a lockdown and to a read budget;
// system_status only reads.
func (b *App) registerOpsTools(r *tools.Set[mcpCaller]) {
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "system_status",
		Available: b.opsTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "How Fridolin stands now, as one JSON snapshot: each brain's runs (its chats' and, beside them, its scheduled agents'), what waits, and its live session; the scheduled agents with their state, last and next run and failures; housekeeping; backups and the vault; the nodes and what they offer; FrIDE's instances and open threads; the Plaud phones; and the core's version, uptime and free disk. It holds no message text and no secrets.",
				InputSchema: tools.Schema(nil),
			}
		},
		Call: b.callSystemStatus,
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "schedule_run",
		Available: b.opsTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Start a scheduled agent now, whatever its schedule, as the Schedules page's Run now does. It runs in its brain's background lane and reports as it always does. An agent never runs twice at once.",
				InputSchema: tools.Schema(map[string]any{
					"brain": tools.Str("The brain's id, as system_status lists it."),
					"name":  tools.Str("The scheduled agent's name."),
				}, "brain", "name"),
			}
		},
		Call: r.Guarded(skill.RiskWrite, b.callScheduleRun),
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "run_stop",
		Available: b.opsTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Stop a brain's current run in one lane: chat (what it answers in a chat) or background (a scheduled agent's run). What waits in the lane runs next; nobody is told. Not the run that calls it. Use it for a run that is stuck or runs far too long, as system_status shows.",
				InputSchema: tools.Schema(map[string]any{
					"brain": tools.Str("The brain's id."),
					"lane":  map[string]any{"type": "string", "enum": []string{"chat", "background"}, "description": "chat or background."},
				}, "brain", "lane"),
			}
		},
		Call: r.Guarded(skill.RiskWrite, b.callRunStop),
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "housekeeping_run",
		Available: b.opsTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Run the housekeeping job now, as the dashboard's Run now does: it looks for leftover and runaway processes, stale locks and sessions, and stops or removes them as housekeeping's settings allow (only reports in a dry run, or while housekeeping.dry_run is on). Returns its report.",
				InputSchema: tools.Schema(map[string]any{
					"dry_run": map[string]any{"type": "boolean", "description": "Only report what it would do (default false: as housekeeping.dry_run says)."},
				}),
			}
		},
		Call: r.Guarded(skill.RiskWrite, b.callHousekeepingRun),
	})
}

// opsTrigger is who started what an ops tool starts, as the run
// histories and the audit log name it.
const opsTrigger = "core brain"

func (b *App) callScheduleRun(ctx context.Context, _ mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var a struct {
		Brain string `json:"brain"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return mcp.ErrorResult("unreadable arguments: " + err.Error()), nil
	}
	out, err := b.agents.runNow(ctx, strings.TrimSpace(a.Brain), strings.TrimSpace(a.Name), opsTrigger)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	r, _ := out.(sched.Run)
	return mcp.TextResult(fmt.Sprintf("Started scheduled agent %s of brain %s (run %s).", r.Agent, r.Brain, r.ID)), nil
}

func (b *App) callRunStop(_ context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var a struct {
		Brain string `json:"brain"`
		Lane  string `json:"lane"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return mcp.ErrorResult("unreadable arguments: " + err.Error()), nil
	}
	lane, ok := brain.ParseLane(strings.TrimSpace(a.Lane))
	if !ok {
		return mcp.ErrorResult(`lane must be "chat" or "background"`), nil
	}
	br := b.brainMgr.Get(strings.TrimSpace(a.Brain))
	if br == nil {
		return mcp.ErrorResult(fmt.Sprintf("there is no brain %q", a.Brain)), nil
	}
	// A lane runs one run at a time: the caller's lane of its own brain
	// runs the caller.
	if br.ID() == c.run.Brain && brain.LaneOf(c.run.Chat) == lane {
		return mcp.ErrorResult(fmt.Sprintf("that is this run itself (brain %s, %s lane): a run does not stop itself; end it by answering", br.ID(), lane)), nil
	}
	rt := br.Snapshot()
	run := rt.Running
	if lane == brain.LaneBackground {
		run = rt.Background
	}
	if !br.StopLane(lane) || run == nil {
		return mcp.TextResult(fmt.Sprintf("Nothing ran in brain %s's %s lane.", br.ID(), lane)), nil
	}
	return mcp.TextResult(fmt.Sprintf("Stopped brain %s's %s run for %s, which had run for %s.", br.ID(), lane, runLabel(run.Chat), time.Since(run.Started).Round(time.Second))), nil
}

// runLabel names the chat a run was for: a scheduled agent by its name.
func runLabel(jid string) string {
	if chat.IsScheduleChat(jid) {
		return "scheduled agent " + strings.TrimPrefix(jid, chat.ScheduleChatPrefix)
	}
	return jid
}

func (b *App) callHousekeepingRun(ctx context.Context, _ mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var a struct {
		DryRun bool `json:"dry_run"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return mcp.ErrorResult("unreadable arguments: " + err.Error()), nil
		}
	}
	rep, err := b.housekeeping.Run(context.WithoutCancel(ctx), opsTrigger, a.DryRun)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	return jsonResult(opsReportOf(rep)), nil
}

// opsReport is a housekeeping report as the tools give it: what each
// finding is about and what came of it, without the command lines, which
// can hold anything.
type opsReport struct {
	Started  time.Time           `json:"started"`
	Finished time.Time           `json:"finished"`
	DryRun   bool                `json:"dry_run"`
	Note     string              `json:"note,omitempty"`
	Summary  string              `json:"summary"`
	Counts   housekeeping.Counts `json:"counts"`
	Findings []opsFinding        `json:"findings"`
	Errors   []string            `json:"errors,omitempty"`
}

type opsFinding struct {
	Kind    string `json:"kind"`
	Action  string `json:"action"`
	PID     int    `json:"pid,omitempty"`
	Name    string `json:"name,omitempty"`
	Owner   string `json:"owner,omitempty"`
	Target  string `json:"target,omitempty"`
	Reason  string `json:"reason"`
	Outcome string `json:"outcome,omitempty"`
	Problem bool   `json:"problem,omitempty"`
}

func opsReportOf(rep housekeeping.Report) opsReport {
	out := opsReport{Started: rep.Started, Finished: rep.Finished, DryRun: rep.DryRun, Note: rep.Note, Summary: rep.Summary(), Counts: rep.Counts(), Findings: []opsFinding{}, Errors: rep.Errors}
	for _, f := range rep.Findings {
		out.Findings = append(out.Findings, opsFinding{Kind: f.Kind, Action: f.Action, PID: f.PID, Name: f.Name, Owner: f.Owner, Target: f.Target, Reason: f.Reason, Outcome: f.Outcome, Problem: f.Problem})
	}
	return out
}
