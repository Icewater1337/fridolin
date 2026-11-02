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
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/connections"
	"github.com/Icewater1337/fridolin/internal/errand"
	"github.com/Icewater1337/fridolin/internal/logx"
	"github.com/Icewater1337/fridolin/internal/manifest"
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

// errandTurns runs each errand's turns one at a time: a message that
// comes while its run answers waits for the next turn.
type errandTurns struct {
	mu       sync.Mutex
	timers   map[string]*time.Timer
	running  map[string]bool
	again    map[string]bool
	forwards map[string][]time.Time
	stopped  bool
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
		State: store.ErrandAsking, ByOwner: byOwner, Run: c.run.ID, Chat: target, Days: days, CreatedAt: now, Deadline: now.Add(time.Duration(days) * 24 * time.Hour)}
	text := message
	if !known {
		text = errand.Intro(s.OwnerName, s.Intro) + "\n\n" + message
	}
	if known && byOwner {
		if err := b.store.AddErrand(ctx, e); err != nil {
			return "", err
		}
		b.auditErrand(e, "asked", "the owner asked in their own chat; written to before, so no approval")
		if err := b.startErrand(ctx, e); err != nil {
			return "", err
		}
		return fmt.Sprintf("Errand %s is open: the message went to %s. Their answers go to the errand's own run, not to you; you hear how it ends, and errand_answer passes on what the owner says.", e.ID, name), nil
	}
	a := store.Approval{Kind: approvalKind, ID: "a" + randomToken(9), Brain: c.run.Brain, Run: c.run.ID, Thread: target, Chat: target, Notify: errandNotify, Risk: "write",
		Action: fmt.Sprintf("Write to %s (+%s): %s", name, number, textx.Truncate(goal, 150)), Details: text, CreatedAt: now, ExpiresAt: now.Add(b.approvalTimeout())}
	e.Approval = a.ID
	if err := b.store.AddErrand(ctx, e); err != nil {
		return "", err
	}
	if err := b.store.AddApproval(ctx, a); err != nil {
		_, _ = b.store.EndErrand(ctx, e.ID, store.ErrandDeclined, "the approval could not be stored", now)
		return "", err
	}
	err = b.outbox.Text(ctx, target, outboundmsg.ErrandDraft(name, number, text, goal, brief, !known), cfg.MaxChunkSize)
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
	return fmt.Sprintf("Errand %s waits for the owner's approval (%s), which they give in the poll that just went to them. Nothing went to %s yet; once they approve, the message goes out and the errand runs by itself.", e.ID, why, name), nil
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
		b.tellErrandOwner(ctx, e, outboundmsg.ErrandOpenedNotice(e.Name, e.ID))
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
		return err
	}
	first := err != nil
	text := e.Message
	if first {
		text = errand.Intro(s.OwnerName, s.Intro) + "\n\n" + text
	}
	now := time.Now()
	if opened, err := b.store.OpenErrand(ctx, e.ID, now.Add(time.Duration(e.Days)*24*time.Hour)); err != nil || !opened {
		if err == nil {
			err = errors.New("it does not wait for its start any more")
		}
		return err
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

// sayInErrand sends text to errand e's person and adds it to the
// conversation.
func (b *App) sayInErrand(ctx context.Context, e store.Errand, text string) error {
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
// open, or nothing at all from someone Fridolin stopped listening to.
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
	if e, err := b.store.LiveErrandWith(ctx, contact.Contact); err == nil && e.State == store.ErrandOpen {
		if err := b.store.AddErrandLine(ctx, e.ID, store.ErrandLine{Sender: errand.FromThem, Text: text, At: m.At}, false); err != nil {
			log.Printf("errand %s: recording their message: %v", e.ID, err)
			return
		}
		b.auditErrand(e, "answered", fmt.Sprintf("%d characters", len([]rune(text))))
		b.errands.soon(e.ID, b.waits.errandGather, b.errandTurn)
		return
	}
	if !b.errands.forward(contact.Contact, time.Now()) {
		log.Printf("errand: %s wrote again; passed on %d times today already", logx.MaskJID(contact.Contact, "", ""), errandForwardsPerDay)
		return
	}
	target := ownerChat(b.cfgs.Get())
	if target == "" {
		return
	}
	if err := b.outbox.Question(ctx, contact.Brain, target, outboundmsg.ErrandForwardNotice(contact.Name, contact.Contact, text), b.cfgs.Get().MaxChunkSize); err != nil {
		log.Printf("errand: passing on a message from %s: %v", logx.MaskJID(contact.Contact, "", ""), err)
	}
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
	e, err := b.store.GetErrand(ctx, id)
	if err != nil || e.State != store.ErrandOpen {
		return
	}
	if time.Now().After(e.Deadline) {
		b.expireErrand(ctx, e, fmt.Sprintf("its %d days are over", e.Days))
		return
	}
	lines, err := b.store.ErrandLines(ctx, id)
	if err != nil || len(lines) == 0 || lines[len(lines)-1].Sender == errand.FromYou {
		return
	}
	if e.Sent >= errand.MaxMessages {
		b.expireErrand(ctx, e, fmt.Sprintf("I wrote its %d messages", errand.MaxMessages))
		return
	}
	s, ok := b.errandSettings(e.Brain)
	if !ok {
		if ended, _ := b.store.EndErrand(ctx, e.ID, store.ErrandStopped, "errands are off for its brain", time.Now()); ended {
			b.auditErrand(e, "ended", store.ErrandStopped+": errands are off for its brain")
		}
		return
	}
	ans, err := b.errandAsk(ctx, e, s, lines)
	if err != nil {
		log.Printf("errand %s: %v", e.ID, err)
		b.auditErrand(e, "answered", "no answer: "+textx.Truncate(err.Error(), 200))
		b.tellErrandOwner(ctx, e, outboundmsg.ErrandFailedNotice(e.Name, e.ID, err.Error()))
		return
	}
	switch ans.Action {
	case errand.Reply:
		if err := b.sayInErrand(ctx, e, ans.Text); err != nil {
			log.Printf("errand %s: sending: %v", e.ID, err)
		}
	case errand.AskOwner:
		if ans.Text != "" {
			if err := b.sayInErrand(ctx, e, ans.Text); err != nil {
				log.Printf("errand %s: sending: %v", e.ID, err)
			}
		}
		b.auditErrand(e, "asked the owner", textx.Truncate(ans.Question, 200))
		b.tellErrandOwner(ctx, e, outboundmsg.ErrandQuestionNotice(e.Name, e.ID, ans.Question))
	case errand.Done:
		if ans.Text != "" {
			if err := b.sayInErrand(ctx, e, ans.Text); err != nil {
				log.Printf("errand %s: sending: %v", e.ID, err)
			}
		}
		summary := ans.Summary
		if summary == "" {
			summary = "it came to its end."
		}
		if ended, _ := b.store.EndErrand(ctx, e.ID, store.ErrandDone, summary, time.Now()); !ended {
			return
		}
		b.auditErrand(e, "ended", store.ErrandDone)
		if ans.Stop {
			// They asked not to be written to: the next errand with them
			// asks the owner first, and what they send meanwhile is a
			// stranger's.
			if _, err := b.store.ForgetErrandContact(ctx, e.Contact); err != nil {
				log.Printf("errand %s: forgetting +%s: %v", e.ID, logx.MaskJID(e.Contact, "", ""), err)
			}
			b.auditErrand(e, "forgotten", "they asked not to be written to again")
		}
		b.tellErrandOwner(ctx, e, outboundmsg.ErrandDoneNotice(e.Name, e.ID, summary, ans.Stop))
	case errand.Abort:
		b.abortErrand(ctx, e, s, ans.Reason, lines)
	}
}

// abortErrand ends errand e because the person tried to make Fridolin do
// what it must not: they get the bridge's own message, the owner hears
// why, and Fridolin stops listening to them.
func (b *App) abortErrand(ctx context.Context, e store.Errand, s manifest.Errands, reason string, lines []store.ErrandLine) {
	if reason == "" {
		reason = "they tried to make me do what I must not."
	}
	if err := b.sayInErrand(ctx, e, outboundmsg.ErrandAbortMessage(strings.TrimSpace(s.OwnerName))); err != nil {
		log.Printf("errand %s: sending the end: %v", e.ID, err)
	}
	if ended, _ := b.store.EndErrand(ctx, e.ID, store.ErrandAborted, reason, time.Now()); !ended {
		return
	}
	contact := store.ErrandContact{Contact: e.Contact, Name: e.Name, Brain: e.Brain, State: store.ContactBlocked, Reason: textx.Truncate(reason, 300), UpdatedAt: time.Now()}
	if c, err := b.store.ErrandContactOf(ctx, e.Contact); err == nil {
		contact.LID = c.LID
	}
	if err := b.store.SaveErrandContact(ctx, contact); err != nil {
		log.Printf("errand %s: blocking +%s: %v", e.ID, logx.MaskJID(e.Contact, "", ""), err)
	}
	b.auditErrand(e, "blocked", textx.Truncate(reason, 200))
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i].Sender == errand.FromThem {
			last = lines[i].Text
			break
		}
	}
	b.tellErrandOwner(ctx, e, outboundmsg.ErrandAbortedNotice(e.Name, e.Contact, e.ID, reason, last))
}

// expireErrand ends errand e, whose time or messages ran out (why).
func (b *App) expireErrand(ctx context.Context, e store.Errand, why string) {
	if ended, _ := b.store.EndErrand(ctx, e.ID, store.ErrandExpired, why, time.Now()); !ended {
		return
	}
	b.auditErrand(e, "ended", store.ErrandExpired+": "+why)
	b.tellErrandOwner(ctx, e, outboundmsg.ErrandExpiredNotice(e.Name, e.ID, why))
}

// errandAsk has errand e's run answer: in Codex mode through the OpenAI
// connection, in Claude mode through `claude -p` with no tools at all.
func (b *App) errandAsk(ctx context.Context, e store.Errand, s manifest.Errands, lines []store.ErrandLine) (errand.Answer, error) {
	cfg := RuntimeConfig(b.cfgs)
	doc := errand.Errand{Owner: strings.TrimSpace(s.OwnerName), Name: e.Name, Goal: e.Goal, Brief: e.Brief, Now: time.Now().In(cfg.Location()), Deadline: e.Deadline}
	for _, l := range lines {
		doc.Lines = append(doc.Lines, errand.Line{From: l.Sender, Text: l.Text, At: l.At})
	}
	ctx, cancel := context.WithTimeout(ctx, errandRunTimeout)
	defer cancel()
	var out string
	var err error
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
			b.expireErrand(ctx, e, fmt.Sprintf("its %d days are over", e.Days))
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

// stopErrand ends errand id of brain without a word to the person.
func (b *App) stopErrand(ctx context.Context, brain, id string) error {
	e, err := b.store.GetErrand(ctx, id)
	if err != nil || e.Brain != brain {
		return fmt.Errorf("no errand %s of this brain", id)
	}
	ended, err := b.store.EndErrand(ctx, e.ID, store.ErrandStopped, "the brain ended it", time.Now())
	if err != nil {
		return err
	}
	if !ended {
		return fmt.Errorf("errand %s ended already (%s)", id, e.State)
	}
	if e.Approval != "" && e.State == store.ErrandAsking {
		_, _ = b.store.DecideApproval(ctx, e.Approval, store.ApprovalDenied, "", "bridge", time.Now())
	}
	b.auditErrand(e, "ended", store.ErrandStopped)
	return nil
}

// unblockErrandContact lets Fridolin hear number again, someone it
// stopped listening to: it forgets them, so that writing to them again
// asks the owner first.
func (b *App) unblockErrandContact(ctx context.Context, number string) (bool, error) {
	c, err := b.store.ErrandContactOf(ctx, number)
	if errors.Is(err, store.ErrNoErrand) || (err == nil && c.State != store.ContactBlocked) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := b.store.ForgetErrandContact(ctx, c.Contact); err != nil {
		return false, err
	}
	return true, nil
}
