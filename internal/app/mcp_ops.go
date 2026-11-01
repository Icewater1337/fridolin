package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/housekeeping"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/mcp/tools"
	"github.com/Icewater1337/fridolin/internal/sched"
	"github.com/Icewater1337/fridolin/internal/skill"
)

// The core brain's ops tools (docs/BRAINS.md, "The core brain's ops
// tools"): what a maintenance agent needs to see how the system stands
// and to act on it. system_status is one snapshot, built from what the
// dashboard's pages compute; schedule_run, run_stop and housekeeping_run
// do what the Schedules page's Run now, a brain's Stop and `fridolin
// housekeeping run` do. Only the core brain's runs that answer its owner,
// and those of its own scheduled agents, have them: not a run for a group
// member, a file handed in or another program, not a sub-brain's run and
// not a delegated one.

// opsTools reports whether the run has the ops tools: a core run that
// answers the owner (ownerAsked, as the brain's own GitHub grant goes),
// or one in a chat of the core brain's own scheduled agents,
// schedule:<name>, the runs FrIDE's news starts there included.
func (b *App) opsTools(c mcpCaller) bool {
	return c.Core() && (chat.IsScheduleChat(c.run.Chat) || ownerAsked(c.run, b.cfgs.Get()))
}

// registerOpsTools registers them: the three that act start or stop work
// and so are write, refused in a lockdown and to a read budget;
// system_status only reads.
func (b *App) registerOpsTools(r *tools.Set[mcpCaller]) {
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "system_status",
		Available: b.opsTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "How Fridolin stands now, as one JSON snapshot: each brain's runs (its chats' and, beside them, its scheduled agents'), with when each started, what waits, and its live session; the scheduled agents with their state, last and next run and failures; housekeeping; backups and the vault; the nodes and what they offer; FrIDE's instances and open threads; the Plaud phones; and the core's version, uptime and free disk. Chats are named by kind (dm, group, schedule:<name>, delegate, internal), never by number. It holds no message text and no secrets.",
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
				Description: "Start a scheduled agent now, whatever its schedule, as the Schedules page's Run now does: one of this brain, or of a brain you hold a grant on (list_brains). It runs in its brain's background lane and reports as it always does; an agent never runs twice at once. Refused for an agent that is paused, switched off or has a change waiting for the owner's approval, and for one whose risk level is above this run's own.",
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
				Description: "Stop one run of this brain, or of a brain you hold a grant on: the one in its chat lane (what it answers in a chat) or its background lane (a scheduled agent's run) that started at started, as system_status reported it. When another run goes there by now, or none, nothing is stopped and you are told what runs. What waits in the lane runs next; nobody is told. Never the run that calls it. Use it for a run that is stuck or runs far too long.",
				InputSchema: tools.Schema(map[string]any{
					"brain":   tools.Str("The brain's id."),
					"lane":    map[string]any{"type": "string", "enum": []string{"chat", "background"}, "description": "chat or background."},
					"started": tools.Str("When the run started, exactly as system_status gave it (the run's started)."),
				}, "brain", "lane", "started"),
			}
		},
		Call: r.Guarded(skill.RiskWrite, b.callRunStop),
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "housekeeping_run",
		Available: b.opsTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Run the housekeeping job now, as `fridolin housekeeping run` does: a live run, not the dashboard's dry Check now. It looks for leftover and runaway processes, stale locks and run records, and may stop those processes, remove those locks and records, and put a process it cannot tell is the bridge's to the owner as an approval poll. It follows housekeeping.dry_run (on, it only reports), and is refused in a lockdown. Returns its report.",
				InputSchema: tools.Schema(map[string]any{
					"dry_run": map[string]any{"type": "boolean", "description": "true only reports what it would do, whatever housekeeping.dry_run says. Leave it out to run as the setting says."},
				}),
			}
		},
		Call: r.Guarded(skill.RiskWrite, b.callHousekeepingRun),
	})
}

// opsTrigger is who started what an ops tool starts, as the run
// histories and the audit log name it.
const opsTrigger = "core brain"

// opsReach reports whether the ops tools that act may act on brain id:
// the caller's own brain, and the brains it holds a grant on, which
// list_brains lists.
func opsReach(c mcpCaller, id string) bool {
	return id == c.run.Brain || slices.Contains(c.Manifest.GrantedTo(""), id)
}

// noReach is the refusal of a brain beyond opsReach.
func noReach(id string) *mcp.CallResult {
	return mcp.ErrorResult(fmt.Sprintf("brain %q is neither this brain nor one it holds a grant on (list_brains): the ops tools do not reach it", id))
}

// agentRisk is the risk level agent s's runs get: its own, else what a
// run gets that sets none.
func agentRisk(s manifest.Schedule) skill.Risk {
	if r := skill.Risk(s.Risk); r.Valid() {
		return r
	}
	return defaultRisk
}

func (b *App) callScheduleRun(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var a struct {
		Brain string `json:"brain"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return mcp.ErrorResult("unreadable arguments: " + err.Error()), nil
	}
	a.Brain, a.Name = strings.TrimSpace(a.Brain), strings.TrimSpace(a.Name)
	if !opsReach(c, a.Brain) {
		return noReach(a.Brain), nil
	}
	// As the Schedules page lists it, with its state and its review.
	list, err := b.agents.view(ctx)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	i := slices.IndexFunc(list.Agents, func(v scheduleView) bool { return v.Brain == a.Brain && v.Name == a.Name })
	if i < 0 {
		return mcp.ErrorResult(fmt.Sprintf("brain %s has no scheduled agent %q", a.Brain, a.Name)), nil
	}
	v := list.Agents[i]
	switch {
	case v.State == AgentPending || v.Review != nil && v.Review.Status != manifest.ReviewInvalid:
		what := "it is new"
		if v.Review != nil {
			what = "its file is " + v.Review.Status
		}
		return mcp.ErrorResult(fmt.Sprintf("scheduled agent %s waits for the owner's approval on the Schedules page (%s): it does not run before that", a.Name, what)), nil
	case !v.Enabled:
		return mcp.ErrorResult(fmt.Sprintf("scheduled agent %s is switched off (enabled false): only the owner switches it on", a.Name)), nil
	case v.Paused:
		return mcp.ErrorResult(fmt.Sprintf("scheduled agent %s is paused on the Schedules page: only the owner resumes it", a.Name)), nil
	}
	// No escalation: a run starts no agent that may do more than itself,
	// as a delegated task's run gets its core run's budget.
	if risk, own := agentRisk(v.Schedule), c.risk(); risk.Rank() > own.Rank() {
		return mcp.ErrorResult(fmt.Sprintf("scheduled agent %s runs at risk level %s, above this run's %s: a run does not start one that may do more than itself", a.Name, risk, own)), nil
	}
	out, err := b.agents.runNow(ctx, a.Brain, a.Name, opsTrigger)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	r, _ := out.(sched.Run)
	return mcp.TextResult(fmt.Sprintf("Started scheduled agent %s of brain %s (run %s).", r.Agent, r.Brain, r.ID)), nil
}

func (b *App) callRunStop(_ context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var a struct {
		Brain   string `json:"brain"`
		Lane    string `json:"lane"`
		Started string `json:"started"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return mcp.ErrorResult("unreadable arguments: " + err.Error()), nil
	}
	lane, ok := brain.ParseLane(strings.TrimSpace(a.Lane))
	if !ok {
		return mcp.ErrorResult(`lane must be "chat" or "background"`), nil
	}
	started, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(a.Started))
	if err != nil {
		return mcp.ErrorResult("started must be the run's started as system_status gave it, such as 2026-11-01T10:00:00.123456Z"), nil
	}
	id := strings.TrimSpace(a.Brain)
	if !opsReach(c, id) {
		return noReach(id), nil
	}
	br := b.brainMgr.Get(id)
	if br == nil {
		return mcp.ErrorResult(fmt.Sprintf("there is no brain %q", id)), nil
	}
	// A lane runs one run at a time: the caller's lane of its own brain
	// runs the caller.
	if br.ID() == c.run.Brain && brain.LaneOf(c.run.Chat) == lane {
		return mcp.ErrorResult(fmt.Sprintf("that is this run itself (brain %s, %s lane): a run does not stop itself; end it by answering", br.ID(), lane)), nil
	}
	run, err := br.StopLaneRun(lane, started)
	switch {
	case errors.Is(err, brain.ErrNoRun):
		return mcp.ErrorResult(fmt.Sprintf("nothing runs in brain %s's %s lane now: nothing was stopped", br.ID(), lane)), nil
	case errors.Is(err, brain.ErrOtherRun):
		return mcp.ErrorResult(fmt.Sprintf("the run in brain %s's %s lane now is another one, for %s, started %s: nothing was stopped; look again with system_status",
			br.ID(), lane, chatKind(run.Chat), run.Started.UTC().Format(time.RFC3339Nano))), nil
	case err != nil:
		return mcp.ErrorResult(err.Error()), nil
	}
	return mcp.TextResult(fmt.Sprintf("Stopped brain %s's %s run for %s, which had run for %s.", br.ID(), lane, chatKind(run.Chat), time.Since(run.Started).Round(time.Second))), nil
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
