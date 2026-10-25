package app

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
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
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/mcp"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/skill"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/textx"
)

// A run asks the owner with request_approval (an action, yes or no) or
// ask_user (a question, with options or free text). Each is a row in
// bridge.db (store.Approval), so that one still pending survives a
// restart, and a native WhatsApp poll in the owner's chat whose ref
// names it. Only votes from the owner's numbers (allowed_numbers)
// count; the dashboard counts as the owner too. No vote within
// policy.approval_timeout_hours counts as no.
//
// A call waits for the answer up to wait_seconds (default
// defaultApprovalWait, at most maxApprovalWait): a run cannot wait a
// day, its own run_timeout ends it long before, and Codex gives a tool
// call little more than the longest delegate wait. Unanswered by then,
// the call answers "pending" with the id, which check_approval waits on
// again; an answer that comes once no call waits goes to the brain as a
// new message (SourceApprovalResult) in the chat it was asked in.
//
// An answer reaches the brain once: in the call that waits for it, or,
// when none does, as that new message, which starts a run of its own.
// decide holds approvalWaiters.settle from its update of bridge.db until
// it has handed the answer on, and a call reads the approval under it
// too. So a call never finds an answer in bridge.db that decide has yet
// to hand on: it would take it, and decide, finding no waiter, would
// send it as a message as well. A call that finds the answer went as a
// message (its wait ended just as the vote came, or check_approval came
// after it) answers pending and says so: two runs told to take the step
// would take it twice.

// The kinds of approval rows.
const (
	approvalKind = "approval"
	questionKind = "question"
)

// approvalRef is a poll's ref for approval id; votes in it go there.
const approvalRefPrefix = "approval:"

var (
	defaultApprovalWait = 5 * time.Minute
	maxApprovalWait     = 10 * time.Minute
)

// approvalTimeout is how long an approval waits for its answer:
// policy.approval_timeout_minutes, unless a test's waits shorten it.
func (b *App) approvalTimeout() time.Duration {
	return cmp.Or(b.waits.approvalTimeout, b.cfgs.Get().Live.Policy.ApprovalTimeout())
}

// approvalWaiters are the tool calls waiting for an answer, by approval
// id, and the free-text questions a call waits on, by chat.
type approvalWaiters struct {
	// settle is held by decide from its update of bridge.db until the
	// answer is handed on, and by a call while it reads the approval.
	settle sync.Mutex
	mu     sync.Mutex
	wait   map[string]chan store.Approval
	text   map[string]string // chat → id of the question asked there without options
	// late are the approvals decided while no call waited, whose answer
	// went to the brain as a message, with when; kept lateMemory.
	late map[string]time.Time
}

// lateMemory is how long the bridge remembers that an answer went to the
// brain as a message, longer than any run that could ask about it again.
const lateMemory = 24 * time.Hour

// add registers a waiter for id, or returns the one registered already
// (ask registers it before the poll goes out).
func (w *approvalWaiters) add(id string) chan store.Approval {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.wait == nil {
		w.wait = map[string]chan store.Approval{}
	}
	if ch := w.wait[id]; ch != nil {
		return ch
	}
	ch := make(chan store.Approval, 1)
	w.wait[id] = ch
	return ch
}

// take removes id's waiter, if a call waits, and returns its channel.
func (w *approvalWaiters) take(id string) chan store.Approval {
	w.mu.Lock()
	defer w.mu.Unlock()
	ch := w.wait[id]
	delete(w.wait, id)
	return ch
}

// hand removes id's waiter and returns its channel, which the answer
// goes into. With no call waiting, it returns nil and, with goesOn,
// marks the answer as one that goes to the brain as a message: in one
// step, so that no call can start or stop waiting in between.
func (w *approvalWaiters) hand(id string, goesOn bool) chan store.Approval {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ch := w.wait[id]; ch != nil {
		delete(w.wait, id)
		return ch
	}
	if goesOn {
		now := time.Now()
		if w.late == nil {
			w.late = map[string]time.Time{}
		}
		for k, at := range w.late {
			if now.Sub(at) > lateMemory {
				delete(w.late, k)
			}
		}
		w.late[id] = now
	}
	return nil
}

// wentLate reports whether id's answer went to the brain as a message.
func (w *approvalWaiters) wentLate(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.late[id]
	return ok
}

// forget drops id's free-text question, once it is decided.
func (w *approvalWaiters) forget(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for c, q := range w.text {
		if q == id {
			delete(w.text, c)
		}
	}
}

// textQuestion registers chat's question id as waiting for a typed answer.
func (w *approvalWaiters) textQuestion(chatJID, id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.text == nil {
		w.text = map[string]string{}
	}
	w.text[chatJID] = id
}

// textFor is the pending free-text question chat's next message from
// the owner answers.
func (w *approvalWaiters) textFor(chatJID string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.text[chatJID]
}

// approvalChat is where run asks: the chat its thread is in, or the
// owner's DM for a run in no WhatsApp chat (a delegated task's whose
// core run was not in one, a site build).
func (b *App) approvalChat(p *runPolicy) string {
	if p != nil && p.Thread != "" && !chat.IsInternalChat(p.Thread) && strings.Contains(p.Thread, "@") {
		return p.Thread
	}
	return ownerChat(b.cfgs.Get())
}

type approvalArgs struct {
	Action      string   `json:"action"`
	Details     string   `json:"details"`
	Risk        string   `json:"risk"`
	Step        string   `json:"step"`
	Question    string   `json:"question"`
	Options     []string `json:"options"`
	ID          string   `json:"id"`
	WaitSeconds int      `json:"wait_seconds"`
}

func (a approvalArgs) wait() time.Duration {
	if a.WaitSeconds > 0 {
		return min(time.Duration(a.WaitSeconds)*time.Second, maxApprovalWait)
	}
	return defaultApprovalWait
}

func (b *App) describeRequestApproval(c mcpCaller) mcp.Tool {
	note := ""
	if c.autoApproves() {
		note = "This brain acts without asking (approvals: auto): this answers approved at once, without a poll, and records the step; say in your reply what you did. "
	}
	if p := c.run.Policy; p != nil && p.Approval != "" {
		note += fmt.Sprintf("The owner approved %q already, before this task was delegated to you (approval_id %s): do not ask for that again. ", textx.Truncate(p.ApprovedAction, 300), p.Approval)
	}
	return mcp.Tool{
		Description: note + "Ask the person you work for to approve an action before you take it, with a poll in their WhatsApp chat (Yes / No / Show me the draft). " +
			"Call it before every irreversible step (submitting, sending to anyone else, paying, deleting outside your folder, publishing) and every step a skill lists under metadata.fridolin.approve; take the step only when this answers approved. " +
			fmt.Sprintf("Waits up to wait_seconds (default %d, at most %d). Unanswered by then it answers pending with an approval_id: wait again with check_approval, or end your turn, and the answer comes to you as a new message. ", int(defaultApprovalWait/time.Second), int(maxApprovalWait/time.Second)) +
			fmt.Sprintf("No answer within %d hours counts as no. This run's risk level is %s.", b.cfgs.Get().Live.Policy.ApprovalTimeoutHours, c.risk()),
		InputSchema: schema(map[string]any{
			"action":       strProp("What you want to do, in one sentence the person can say yes or no to, e.g. \"Submit the 2025 tax return through ELSTER\"."),
			"details":      strProp("The draft or the particulars: what exactly will be sent, submitted, paid or deleted. Shown when they ask to see the draft."),
			"risk":         map[string]any{"type": "string", "enum": []string{"write", "irreversible"}, "description": "irreversible (default) or write."},
			"step":         strProp("The skill's step this approves, as named under metadata.fridolin.approve, if any."),
			"wait_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": int(maxApprovalWait / time.Second)},
		}, "action"),
	}
}

func (b *App) describeAskUser(mcpCaller) mcp.Tool {
	return mcp.Tool{
		Description: "Ask the person you work for a question in their WhatsApp chat and wait for the answer: with options, as a poll they tap; without, as a message whose reply is the answer. " +
			fmt.Sprintf("Waits up to wait_seconds (default %d, at most %d); unanswered by then it answers pending with an id for check_approval, and a later answer comes to you as a new message. ", int(defaultApprovalWait/time.Second), int(maxApprovalWait/time.Second)) +
			"Ask only what you cannot find out yourself.",
		InputSchema: schema(map[string]any{
			"question":     strProp("The question."),
			"options":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 2, "maxItems": outboundmsg.MaxPollOptions, "description": "The answers to choose from, for a poll; leave out for a free answer."},
			"wait_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": int(maxApprovalWait / time.Second)},
		}, "question"),
	}
}

func describeCheckApproval(mcpCaller) mcp.Tool {
	return mcp.Tool{
		Description: "Wait again for the answer to a request_approval or ask_user of yours that answered pending, by its id.",
		InputSchema: schema(map[string]any{
			"id":           strProp("The approval_id it answered."),
			"wait_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": int(maxApprovalWait / time.Second)},
		}, "id"),
	}
}

// canAsk reports whether the run may ask the owner anything: not an
// observe-only run, which nobody addressed.
func (c mcpCaller) canAsk() bool { return c.run.Policy == nil || !c.run.Policy.Silent }

// risk is the run's budget.
func (c mcpCaller) risk() skill.Risk {
	if c.run.Policy == nil || !c.run.Policy.Risk.Valid() {
		return defaultRisk
	}
	return c.run.Policy.Risk
}

func (b *App) callRequestApproval(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var args approvalArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcp.ErrorResult(`the arguments are {"action": "…", "details": "…", "risk": "irreversible", "step": "…"}: ` + err.Error()), nil
	}
	args.Action = strings.TrimSpace(args.Action)
	if args.Action == "" {
		return mcp.ErrorResult("action is empty: say what you want to do"), nil
	}
	risk := skill.RiskIrreversible
	if args.Risk != "" {
		r, err := skill.ParseRisk(args.Risk)
		if err != nil || r == skill.RiskRead {
			return mcp.ErrorResult("risk is write or irreversible"), nil
		}
		risk = r
	}
	if b.locked() {
		return mcp.ErrorResult("the bridge is in lockdown: nothing that writes, sends or submits goes ahead, and no approval is asked. Do not take the action; say so in your reply."), nil
	}
	if risk == skill.RiskIrreversible {
		n, err := b.store.CountPending(ctx, approvalKind, string(skill.RiskIrreversible))
		if err != nil {
			return nil, err
		}
		if max := b.cfgs.Get().Live.Policy.MaxPendingIrreversible; n >= max {
			return mcp.ErrorResult(fmt.Sprintf("%d irreversible actions wait for approval already, the most policy.max_pending_irreversible allows: wait for those to be answered. Do not take the action.", n)), nil
		}
	}
	req := store.Approval{Kind: approvalKind, Action: args.Action, Details: strings.TrimSpace(args.Details), Risk: string(risk), Step: strings.TrimSpace(args.Step)}
	if c.autoApproves() {
		a, err := b.autoApprove(ctx, c, req)
		if err != nil {
			return mcp.ErrorResult(err.Error()), nil
		}
		return mcp.TextResult(fmt.Sprintf("approved: this brain acts without asking (approvals: auto), so the owner was not asked; the step is recorded (approval_id %s). Take exactly this action, nothing more; pass approval_id %s to a tool that asks for one, and say in your reply what you did.", a.ID, a.ID)), nil
	}
	a, err := b.ask(ctx, c, req)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	return b.awaitAnswer(ctx, a, args.wait()), nil
}

// autoApprovedBy is who decided an approval the brain's approvals: auto
// answered (store.Approval.DecidedBy).
const autoApprovedBy = "auto"

// autoApproves reports whether the run's request_approval answers
// approved without a poll: its brain's approvals setting is auto, and
// its risk budget allows more than reading. A run held to read, by its
// skill or a lockdown, asks the owner as any brain's would: auto never
// lets through what the budget does not.
func (c mcpCaller) autoApproves() bool {
	return c.manifest.AutoApproves() && c.risk().Rank() > skill.RiskRead.Rank()
}

// autoApprove records a as approved by "auto", without a poll, for a
// brain whose owner lets it act without asking: it shows in the
// approvals' history and the audit log as any other.
func (b *App) autoApprove(ctx context.Context, c mcpCaller, a store.Approval) (store.Approval, error) {
	now := time.Now()
	b.fillApproval(c, &a, now)
	if err := b.store.AddApproval(ctx, a); err != nil {
		return a, fmt.Errorf("the bridge could not record the request: %w", err)
	}
	decided, err := b.store.DecideApproval(ctx, a.ID, store.ApprovalApproved, "", autoApprovedBy, now)
	if err != nil {
		return a, fmt.Errorf("the bridge could not record the request: %w", err)
	}
	detail := a.Action
	if a.Step != "" {
		detail += " (step " + a.Step + ")"
		// A skill's approve step goes ahead without the owner's yes: say so.
		log.Printf("brain %s: step %q of skill %q approved by approvals: auto, without asking the owner (%s)", a.Brain, a.Step, a.Skill, a.ID)
	}
	b.audit.Append(audit.Entry{Kind: audit.ApprovalRequested, Thread: a.Thread, Chat: a.Chat, Brain: a.Brain, Run: a.Run, ID: a.ID, Skill: a.Skill, Risk: a.Risk, Detail: detail, Outcome: "pending"})
	b.audit.Append(audit.Entry{Kind: audit.ApprovalDecided, Thread: a.Thread, Chat: a.Chat, Brain: a.Brain, Run: a.Run, ID: a.ID, Risk: a.Risk, By: autoApprovedBy, Outcome: store.ApprovalApproved})
	log.Printf("brain %s: approved without asking the owner (approvals: auto, %s)", a.Brain, a.ID)
	return decided, nil
}

func (b *App) callAskUser(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var args approvalArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcp.ErrorResult(`the arguments are {"question": "…", "options": ["…", "…"]}: ` + err.Error()), nil
	}
	args.Question = strings.TrimSpace(args.Question)
	if args.Question == "" {
		return mcp.ErrorResult("question is empty"), nil
	}
	opts := trimAll(args.Options)
	switch {
	case len(args.Options) > 0 && len(opts) < 2:
		return mcp.ErrorResult("give at least two different options, or none for a free answer"), nil
	case len(opts) > outboundmsg.MaxPollOptions:
		return mcp.ErrorResult(fmt.Sprintf("a poll has at most %d options", outboundmsg.MaxPollOptions)), nil
	}
	for _, o := range opts {
		if len(o) > 100 {
			return mcp.ErrorResult("an option is longer than 100 characters: keep them short"), nil
		}
	}
	a, err := b.ask(ctx, c, store.Approval{Kind: questionKind, Action: args.Question, Options: opts})
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	return b.awaitAnswer(ctx, a, args.wait()), nil
}

func (b *App) callCheckApproval(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var args approvalArgs
	if err := json.Unmarshal(raw, &args); err != nil || args.ID == "" {
		return mcp.ErrorResult(`the arguments are {"id": "<approval_id>"}`), nil
	}
	a, err := b.store.ApprovalByID(ctx, args.ID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && a.Brain != c.run.Brain) {
		return mcp.ErrorResult(fmt.Sprintf("there is no approval %q of yours", args.ID)), nil
	}
	if err != nil {
		return nil, err
	}
	return b.awaitAnswer(ctx, a, args.wait()), nil
}

// fillApproval gives a, a request of c's run, its id, where it belongs
// and when it ends.
func (b *App) fillApproval(c mcpCaller, a *store.Approval, now time.Time) {
	p := c.run.Policy
	if p == nil {
		p = &runPolicy{ID: c.run.ID, Brain: c.run.Brain, Chat: c.run.Chat, Thread: c.run.Chat, Risk: defaultRisk}
	}
	a.ID = "a" + randomToken(9)
	a.Brain, a.Run, a.Thread, a.Chat, a.Skill = c.run.Brain, p.ID, p.Thread, b.approvalChat(p), p.Skill
	a.Notify = c.run.Brain
	if p.FromBrain != "" {
		a.Notify = p.FromBrain // a delegated run is over by then: its core brain hears
	}
	a.CreatedAt, a.ExpiresAt = now, now.Add(b.approvalTimeout())
}

// ask stores a and puts its poll (or question) into the owner's chat.
func (b *App) ask(ctx context.Context, c mcpCaller, a store.Approval) (store.Approval, error) {
	p := c.run.Policy
	if p == nil {
		p = &runPolicy{ID: c.run.ID, Brain: c.run.Brain, Chat: c.run.Chat, Thread: c.run.Chat, Risk: defaultRisk}
	}
	target := b.approvalChat(p)
	if target == "" {
		return a, errors.New("there is nobody to ask: allowed_numbers holds no number of the owner. Do not take the action; say so in your reply")
	}
	now := time.Now()
	b.fillApproval(c, &a, now)
	if err := b.store.AddApproval(ctx, a); err != nil {
		return a, fmt.Errorf("the bridge could not record the request: %w", err)
	}
	// The call waits from before the poll goes out: a vote that comes
	// before awaitAnswer would otherwise find no waiter and go to the
	// brain as a late answer as well, a second run.
	b.waiters.add(a.ID)
	max := b.cfgs.Get().MaxChunkSize
	var err error
	switch {
	case a.Kind == approvalKind:
		err = b.outbox.Poll(ctx, target, outboundmsg.ApprovalQuestion(a.Action), []string{outboundmsg.ApprovalYes, outboundmsg.ApprovalNo, outboundmsg.ApprovalDraft}, approvalRefPrefix+a.ID)
	case len(a.Options) > 0:
		err = b.outbox.Poll(ctx, target, outboundmsg.AskQuestion(a.Action), a.Options, approvalRefPrefix+a.ID)
	default:
		err = b.outbox.Question(ctx, a.Notify, target, outboundmsg.AskQuestion(a.Action), max)
	}
	if err != nil {
		b.waiters.take(a.ID)
		_, _ = b.store.DecideApproval(ctx, a.ID, store.ApprovalDenied, "", "bridge", now)
		return a, fmt.Errorf("the question could not be sent: %w", err)
	}
	if a.Kind == questionKind && len(a.Options) == 0 {
		b.waiters.textQuestion(a.Chat, a.ID)
	}
	kind := audit.ApprovalRequested
	if a.Kind == questionKind {
		kind = audit.QuestionAsked
	}
	detail := a.Action
	if a.Step != "" {
		detail += " (step " + a.Step + ")"
	}
	b.audit.Append(audit.Entry{Kind: kind, Thread: a.Thread, Chat: a.Chat, Brain: a.Brain, Run: a.Run, ID: a.ID, Skill: a.Skill, Risk: a.Risk, Detail: detail, Outcome: "pending"})
	log.Printf("brain %s: asked the owner in %s (%s %s)", a.Brain, a.Chat, a.Kind, a.ID)
	return a, nil
}

// awaitAnswer waits up to wait for a's answer, and says what it is.
func (b *App) awaitAnswer(ctx context.Context, a store.Approval, wait time.Duration) *mcp.CallResult {
	ch := b.waiters.add(a.ID)
	// Decided already: by an answer that found this waiter, and is in the
	// channel, or by one that found none, and went on to the brain.
	if cur, late, err := b.settledApproval(ctx, a.ID); err == nil && cur.Status != store.ApprovalPending {
		if b.waiters.take(a.ID) == nil {
			return answerResult(<-ch)
		}
		return decidedResult(cur, late)
	}
	deadline := wait
	if left := time.Until(a.ExpiresAt); left < deadline {
		deadline = max(left, 0)
	}
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	select {
	case got := <-ch:
		return answerResult(got)
	case <-timer.C:
	case <-ctx.Done():
	}
	if b.waiters.take(a.ID) == nil {
		// Taken by an answer as the wait ended: it is in the channel.
		return answerResult(<-ch)
	}
	if b.hooks.approvalWaitEnded != nil {
		b.hooks.approvalWaitEnded(a.ID)
	}
	// No call waits any more: an answer from here on goes on to the brain
	// as a message, which decidedResult does not hand over a second time.
	if !time.Now().Before(a.ExpiresAt) {
		if got, ok := b.decide(context.WithoutCancel(ctx), a.ID, store.ApprovalExpired, "", "timeout", callerAnswers); ok {
			return answerResult(got)
		}
	}
	cur, late, err := b.settledApproval(context.WithoutCancel(ctx), a.ID)
	if err == nil && cur.Status != store.ApprovalPending {
		return decidedResult(cur, late)
	}
	return mcp.TextResult(fmt.Sprintf("pending: no answer yet after %s (approval_id %s). Call check_approval with this id to wait again, or end your turn: the answer comes to you as a new message in this chat. Until it is approved, do not take the action.", wait.Round(time.Second), a.ID))
}

// settledApproval reads approval id once no decide is under way, and
// whether its answer went on to the brain as a message.
func (b *App) settledApproval(ctx context.Context, id string) (store.Approval, bool, error) {
	b.waiters.settle.Lock()
	defer b.waiters.settle.Unlock()
	a, err := b.store.ApprovalByID(ctx, id)
	return a, b.waiters.wentLate(id), err
}

// decidedResult is the tool result for decided a, which no waiting call
// took: pending, for an answer that went on to the brain as a message
// (late), where the brain acts on it.
func decidedResult(a store.Approval, late bool) *mcp.CallResult {
	if !late {
		return answerResult(a)
	}
	return mcp.TextResult(fmt.Sprintf("pending: the answer came once you had stopped waiting, and comes to you as a new message in this chat (approval_id %s). Do not act on it in this run: end your turn, and act on that message.", a.ID))
}

// answerResult is the tool result for a decided approval or question.
func answerResult(a store.Approval) *mcp.CallResult {
	switch a.Status {
	case store.ApprovalApproved:
		return mcp.TextResult(fmt.Sprintf("approved: the owner said yes (approval_id %s). Take exactly this action, nothing more; pass approval_id %s to a tool that asks for one.", a.ID, a.ID))
	case store.ApprovalDenied:
		return mcp.TextResult(fmt.Sprintf("denied: the owner said no (approval_id %s). Do not take the action; say in your reply that you did not.", a.ID))
	case store.ApprovalExpired:
		if a.Kind == questionKind {
			return mcp.TextResult(fmt.Sprintf("expired: no answer in time (id %s). Go on without it, or ask again later.", a.ID))
		}
		return mcp.TextResult(fmt.Sprintf("denied: no answer in time, which counts as no (approval_id %s). Do not take the action.", a.ID))
	case store.ApprovalAnswered:
		return mcp.TextResult("answered: " + a.Answer)
	}
	return mcp.TextResult("pending (approval_id " + a.ID + ")")
}

// lateAnswer is what decide does with an answer no call waits for.
type lateAnswer int

const (
	// callerAnswers: the caller, a call whose wait ended, answers with it.
	callerAnswers lateAnswer = iota
	// answerAsMessage: it goes to the brain as a new message (deliverAnswer).
	answerAsMessage
	// messageGoesOn: the owner's message that answered goes on to the
	// brain itself (answerText).
	messageGoesOn
)

// decide ends pending approval id with status, once: it hands the answer
// to the call that waits for it, or, as late says, on to the brain. It
// reports false for an approval no longer pending.
func (b *App) decide(ctx context.Context, id, status, answer, by string, late lateAnswer) (store.Approval, bool) {
	b.waiters.settle.Lock()
	a, err := b.store.DecideApproval(ctx, id, status, answer, by, time.Now())
	if errors.Is(err, store.ErrNotPending) {
		b.waiters.settle.Unlock()
		return a, false
	}
	if err != nil {
		b.waiters.settle.Unlock()
		log.Printf("approval %s: %v", id, err)
		return a, false
	}
	if b.hooks.approvalDecided != nil {
		b.hooks.approvalDecided(id)
	}
	ch := b.waiters.hand(id, late != callerAnswers)
	b.waiters.settle.Unlock()
	kind := audit.ApprovalDecided
	if a.Kind == questionKind {
		kind = audit.QuestionAnswered
	}
	detail := ""
	if a.Kind == questionKind && answer != "" {
		detail = "answer: " + answer
	}
	b.audit.Append(audit.Entry{Kind: kind, Thread: a.Thread, Chat: a.Chat, Brain: a.Brain, Run: a.Run, ID: a.ID, Risk: a.Risk, By: by, Detail: detail, Outcome: status})
	b.waiters.forget(id)
	if ch != nil {
		ch <- a
		return a, true
	}
	if late == answerAsMessage {
		b.deliverAnswer(a)
	}
	return a, true
}

// deliverAnswer hands the brain an answer no call waited for, as a new
// message in the chat it was asked in.
func (b *App) deliverAnswer(a store.Approval) {
	if a.Notify == housekeepingNotify {
		b.housekeepingDecided(a)
		return
	}
	if a.Notify == workNotify {
		b.work.decided(a)
		return
	}
	// A move's answers ask the nodes, which takes a while: not on the
	// goroutine that brought the vote.
	if a.Notify == coreMoveNotify {
		go b.coreMoveDecided(a)
		return
	}
	if a.Notify == coreSecretsNotify {
		go b.coreSecretsDecided(a)
		return
	}
	br := b.brainMgr.Get(a.Notify)
	if br == nil || a.Chat == "" {
		log.Printf("approval %s: the answer came once its run was over, and brain %s is not there to hear it", a.ID, a.Notify)
		return
	}
	var text string
	action := textx.Truncate(a.Action, 300)
	switch {
	case a.Kind == questionKind && a.Status == store.ApprovalAnswered:
		text = fmt.Sprintf("The answer to your question %q: %s", action, a.Answer)
	case a.Kind == questionKind:
		text = fmt.Sprintf("Your question %q got no answer in time.", action)
	case a.Status == store.ApprovalApproved:
		text = fmt.Sprintf("The owner approved %q (approval_id %s). Take exactly that action now if it still applies, passing approval_id %s to a tool that asks for one, and report what you did.", action, a.ID, a.ID)
	case a.Status == store.ApprovalDenied:
		text = fmt.Sprintf("The owner denied %q (approval_id %s). Do not take that action.", action, a.ID)
	default:
		text = fmt.Sprintf("Nobody answered the approval of %q in time, which counts as no (approval_id %s). Do not take that action.", action, a.ID)
	}
	m := brainruntime.Inbound{Inbound: chat.Inbound{JID: a.Chat, Brain: br.ID(), Sender: "fridolin", Text: text, Source: chat.SourceApprovalResult, At: time.Now()}}
	if _, _, _, err := b.brainMgr.Submit(m); err != nil {
		log.Printf("approval %s: brain %s did not take the answer: %v", a.ID, br.ID(), err)
	}
}

// approvalVote takes a vote in an approval's poll: from the owner, it
// decides it, or for "Show me the draft" sends the draft; from anybody
// else it counts for nothing.
func (b *App) approvalVote(id string, v pollVote) {
	ctx := context.Background()
	a, err := b.store.ApprovalByID(ctx, id)
	if err != nil {
		log.Printf("a vote for approval %s, which the bridge does not know: %v", id, err)
		return
	}
	voter := v.Voter.User
	entry := audit.Entry{Kind: audit.ApprovalVote, Thread: a.Thread, Chat: a.Chat, Brain: a.Brain, Run: a.Run, ID: a.ID, Risk: a.Risk, By: voter, Detail: strings.Join(v.Selected, ", ")}
	switch {
	case !b.isOwner(voter):
		entry.Outcome = "ignored: not an allowed number"
		b.audit.Append(entry)
		log.Printf("approval %s: a vote from a number that is not the owner's was ignored", a.ID)
		return
	case len(v.Selected) == 0:
		entry.Outcome = "ignored: vote taken back"
		b.audit.Append(entry)
		return
	case a.Status != store.ApprovalPending:
		entry.Outcome = "ignored: " + a.Status + " already"
		b.audit.Append(entry)
		return
	}
	choice := v.Selected[0]
	if a.Kind == questionKind {
		entry.Outcome = "answered"
		b.audit.Append(entry)
		b.decide(ctx, a.ID, store.ApprovalAnswered, choice, voter, answerAsMessage)
		return
	}
	switch {
	case slices.Contains(v.Selected, outboundmsg.ApprovalDraft):
		entry.Outcome = "draft shown"
		b.audit.Append(entry)
		if err := b.outbox.Text(ctx, a.Chat, outboundmsg.ApprovalDraftReply(a.Action, a.Details), b.cfgs.Get().MaxChunkSize); err != nil {
			log.Printf("approval %s: sending the draft: %v", a.ID, err)
		}
	case choice == outboundmsg.ApprovalYes:
		entry.Outcome = store.ApprovalApproved
		b.audit.Append(entry)
		b.decide(ctx, a.ID, store.ApprovalApproved, choice, voter, answerAsMessage)
	case choice == outboundmsg.ApprovalNo:
		entry.Outcome = store.ApprovalDenied
		b.audit.Append(entry)
		b.decide(ctx, a.ID, store.ApprovalDenied, choice, voter, answerAsMessage)
	default:
		entry.Outcome = "ignored: not an option"
		b.audit.Append(entry)
	}
}

// answerText takes m, from the owner, as the answer to the free-text
// question pending in its chat. It reports whether a call waited for
// it, and so has it: m then goes no further. Without one, m goes on to
// the brain as any message, which answers the question in its history.
func (b *App) answerText(m brainruntime.Inbound) bool {
	if m.Silent || m.HandedIn() || strings.TrimSpace(m.Text) == "" {
		return false
	}
	id := b.waiters.textFor(m.JID)
	if id == "" || !b.isOwner(m.Sender) {
		return false
	}
	n, _ := chat.PhoneNumber(m.Sender)
	a, ok := b.decide(context.Background(), id, store.ApprovalAnswered, m.Text, n, messageGoesOn)
	if !ok || b.waiters.wentLate(id) {
		return false // no call waited: m goes on to the brain
	}
	if br := b.brainMgr.Get(a.Notify); br != nil {
		br.AppendHistory(m.JID, m.Sender, m.Text, history.In)
	}
	return true
}

// sweepApprovals ends the pending approvals whose time is up: no vote
// counts as no.
func (b *App) sweepApprovals(ctx context.Context) {
	expired, err := b.store.ExpiredApprovals(ctx, time.Now())
	if err != nil {
		log.Printf("approvals: %v", err)
		return
	}
	for _, a := range expired {
		b.decide(ctx, a.ID, store.ApprovalExpired, "", "timeout", answerAsMessage)
	}
}

// watchApprovals sweeps every minute until ctx ends, and tells the owner
// of what newly waits for their approval (alertPendingApprovals).
func (b *App) watchApprovals(ctx context.Context) {
	b.sweepApprovals(ctx)
	b.alertPendingApprovals(ctx)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.sweepApprovals(ctx)
			b.alertPendingApprovals(ctx)
		}
	}
}

// approvalsView answers GET /api/approvals.
func (b *App) approvalsView(ctx context.Context) (any, error) {
	b.sweepApprovals(ctx)
	pending, decided, err := b.store.Approvals(ctx, 100)
	if err != nil {
		return nil, err
	}
	return map[string]any{"pending": pending, "history": decided}, nil
}

// dashboardDecide answers POST /api/approvals/{id}: the owner decides in
// the dashboard.
func (b *App) dashboardDecide(ctx context.Context, id string, approve bool) (any, error) {
	a, err := b.store.ApprovalByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.New(apperr.NotFound, "no approval "+id)
	}
	if err != nil {
		return nil, err
	}
	if a.Kind != approvalKind {
		return nil, apperr.New(apperr.Invalid, "that is a question: answer it in the chat")
	}
	status := store.ApprovalDenied
	if approve {
		status = store.ApprovalApproved
	}
	b.audit.Append(audit.Entry{Kind: audit.ApprovalVote, Thread: a.Thread, Chat: a.Chat, Brain: a.Brain, Run: a.Run, ID: a.ID, Risk: a.Risk, By: "dashboard", Outcome: status})
	got, ok := b.decide(ctx, id, status, "", "dashboard", answerAsMessage)
	if !ok {
		return nil, apperr.New(apperr.Conflict, "that approval is "+got.Status+" already")
	}
	return got, nil
}

// useApproval takes approval id for one call of an irreversible tool by
// the run of c: approved, of the same brain or handed to the run with its
// delegated task (runPolicy.Approval), not taken before.
func (b *App) useApproval(ctx context.Context, c mcpCaller, id string) error {
	a, err := b.store.ApprovalByID(ctx, id)
	if err != nil || (a.Brain != c.run.Brain && !c.handed(id)) {
		return fmt.Errorf("there is no approval %q of yours", id)
	}
	if a.Status != store.ApprovalApproved {
		return fmt.Errorf("approval %s is %s, not approved", id, a.Status)
	}
	ok, err := b.store.UseApproval(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("approval %s was used already: ask again for another action", id)
	}
	return nil
}

// handed reports whether approval id came to the run with its delegated
// task: the owner approved the step in the core run that delegated it.
func (c mcpCaller) handed(id string) bool {
	return id != "" && c.run.Policy != nil && c.run.Policy.Approval == id
}
