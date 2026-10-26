package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/audit"
	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/mcp/tools"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/textx"
)

// How long a delegate call waits for the sub-brain's answer, unless it
// says otherwise, and at most. An answer that comes later goes to the
// core brain's chat as a new message (deliverLate).
var (
	defaultDelegateWait = 2 * time.Minute
	maxDelegateWait     = 10 * time.Minute
)

// brainName is brain id's display name, "" when it is not loaded.
func (b *App) brainName(id string) string {
	if br := b.brainMgr.Get(id); br != nil {
		return br.Name()
	}
	return ""
}

func (b *App) describeDelegate(c mcpCaller) mcp.Tool {
	targets := c.Granted(manifest.GrantDelegate)
	return mcp.Tool{
		Description: "Hand a bounded task to another brain and get its answer. That brain works on it with its own instructions, memory and files, which you do not see, so put everything it needs into the task. " +
			fmt.Sprintf("Waits up to timeout_seconds (default %d, at most %d); an answer that comes later arrives in this chat as a new message. ", int(defaultDelegateWait/time.Second), int(maxDelegateWait/time.Second)) +
			"Delegating is not an irreversible step itself: never ask the person for approval just to hand a task over. " +
			"When the task holds an irreversible step (sending, posting, paying, submitting, deleting), ask for that step once, before you delegate: call request_approval with the step as the brain will take it (\"Post 'Hello' in the family group\", not \"Ask the family brain to post\"), and pass the approval_id it answers approved with here. The brain then takes exactly that step without asking again; any other irreversible step it still asks for. Without approval_id, the brain asks the person itself. " +
			"Brains you may delegate to: " + tools.BrainList(targets, b.brainName) + ".",
		InputSchema: tools.Schema(map[string]any{
			"brain":           map[string]any{"type": "string", "enum": targets, "description": "The brain's id."},
			"task":            tools.Str("What the brain is to do, with all it needs to know. It cannot ask you back."),
			"approval_id":     tools.Str("The approval_id of the request_approval in which the person approved the task's irreversible step, if you asked them."),
			"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": int(maxDelegateWait / time.Second), "description": "How long to wait for the answer."},
		}, "brain", "task"),
	}
}

func (b *App) callDelegate(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var args struct {
		Brain          string `json:"brain"`
		Task           string `json:"task"`
		ApprovalID     string `json:"approval_id"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcp.ErrorResult("the arguments are {\"brain\": \"<id>\", \"task\": \"…\", \"approval_id\": \"…\", \"timeout_seconds\": N}: " + err.Error()), nil
	}
	if strings.TrimSpace(args.Task) == "" {
		return mcp.ErrorResult("task is empty: say what the brain is to do"), nil
	}
	// Checked again here, not only by Available: the grant may have gone
	// since the run listed its tools.
	if !c.Manifest.Granted(args.Brain, manifest.GrantDelegate) || !c.Running(args.Brain) {
		return mcp.ErrorResult(fmt.Sprintf("you may not delegate to %q; you may delegate to: %s", args.Brain, tools.BrainList(c.Granted(manifest.GrantDelegate), b.brainName))), nil
	}
	wait := defaultDelegateWait
	if args.TimeoutSeconds > 0 {
		wait = min(time.Duration(args.TimeoutSeconds)*time.Second, maxDelegateWait)
	}
	var approval store.Approval
	if id := strings.TrimSpace(args.ApprovalID); id != "" {
		a, msg, err := b.handOver(ctx, c, id, args.Brain)
		if err != nil {
			return nil, err
		}
		if msg != "" {
			return mcp.ErrorResult(msg), nil
		}
		approval = a
	}
	return b.delegate(ctx, c, args.Brain, args.Task, approval, wait), nil
}

// handOver checks approval id, which c's run passes on to brain to with
// a delegated task: an approved request_approval of the run's brain, not
// used yet. It returns what goes with the task, or msg, why nothing may,
// for the run. An approval the core's approvals: auto gave, not the
// owner, goes only to a brain that approves by itself too: to one that
// asks, it does not travel, and that brain asks as its own setting says.
func (b *App) handOver(ctx context.Context, c mcpCaller, id, to string) (a store.Approval, msg string, err error) {
	a, err = b.store.ApprovalByID(ctx, id)
	switch {
	case errors.Is(err, sql.ErrNoRows) || (err == nil && (a.Brain != c.run.Brain || a.Kind != approvalKind)):
		return a, fmt.Sprintf("there is no approval %q of yours: pass the approval_id request_approval answered approved with, or none", id), nil
	case err != nil:
		return a, "", err
	case a.Status != store.ApprovalApproved:
		return a, fmt.Sprintf("approval %s is %s, not approved: do not delegate its step", id, a.Status), nil
	case a.Used:
		return a, fmt.Sprintf("approval %s was used already: ask again for another action", id), nil
	}
	if a.DecidedBy == autoApprovedBy {
		if br := b.brainMgr.Get(to); br == nil || !br.Manifest().AutoApproves() {
			log.Printf("brain %s: approval %s came from its approvals: auto, not the owner; it does not go to %s, which asks the owner itself", c.run.Brain, id, to)
			return store.Approval{}, "", nil
		}
	}
	return a, "", nil
}

func (b *App) callListBrains(_ context.Context, c mcpCaller, _ json.RawMessage) (*mcp.CallResult, error) {
	type entry struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Grants  []string `json:"grants"`
		Running bool     `json:"running"`
	}
	out := []entry{}
	for _, id := range c.Manifest.GrantedTo("") {
		out = append(out, entry{ID: id, Name: b.brainName(id), Grants: c.Manifest.Normalized().Grants[id], Running: c.Running(id)})
	}
	data, _ := json.MarshalIndent(map[string]any{"brains": out}, "", "  ")
	return mcp.TextResult(string(data)), nil
}

// delegations are the tasks core runs handed to sub-brains that have not
// ended yet, by the id their message carries (chat.Inbound.ID).
type delegations struct {
	mu      sync.Mutex
	pending map[string]*delegation
}

// delegation is one task a core run handed to a sub-brain.
type delegation struct {
	id       string
	from     runInfo // the core run that delegated
	to, task string
	// approval is the owner's approval the core run handed over with the
	// task (delegate's approval_id); its ID is "" for none.
	approval store.Approval
	started  time.Time
	// result gets the answer while the core run waits for it.
	result chan delegationResult
	// late is set once the core run stopped waiting: the answer then goes
	// to the core brain's chat as a new message.
	late bool
}

type delegationResult struct {
	text string
	err  error
}

func (ds *delegations) add(d *delegation) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	if ds.pending == nil {
		ds.pending = map[string]*delegation{}
	}
	ds.pending[d.id] = d
}

// get returns delegation id while it is pending, or nil.
func (ds *delegations) get(id string) *delegation {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	return ds.pending[id]
}

// end takes delegation id out, once: the second end of the same task (the
// run's Done after its Finished) finds nothing. It reports whether the
// core run had stopped waiting by then.
func (ds *delegations) end(id string) (d *delegation, late bool) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	d = ds.pending[id]
	if d == nil {
		return nil, false
	}
	delete(ds.pending, id)
	return d, d.late
}

// giveUp marks id late, unless it has ended meanwhile, which it reports.
func (ds *delegations) giveUp(id string) (ended bool) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	d := ds.pending[id]
	if d == nil {
		return true
	}
	d.late = true
	return false
}

// delegateChat is the chat a core run's delegated tasks are in, in the
// sub-brain's history: one per core run, so that the sub-brain sees what
// it was asked before in the same run, and nothing of other runs.
func delegateChat(coreRun string) string { return chat.DelegateChatPrefix + coreRun }

// delegate hands task to brain to as a message from the core brain in
// its delegate chat, and waits up to wait for the answer: the sub-brain's
// worker runs it like any message, with its own prompt, memory and
// sandbox. Its answer comes back here, never to a chat (delegationFinished).
func (b *App) delegate(ctx context.Context, c mcpCaller, to, task string, approval store.Approval, wait time.Duration) *mcp.CallResult {
	d := &delegation{id: "d" + randomToken(9), from: c.run, to: to, task: task, approval: approval, started: time.Now(), result: make(chan delegationResult, 1)}
	b.delegations.add(d)
	m := brainruntime.Inbound{Inbound: chat.Inbound{
		JID: delegateChat(c.run.ID), Brain: to, Sender: c.run.Brain, Text: delegatedTask(task, approval),
		Source: chat.SourceDelegate, At: d.started, ID: d.id,
		// After a run Finished has handled, this finds the task ended; it
		// ends one that never ran: dropped, or its brain removed.
		Done: func(err error) {
			if err == nil {
				err = errors.New("the run ended without an answer")
			}
			b.endDelegation(d.id, "", err)
		},
	}}
	if _, _, _, err := b.brainMgr.Submit(m); err != nil {
		b.delegations.end(d.id)
		b.auditDelegation(d, "refused: "+err.Error())
		return mcp.ErrorResult(fmt.Sprintf("%s did not take the task: %v", to, err))
	}
	log.Printf("brain %s: delegated a task to %s (%s)", c.run.Brain, to, d.id)
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case r := <-d.result:
		return delegationAnswer(to, r)
	case <-timer.C:
	case <-ctx.Done():
	}
	if b.delegations.giveUp(d.id) {
		// It ended as the wait did: the answer is there after all.
		return delegationAnswer(to, <-d.result)
	}
	return mcp.TextResult(fmt.Sprintf("%s is still working on it after %s. Its answer will come to you in this chat as a new message once it is there; do not delegate the same task again meanwhile.", to, wait.Round(time.Second)))
}

// delegatedTask is the message a delegated task's run is handed: the task,
// after the step the owner approved for it, if the core run handed an
// approval over, as a scheduled agent's message names the steps approved
// with its schedule (sched.Message). The owner is asked once, where the
// conversation is: in the core run, before it delegates. The run may use
// the id (runPolicy.Approval); every other irreversible step still asks.
func delegatedTask(task string, a store.Approval) string {
	if a.ID == "" {
		return task
	}
	return fmt.Sprintf("The owner approved this step already, before the task was handed to you (approval_id %s): %q. Take it without request_approval, and pass approval_id %s to a tool that asks for one. Every other irreversible step still needs request_approval.\n\nThe task:\n\n%s",
		a.ID, textx.Truncate(a.Action, 300), a.ID, task)
}

// delegationAnswer is the tool result for a delegated task's outcome.
func delegationAnswer(to string, r delegationResult) *mcp.CallResult {
	if r.err != nil {
		return mcp.ErrorResult(fmt.Sprintf("%s could not do the task: %s", to, delegationFailure(r.err)))
	}
	if r.text == "" {
		return mcp.TextResult(fmt.Sprintf("%s finished without an answer.", to))
	}
	return mcp.TextResult(r.text)
}

// delegationFailure says why a delegated task failed, without the CLI's
// stderr that a run's error can hold: it ends up in a chat.
func delegationFailure(err error) string {
	switch {
	case errors.Is(err, brainruntime.ErrDropped), errors.Is(err, brainruntime.ErrStopped):
		return "it was dropped before it ran (the brain was stopped, removed or restarted)"
	case errors.Is(err, claudecli.ErrInterrupted), errors.Is(err, context.Canceled):
		return "its run was stopped"
	}
	return "its run failed (the dashboard's log says why)"
}

// delegationFinished takes a delegated task's finished run, in place of
// delivery to a chat: the answer goes to the core run that waits for it,
// or, once that stopped waiting, to the core brain's chat. It goes into the
// sub-brain's history too, in the task's chat, so that a later task of the
// same core run sees it.
func (b *App) delegationFinished(br *brainruntime.Brain, m brainruntime.Inbound, res claudecli.Result) {
	if res.Err == nil && res.Text != "" {
		br.AppendEntry(history.Entry{From: br.ID(), Chat: m.JID, Text: res.Text, Type: history.Out})
	}
	b.endDelegation(m.ID, res.Text, res.Err)
}

// endDelegation ends task id with its answer or why there is none, once.
func (b *App) endDelegation(id, text string, err error) {
	d, late := b.delegations.end(id)
	if d == nil {
		return
	}
	outcome := "ok"
	switch {
	case err != nil:
		outcome = "failed: " + delegationFailure(err)
	case text == "":
		outcome = "ok, no answer"
	}
	if late {
		outcome += ", after the wait"
	}
	b.auditDelegation(d, outcome)
	r := delegationResult{text: text, err: err}
	if !late {
		d.result <- r
		return
	}
	b.deliverLate(d, r)
}

// deliverLate hands the core brain the answer its run stopped waiting for,
// as a new message in the chat that run was for, which its reply goes to.
func (b *App) deliverLate(d *delegation, r delegationResult) {
	core := b.brainMgr.Get(d.from.Brain)
	if core == nil || d.from.Chat == "" || chat.IsInternalChat(d.from.Chat) {
		log.Printf("brain %s: the answer of %s to its delegated task %s came after the wait, and has nowhere to go", d.from.Brain, d.to, d.id)
		return
	}
	text := fmt.Sprintf("The answer of brain %s to the task you delegated to it (%q):\n\n%s", d.to, textx.Truncate(d.task, 200), r.text)
	if r.err != nil {
		text = fmt.Sprintf("Brain %s could not do the task you delegated to it (%q): %s.", d.to, textx.Truncate(d.task, 200), delegationFailure(r.err))
	} else if r.text == "" {
		text = fmt.Sprintf("Brain %s finished the task you delegated to it (%q) without an answer.", d.to, textx.Truncate(d.task, 200))
	}
	m := brainruntime.Inbound{Inbound: chat.Inbound{JID: d.from.Chat, Brain: core.ID(), Sender: d.to, Text: text, Source: chat.SourceDelegateResult, At: time.Now()}}
	if _, _, _, err := b.brainMgr.Submit(m); err != nil {
		log.Printf("brain %s: the late answer of %s (%s) was not taken: %v", d.from.Brain, d.to, d.id, err)
	}
}

// auditDelegation writes the one audit line of a delegated task.
func (b *App) auditDelegation(d *delegation, outcome string) {
	e := audit.Entry{
		Kind: audit.Delegation, Thread: d.from.Chat, Chat: d.from.Chat, Brain: d.from.Brain, Run: d.from.ID, ID: d.id,
		Detail: "to " + d.to + ": " + textx.Truncate(d.task, 200), Outcome: outcome,
		DurationMS: time.Since(d.started).Milliseconds(),
	}
	if p := d.from.Policy; p != nil {
		e.Thread, e.Skill, e.Risk = p.Thread, p.Skill, string(p.Risk)
	}
	if d.approval.ID != "" {
		e.Detail += " (approval " + d.approval.ID + ")"
	}
	b.audit.Append(e)
}

// deliverRun delivers a finished run: to its chat, through the pipeline,
// for a delegated task to the core run that waits for it, and for a
// scheduled agent where its report setting says. A delegated run's reply
// never reaches a WhatsApp chat. A message with Answer hears the answer
// first, and one with AnswerOnly nothing more is done with.
func (b *App) deliverRun(br *brainruntime.Brain, m brainruntime.Inbound, res claudecli.Result) {
	if m.Answer != nil && res.Err == nil && res.Text != "" && !m.Silent {
		res.Text = m.Answer(res.Text)
	}
	if m.AnswerOnly {
		return
	}
	switch m.Source {
	case chat.SourceDelegate:
		b.delegationFinished(br, m, res)
		return
	case chat.SourceScheduled:
		b.agents.finished(br, m, res)
		return
	}
	b.pipeline.Finished(br, m, res)
}

// workingNotice tells a chat that a run is at work on its message; a
// delegated task's chat, or a scheduled agent's, is no chat anybody reads.
func (b *App) workingNotice(jid, text string) {
	if chat.IsInternalChat(jid) {
		return
	}
	b.pipeline.WorkingNotice(jid, text)
}

// askQuestion sends a question a run asks mid-way to its chat. A delegated
// or scheduled run has nobody to ask: its question is not sent, and it
// goes on to its final reply.
func (b *App) askQuestion(br *brainruntime.Brain, jid, text string) bool {
	if chat.IsInternalChat(jid) {
		return false
	}
	return b.pipeline.Question(br, jid, text)
}
