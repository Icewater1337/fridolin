package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/Icewater1337/fridolin/internal/audit"
	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/connections"
	"github.com/Icewater1337/fridolin/internal/errand"
	"github.com/Icewater1337/fridolin/internal/logx"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/otp"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/textx"
)

// Errands (docs/BRAINS.md, "Errands"): the core brain has Fridolin write
// to someone who is not in allowed_numbers, and Fridolin carries the
// conversation with them until it comes to an end.
//
// What keeps a person Fridolin writes to from turning it against the
// owner is the bridge, not a model's judgement:
//   - The first message to someone waits for the owner's approval, and so
//     does any errand a run opens that the owner did not ask for in their
//     own direct chat (errandByOwner).
//   - The person's answers never reach a brain. Each goes to the errand's
//     own run (errandAsk), which has no tools, reads only the errand's
//     goal, brief and conversation, and answers with one JSON object. The
//     bridge sends what it writes to that person only.
//   - What the run cannot settle with the brief goes to the owner, and a
//     run that finds the person trying to make it misbehave ends the
//     errand: the person gets the bridge's own words, and the bridge stops
//     listening to them until the owner says !unblock.

// errandNotify is the Notify of the approvals of an errand's first
// message: their answers come back to errandDecided, not to a brain.
const errandNotify = "fridolin:errand"

// errandRunTimeout bounds one answer of an errand's run.
const errandRunTimeout = 3 * time.Minute

// errandForwardsPerDay bounds what Fridolin passes on to the owner from
// someone it wrote to while no errand with them is open.
const errandForwardsPerDay = 5

// Conversations someone begins (docs/BRAINS.md, "Conversations"): at most
// errandChatsPerDay of them, and errandHeardPerDay of their messages, a
// day; past either, what they write goes to the owner as from someone
// whose messages are passed on. One stays open errandChatDays.
const (
	errandChatsPerDay = 3
	errandHeardPerDay = 40
	errandChatDays    = 1
)

// errandTurns runs each errand's turns one at a time: a message that
// comes while its run answers waits for the next turn.
type errandTurns struct {
	// begin keeps two messages of one person that come at once from
	// beginning two conversations.
	begin    sync.Mutex
	mu       sync.Mutex
	timers   map[string]*time.Timer
	running  map[string]bool
	again    map[string]bool
	forwards map[string][]time.Time
	// calls are the errands on a call now, whose turns the call takes.
	calls   map[string]bool
	stopped bool
}

// setCalling marks errand id as on a call, or no longer.
func (t *errandTurns) setCalling(id string, on bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.calls == nil {
		t.calls = map[string]bool{}
	}
	if on {
		t.calls[id] = true
	} else {
		delete(t.calls, id)
	}
}

// calling reports whether errand id is on a call.
func (t *errandTurns) calling(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls[id]
}

// forward counts a message of contact's passed on to the owner at now,
// unless errandForwardsPerDay went in the day before.
func (t *errandTurns) forward(contact string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.forwards == nil {
		t.forwards = map[string][]time.Time{}
	}
	cut := now.Add(-24 * time.Hour)
	recent := slices.DeleteFunc(t.forwards[contact], func(at time.Time) bool { return !at.After(cut) })
	if len(recent) >= errandForwardsPerDay {
		t.forwards[contact] = recent
		return false
	}
	t.forwards[contact] = append(recent, now)
	return true
}

// soon runs turn(id) after wait, unless one is due or running already:
// then that one, or the one after it, sees what came.
func (t *errandTurns) soon(id string, wait time.Duration, turn func(string)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return
	}
	if t.running[id] {
		t.again[id] = true
		return
	}
	if t.timers == nil {
		t.timers, t.running, t.again = map[string]*time.Timer{}, map[string]bool{}, map[string]bool{}
	}
	if old := t.timers[id]; old != nil {
		old.Stop()
	}
	t.timers[id] = time.AfterFunc(wait, func() { t.run(id, turn) })
}

func (t *errandTurns) run(id string, turn func(string)) {
	t.mu.Lock()
	delete(t.timers, id)
	if t.stopped || t.running[id] {
		t.mu.Unlock()
		return
	}
	t.running[id] = true
	t.mu.Unlock()
	for {
		turn(id)
		t.mu.Lock()
		if t.stopped || !t.again[id] {
			delete(t.running, id)
			delete(t.again, id)
			t.mu.Unlock()
			return
		}
		delete(t.again, id)
		t.mu.Unlock()
	}
}

// stop lets no turn start any more.
func (t *errandTurns) stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = true
	for _, timer := range t.timers {
		timer.Stop()
	}
}

// errandSettings is brain id's errands, when it is the core brain and
// brain.json allows them.
func (b *App) errandSettings(id string) (manifest.Errands, bool) {
	br := b.brainMgr.Get(id)
	if br == nil {
		return manifest.Errands{}, false
	}
	m := br.Manifest()
	if !m.IsCore() || m.Errands == nil {
		return manifest.Errands{}, false
	}
	return *m.Errands, true
}

// errandEngineReady says why errands cannot run now: in Codex mode they
// need the OpenAI connection's key, since Codex's CLI cannot run without
// tools.
func (b *App) errandEngineReady(cfg *config.Settings) error {
	if b.hooks.errandRun != nil || cfg.AgentProvider != config.ProviderCodex {
		return nil
	}
	if b.conns == nil || !b.conns.Configured(connections.OpenAI) {
		return errors.New("in Codex mode an errand's runs go through Fridolin's OpenAI connection, which has no key: the owner sets one under Integrations → OpenAI (docs/CONNECTIONS.md)")
	}
	return nil
}

// errandByOwner reports whether run c answers the owner's own message in
// their own direct chat: only then may an errand to someone Fridolin
// wrote to before go out without asking. The bridge tells, from the
// message that started the run; nothing the model says counts.
func errandByOwner(c runInfo, cfg *config.Settings) bool {
	owner := ownerChat(cfg)
	return owner != "" && ownerAsked(c, cfg) && chat.IdentityKey(c.Message.Sender) == chat.IdentityKey(owner)
}

// errandRequest is what contact_person hands the bridge.
type errandRequest struct {
	Number, Name, Goal, Brief, Message string
	Days                               int
	// Call reaches them by a voice call instead of a message.
	Call bool
}

// openErrand starts an errand for run c: at once when the owner asked
// for it in their own chat and Fridolin wrote to the person before, or
// else once the owner approves its first message. It answers what the
// brain is told.
func (b *App) openErrand(ctx context.Context, c mcpCaller, r errandRequest) (string, error) {
	cfg := b.cfgs.Get()
	s, ok := b.errandSettings(c.run.Brain)
	if !ok {
		return "", errors.New("errands are off for this brain: brain.json's errands, on the core brain, turns them on")
	}
	if err := b.errandEngineReady(cfg); err != nil {
		return "", err
	}
	number, err := chat.PhoneNumber(r.Number)
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(r.Name)
	goal, brief, message := strings.TrimSpace(r.Goal), strings.TrimSpace(r.Brief), strings.TrimSpace(r.Message)
	switch {
	case name == "" || len([]rune(name)) > errand.MaxName || strings.ContainsAny(name, "\r\n"):
		return "", fmt.Errorf("name is how Fridolin calls them: one line of at most %d characters", errand.MaxName)
	case goal == "" || len([]rune(goal)) > errand.MaxGoal:
		return "", fmt.Errorf("goal is what the errand is for, at most %d characters", errand.MaxGoal)
	case brief == "" || len([]rune(brief)) > errand.MaxBrief:
		return "", fmt.Errorf("brief is what Fridolin may tell them and agree to, at most %d characters", errand.MaxBrief)
	case message == "" || len([]rune(message)) > errand.MaxText-errand.MaxText/5:
		return "", fmt.Errorf("message is the first message, at most %d characters", errand.MaxText-errand.MaxText/5)
	case r.Days < 0 || r.Days > errand.MaxDays:
		return "", fmt.Errorf("days is how long the errand stays open, 1 to %d (default %d)", errand.MaxDays, errand.DefaultDays)
	case slices.ContainsFunc(cfg.AllowedNumbers, func(n string) bool { return chat.IdentityKey(n) == number }):
		return "", errors.New("that number is in allowed_numbers: write to them in their own chat, not in an errand")
	}
	days := r.Days
	if days == 0 {
		days = errand.DefaultDays
	}
	contact, err := b.store.ErrandContactOf(ctx, number)
	switch {
	case err == nil && contact.State == store.ContactBlocked:
		return "", fmt.Errorf("+%s is blocked: Fridolin stopped listening to them in an errand (%s), and only the owner can lift that, with !unblock %s", number, contact.Reason, number)
	case err != nil && !errors.Is(err, store.ErrNoErrand):
		return "", err
	}
	known := err == nil
	if r.Call {
		if !known {
			return "", errors.New("only someone Fridolin wrote to before is called: write to them first (call: false)")
		}
		if err := b.errandCallsReady(ctx, cfg, ""); err != nil {
			return "", err
		}
	}
	if live, err := b.store.LiveErrandWith(ctx, number); err == nil {
		return "", fmt.Errorf("errand %s with +%s has not ended (%s): one errand at a time per person; errand_answer adds to it, errand_stop ends it", live.ID, number, live.State)
	}
	target := ownerChat(cfg)
	if target == "" {
		return "", errors.New("allowed_numbers holds no number of the owner to ask or to report to")
	}
	byOwner := errandByOwner(c.run, cfg)
	now := time.Now()
	e := store.Errand{ID: "e" + randomToken(7), Brain: c.run.Brain, Contact: number, Name: name, Goal: goal, Brief: brief, Message: message,
		State: store.ErrandAsking, ByOwner: byOwner, Run: c.run.ID, Chat: target, Days: days, CreatedAt: now, Deadline: now.Add(time.Duration(days) * 24 * time.Hour), Mode: store.ErrandByMessage}
	text := message
	if !known {
		text = errand.Intro(s.OwnerName, s.Intro) + "\n\n" + message
	}
	if r.Call {
		e.Mode = store.ErrandByCall
		text = errand.Intro(s.OwnerName, s.Intro) + " " + cfg.Calls.NoticeOrDefault() + " " + message
	}
	if known && byOwner {
		if err := b.store.AddErrand(ctx, e); err != nil {
			return "", err
		}
		b.auditErrand(e, "asked", "the owner asked in their own chat; written to before, so no approval")
		if err := b.startErrand(ctx, e); err != nil {
			return "", err
		}
		if r.Call {
			return fmt.Sprintf("Errand %s is open: Fridolin is calling %s now. What they say goes to the errand's own run, not to you; you hear how it ends, and errand_answer passes on what the owner says.", e.ID, name), nil
		}
		return fmt.Sprintf("Errand %s is open: the message went to %s. Their answers go to the errand's own run, not to you; you hear how it ends, and errand_answer passes on what the owner says.", e.ID, name), nil
	}
	a := store.Approval{Kind: approvalKind, ID: "a" + randomToken(9), Brain: c.run.Brain, Run: c.run.ID, Thread: target, Chat: target, Notify: errandNotify, Risk: "write",
		Action: fmt.Sprintf("Write to %s (+%s): %s", name, number, textx.Truncate(goal, 150)), Details: text, CreatedAt: now, ExpiresAt: now.Add(b.approvalTimeout())}
	draft := outboundmsg.ErrandDraft(name, number, text, goal, brief, !known)
	if r.Call {
		a.Action = fmt.Sprintf("Call %s (+%s): %s", name, number, textx.Truncate(goal, 150))
		draft = outboundmsg.ErrandCallDraft(name, number, text, goal, brief)
	}
	e.Approval = a.ID
	if err := b.store.AddErrand(ctx, e); err != nil {
		return "", err
	}
	if err := b.store.AddApproval(ctx, a); err != nil {
		_, _ = b.store.EndErrand(ctx, e.ID, store.ErrandDeclined, "the approval could not be stored", now)
		return "", err
	}
	err = b.outbox.Text(ctx, target, draft, cfg.MaxChunkSize)
	if err == nil {
		err = b.outbox.Poll(ctx, target, outboundmsg.ApprovalQuestion(a.Action), []string{outboundmsg.ApprovalYes, outboundmsg.ApprovalNo}, approvalRefPrefix+a.ID)
	}
	if err != nil {
		_, _ = b.store.DecideApproval(ctx, a.ID, store.ApprovalDenied, "", "bridge", now)
		_, _ = b.store.EndErrand(ctx, e.ID, store.ErrandDeclined, "the approval could not be sent", now)
		return "", err
	}
	b.audit.Append(audit.Entry{Kind: audit.ApprovalRequested, Chat: target, Brain: a.Brain, Run: a.Run, ID: a.ID, Risk: a.Risk, Detail: a.Action, Outcome: "pending"})
	why := "Fridolin never wrote to them"
	if known {
		why = "the owner did not ask for it in their own chat"
	}
	b.auditErrand(e, "asked", "waits for the owner: "+why)
	return fmt.Sprintf("Errand %s waits for the owner's approval (%s), which they give in the poll that just went to them. Nothing went to %s yet; once they approve, the %s goes out and the errand runs by itself.", e.ID, why, name, map[bool]string{false: "message", true: "call"}[r.Call]), nil
}

// errandDecided takes the owner's answer to an errand's approval.
func (b *App) errandDecided(a store.Approval) {
	ctx := context.Background()
	e, err := b.store.ErrandByApproval(ctx, a.ID)
	if err != nil {
		log.Printf("errand: approval %s is no errand's: %v", a.ID, err)
		return
	}
	if a.Status == store.ApprovalApproved {
		if err := b.startErrand(ctx, e); err != nil {
			log.Printf("errand %s: %v", e.ID, err)
			b.tellErrandOwner(ctx, e, outboundmsg.ErrandFailedNotice(e.Name, e.ID, err.Error()))
			return
		}
		if e.Mode == store.ErrandByCall {
			b.tellErrandOwner(ctx, e, outboundmsg.ErrandCallingNotice(e.Name, e.ID))
		} else {
			b.tellErrandOwner(ctx, e, outboundmsg.ErrandOpenedNotice(e.Name, e.ID))
		}
		return
	}
	outcome := "the owner said no"
	if a.Status != store.ApprovalDenied {
		outcome = "nobody approved it in time"
	}
	if ended, _ := b.store.EndErrand(ctx, e.ID, store.ErrandDeclined, outcome, time.Now()); ended {
		b.auditErrand(e, "ended", store.ErrandDeclined+": "+outcome)
		if a.Status != store.ApprovalDenied {
			b.tellErrandOwner(ctx, e, outboundmsg.ErrandExpiredApprovalNotice(e.Name, e.ID))
		}
	}
}

// startErrand sends errand e's first message, the introduction ahead of
// it when Fridolin never wrote to the person, and opens it.
func (b *App) startErrand(ctx context.Context, e store.Errand) error {
	s, ok := b.errandSettings(e.Brain)
	if !ok {
		_, _ = b.store.EndErrand(ctx, e.ID, store.ErrandDeclined, "errands are off for its brain", time.Now())
		return errors.New("errands are off for its brain now")
	}
	contact, err := b.store.ErrandContactOf(ctx, e.Contact)
	switch {
	case err == nil && contact.State == store.ContactBlocked:
		_, _ = b.store.EndErrand(ctx, e.ID, store.ErrandDeclined, "Fridolin stopped listening to them", time.Now())
		return fmt.Errorf("+%s was blocked meanwhile", e.Contact)
	case err != nil && !errors.Is(err, store.ErrNoErrand):
		return b.endUnstarted(ctx, e, err)
	}
	first := err != nil
	text := e.Message
	if first {
		text = errand.Intro(s.OwnerName, s.Intro) + "\n\n" + text
	}
	now := time.Now()
	if opened, err := b.store.OpenErrand(ctx, e.ID, now.Add(time.Duration(e.Days)*24*time.Hour)); err != nil || !opened {
		if err == nil {
			return errors.New("it does not wait for its start any more")
		}
		return b.endUnstarted(ctx, e, err)
	}
	// Known before the message goes out, so that an answer that comes at
	// once is the errand's and not a stranger's.
	lid := ""
	if b.lids != nil {
		lid = lidOf(ctx, b.lids(), types.NewJID(e.Contact, types.DefaultUserServer)).User
	}
	if err := b.store.SaveErrandContact(ctx, store.ErrandContact{Contact: e.Contact, LID: lid, Name: e.Name, Brain: e.Brain, State: store.ContactKnown, UpdatedAt: now}); err != nil {
		_, _ = b.store.EndErrand(ctx, e.ID, store.ErrandDeclined, "the bridge could not remember them: "+err.Error(), now)
		return err
	}
	if e.Mode == store.ErrandByCall {
		if first {
			// contact_person calls only someone known; one forgotten
			// meanwhile is asked for afresh.
			_, _ = b.store.ForgetErrandContact(ctx, e.Contact)
			_, _ = b.store.EndErrand(ctx, e.ID, store.ErrandDeclined, "Fridolin calls only someone it wrote to before", now)
			return errors.New("only someone Fridolin wrote to before is called")
		}
		if err := b.errandCallsReady(ctx, b.cfgs.Get(), e.ID); err != nil {
			_, _ = b.store.EndErrand(ctx, e.ID, store.ErrandDeclined, err.Error(), now)
			return err
		}
		b.auditErrand(e, "opened", "calling them")
		go b.callErrand(e, s)
		return nil
	}
	if err := b.sayInErrand(ctx, e, text); err != nil {
		if first {
			_, _ = b.store.ForgetErrandContact(ctx, e.Contact)
		}
		_, _ = b.store.EndErrand(ctx, e.ID, store.ErrandDeclined, "the first message could not be sent: "+err.Error(), now)
		return err
	}
	b.auditErrand(e, "opened", "the first message went out")
	return nil
}

// endUnstarted ends errand e as declined after a store error kept it from
// starting, so that it does not stay asking and block its person for good.
// Nothing goes to the person; it returns cause.
func (b *App) endUnstarted(ctx context.Context, e store.Errand, cause error) error {
	if _, err := b.store.EndErrand(ctx, e.ID, store.ErrandDeclined, "could not start: "+cause.Error(), time.Now()); err != nil {
		log.Printf("errand %s: ending it after %v: %v", e.ID, cause, err)
	}
	return cause
}

// errandEnded is the error of a send to the person of an errand that
// is not open (any more): nothing was sent.
var errandEnded = errors.New("the errand has ended")

// recordAndSend adds text to errand id's conversation and sends it, only
// while the errand is open: the line is recorded first, in the statement
// that checks that, so a stop that comes in between sends nothing. A line
// that could not be sent is taken back, so that what the person said last
// still counts as unanswered.
func (b *App) recordAndSend(ctx context.Context, id, text string, send func() error) error {
	line, ok, err := b.store.AddErrandLineWhileOpen(ctx, id, store.ErrandLine{Sender: errand.FromYou, Text: text, At: time.Now()})
	if err != nil {
		return err
	}
	if !ok {
		return errandEnded
	}
	if err := send(); err != nil {
		// ctx may be why the send failed.
		if terr := b.store.TakeBackErrandLine(context.WithoutCancel(ctx), id, line); terr != nil {
			log.Printf("errand %s: taking back what was not sent: %v", id, terr)
		}
		return err
	}
	return nil
}

// sayInErrand sends text to errand e's person and adds it to the
// conversation, only while e is open.
func (b *App) sayInErrand(ctx context.Context, e store.Errand, text string) error {
	if err := b.recordAndSend(ctx, e.ID, text, func() error {
		return b.outbox.Text(ctx, chat.DMJID(e.Contact), text, b.cfgs.Get().MaxChunkSize)
	}); err != nil {
		return err
	}
	b.auditErrand(e, "sent", fmt.Sprintf("%d characters", len([]rune(text))))
	return nil
}

// sayEnding sends text to errand e's person and records it without asking
// whether e is open: for the last words of an end this code has itself
// just made.
func (b *App) sayEnding(ctx context.Context, e store.Errand, text string) error {
	if err := b.outbox.Text(ctx, chat.DMJID(e.Contact), text, b.cfgs.Get().MaxChunkSize); err != nil {
		return err
	}
	if err := b.store.AddErrandLine(ctx, e.ID, store.ErrandLine{Sender: errand.FromYou, Text: text, At: time.Now()}, true); err != nil {
		log.Printf("errand %s: recording what went out: %v", e.ID, err)
	}
	b.auditErrand(e, "sent", fmt.Sprintf("%d characters", len([]rune(text))))
	return nil
}

// tellErrandOwner tells the owner text about errand e, in the chat the
// errand reports to. The brain's history records it, so that the brain
// knows what the owner answers.
func (b *App) tellErrandOwner(ctx context.Context, e store.Errand, text string) {
	if err := b.outbox.Question(ctx, e.Brain, e.Chat, text, b.cfgs.Get().MaxChunkSize); err != nil {
		log.Printf("errand %s: telling the owner: %v", e.ID, err)
	}
}

// Refs of the notices that carry the person's words (Outbox.Notice): a
// reply quoting one is known by them (readQuote).
const (
	// errandQuestionRef is an errand's question, "errand-question:<id>",
	// whose quote-reply by the owner answers it.
	errandQuestionRef = "errand-question:"
	errandNoteRef     = "errand:"         // "errand:<id>": how it ended
	errandForwardRef  = "errand-forward:" // "errand-forward:<number>"
)

// tellErrandOwnerApart tells the owner text about errand e, which carries
// words the person shaped: the brain's history records history in its
// place, and a reply quoting it shows history too (ref, Outbox.Notice).
// The brain's runs meet the person's words only through `errands`, which
// marks them as theirs.
func (b *App) tellErrandOwnerApart(ctx context.Context, e store.Errand, text, history, ref string) {
	if err := b.outbox.Notice(ctx, e.Brain, e.Chat, text, history, ref, b.cfgs.Get().MaxChunkSize); err != nil {
		log.Printf("errand %s: telling the owner: %v", e.ID, err)
	}
}

// auditErrand writes one step of errand e to the audit log.
func (b *App) auditErrand(e store.Errand, detail, outcome string) {
	run := e.Run
	if detail != "asked" {
		run = "errand:" + e.ID
	}
	b.audit.Append(audit.Entry{Kind: audit.Errand, Brain: e.Brain, Run: run, ID: e.ID, Chat: logx.MaskJID(e.Contact, "", ""), Detail: detail, Outcome: outcome})
}

// errandClaims reports whether a direct message from sender, whom
// allowed_numbers does not let in, is for the errands: someone Fridolin
// wrote to, or stopped listening to. sender is a phone JID, or a LID that
// did not resolve.
func (b *App) errandClaims(sender types.JID) bool {
	if sender.User == "" {
		return false
	}
	_, err := b.store.ErrandContactOf(context.Background(), sender.User)
	return err == nil
}

// errandInbound takes a message from someone the errands claimed: the
// next line of their open errand, passed on to the owner while none is
// open or past a day's messages in a conversation they began, or nothing
// at all from someone Fridolin stopped listening to.
func (b *App) errandInbound(ctx context.Context, m chat.Inbound) {
	if !b.intake.Enter() {
		log.Printf("errand: a message from %s came in as the bridge stopped; it is lost", logx.MaskJID(m.Sender, "", ""))
		return
	}
	defer b.intake.Leave()
	contact, err := b.store.ErrandContactOf(ctx, m.Sender)
	if err != nil {
		log.Printf("errand: a message from %s, whom no errand knows any more: ignored", logx.MaskJID(m.Sender, "", ""))
		return
	}
	if contact.State == store.ContactBlocked {
		log.Printf("errand: a message from %s, whom Fridolin stopped listening to: ignored", logx.MaskJID(contact.Contact, "", ""))
		return
	}
	text := strings.TrimSpace(m.Text)
	if text == "" {
		return
	}
	b.errands.begin.Lock()
	e, err := b.store.LiveErrandWith(ctx, contact.Contact)
	switch {
	case err == nil && e.State == store.ErrandOpen && !b.heardEnough(ctx, e):
		err := b.store.AddErrandLine(ctx, e.ID, store.ErrandLine{Sender: errand.FromThem, Text: text, At: m.At}, false)
		b.errands.begin.Unlock()
		if err != nil {
			log.Printf("errand %s: recording their message: %v", e.ID, err)
			return
		}
		b.auditErrand(e, "answered", fmt.Sprintf("%d characters", len([]rune(text))))
		b.errands.soon(e.ID, b.waits.errandGather, b.errandTurn)
		return
	case errors.Is(err, store.ErrNoErrand) && contact.Mode == store.ContactConverse:
		begun := b.beginConversation(ctx, contact, text, m.At)
		b.errands.begin.Unlock()
		if begun {
			return
		}
	default:
		// One that waits for the owner's approval, a conversation past the
		// day's messages, or a store that cannot tell: the owner hears of it.
		b.errands.begin.Unlock()
	}
	if !b.errands.forward(contact.Contact, time.Now()) {
		log.Printf("errand: %s wrote again; passed on %d times today already", logx.MaskJID(contact.Contact, "", ""), errandForwardsPerDay)
		return
	}
	target := ownerChat(b.cfgs.Get())
	if target == "" {
		return
	}
	notice := outboundmsg.ErrandForwardNotice(contact.Name, contact.Contact, text)
	history := outboundmsg.ErrandForwardHistory(contact.Name, contact.Contact)
	if err := b.outbox.Notice(ctx, contact.Brain, target, notice, history, errandForwardRef+contact.Contact, b.cfgs.Get().MaxChunkSize); err != nil {
		log.Printf("errand: passing on a message from %s: %v", logx.MaskJID(contact.Contact, "", ""), err)
	}
}

// heardEnough reports whether the person in e, a conversation they began,
// reached a day's messages, or the store cannot tell. Errands the owner
// started have no such limit: the owner asked for that conversation.
func (b *App) heardEnough(ctx context.Context, e store.Errand) bool {
	if e.OpenedBy != store.OpenedByThem {
		return false
	}
	heard, err := b.store.ErrandLinesFromSince(ctx, e.Contact, errand.FromThem, time.Now().Add(-24*time.Hour))
	if err != nil {
		log.Printf("errand %s: counting the messages of %s: %v", e.ID, logx.MaskJID(e.Contact, "", ""), err)
		return true
	}
	if heard >= errandHeardPerDay {
		log.Printf("errand %s: %s wrote again after %d messages today: passed on to the owner", e.ID, logx.MaskJID(e.Contact, "", ""), heard)
		return true
	}
	return false
}

// beginConversation begins a conversation with contact, who wrote text
// at at while nothing was open with them: an errand of its own, with no
// approval (the owner approved writing to them once, and they wrote
// first), which errandTurn answers like any. It reports false, and
// begins nothing, when errands are off for their brain or they reached
// a day's limit: their message then goes to the owner.
func (b *App) beginConversation(ctx context.Context, contact store.ErrandContact, text string, at time.Time) bool {
	s, ok := b.errandSettings(contact.Brain)
	cfg := b.cfgs.Get()
	target := ownerChat(cfg)
	if !ok || target == "" || b.errandEngineReady(cfg) != nil {
		return false
	}
	now := time.Now()
	day := now.Add(-24 * time.Hour)
	begun, err := b.store.ErrandsOpenedByThemSince(ctx, contact.Contact, day)
	if err != nil {
		log.Printf("errand: counting the conversations of %s: %v", logx.MaskJID(contact.Contact, "", ""), err)
		return false
	}
	heard, err := b.store.ErrandLinesFromSince(ctx, contact.Contact, errand.FromThem, day)
	if err != nil {
		log.Printf("errand: counting the messages of %s: %v", logx.MaskJID(contact.Contact, "", ""), err)
		return false
	}
	if begun >= errandChatsPerDay || heard >= errandHeardPerDay {
		log.Printf("errand: %s wrote again after %d conversations and %d messages today: passed on to the owner", logx.MaskJID(contact.Contact, "", ""), begun, heard)
		return false
	}
	e := store.Errand{ID: "e" + randomToken(7), Brain: contact.Brain, Contact: contact.Contact, Name: contact.Name, Goal: errand.ConversationGoal(s.OwnerName),
		State: store.ErrandOpen, Chat: target, Days: errandChatDays, CreatedAt: now, Deadline: now.Add(errandChatDays * 24 * time.Hour),
		Mode: store.ErrandByMessage, OpenedBy: store.OpenedByThem}
	if err := b.store.AddErrand(ctx, e); err != nil {
		log.Printf("errand: beginning a conversation with %s: %v", logx.MaskJID(contact.Contact, "", ""), err)
		return false
	}
	if err := b.store.AddErrandLine(ctx, e.ID, store.ErrandLine{Sender: errand.FromThem, Text: text, At: at}, false); err != nil {
		log.Printf("errand %s: recording their message: %v", e.ID, err)
		_, _ = b.store.EndErrand(ctx, e.ID, store.ErrandStopped, "their message could not be recorded", now)
		return false
	}
	b.auditErrand(e, "opened", "they wrote first")
	b.errands.soon(e.ID, b.waits.errandGather, b.errandTurn)
	return true
}

// errandTurn answers what the person (or the owner) said in errand id
// since Fridolin last wrote, or ends the errand when its time or its
// messages ran out.
func (b *App) errandTurn(id string) {
	if !b.intake.Enter() {
		return
	}
	defer b.intake.Leave()
	ctx := context.Background()
	if b.errands.calling(id) {
		// A message they send during the call is a line of it; the call
		// answers.
		return
	}
	e, err := b.store.GetErrand(ctx, id)
	if err != nil || e.State != store.ErrandOpen {
		return
	}
	lines, err := b.store.ErrandLines(ctx, id)
	if err != nil {
		return
	}
	if time.Now().After(e.Deadline) {
		b.expireHearing(ctx, e, lines, errandTimeUp(e))
		return
	}
	if len(lines) == 0 || lines[len(lines)-1].Sender == errand.FromYou {
		return
	}
	if e.Sent >= errand.MaxMessages {
		b.expireHearing(ctx, e, lines, fmt.Sprintf("I wrote its %d messages", errand.MaxMessages))
		return
	}
	s, ok := b.errandSettings(e.Brain)
	if !ok {
		if ended, _ := b.store.EndErrand(ctx, e.ID, store.ErrandStopped, "errands are off for its brain", time.Now()); ended {
			b.auditErrand(e, "ended", store.ErrandStopped+": errands are off for its brain")
		}
		return
	}
	ans, err := b.errandAsk(ctx, e, s, lines, false)
	if err != nil {
		log.Printf("errand %s: %v", e.ID, err)
		b.auditErrand(e, "answered", "no answer: "+textx.Truncate(err.Error(), 200))
		b.tellErrandOwner(ctx, e, outboundmsg.ErrandFailedNotice(e.Name, e.ID, err.Error()))
		return
	}
	b.errandAct(ctx, e, ans, lines, errandChannel{
		say:       func(text string) error { return b.sayInErrand(ctx, e, text) },
		sayEnding: func(text string) error { return b.sayEnding(ctx, e, text) },
		abort:     outboundmsg.ErrandAbortMessage(strings.TrimSpace(s.OwnerName)),
		warn:      outboundmsg.ErrandWarnMessage(strings.TrimSpace(s.OwnerName)),
	})
}

// errandTimeUp is why errand e ended when its time ran out.
func errandTimeUp(e store.Errand) string {
	if e.OpenedBy == store.OpenedByThem {
		return "the day a conversation they begin stays open is over"
	}
	return fmt.Sprintf("its %d days are over", e.Days)
}

// errandChannel is how what an errand's run writes reaches its person:
// by message, or read aloud on a call.
type errandChannel struct {
	// say sends text, and adds it to the conversation as Fridolin's.
	say func(text string) error
	// sayEnding sends and records text without the check that the errand
	// is open: the last words of an end already made.
	sayEnding func(text string) error
	// abort is the bridge's own last words when the run aborts; warn its
	// words when the person is warned instead.
	abort, warn string
}

// errandAct does what errand e's run answered over ch, and reports
// whether the conversation goes on there: after a reply it does; after
// a question for the owner, the end or an abort it does not.
func (b *App) errandAct(ctx context.Context, e store.Errand, ans errand.Answer, lines []store.ErrandLine, ch errandChannel) bool {
	switch ans.Action {
	case errand.Reply:
		err := ch.say(ans.Text)
		// The note goes on whether or not the reply did; ctx may be why it did not.
		b.passOnNote(context.WithoutCancel(ctx), e, ans.Note)
		if err != nil {
			log.Printf("errand %s: sending: %v", e.ID, err)
			return !errors.Is(err, errandEnded)
		}
		return true
	case errand.AskOwner:
		if ans.Text != "" {
			if err := ch.say(ans.Text); err != nil {
				log.Printf("errand %s: sending: %v", e.ID, err)
				if errors.Is(err, errandEnded) {
					return false
				}
			}
		} else if cur, err := b.store.GetErrand(ctx, e.ID); err != nil || cur.State != store.ErrandOpen {
			return false
		}
		b.auditErrand(e, "asked the owner", textx.Truncate(ans.Question, 200))
		b.tellErrandOwnerApart(ctx, e, outboundmsg.ErrandQuestionNotice(e.Name, e.ID, ans.Question),
			outboundmsg.ErrandQuestionHistory(e.Name, e.ID), errandQuestionRef+e.ID)
	case errand.Done:
		summary := ans.Summary
		if summary == "" {
			summary = "it came to its end."
		}
		// Ended before the last words go out, so that what they write to
		// those begins a conversation of its own instead of joining one
		// that is over.
		if ended, _ := b.store.EndErrand(ctx, e.ID, store.ErrandDone, summary, time.Now()); !ended {
			return false
		}
		if ans.Text != "" {
			if err := ch.sayEnding(ans.Text); err != nil {
				log.Printf("errand %s: sending: %v", e.ID, err)
			}
		}
		b.auditErrand(e, "ended", store.ErrandDone)
		defer b.hearLate(ctx, e, len(lines))
		if ans.Stop {
			// They asked not to be written to: the next errand with them
			// asks the owner first, and what they send meanwhile is a
			// stranger's.
			if _, err := b.store.ForgetErrandContact(ctx, e.Contact); err != nil {
				log.Printf("errand %s: forgetting +%s: %v", e.ID, logx.MaskJID(e.Contact, "", ""), err)
			}
			b.auditErrand(e, "forgotten", "they asked not to be written to again")
		}
		b.passOnNote(ctx, e, ans.Note)
		if e.OpenedBy == store.OpenedByThem {
			b.tellErrandOwnerApart(ctx, e, outboundmsg.ErrandChatEndedNotice(e.Name, e.ID, summary, ans.Stop),
				outboundmsg.ErrandChatEndedHistory(e.Name, e.ID, ans.Stop), errandNoteRef+e.ID)
			return false
		}
		b.tellErrandOwnerApart(ctx, e, outboundmsg.ErrandDoneNotice(e.Name, e.ID, summary, ans.Stop),
			outboundmsg.ErrandDoneHistory(e.Name, e.ID, ans.Stop), errandNoteRef+e.ID)
		return false
	case errand.Abort:
		if !ans.Severe {
			if c, err := b.store.ErrandContactOf(ctx, e.Contact); err == nil && !c.Warned(time.Now()) {
				b.warnInErrand(ctx, e, ans.Reason, lines, ch)
				return true
			}
		}
		b.abortErrand(ctx, e, ans.Reason, lines, ch)
		return false
	}
	b.passOnNote(ctx, e, ans.Note)
	return false
}

// hearLate takes what errand e's person wrote after its first seen lines,
// which the run that ended it did not read: e is over, so it goes the
// way a message does while nothing is open with them, to a conversation
// of its own or to the owner. A call's are not: they were said on it.
func (b *App) hearLate(ctx context.Context, e store.Errand, seen int) {
	if e.Mode == store.ErrandByCall {
		return
	}
	all, err := b.store.ErrandLines(ctx, e.ID)
	if err != nil || len(all) <= seen {
		return
	}
	var late []string
	var at time.Time
	for _, l := range all[seen:] {
		if l.Sender == errand.FromThem {
			late, at = append(late, l.Text), l.At
		}
	}
	if len(late) == 0 {
		return
	}
	b.errandInbound(ctx, chat.Inbound{JID: chat.DMJID(e.Contact), Sender: e.Contact, Text: strings.Join(late, "\n"), At: at, Source: chat.SourceWhatsApp})
}

// passOnNote tells the owner note, what errand e's person asked Fridolin
// to pass on: their words, which the brain's history leaves out.
func (b *App) passOnNote(ctx context.Context, e store.Errand, note string) {
	if note == "" {
		return
	}
	// A note the owner already has (a turn after a reply nobody heard
	// answers it again, #611) does not go out twice. A failed record
	// still passes it on: better twice than lost.
	if fresh, err := b.store.AddErrandNote(ctx, e.ID, note, time.Now()); err != nil {
		log.Printf("errand %s: recording the note: %v", e.ID, err)
	} else if !fresh {
		b.auditErrand(e, "not passed on again", fmt.Sprintf("%d characters", len([]rune(note))))
		return
	}
	b.auditErrand(e, "passed on", fmt.Sprintf("%d characters", len([]rune(note))))
	b.tellErrandOwnerApart(ctx, e, outboundmsg.ErrandNoteNotice(e.Name, e.ID, note), outboundmsg.ErrandNoteHistory(e.Name, e.ID), errandNoteRef+e.ID)
}

// lastFromThem is the latest of lines the person wrote, "" for none.
func lastFromThem(lines []store.ErrandLine) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i].Sender == errand.FromThem {
			return lines[i].Text
		}
	}
	return ""
}

// warnInErrand answers the person of errand e, who tried for the first
// time in ErrandWarningKeep to make Fridolin do what it must not (and
// not severely), with the bridge's own warning (ch.warn) instead of the
// end: the conversation goes on, the owner hears why, and a second try
// ends it.
func (b *App) warnInErrand(ctx context.Context, e store.Errand, reason string, lines []store.ErrandLine, ch errandChannel) {
	if reason == "" {
		reason = "they tried to make me do what I must not."
	}
	// A warning that did not go out is none: the turn that follows must not
	// take a second try from what they have not been told.
	if err := ch.say(ch.warn); err != nil {
		log.Printf("errand %s: sending the warning: %v", e.ID, err)
		return
	}
	if err := b.store.WarnErrandContact(ctx, e.Contact, time.Now()); err != nil {
		log.Printf("errand %s: warning +%s: %v", e.ID, logx.MaskJID(e.Contact, "", ""), err)
	}
	b.auditErrand(e, "warned", textx.Truncate(reason, 200))
	b.tellErrandOwnerApart(ctx, e, outboundmsg.ErrandWarnedNotice(e.Name, e.ID, reason, lastFromThem(lines)),
		outboundmsg.ErrandWarnedHistory(e.Name, e.ID), errandNoteRef+e.ID)
}

// abortErrand ends errand e because the person tried to make Fridolin do
// what it must not: they get the bridge's own words (ch.abort), the
// owner hears why, and Fridolin stops listening to them.
func (b *App) abortErrand(ctx context.Context, e store.Errand, reason string, lines []store.ErrandLine, ch errandChannel) {
	if reason == "" {
		reason = "they tried to make me do what I must not."
	}
	// Blocked either way; the words only go to someone whose errand this
	// ends, since errand_stop and expiry end one without a word to them.
	ended, _ := b.store.EndErrand(ctx, e.ID, store.ErrandAborted, reason, time.Now())
	if ended {
		if err := ch.sayEnding(ch.abort); err != nil {
			log.Printf("errand %s: sending the end: %v", e.ID, err)
		}
	}
	contact := store.ErrandContact{Contact: e.Contact, Name: e.Name, Brain: e.Brain, State: store.ContactBlocked, Reason: textx.Truncate(reason, 300), UpdatedAt: time.Now()}
	if c, err := b.store.ErrandContactOf(ctx, e.Contact); err == nil {
		contact.LID = c.LID
	}
	if err := b.store.SaveErrandContact(ctx, contact); err != nil {
		log.Printf("errand %s: blocking +%s: %v", e.ID, logx.MaskJID(e.Contact, "", ""), err)
	}
	b.auditErrand(e, "blocked", textx.Truncate(reason, 200))
	b.tellErrandOwnerApart(ctx, e, outboundmsg.ErrandAbortedNotice(e.Name, e.Contact, e.ID, reason, lastFromThem(lines)),
		outboundmsg.ErrandAbortedHistory(e.Name, e.Contact, e.ID), errandNoteRef+e.ID)
}

// expireErrand ends errand e, whose time or messages ran out (why).
// It reports whether this call ended it.
func (b *App) expireErrand(ctx context.Context, e store.Errand, why string) bool {
	if ended, _ := b.store.EndErrand(ctx, e.ID, store.ErrandExpired, why, time.Now()); !ended {
		return false
	}
	b.auditErrand(e, "ended", store.ErrandExpired+": "+why)
	b.tellErrandOwner(ctx, e, outboundmsg.ErrandExpiredNotice(e.Name, e.ID, why))
	return true
}

// expireHearing ends errand e like expireErrand, and when this call
// ended it, passes on what its person wrote that nobody answered (the
// tail of lines they wrote), to a conversation of its own or to the
// owner. errandTurn returns early on an ended errand, so this is the
// only place those lines go on, once.
func (b *App) expireHearing(ctx context.Context, e store.Errand, lines []store.ErrandLine, why string) {
	if !b.expireErrand(ctx, e, why) {
		return
	}
	unanswered := len(lines)
	for unanswered > 0 && lines[unanswered-1].Sender == errand.FromThem {
		unanswered--
	}
	b.hearLate(ctx, e, unanswered)
}

// errandAsk has errand e's run answer, on a call (call) or by message:
// in Codex mode through the OpenAI
// connection, in Claude mode through `claude -p` with no tools at all.
func (b *App) errandAsk(ctx context.Context, e store.Errand, s manifest.Errands, lines []store.ErrandLine, call bool) (errand.Answer, error) {
	cfg := RuntimeConfig(b.cfgs)
	doc := errand.Errand{Owner: strings.TrimSpace(s.OwnerName), Name: e.Name, Goal: e.Goal, Brief: e.Brief, Now: time.Now().In(cfg.Location()), Deadline: e.Deadline, Call: call,
		Persona: s.Persona, Profile: s.Profile}
	if c, err := b.store.ErrandContactOf(ctx, e.Contact); err == nil {
		doc.ForThem = c.Brief
	}
	// What was said before, but not what the owner said then: that was
	// meant for its own errand, and the run follows the owner's lines.
	earlier, err := b.store.EarlierErrandLines(ctx, e.Contact, e.ID, 2*errand.MaxEarlier)
	if err != nil {
		log.Printf("errand %s: reading the earlier conversations: %v", e.ID, err)
	}
	for _, l := range earlier {
		if l.Sender != errand.FromOwner {
			doc.Earlier = append(doc.Earlier, errand.Line{From: l.Sender, Text: l.Text, At: l.At})
		}
	}
	for _, l := range lines {
		doc.Lines = append(doc.Lines, errand.Line{From: l.Sender, Text: l.Text, At: l.At})
	}
	if doc.Passed, err = b.store.ErrandNotes(ctx, e.ID); err != nil {
		log.Printf("errand %s: reading the notes passed on: %v", e.ID, err)
	}
	ctx, cancel := context.WithTimeout(ctx, errandRunTimeout)
	defer cancel()
	var out string
	switch {
	case b.hooks.errandRun != nil:
		out, err = b.hooks.errandRun(doc)
	case cfg.AgentProvider == config.ProviderCodex:
		out, err = b.errandOpenAI(ctx, e, s, cfg, doc)
	default:
		out, err = b.errandClaude(ctx, e, cfg, doc)
	}
	if err != nil {
		return errand.Answer{}, err
	}
	return errand.ParseAnswer(out)
}

// errandOpenAI asks OpenAI's Responses API, with the owner's key and the
// brain's monthly limit of the connection, for errand e's answer.
func (b *App) errandOpenAI(ctx context.Context, e store.Errand, s manifest.Errands, cfg *config.Settings, doc errand.Errand) (string, error) {
	if err := b.errandEngineReady(cfg); err != nil {
		return "", err
	}
	model := s.Model
	if model == "" {
		model = errand.DefaultModel
	}
	body, err := errand.ResponsesRequest(model, doc)
	if err != nil {
		return "", err
	}
	caller := connections.BrainCaller(e.Brain, "errand:"+e.ID, connectionSettingsOf(cfg).Of(connections.OpenAI).BrainMonthlyUSD)
	resp, err := b.conns.Responses(ctx, caller, body)
	if err != nil {
		return "", err
	}
	return errand.ResponsesText(resp)
}

// errandClaude runs `claude -p` for errand e's answer: no tools, no MCP
// server, the rules as its system prompt, in an empty folder of its
// own, under the brain's sandbox and with its login.
func (b *App) errandClaude(ctx context.Context, e store.Errand, cfg *config.Settings, doc errand.Errand) (string, error) {
	br := b.brainMgr.Get(e.Brain)
	if br == nil {
		return "", fmt.Errorf("brain %s is not running", e.Brain)
	}
	dataDir := AbsPath(cfg.DataDir)
	state, err := prepareBrainState(dataDir, cfg.MediaDir, e.Brain, cfg.InheritClaudeUserConfig)
	if err != nil {
		return "", fmt.Errorf("the brain's state: %w", err)
	}
	// An empty folder: the run must not find the brain's CLAUDE.md, its
	// notes or anything else of the owner's.
	dir, err := os.MkdirTemp(brainStateOf(dataDir, e.Brain).tmp, "errand-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	extra := []string{"--tools", "", "--disable-slash-commands", "--no-session-persistence", "--system-prompt", errand.Instructions}
	model := br.Manifest().Model
	if model == "" {
		model = cfg.BrainModel
	}
	if model != "" {
		extra = append(extra, "--model", model)
	}
	args := claudecli.ArgList(true, excludeInstructionsAbove(dir, cfg.Home), extra...)
	bin, err := resolveAgentBinary("claude", cfg.ClaudePath)
	if err != nil {
		return "", err
	}
	env := childEnv(b.env, claudeChild, cfg.ChildEnvPassthrough, append(state, "DISABLE_AUTOUPDATER=1")...)
	if sp := b.brainMgr.profiler; sp != nil && sp.Enabled() {
		sbBin, sbArgs, err := sp.Command(e.Brain, bin, args)
		if err != nil {
			// Not err itself: it names the sandbox's files, which the
			// brain's own runs log when its sandbox cannot be set up.
			return "", errors.New("the brain's sandbox could not be set up")
		}
		bin, args = sbBin, sbArgs
	}
	var out bytes.Buffer
	proc, err := claudecli.Start(ctx, claudecli.Spawn{WaitDelay: b.waits.runWaitDelay, Binary: bin, Args: args, Dir: dir, Env: env, Prompt: "The errand:\n" + errand.Document(doc), Stdout: &out})
	if err == nil {
		err = proc.Wait()
	}
	if err != nil {
		if proc != nil {
			return "", fmt.Errorf("claude: %w: %s", err, textx.Tail(proc.StderrTail(), 300))
		}
		return "", fmt.Errorf("claude: %w", err)
	}
	res := claudecli.Parse(&out, discardStream{})
	if res.Err != nil {
		return "", res.Err
	}
	return res.Text, nil
}

// discardStream is a run's stream nobody watches.
type discardStream struct{}

func (discardStream) Log(string)              {}
func (discardStream) Question(string) bool    { return false }
func (discardStream) Result(claudecli.Result) {}

// watchErrands answers, after a restart, what came in while the bridge
// was down, then ends errands whose time ran out, every errandSweep,
// until ctx ends.
func (b *App) watchErrands(ctx context.Context) {
	defer b.errands.stop()
	b.sweepErrands(ctx, time.Now(), true)
	every := b.waits.errandSweep
	if every <= 0 {
		every = time.Minute
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			b.sweepErrands(ctx, time.Now(), false)
		}
	}
}

// sweepErrands ends the open errands whose time ran out by now; resume
// also answers those whose person (or owner) had the last word.
func (b *App) sweepErrands(ctx context.Context, now time.Time, resume bool) {
	open, err := b.store.OpenErrands(ctx)
	if err != nil {
		log.Printf("errands: %v", err)
		return
	}
	for _, e := range open {
		if now.After(e.Deadline) {
			lines, err := b.store.ErrandLines(ctx, e.ID)
			if err != nil {
				// Without the lines nothing can be told apart as unanswered.
				b.expireErrand(ctx, e, errandTimeUp(e))
				continue
			}
			b.expireHearing(ctx, e, lines, errandTimeUp(e))
			continue
		}
		if !resume {
			continue
		}
		if lines, err := b.store.ErrandLines(ctx, e.ID); err == nil && len(lines) > 0 && lines[len(lines)-1].Sender != errand.FromYou {
			b.errands.soon(e.ID, 0, b.errandTurn)
		}
	}
}

// answerErrand hands errand id what the owner said, for its run to pass
// on.
func (b *App) answerErrand(ctx context.Context, brain, id, text string) error {
	e, err := b.store.GetErrand(ctx, id)
	if err != nil || e.Brain != brain {
		return fmt.Errorf("no errand %s of this brain", id)
	}
	if e.State != store.ErrandOpen {
		return fmt.Errorf("errand %s is %s, not open", id, e.State)
	}
	if err := b.store.AddErrandLine(ctx, e.ID, store.ErrandLine{Sender: errand.FromOwner, Text: text, At: time.Now()}, false); err != nil {
		return err
	}
	b.auditErrand(e, "owner answered", fmt.Sprintf("%d characters", len([]rune(text))))
	b.errands.soon(e.ID, 0, b.errandTurn)
	return nil
}

// quoteShownLen is how much of a note's history a quote of it shows.
const quoteShownLen = 300

// quoteUnread is what a quote shows when the bridge could not tell
// whether it quotes a notice that carries someone's words.
const quoteUnread = "[a message of mine, which could not be read]"

// readQuote reads what m quotes when it is a notice of the bridge's kept
// as a store.Note (Outbox.Notice): the quote shows the note's history
// instead of the words WhatsApp quotes, so that an errand's person's
// words in it never reach a run, and the owner's reply to an errand's
// question goes to the errand at once, with no run at all (answered).
func (b *App) readQuote(ctx context.Context, m brainruntime.Inbound) (_ brainruntime.Inbound, answered bool) {
	q := m.Quoted
	if q == nil || q.ID == "" {
		return m, false
	}
	n, err := b.store.NoteOf(ctx, q.ID)
	switch {
	case errors.Is(err, store.ErrNoNote):
		return m, false
	case err != nil:
		// Whether it quotes such a notice cannot be told: its words stay out.
		log.Printf("reading the message a reply in %s quotes: %v", maskChatJID(m.JID), err)
		m.Quoted = &chat.Quote{ID: q.ID, From: q.From, Text: quoteUnread}
		return m, false
	}
	if id, ok := strings.CutPrefix(n.Ref, errandQuestionRef); ok && b.answerByQuote(ctx, m, n, id) {
		return m, true
	}
	m.Quoted = &chat.Quote{ID: q.ID, From: q.From, Text: textx.Truncate(n.History, quoteShownLen)}
	return m, false
}

// answerByQuote passes m, a reply quoting n, the question of errand id,
// on to the errand as the owner's answer, and tells them so. Only the
// owner's own text in the chat n went to counts, and not a command or
// what looks like a one-time code, which go their usual way; nor does
// a reply once the errand has ended. It reports whether it passed m on;
// what it does not goes to the brain as any message does.
func (b *App) answerByQuote(ctx context.Context, m brainruntime.Inbound, n store.Note, id string) bool {
	owner := ownerChat(b.cfgs.Get())
	text := strings.TrimSpace(m.Text)
	if _, code := otp.Code(text); code {
		return false
	}
	switch {
	case owner == "" || chat.IdentityKey(m.Sender) != chat.IdentityKey(owner),
		!slices.Contains(chatJIDs(m), n.Chat),
		text == "" || len(m.Media) > 0 || strings.HasPrefix(text, "!"):
		return false
	}
	e, err := b.store.GetErrand(ctx, id)
	if err != nil || e.State != store.ErrandOpen {
		return false
	}
	if err := b.answerErrand(ctx, e.Brain, e.ID, text); err != nil {
		log.Printf("errand %s: passing on the owner's reply: %v", e.ID, err)
		return false
	}
	if err := b.outbox.Question(ctx, e.Brain, n.Chat, outboundmsg.ErrandAnsweredReply(e.Name, e.ID), b.cfgs.Get().MaxChunkSize); err != nil {
		log.Printf("errand %s: telling the owner: %v", e.ID, err)
	}
	return true
}

// stopErrand ends errand id of brain without a word to the person.
func (b *App) stopErrand(ctx context.Context, brain, id string) error {
	return b.stopErrandFor(ctx, brain, id, "the brain ended it")
}

// stopErrandFor is stopErrand with why, the errand's outcome.
func (b *App) stopErrandFor(ctx context.Context, brain, id, why string) error {
	e, err := b.store.GetErrand(ctx, id)
	if err != nil || e.Brain != brain {
		return fmt.Errorf("no errand %s of this brain", id)
	}
	ended, err := b.store.EndErrand(ctx, e.ID, store.ErrandStopped, why, time.Now())
	if err != nil {
		return err
	}
	if !ended {
		return fmt.Errorf("errand %s ended already (%s)", id, e.State)
	}
	if e.Approval != "" && e.State == store.ErrandAsking {
		// callerAnswers: no call waits on an errand's approval, so the
		// denial goes nowhere and errandDecided never hears of it.
		b.decide(ctx, e.Approval, store.ApprovalDenied, "", "bridge", callerAnswers)
	}
	b.auditErrand(e, "ended", store.ErrandStopped)
	return nil
}

// unblockErrandContact lets Fridolin hear number again, someone it
// stopped listening to.
func (b *App) unblockErrandContact(ctx context.Context, number string) (bool, error) {
	c, err := b.store.ErrandContactOf(ctx, number)
	if errors.Is(err, store.ErrNoErrand) || (err == nil && c.State != store.ContactBlocked) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Known again, so that the owner's own request writes to them without
	// a poll; what they write goes to the owner until the owner lets
	// Fridolin talk with them again (errand_contact).
	return b.store.UnblockErrandContact(ctx, c.Contact, time.Now())
}
