package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/audit"
	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/skill"
)

// runPolicy is one run's standing with the policy, fixed as it starts:
// its thread, the skill the bridge took it for, and its risk budget, the
// highest risk the bridge's tools grant it. The bridge infers the skill
// from the triggers of the brain's approved skills in the message; a
// delegated task's run inherits its core run's thread and budget.
type runPolicy struct {
	ID, Brain, Chat string
	// Thread is the chat the run serves; a delegated task's run is in its
	// core run's thread.
	Thread string
	Skill  string
	Risk   skill.Risk
	// From is, for a delegated task's run, the core run that delegated it,
	// and FromBrain its brain.
	From, FromBrain string
	// Approval is, for a delegated task's run, the owner's approval the
	// core run handed over with the task (delegate's approval_id), and
	// ApprovedAction what it approved: the run takes that step without
	// asking again, and its irreversible tools take the id.
	Approval, ApprovedAction string
	// Silent is an observe-only run, which asks nobody anything.
	Silent bool
	// Scheduled is, for a scheduled agent's run, the agent; Timeout and
	// DisallowedTools are what the agent sets for its runs
	// (agents.admitScheduled).
	Scheduled       string
	Timeout         time.Duration
	DisallowedTools *[]string
	started         time.Time
}

// defaultRisk is the budget of a run no skill trigger matched: ordinary
// work, which may write (files, a calendar event) and reports; an
// irreversible step still needs an approval.
const defaultRisk = skill.RiskWrite

// policyState is the policy's state in memory: the lockdown (kept in
// bridge.db too, so a restart keeps it) and each chat's recent runs.
type policyState struct {
	mu       sync.Mutex
	lockdown bool
	since    time.Time
	by       string
	runs     map[string][]time.Time // chat → start times in the last hour
}

const lockdownKey = "lockdown"

// loadPolicy reads the lockdown kept in bridge.db.
func (b *App) loadPolicy() {
	v, at, err := b.store.PolicyState(context.Background(), lockdownKey)
	if err != nil {
		log.Printf("policy: reading the lockdown state: %v", err)
		return
	}
	if v != "" {
		b.policy.lockdown, b.policy.since, b.policy.by = true, at, v
		log.Printf("policy: the bridge is in lockdown (since %s, by %s): only read-only work runs until it is unlocked", at.Format(time.RFC3339), v)
	}
}

// locked reports whether the bridge is in lockdown.
func (b *App) locked() bool {
	b.policy.mu.Lock()
	defer b.policy.mu.Unlock()
	return b.policy.lockdown
}

// setLockdown locks the bridge down or unlocks it, for by (a number, or
// "dashboard"). It reports whether it was in that state already.
func (b *App) setLockdown(ctx context.Context, on bool, by string) (already bool, err error) {
	b.policy.mu.Lock()
	defer b.policy.mu.Unlock()
	if b.policy.lockdown == on {
		return true, nil
	}
	now := time.Now()
	value := ""
	if on {
		value = by
	}
	if err := b.store.SetPolicyState(ctx, lockdownKey, value, now); err != nil {
		return false, fmt.Errorf("recording the lockdown: %w", err)
	}
	b.policy.lockdown, b.policy.since, b.policy.by = on, now, by
	kind := audit.Unlock
	if on {
		kind = audit.Lockdown
	}
	b.audit.Append(audit.Entry{Kind: kind, By: by, Outcome: "ok"})
	log.Printf("policy: %s by %s", kind, by)
	if on && b.work != nil {
		// Stopping a session asks its node; the lock is not held for it.
		go b.work.lockdown()
	}
	return false, nil
}

// isOwner reports whether number, a sender or voter, is the owner's: a
// number listed in allowed_numbers. A bridge open to every DM has no
// owner but the dashboard.
func (b *App) isOwner(number string) bool {
	n, err := chat.PhoneNumber(number)
	if err != nil {
		return false
	}
	return slices.Contains(chat.NumbersAsNumbers(b.cfgs.Get().AllowedNumbers), n)
}

// lockdownCommand answers !lockdown and !unlock from m, which only the
// owner may send.
func (b *App) lockdownCommand(m brainruntime.Inbound, on bool) string {
	if !b.isOwner(m.Sender) {
		return outboundmsg.LockdownOwnerOnly
	}
	n, _ := chat.PhoneNumber(m.Sender)
	already, err := b.setLockdown(context.Background(), on, n)
	if err != nil {
		log.Printf("policy: %v", err)
		return outboundmsg.ChatErrGeneric
	}
	return outboundmsg.LockdownReply(on, already)
}

// allowRun counts a run in chat's last hour, unless it would be more
// than max; 0 is no limit.
func (p *policyState) allowRun(chatJID string, now time.Time, maxRuns int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.runs == nil {
		p.runs = map[string][]time.Time{}
	}
	cut := now.Add(-time.Hour)
	recent := slices.DeleteFunc(p.runs[chatJID], func(t time.Time) bool { return t.Before(cut) })
	if maxRuns > 0 && len(recent) >= maxRuns {
		p.runs[chatJID] = recent
		return false
	}
	p.runs[chatJID] = append(recent, now)
	return true
}

// admitRun decides, before a run starts, whether it may, and with what
// standing (runPolicy). It refuses, with what the chat is told, a run in
// a lockdown that needs more than reading, and one over its chat's
// policy.max_runs_per_hour.
func (b *App) admitRun(spec brainruntime.RunSpec) (*runPolicy, error) {
	now := time.Now()
	p := &runPolicy{ID: "r" + randomToken(9), Brain: spec.ID, Chat: spec.Message.JID, Thread: spec.Message.JID, Silent: spec.Message.Silent, started: now}
	if spec.Message.Source == chat.SourceDelegate {
		p.Risk = skill.RiskRead
		if d := b.delegations.get(spec.Message.ID); d != nil && d.from.Policy != nil {
			from := d.from.Policy
			p.Thread, p.Skill, p.Risk, p.From, p.FromBrain = from.Thread, from.Skill, from.Risk, from.ID, from.Brain
			p.Approval, p.ApprovedAction = d.approval.ID, d.approval.Action
		}
	} else {
		p.Skill, p.Risk = b.inferSkill(spec.ID, spec.Message.Text)
	}
	declared := p.Skill != ""
	if spec.Message.Source == chat.SourceScheduled {
		b.agents.admitScheduled(p, spec.Message)
		declared = declared || b.agents.declaresRisk(spec.Message.ID)
	}
	locked := b.locked()
	refuse := func(reason, notice string) (*runPolicy, error) {
		b.audit.Append(audit.Entry{Kind: audit.RunRefused, Thread: p.Thread, Chat: p.Chat, Brain: p.Brain, Run: p.ID, Skill: p.Skill, Risk: string(p.Risk), Outcome: reason})
		log.Printf("brain %s: the policy did not start a run for %s: %s", p.Brain, p.Chat, reason)
		return nil, &brainruntime.RunRefused{Reason: reason, Notice: notice}
	}
	if locked && declared && p.Risk.Rank() > skill.RiskRead.Rank() {
		what := "skill " + p.Skill
		if p.Skill == "" {
			what = "scheduled agent " + p.Scheduled
		}
		return refuse(fmt.Sprintf("lockdown, and %s is %s", what, p.Risk), outboundmsg.LockdownRefusedNotice)
	}
	if locked {
		p.Risk = skill.RiskRead
	}
	handedIn := chat.Inbound{Source: spec.Message.Source}.HandedIn()
	if maxRuns := b.cfgs.Get().Live.Policy.MaxRunsPerHour; !handedIn && !spec.Message.Silent && !b.policy.allowRun(spec.Message.JID, now, maxRuns) {
		return refuse(fmt.Sprintf("rate limit, %d runs in the last hour", maxRuns), outboundmsg.RateLimitedNotice(maxRuns))
	}
	detail := ""
	if p.From != "" {
		detail = "delegated by run " + p.From + " of " + p.FromBrain
		if p.Approval != "" {
			detail += ", with approval " + p.Approval
		}
	}
	if p.Scheduled != "" {
		detail = "scheduled agent " + p.Scheduled
	}
	b.audit.Append(audit.Entry{Kind: audit.RunStart, Thread: p.Thread, Chat: p.Chat, Brain: p.Brain, Run: p.ID, Skill: p.Skill, Risk: string(p.Risk), Detail: detail})
	return p, nil
}

// runEnded writes the audit line of a run that ended.
func (b *App) runEnded(p *runPolicy, res claudecli.Result) {
	if p == nil {
		return
	}
	outcome := "ok"
	switch {
	case res.Err == nil:
	case errors.Is(res.Err, claudecli.ErrInterrupted), errors.Is(res.Err, context.Canceled):
		outcome = "stopped"
	default:
		outcome = "failed"
	}
	b.audit.Append(audit.Entry{Kind: audit.RunEnd, Thread: p.Thread, Chat: p.Chat, Brain: p.Brain, Run: p.ID, Skill: p.Skill, Risk: string(p.Risk), Outcome: outcome, DurationMS: time.Since(p.started).Milliseconds()})
}

// inferSkill is the skill a message is for, and the run's budget: of the
// brain's skills whose approved triggers the text names, the riskiest;
// none, defaultRisk.
func (b *App) inferSkill(brainID, text string) (string, skill.Risk) {
	if strings.TrimSpace(text) == "" {
		return "", defaultRisk
	}
	skills, err := b.brainSkills(context.Background(), brainID)
	if err != nil {
		return "", defaultRisk
	}
	name, risk := "", skill.Risk("")
	for _, s := range skills {
		if !s.matches(text) {
			continue
		}
		if name == "" || s.Effective.Risk.Rank() > risk.Rank() {
			name, risk = s.Name, s.Effective.Risk
		}
	}
	if name == "" {
		return "", defaultRisk
	}
	return name, risk
}

// policyStatus answers GET /api/policy.
func (b *App) policyStatus(ctx context.Context) (any, error) {
	b.sweepApprovals(ctx)
	pending, err := b.store.CountPending(ctx, approvalKind, "")
	if err != nil {
		return nil, err
	}
	questions, err := b.store.CountPending(ctx, questionKind, "")
	if err != nil {
		return nil, err
	}
	p := b.cfgs.Get().Live.Policy
	b.policy.mu.Lock()
	defer b.policy.mu.Unlock()
	out := map[string]any{
		"lockdown": b.policy.lockdown, "lockdown_by": b.policy.by, "lockdown_since": nil,
		"pending_approvals": pending, "pending_questions": questions,
		"max_runs_per_hour": p.MaxRunsPerHour, "max_pending_irreversible": p.MaxPendingIrreversible, "approval_timeout_hours": p.ApprovalTimeoutHours,
	}
	if b.policy.lockdown {
		out["lockdown_since"] = b.policy.since.UTC().Format(time.RFC3339)
	}
	return out, nil
}

// dashboardLockdown answers POST /api/policy/lockdown.
func (b *App) dashboardLockdown(ctx context.Context, on bool) (any, error) {
	already, err := b.setLockdown(ctx, on, "dashboard")
	if err != nil {
		return nil, apperr.Wrap(apperr.Unavailable, err)
	}
	return map[string]any{"lockdown": on, "changed": !already}, nil
}
