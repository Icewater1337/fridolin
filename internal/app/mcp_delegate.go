package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/audit"
	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/textx"
)

// How long a delegate call waits for the sub-brain's answer, unless it
// says otherwise, and at most. An answer that comes later goes to the
// core brain's chat as a new message (deliverLate).
var (
	defaultDelegateWait = 2 * time.Minute
	maxDelegateWait     = 10 * time.Minute
)

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
func (b *App) delegate(ctx context.Context, c mcpCaller, to, task string, wait time.Duration) *mcp.CallResult {
	d := &delegation{id: "d" + randomToken(9), from: c.run, to: to, task: task, started: time.Now(), result: make(chan delegationResult, 1)}
	b.delegations.add(d)
	m := brainruntime.Inbound{Inbound: chat.Inbound{
		JID: delegateChat(c.run.ID), Brain: to, Sender: c.run.Brain, Text: task,
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
	if core == nil || d.from.Chat == "" || chat.IsDelegateChat(d.from.Chat) {
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
	b.audit.Append(e)
}

// deliverRun delivers a finished run: to its chat, through the pipeline,
// or for a delegated task to the core run that waits for it. A delegated
// run's reply never reaches a WhatsApp chat.
func (b *App) deliverRun(br *brainruntime.Brain, m brainruntime.Inbound, res claudecli.Result) {
	if m.Source == chat.SourceDelegate {
		b.delegationFinished(br, m, res)
		return
	}
	b.pipeline.Finished(br, m, res)
}

// workingNotice tells a chat that a run is at work on its message; a
// delegated task's chat is no chat anybody reads.
func (b *App) workingNotice(jid, text string) {
	if chat.IsDelegateChat(jid) {
		return
	}
	b.pipeline.WorkingNotice(jid, text)
}

// askQuestion sends a question a run asks mid-way to its chat. A delegated
// run has nobody to ask: its question is not sent, and it goes on to its
// final reply, which the core brain gets.
func (b *App) askQuestion(br *brainruntime.Brain, jid, text string) bool {
	if chat.IsDelegateChat(jid) {
		return false
	}
	return b.pipeline.Question(br, jid, text)
}
