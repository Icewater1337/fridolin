package app

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/nodes"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/store"
)

// The core moves itself from the owner's chat (#187 §3, §5; docs/CORE.md,
// "Moving the core from WhatsApp"). !movecore <node> runs the move's
// pre-flight (nodes.MoveCheck), answers with a report and, when nothing
// blocks the move, a poll: Move now / Cancel. The poll is an approval
// row of the question kind, so only the owner's vote counts, the
// dashboard cannot answer it (a stolen dashboard session alone cannot
// move the core), and no vote within coreMovePollTTL is a no. "Move now"
// starts the same move as `fridolin nodes move` (nodes.StartMove); the
// owner hears when the core stops, and the core that runs afterwards,
// on either machine, says how it ended (coreMoveEnded). Then a second
// poll asks whether the machine the core left keeps its copy of the
// host secrets; no vote within coreSecretsPollTTL forgets them.
//
// !movecore alone lists the core hosts and what keeps the core from
// moving to each. No brain has a tool for any of it.

// The Notify of the two polls' approval rows (deliverAnswer).
const (
	coreMoveNotify    = "fridolin:coremove"
	coreSecretsNotify = "fridolin:coresecrets"
)

// coreMoveAsksPerHour is how often !movecore <node> answers in an hour.
const coreMoveAsksPerHour = 3

// How long the move poll and the keep/forget poll wait for a vote, and
// the move's timings (the defaults of nodes when zero). Variables, so
// tests need not wait.
var (
	coreMovePollTTL    = 30 * time.Minute
	coreSecretsPollTTL = 24 * time.Hour
	coreMoveOptions    nodes.MoveOptions
	// coreMoveSiteTries and coreMoveSiteWait: how often, and how far
	// apart, the core that runs after a move asks whether the site
	// answers at its domain.
	coreMoveSiteTries = 6
	coreMoveSiteWait  = 30 * time.Second
)

// coreMoveAsk is the one !movecore that is checked or waits for its vote.
type coreMoveAsk struct {
	mu   sync.Mutex
	busy string // "checking", or the pending approval's id
}

func (a *coreMoveAsk) take() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy != "" {
		return false
	}
	a.busy = "checking"
	return true
}

func (a *coreMoveAsk) set(v string) {
	a.mu.Lock()
	a.busy = v
	a.mu.Unlock()
}

// done ends id's ask ("" for the one being checked).
func (a *coreMoveAsk) done(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id == "" || a.busy == id {
		a.busy = ""
	}
}

// moveCoreCommand answers !movecore [node] from the owner.
func (b *App) moveCoreCommand(args []string, record func(id, outcome string)) string {
	switch {
	case !b.nodesEnabled:
		record("", "refused: nodes are off")
		return outboundmsg.NodesOffNotice
	case len(args) > 1:
		return outboundmsg.CoreMoveUsage
	case b.locked():
		record("", "refused: lockdown")
		return outboundmsg.CoreMoveLockdownNotice
	case len(args) == 0:
		record("", "listed")
		return b.coreMoveTargets()
	}
	dst, err := b.nodes.FindNode(args[0])
	switch {
	case errors.Is(err, nodes.ErrUnknownNode):
		record(args[0], "refused: unknown node")
		return outboundmsg.NodeUnknownReply(args[0])
	case err != nil:
		record(args[0], "refused: "+err.Error())
		return outboundmsg.CoreMoveNotStarted(args[0], err.Error())
	}
	if b.nodes.MoveActive() != "" || !b.coreMove.take() {
		record(dst.ID, "refused: a move runs")
		return outboundmsg.CoreMoveRunningNotice
	}
	if !b.ownerAsks.allowN("!movecore", coreMoveAsksPerHour, time.Now()) {
		b.coreMove.done("")
		record(dst.ID, "refused: rate limit")
		return outboundmsg.OwnerCommandRateLimited("!movecore", coreMoveAsksPerHour)
	}
	record(dst.ID, "checking")
	go b.coreMoveCheck(dst.ID, dst.Name)
	return outboundmsg.CoreMoveChecking(dst.Name)
}

// coreMoveSource finds the core host that runs the core, and lists them
// all.
func (b *App) coreMoveSource(ctx context.Context) (*nodes.CoreHostState, []nodes.CoreHostState) {
	hosts, err := b.nodes.CoreHosts(ctx)
	if err != nil {
		log.Printf("core move: listing the core hosts: %v", err)
	}
	for i := range hosts {
		if hosts[i].RunsCore {
			return &hosts[i], hosts
		}
	}
	return nil, hosts
}

// coreMoveTargets answers !movecore alone.
func (b *App) coreMoveTargets() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	src, hosts := b.coreMoveSource(ctx)
	runsOn := ""
	if src != nil {
		runsOn = src.Name
	}
	var list []outboundmsg.CoreMoveHost
	for _, h := range hosts {
		if src != nil && h.ID == src.ID {
			continue
		}
		e := outboundmsg.CoreMoveHost{Name: h.Name, ID: h.ID, Online: h.Online, Pending: h.Pending}
		switch {
		case !h.Approved && !h.Pending:
			e.Problem = "its device or its core-host is not approved"
		case !h.Approved || !h.Online:
		case h.Problem != "":
			e.Problem = h.Problem
		case src != nil:
			rep, err := b.nodes.MoveCheck(ctx, src.ID, h.ID)
			switch {
			case err != nil:
				e.Problem = err.Error()
			case len(rep.Blocking) > 0:
				e.Problem = rep.Blocking[0]
			}
		}
		list = append(list, e)
	}
	return outboundmsg.CoreMoveTargets(runsOn, list)
}

// coreMoveCheck runs the move's pre-flight to node dst and sends the
// owner the report and, when nothing blocks it, the poll.
func (b *App) coreMoveCheck(dst, dstName string) {
	asked := false
	defer func() {
		if !asked {
			b.coreMove.done("")
		}
	}()
	cfg := b.cfgs.Get()
	target := ownerChat(cfg)
	if target == "" || b.outbox == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	say := func(text string) {
		if err := b.outbox.Text(ctx, target, text, cfg.MaxChunkSize); err != nil {
			log.Printf("core move: telling the owner: %v", err)
		}
	}
	src, _ := b.coreMoveSource(ctx)
	switch {
	case src == nil:
		say(outboundmsg.CoreMoveNoSource)
		return
	case src.ID == dst:
		say(outboundmsg.CoreMoveHereAlready(dstName))
		return
	}
	rep, err := b.nodes.MoveCheck(ctx, src.ID, dst)
	if err != nil {
		say(outboundmsg.CoreMoveNotStarted(dstName, err.Error()))
		return
	}
	text := outboundmsg.CoreMoveReport(coreMoveCheckText(rep))
	say(text)
	if len(rep.Blocking) > 0 {
		b.audit.Append(audit.Entry{Kind: audit.CoreMoveRequested, Chat: target, ID: dst, Detail: rep.Source.Name + " → " + rep.Target.Name, Outcome: "blocked: " + rep.Blocking[0]})
		return
	}
	now := time.Now()
	question := outboundmsg.CoreMoveQuestion(rep.Source.Name, rep.Target.Name)
	a := store.Approval{Kind: questionKind, ID: "a" + randomToken(9), Run: "movecore", Thread: target, Chat: target,
		Notify: coreMoveNotify, Risk: "irreversible", Action: question, Details: text, Step: src.ID + " " + dst,
		Options:   []string{outboundmsg.CoreMoveNow, outboundmsg.CoreMoveCancel},
		CreatedAt: now, ExpiresAt: now.Add(coreMovePollTTL)}
	if err := b.store.AddApproval(ctx, a); err != nil {
		log.Printf("core move: recording the poll: %v", err)
		say(outboundmsg.CoreMoveNotStarted(dstName, "the poll could not be recorded"))
		return
	}
	b.coreMove.set(a.ID)
	asked = true
	if err := b.outbox.Poll(ctx, target, question, a.Options, approvalRefPrefix+a.ID); err != nil {
		_, _ = b.store.DecideApproval(ctx, a.ID, store.ApprovalDenied, "", "bridge", now)
		b.coreMove.done(a.ID)
		log.Printf("core move: sending the poll: %v", err)
		return
	}
	b.audit.Append(audit.Entry{Kind: audit.CoreMoveRequested, Chat: target, ID: dst, Detail: rep.Source.Name + " → " + rep.Target.Name + " (approval " + a.ID + ")", Outcome: "pending"})
}

// coreMoveCheckText is rep as the owner reads it.
func coreMoveCheckText(rep nodes.MoveReport) outboundmsg.CoreMoveCheck {
	c := outboundmsg.CoreMoveCheck{Source: rep.Source.Name, Target: rep.Target.Name, TargetID: rep.Target.ID,
		TargetOS: rep.Target.OS, TargetArch: rep.Target.Arch, Box: rep.Target.Box, Fingerprint: rep.Target.Fingerprint,
		Outage: rep.Outage, NotOnTarget: rep.MachineBound, Warnings: rep.Warnings, Blocking: rep.Blocking,
		PollMinutes: int(coreMovePollTTL / time.Minute)}
	if rep.Site != nil {
		c.Site = rep.Site.After
	}
	return c
}

// coreMoveDecided takes the owner's answer to the move poll: "Move now"
// starts the move, anything else, or no vote in time, leaves the core
// where it is.
func (b *App) coreMoveDecided(a store.Approval) {
	b.coreMove.done(a.ID)
	cfg := b.cfgs.Get()
	ctx := context.Background()
	say := func(text string) {
		if err := b.outbox.Text(ctx, a.Chat, text, cfg.MaxChunkSize); err != nil {
			log.Printf("core move: telling the owner: %v", err)
		}
	}
	src, dst, _ := strings.Cut(a.Step, " ")
	dstName := dst
	if n, err := b.nodes.Registry().Get(dst); err == nil {
		dstName = n.Name
	}
	entry := audit.Entry{Kind: audit.CoreMoveApproved, Chat: a.Chat, ID: a.ID, By: a.DecidedBy, Detail: src + " → " + dst}
	switch {
	case a.Status == store.ApprovalExpired:
		entry.Outcome = "expired"
		b.audit.Append(entry)
		say(outboundmsg.CoreMoveCancelled(dstName, true))
		return
	case a.Status != store.ApprovalAnswered || a.Answer != outboundmsg.CoreMoveNow:
		entry.Outcome = "cancelled"
		b.audit.Append(entry)
		say(outboundmsg.CoreMoveCancelled(dstName, false))
		return
	case b.locked():
		entry.Outcome = "refused: lockdown"
		b.audit.Append(entry)
		say(outboundmsg.CoreMoveLockdownNotice)
		return
	}
	o := coreMoveOptions
	o.Approval = a.ID
	rec, err := b.nodes.StartMove(ctx, src, dst, o)
	if err != nil {
		why := err.Error()
		if len(rec.Report.Blocking) > 0 {
			why = rec.Report.Blocking[0]
		}
		entry.Outcome = "not started: " + why
		b.audit.Append(entry)
		say(outboundmsg.CoreMoveNotStarted(dstName, why))
		return
	}
	entry.Outcome = "started " + rec.ID
	b.audit.Append(entry)
	say(outboundmsg.CoreMoveStarted(rec.SourceName, rec.TargetName, rec.ID))
}

// coreMoveStopping tells the owner that the source is told to stop the
// core now: the time offline begins.
func (b *App) coreMoveStopping(r nodes.MoveRecord) {
	b.tellOwner(outboundmsg.CoreMoveStopping(r.SourceName, r.TargetName, r.Report.Outage))
}

// tellOwner sends text into the owner's direct chat.
func (b *App) tellOwner(text string) {
	cfg := b.cfgs.Get()
	target := ownerChat(cfg)
	if target == "" || b.outbox == nil {
		return
	}
	if err := b.outbox.Text(context.Background(), target, text, cfg.MaxChunkSize); err != nil {
		log.Printf("core move: telling the owner: %v", err)
	}
}

// coreMoveEnded tells the owner, in their direct chat, how a core move
// ended: the core that runs afterwards says it, on either machine. Then
// it asks whether the machine that does not run the core keeps the host
// secrets: the source after a move, the target after one that failed
// once the secrets had gone there. A site with a domain is asked whether
// it answers from where the core runs now.
func (b *App) coreMoveEnded(r nodes.MoveRecord) {
	b.audit.Append(audit.Entry{Kind: audit.CoreMoveEnded, ID: r.ID, Detail: r.SourceName + " → " + r.TargetName + ": " + r.Detail, Outcome: r.State})
	var notOnTarget []string
	if r.State == nodes.MoveDone {
		notOnTarget = r.Report.MachineBound
	}
	b.tellOwner(outboundmsg.CoreMoveEnded(r.ID, r.SourceName, r.TargetName, r.State, r.Detail, notOnTarget))
	left, leftName := "", ""
	switch {
	case r.State == nodes.MoveDone:
		left, leftName = r.Source, r.SourceName
	case secretsSent(r):
		left, leftName = r.Target, r.TargetName
	}
	if left != "" {
		b.askKeepSecrets(r.ID, left, leftName)
	}
	if r.State == nodes.MoveDone && b.site != nil {
		if s := b.site.Settings(); s.Enabled && s.Domain != "" {
			go b.coreMoveSite(s.Domain, r.TargetName)
		}
	}
}

// secretsSent reports whether r's host secrets went to its target.
func secretsSent(r nodes.MoveRecord) bool {
	for _, s := range r.Steps {
		if s.Step == "secrets" {
			return true
		}
	}
	return false
}

// coreMoveSite asks, a few times, whether the site answers at domain
// once the core runs on target, and says what it found.
func (b *App) coreMoveSite(domain, target string) {
	var last string
	for i := range coreMoveSiteTries {
		if i > 0 {
			time.Sleep(coreMoveSiteWait)
		}
		c := b.site.CheckDomain(context.Background())
		if c.OK {
			b.tellOwner(outboundmsg.CoreMoveSiteAnswers(domain, target))
			return
		}
		last = c.Error
	}
	b.tellOwner(outboundmsg.CoreMoveSiteDown(domain, target, last))
}

// askKeepSecrets asks the owner whether node keeps its copy of the host
// secrets after move.
func (b *App) askKeepSecrets(move, node, name string) {
	cfg := b.cfgs.Get()
	target := ownerChat(cfg)
	if target == "" || b.outbox == nil {
		return
	}
	ctx := context.Background()
	now := time.Now()
	question := outboundmsg.CoreSecretsQuestion(name, int(coreSecretsPollTTL/time.Hour))
	a := store.Approval{Kind: questionKind, ID: "a" + randomToken(9), Run: move, Thread: target, Chat: target,
		Notify: coreSecretsNotify, Risk: "write", Action: question, Step: node,
		Options:   []string{outboundmsg.CoreSecretsKeep(name), outboundmsg.CoreSecretsForget(name)},
		CreatedAt: now, ExpiresAt: now.Add(coreSecretsPollTTL)}
	if err := b.store.AddApproval(ctx, a); err != nil {
		log.Printf("core move %s: recording the keep/forget poll: %v", move, err)
		return
	}
	if err := b.outbox.Poll(ctx, target, question, a.Options, approvalRefPrefix+a.ID); err != nil {
		_, _ = b.store.DecideApproval(ctx, a.ID, store.ApprovalDenied, "", "bridge", now)
		log.Printf("core move %s: sending the keep/forget poll: %v", move, err)
	}
}

// coreSecretsDecided takes the owner's answer to the keep/forget poll:
// keep leaves the secrets there; forget, or no vote in time, asks the
// machine to remove what a move brought it (core.secrets.forget).
func (b *App) coreSecretsDecided(a store.Approval) {
	node := a.Step
	name := node
	if n, err := b.nodes.Registry().Get(node); err == nil {
		name = n.Name
	}
	expired := a.Status == store.ApprovalExpired
	keep := a.Status == store.ApprovalAnswered && len(a.Options) > 0 && a.Answer == a.Options[0]
	entry := audit.Entry{Kind: audit.CoreSecrets, Chat: a.Chat, ID: node, By: a.DecidedBy, Detail: "move " + a.Run}
	if keep {
		entry.Outcome = "kept"
		b.audit.Append(entry)
		b.tellOwner(outboundmsg.CoreSecretsKept(name))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	f := b.nodes.ForgetCoreSecrets(ctx, node)
	entry.Outcome = "forgotten"
	if f.Error != "" {
		entry.Outcome = "forget failed: " + f.Error
	}
	b.audit.Append(entry)
	b.tellOwner(outboundmsg.CoreSecretsForgotten(name, f.Forgotten, f.Error, expired))
}
