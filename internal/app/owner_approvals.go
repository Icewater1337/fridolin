package app

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/registry"
	"github.com/Icewater1337/fridolin/internal/skill"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/textx"
)

// Everything that waits for the owner's approval is one list
// (pendingApprovals): a node's device, what a node offers (core-host
// among it), a brain's scheduled agent, its changed brain.json, its new
// or changed skill, and a run's request_approval. The owner hears of each
// once in their direct chat, in plain words (alertPendingApprovals), and
// answers with !approve or !deny alone: with one thing waiting that one,
// with several a poll that picks one (ownerPickVote). `!approve <ref>`
// still names one.
//
// Safety: only the owner, in their direct chat (ownerCommand), and only
// the owner's vote in the poll. Something the owner did not start (a
// node that joined with a code neither !join nor the dashboard made, a
// file a brain wrote into its own folder), something older than
// approvalFreshFor, and something the owner was never shown, asks for a
// second !approve within confirmWindow first. Every decision goes into
// the audit log.

const (
	// confirmWindow is how long the second !approve may take.
	confirmWindow = 2 * time.Minute
	// approvalFreshFor is how long something waits before !approve alone
	// no longer approves it without asking again.
	approvalFreshFor = 24 * time.Hour
	// ownerPickPrefix is the ref of the poll that picks one; the token
	// after it names the pick.
	ownerPickPrefix = "owner-pick:"
)

// The kinds of pending things.
const (
	pendingNode     = "node"
	pendingRun      = "run"
	pendingSchedule = "schedule"
	pendingBrain    = "brain"
	pendingSkill    = "skill"
	// pendingCoreMove is !movecore's poll, Move now / Cancel, and
	// pendingCoreSecrets the poll after a move, whether the machine the
	// core left keeps the host secrets (coremove_runtime.go).
	pendingCoreMove    = "coremove"
	pendingCoreSecrets = "coresecrets"
)

// pendingItem is one thing that waits for the owner.
type pendingItem struct {
	msg outboundmsg.OwnerApproval
	// key names it as it is now: a change to it is another key, which
	// is told anew.
	key  string
	kind string
	// since is when it started, expires when !approve alone no longer
	// approves it; zero for never.
	since, expires time.Time
	// silent ones have an alert of their own (a run's poll).
	silent bool
	// dismissible ones stay as they are when turned down: deny only
	// stops asking about this version.
	dismissible bool
	approve     func(ctx context.Context, by string) (string, error)
	deny        func(ctx context.Context, by string) (string, error)
}

// ownerApprovals is the App's state for them.
type ownerApprovals struct {
	mu     sync.Mutex
	loaded bool
	state  ownerApprovalsFile
	// confirm is the one that waits for a second !approve.
	confirm *ownerConfirm
	// picks are the polls sent, by token.
	picks map[string]ownerPick
}

// ownerApprovalsFile is what owner-approvals.json keeps across restarts:
// the keys the owner was told of and those they turned down, with when.
type ownerApprovalsFile struct {
	Alerted   map[string]time.Time `json:"alerted"`
	Dismissed map[string]time.Time `json:"dismissed"`
}

type ownerConfirm struct {
	key string
	at  time.Time
}

type ownerPick struct {
	approve bool
	keys    map[string]string // option → key
	at      time.Time
}

// load reads the file once; the caller holds mu.
func (o *ownerApprovals) load(path string) {
	if o.loaded {
		return
	}
	o.loaded = true
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &o.state); err != nil {
			log.Printf("approvals: %s does not read, starting afresh: %v", filepath.Base(path), err)
		}
	}
	if o.state.Alerted == nil {
		o.state.Alerted = map[string]time.Time{}
	}
	if o.state.Dismissed == nil {
		o.state.Dismissed = map[string]time.Time{}
	}
}

// save writes the file; the caller holds mu.
func (o *ownerApprovals) save(path string) {
	data, err := json.MarshalIndent(o.state, "", "  ")
	if err == nil {
		if _, err = config.EnsureDir(filepath.Dir(path)); err == nil {
			err = fsx.WriteFileAtomic(path, data, 0o600, false)
		}
	}
	if err != nil {
		log.Printf("approvals: writing %s: %v", filepath.Base(path), err)
	}
}

func (b *App) ownerApprovalsPath() string { return b.cfgs.Boot().Layout().OwnerApprovals() }

// hashOf names v's content in a key.
func hashOf(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:6])
}

// pendingApprovals is everything that waits for the owner now, the ones
// they turned down included (all), oldest first.
func (b *App) pendingApprovals(ctx context.Context) []pendingItem {
	var out []pendingItem
	out = append(out, b.pendingNodes(ctx)...)
	out = append(out, b.pendingRuns(ctx)...)
	out = append(out, b.pendingCoreQuestions(ctx)...)
	out = append(out, b.pendingBrainFiles(ctx)...)
	slices.SortStableFunc(out, func(x, y pendingItem) int {
		return cmp.Or(x.since.Compare(y.since), cmp.Compare(x.key, y.key))
	})
	return out
}

// waiting is pendingApprovals less what the owner turned down.
func (b *App) waiting(ctx context.Context) []pendingItem {
	all := b.pendingApprovals(ctx)
	b.approvals.mu.Lock()
	defer b.approvals.mu.Unlock()
	b.approvals.load(b.ownerApprovalsPath())
	return slices.DeleteFunc(all, func(it pendingItem) bool {
		_, gone := b.approvals.state.Dismissed[it.key]
		return gone
	})
}

// --- nodes

func (b *App) pendingNodes(ctx context.Context) []pendingItem {
	if b.nodes == nil {
		return nil
	}
	list, err := b.nodes.Registry().List()
	if err != nil {
		log.Printf("approvals: reading the node registry: %v", err)
	}
	var out []pendingItem
	for _, n := range list {
		if it, ok := b.nodeItem(ctx, n); ok {
			out = append(out, it)
		}
	}
	return out
}

// nodeItem is what node n waits for, if anything.
func (b *App) nodeItem(ctx context.Context, n *nodes.Node) (pendingItem, bool) {
	pendingDevice := n.Device == nodeproto.DevicePending
	if !pendingDevice && (n.Device != nodeproto.DeviceApproved || n.Commands() != nodeproto.CommandsPending) {
		return pendingItem{}, false
	}
	os := osName(n.OS)
	it := pendingItem{kind: pendingNode, key: "node:" + n.ID + ":" + n.Device + ":" + hashOf(n.Declared)}
	m := &it.msg
	m.Ref = n.ID
	m.Label = "*" + n.Name + "*"
	m.Facts = append(m.Facts, "From: "+b.nodeWhere(ctx, n.Address))
	coreHost := n.Declared.CoreHost != nil && !nodeproto.Covers(n.Approved, nodeproto.Capabilities{CoreHost: n.Declared.CoreHost})
	fride := nodes.FridePending(n)
	if pendingDevice {
		it.since = n.JoinedAt
		m.Subject = fmt.Sprintf("*%s* (%s) wants to join as a node", n.Name, os)
		m.Option = fmt.Sprintf("%s (%s) joins as a node", n.Name, os)
		if coreHost {
			m.Subject += ", holding all of Fridolin's secrets (core-host)"
			m.Option += ", core-host"
		}
		switch n.JoinedVia {
		case nodes.JoinedFromChat:
			m.Facts = append(m.Facts, "Started: with a join code you asked for with !join")
		case nodes.JoinedFromDashboard:
			m.Facts = append(m.Facts, "Started: with a join code from the dashboard (or fridolin nodes join-code)")
		default:
			m.Unexpected = "its join code came neither from your !join nor from the dashboard."
		}
	} else {
		if n.DeclaredAt != nil {
			it.since = *n.DeclaredAt
		}
		m.Unexpected = "the machine asked for this itself; nobody asked from WhatsApp or the dashboard."
		switch {
		case coreHost:
			m.Subject = fmt.Sprintf("*%s* (%s) asks to hold all of Fridolin's secrets (core-host)", n.Name, os)
			m.Option = fmt.Sprintf("%s holds all secrets (core-host)", n.Name)
		case fride:
			m.Subject = fmt.Sprintf("*%s* (%s) asks to let FrIDE's agents run anything on it (fride)", n.Name, os)
			m.Option = fmt.Sprintf("%s runs FrIDE (fride)", n.Name)
		case n.CommandsApprovedAt != nil:
			m.Subject = fmt.Sprintf("*%s* (%s) asks to do more than you approved", n.Name, os)
			m.Option = fmt.Sprintf("%s does more than approved", n.Name)
		default:
			m.Subject = fmt.Sprintf("*%s* (%s) asks to serve the core", n.Name, os)
			m.Option = fmt.Sprintf("%s serves the core", n.Name)
		}
	}
	if coreHost {
		m.Note = nodeproto.CoreHostApproval
		m.Facts = append(m.Facts, "Device key: `"+nodeproto.Fingerprint(n.PublicKey)+"`")
		if len(n.Declared.CoreHost.SendTo) > 0 {
			var to []string
			for _, id := range n.Declared.CoreHost.SendTo {
				name := id
				if t, err := b.nodes.Registry().Get(id); err == nil {
					name = t.Name
				}
				to = append(to, name)
			}
			m.Facts = append(m.Facts, "Sends secrets only to: "+strings.Join(to, ", "))
		}
	}
	if fride {
		m.Note = strings.TrimSpace(m.Note + "\n\n" + nodeproto.FrideApproval)
	}
	m.When = it.since
	m.Allows = n.Declared.Plain()
	if !it.since.IsZero() {
		it.expires = it.since.Add(approvalFreshFor)
	}
	id := n.ID
	it.approve = func(context.Context, string) (string, error) {
		got, err := b.nodes.ApproveDevice(id)
		if err == nil {
			got, err = b.nodes.ApproveCommands(id)
		}
		if err != nil {
			return "", err
		}
		reply := outboundmsg.NodeApprovedReply(got.Name, got.ID, got.Effective().Plain())
		if coreHost && got.Effective().CoreHost != nil {
			reply += "\n\n" + outboundmsg.NodeCoreHostApprovedNote(nodeproto.CoreHostApproval)
		}
		if fride && got.Effective().Fride != nil {
			reply += "\n\n" + outboundmsg.NodeFrideApprovedNote(nodeproto.FrideApproval)
		}
		return reply, nil
	}
	if pendingDevice {
		it.deny = func(context.Context, string) (string, error) {
			got, err := b.nodes.Revoke(id)
			if err != nil {
				return "", err
			}
			return outboundmsg.NodeRevokedReply(got.Name, got.ID), nil
		}
	} else {
		// A device approved before keeps what was approved of it.
		it.dismissible = true
		it.deny = func(context.Context, string) (string, error) {
			return outboundmsg.DeniedReply(m.Label+"'s request", "It keeps serving only what you approved before. `!revoke "+id+"` shuts it out altogether."), nil
		}
	}
	return it, true
}

// nodeWhere says where a node connected from, address being its
// ip:port: the device and user the tailnet names, when the core is on
// it, or what kind of network the address is on. Nothing is looked up
// outside this machine.
func (b *App) nodeWhere(ctx context.Context, address string) string {
	if address == "" {
		return "not known"
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	b.core.mu.Lock()
	tn := b.core.node
	b.core.mu.Unlock()
	if tn != nil {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if who, err := tn.WhoIs(ctx, address); err == nil && who.Node != "" {
			s := who.Node
			if who.LoginName != "" {
				s += " (" + who.LoginName + ")"
			}
			return s + " on your tailnet, " + host
		}
	}
	ip := net.ParseIP(host)
	switch {
	case ip == nil:
		return host
	case ip.IsLoopback():
		return "this machine itself, " + host
	case tailnetRange.Contains(ip):
		return "your tailnet, " + host
	case ip.IsPrivate() || ip.IsLinkLocalUnicast():
		return "your local network, " + host
	}
	return "the internet, " + host
}

// tailnetRange is Tailscale's address range (100.64.0.0/10).
var tailnetRange = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func osName(goos string) string {
	switch goos {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	case "":
		return "unknown OS"
	}
	return goos
}

// --- runs

// pendingRuns are the runs' request_approval that wait. Each has its own
// poll, Yes / No / Show me the draft, which tells it; !approve can
// answer it too.
func (b *App) pendingRuns(ctx context.Context) []pendingItem {
	if b.store == nil {
		return nil
	}
	pending, _, err := b.store.Approvals(ctx, 0)
	if err != nil {
		log.Printf("approvals: %v", err)
		return nil
	}
	var out []pendingItem
	for _, a := range pending {
		if a.Kind != approvalKind {
			continue
		}
		name := "Fridolin"
		if br := b.brainMgr.Get(a.Brain); br != nil {
			name = br.Manifest().Name
		}
		action := textx.Truncate(a.Action, 200)
		it := pendingItem{kind: pendingRun, key: "run:" + a.ID, since: a.CreatedAt, silent: true}
		it.msg = outboundmsg.OwnerApproval{
			Subject: fmt.Sprintf("*%s* asks to: %s", name, action),
			Label:   "“" + action + "”",
			Option:  textx.Truncate(name+": "+a.Action, 90),
			When:    a.CreatedAt, Ref: a.ID,
		}
		if a.Risk == string(skill.RiskIrreversible) {
			it.msg.Facts = append(it.msg.Facts, "It cannot be undone.")
		}
		if !b.ownerThread(a.Thread) {
			it.msg.Unexpected = "a run in another chat asked, not one you started here."
		}
		id := a.ID
		decide := func(status, by string) (string, error) {
			b.audit.Append(audit.Entry{Kind: audit.ApprovalVote, Thread: a.Thread, Chat: a.Chat, Brain: a.Brain, Run: a.Run, ID: id, Risk: a.Risk, By: by, Detail: "chat command", Outcome: status})
			if got, ok := b.decide(ctx, id, status, "", by, answerAsMessage); !ok {
				return "", fmt.Errorf("it is %s already", got.Status)
			}
			return "", nil
		}
		it.approve = func(_ context.Context, by string) (string, error) {
			if _, err := decide(store.ApprovalApproved, by); err != nil {
				return "", err
			}
			return outboundmsg.ApprovedReply(it.msg.Label, "*"+name+"* hears so and goes ahead."), nil
		}
		it.deny = func(_ context.Context, by string) (string, error) {
			if _, err := decide(store.ApprovalDenied, by); err != nil {
				return "", err
			}
			return outboundmsg.DeniedReply(it.msg.Label, "*"+name+"* hears so and does not do it."), nil
		}
		out = append(out, it)
	}
	return out
}

// pendingCoreQuestions are the core move's polls that wait: !movecore's
// Move now / Cancel, and keep or forget the host secrets after a move.
// Each has its own poll, which tells it; !approve answers it with the
// first option (move, keep), !deny with the second (cancel, forget).
func (b *App) pendingCoreQuestions(ctx context.Context) []pendingItem {
	if b.store == nil || b.nodes == nil {
		return nil
	}
	pending, _, err := b.store.Approvals(ctx, 0)
	if err != nil {
		log.Printf("approvals: %v", err)
		return nil
	}
	nodeName := func(id string) string {
		if n, err := b.nodes.Registry().Get(id); err == nil {
			return n.Name
		}
		return id
	}
	var out []pendingItem
	for _, a := range pending {
		if a.Kind != questionKind || len(a.Options) != 2 {
			continue
		}
		it := pendingItem{key: "question:" + a.ID, since: a.CreatedAt, silent: true}
		m := &it.msg
		m.When, m.Ref = a.CreatedAt, a.ID
		// What follows the answer says what came of it (coreMoveDecided,
		// coreSecretsDecided).
		switch a.Notify {
		case coreMoveNotify:
			src, dst, _ := strings.Cut(a.Step, " ")
			srcName, dstName := nodeName(src), nodeName(dst)
			it.kind = pendingCoreMove
			m.Subject = fmt.Sprintf("You asked to move the core from *%s* to *%s*", srcName, dstName)
			m.Label = fmt.Sprintf("the core's move from *%s* to *%s*", srcName, dstName)
			m.Option = textx.Truncate("Move the core to "+dstName, 90)
			m.Facts = []string{"Started: with your !movecore"}
			m.Allows = []string{fmt.Sprintf("stop the core on %s and start it on %s, with all of Fridolin's secrets", srcName, dstName)}
		case coreSecretsNotify:
			name := nodeName(a.Step)
			it.kind = pendingCoreSecrets
			m.Subject = fmt.Sprintf("*%s* still holds Fridolin's secrets after the core left it", name)
			m.Label = fmt.Sprintf("keeping the host secrets on *%s*", name)
			m.Option = textx.Truncate("Keep the secrets on "+name, 90)
			m.Facts = []string{"Started: by the core move " + a.Run}
			m.Allows = []string{"keep the host secrets, the secret files and the core's data dir a move brought it, so the next move is faster"}
		default:
			continue
		}
		id, keep, drop := a.ID, a.Options[0], a.Options[1]
		answer := func(ctx context.Context, option, by string) error {
			if got, ok := b.decide(ctx, id, store.ApprovalAnswered, option, by, answerAsMessage); !ok {
				return fmt.Errorf("it is %s already", got.Status)
			}
			return nil
		}
		it.approve = func(ctx context.Context, by string) (string, error) {
			if err := answer(ctx, keep, by); err != nil {
				return "", err
			}
			return outboundmsg.ApprovedReply(m.Label, ""), nil
		}
		it.deny = func(ctx context.Context, by string) (string, error) {
			if err := answer(ctx, drop, by); err != nil {
				return "", err
			}
			return outboundmsg.DeniedReply(m.Label, ""), nil
		}
		out = append(out, it)
	}
	return out
}

// ownerThread reports whether thread is the owner's own direct chat.
func (b *App) ownerThread(thread string) bool {
	if thread == "" || chat.IsGroupJID(thread) {
		return false
	}
	return b.isOwner(thread)
}

// --- brain files: scheduled agents, brain.json, skills

// brainFileUnexpected flags what a brain wrote into its own folder: the
// dashboard writes the approved copy at once and asks nothing.
const brainFileUnexpected = "it was written into the brain's folder, not made in the dashboard."

func (b *App) pendingBrainFiles(ctx context.Context) []pendingItem {
	if b.brainMgr == nil || b.registry == nil {
		return nil
	}
	cfg := b.cfgs.Get()
	var out []pendingItem
	drifted := map[string]bool{}
	_, issues := ResolveBrains(cfg)
	for _, is := range issues {
		if is.Kind == registry.IssueDrift && is.ID != "" {
			drifted[is.ID] = true
		}
	}
	for _, br := range b.brainMgr.Brains() {
		id, dir, name := br.ID(), br.Dir(), br.Manifest().Name
		mtime := func(rel string) time.Time {
			if st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err == nil {
				return st.ModTime()
			}
			return time.Time{}
		}
		for _, r := range b.registry.ScheduleReviews(cfg, id) {
			if r.Status == manifest.ReviewInvalid {
				continue
			}
			out = append(out, b.scheduleItem(id, name, r, mtime(r.Source)))
		}
		if drifted[id] {
			if disk, err := manifest.Read(dir); err == nil {
				out = append(out, b.brainItem(id, name, *disk, br.Manifest(), mtime("brain.json")))
			}
		}
		skills, err := b.brainSkills(ctx, id)
		if err != nil {
			continue
		}
		for _, s := range skills {
			if s.Status == skillNew || s.Status == skillChanged {
				out = append(out, b.skillItem(id, name, s, mtime(s.Path)))
			}
		}
	}
	return out
}

func freshness(it *pendingItem, since time.Time) {
	it.since, it.msg.When = since, since
	if !since.IsZero() {
		it.expires = since.Add(approvalFreshFor)
	}
}

func (b *App) scheduleItem(brain, brainName string, r manifest.ScheduleReview, since time.Time) pendingItem {
	s := manifest.Schedule{Name: r.Name}
	if r.Disk != nil {
		s = *r.Disk
	}
	it := pendingItem{kind: pendingSchedule, key: "schedule:" + brain + "/" + r.Name + ":" + r.Status + ":" + hashOf(r.Disk), dismissible: true}
	m := &it.msg
	m.Ref = brain + "/" + r.Name
	m.Label = "the scheduled agent *" + r.Name + "* of " + brainName
	m.Unexpected = brainFileUnexpected
	switch r.Status {
	case manifest.ReviewRemoved:
		m.Subject = fmt.Sprintf("*%s* wants to stop its scheduled agent *%s*", brainName, r.Name)
		m.Option = fmt.Sprintf("%s stops agent %s", brainName, r.Name)
	case manifest.ReviewNew:
		m.Subject = fmt.Sprintf("*%s* wants a new scheduled agent, *%s*", brainName, r.Name)
		m.Option = fmt.Sprintf("%s: new agent %s", brainName, r.Name)
	default:
		m.Subject = fmt.Sprintf("*%s* changed its scheduled agent *%s*", brainName, r.Name)
		m.Option = fmt.Sprintf("%s: changed agent %s", brainName, r.Name)
	}
	if r.Status != manifest.ReviewRemoved {
		if s.When != "" {
			m.Facts = append(m.Facts, "Runs: "+s.When+", on its own")
		}
		if s.Description != "" {
			m.Facts = append(m.Facts, "For: "+textx.Truncate(s.Description, 200))
		}
		m.Facts = append(m.Facts, "Does: "+taskOf(s))
		if c := s.Command; c != nil && len(c.Argv) > 0 {
			m.Facts = append(m.Facts, b.programFact(c.Node, c.Argv))
		}
		for _, r := range risksOf(s) {
			m.Allows = append(m.Allows, canPhrase(r))
		}
	}
	freshness(&it, since)
	name, disk := r.Name, r.Disk
	it.approve = func(ctx context.Context, _ string) (string, error) {
		if _, err := b.agents.approve(ctx, brain, name, disk); err != nil {
			return "", err
		}
		return outboundmsg.ApprovedReply(m.Label, "It runs as its file says from now on."), nil
	}
	it.deny = func(context.Context, string) (string, error) {
		return outboundmsg.DeniedReply(m.Label, "What you approved before stays; the file waits on the Schedules page."), nil
	}
	return it
}

// programFact says what approving a scheduled agent that runs argv on
// node ref does to that node's allowlist (#221, programOn), or why the
// program would not run there yet.
func (b *App) programFact(ref string, argv []string) string {
	var views []nodes.View
	if b.nodes != nil {
		views, _ = b.nodes.Views()
	}
	p := programOn(views, ref, argv, false)
	switch p.State {
	case ProgramAllowed:
		return "Program: " + p.Detail + " already"
	case ProgramWaiting:
		return "Program: " + p.Detail
	}
	return "Program: it would not run yet — " + p.Detail
}

// canPhrase turns one of risksOf's lines ("runs a program …") into one
// that follows "If you approve, it can:".
func canPhrase(risk string) string {
	for _, p := range [][2]string{{"runs ", "run "}, {"may take ", "take "}, {"can write ", "write "}, {"may use ", "use "}} {
		if rest, ok := strings.CutPrefix(risk, p[0]); ok {
			return p[1] + rest
		}
	}
	return risk
}

func (b *App) brainItem(id, brainName string, disk, approved manifest.Manifest, since time.Time) pendingItem {
	it := pendingItem{kind: pendingBrain, key: "brain:" + id + ":" + hashOf(disk), dismissible: true}
	m := &it.msg
	m.Ref = id
	m.Label = "the new brain.json of *" + brainName + "*"
	m.Subject = fmt.Sprintf("*%s* changed its settings (brain.json)", brainName)
	m.Option = brainName + ": changed brain.json"
	m.Unexpected = brainFileUnexpected
	if changed := changedFields(disk, approved); len(changed) > 0 {
		m.Allows = []string{"run with these settings changed: " + strings.Join(changed, ", ")}
	}
	freshness(&it, since)
	it.approve = func(context.Context, string) (string, error) {
		if err := b.registry.Approve(id, disk); err != nil {
			return "", err
		}
		return outboundmsg.ApprovedReply(m.Label, "The brain runs with it from now on."), nil
	}
	it.deny = func(context.Context, string) (string, error) {
		return outboundmsg.DeniedReply(m.Label, "It keeps running as you approved it before; the change waits on the Brains page."), nil
	}
	return it
}

// changedFields are the settings of brain.json that differ between disk
// and approved, by their names in the file; its scheduled agents are
// approved on their own.
func changedFields(disk, approved manifest.Manifest) []string {
	var x, y map[string]json.RawMessage
	dx, _ := json.Marshal(disk.Normalized())
	dy, _ := json.Marshal(approved.Normalized())
	_ = json.Unmarshal(dx, &x)
	_ = json.Unmarshal(dy, &y)
	var out []string
	for k := range x {
		if k != "schedules" && string(x[k]) != string(y[k]) {
			out = append(out, k)
		}
	}
	for k := range y {
		if _, ok := x[k]; !ok && k != "schedules" {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func (b *App) skillItem(brain, brainName string, s brainSkill, since time.Time) pendingItem {
	it := pendingItem{kind: pendingSkill, key: "skill:" + brain + "/" + s.Path + ":" + s.Hash, dismissible: true}
	m := &it.msg
	label := cmp.Or(s.Name, s.Path)
	m.Ref = brain + "/" + s.Path
	m.Label = "the skill *" + label + "* of " + brainName
	if s.Status == skillNew {
		m.Subject = fmt.Sprintf("*%s* has a new skill, *%s*", brainName, label)
		m.Option = fmt.Sprintf("%s: new skill %s", brainName, label)
	} else {
		m.Subject = fmt.Sprintf("*%s* changed what its skill *%s* may do", brainName, label)
		m.Option = fmt.Sprintf("%s: changed skill %s", brainName, label)
	}
	if s.Description != "" {
		m.Facts = append(m.Facts, "For: "+textx.Truncate(s.Description, 200))
	}
	m.Unexpected = brainFileUnexpected
	m.Allows = skillPlain(s.Policy.Normalized())
	freshness(&it, since)
	path, hash := s.Path, s.Hash
	it.approve = func(ctx context.Context, by string) (string, error) {
		cur, err := b.findSkill(ctx, brain, path)
		if err != nil {
			return "", err
		}
		if cur.Hash != hash {
			return "", errors.New("the skill changed again since — send !approve again for the new version")
		}
		if err := b.recordSkillApproval(ctx, brain, path, cur.Policy, by); err != nil {
			return "", err
		}
		return outboundmsg.ApprovedReply(m.Label, "It applies as its file says from now on."), nil
	}
	it.deny = func(context.Context, string) (string, error) {
		return outboundmsg.DeniedReply(m.Label, "What you approved before stays (or, for a new one, it only reads); it waits on the brain's Skills page."), nil
	}
	return it
}

// skillPlain says what a skill's policy lets its runs do.
func skillPlain(p skill.Policy) []string {
	var out []string
	switch p.Risk {
	case skill.RiskIrreversible:
		out = append(out, "take steps that cannot be undone, such as sending or submitting things (each still asks you first)")
	case skill.RiskWrite:
		out = append(out, "change things, such as writing files")
	case skill.RiskRead:
		out = append(out, "only read")
	default:
		out = append(out, "only read")
	}
	if len(p.Triggers) > 0 {
		out = append(out, "start when a message says: "+strings.Join(p.Triggers, ", "))
	}
	if len(p.Vault) > 0 {
		out = append(out, "use these vault files: "+strings.Join(p.Vault, ", "))
	}
	if len(p.Approve) > 0 {
		out = append(out, "stop for your yes before: "+strings.Join(p.Approve, ", "))
	}
	return out
}

// --- alerts

// alertPendingApprovals tells the owner, in their direct chat, of what
// waits for them that they were not told of yet: one alert for one, one
// list for several. It forgets what no longer waits.
func (b *App) alertPendingApprovals(ctx context.Context) {
	cfg := b.cfgs.Get()
	target := ownerChat(cfg)
	if target == "" || b.outbox == nil {
		return
	}
	all := b.pendingApprovals(ctx)
	now := time.Now()
	b.approvals.mu.Lock()
	path := b.ownerApprovalsPath()
	b.approvals.load(path)
	st := &b.approvals.state
	current := map[string]bool{}
	var tell []pendingItem
	for _, it := range all {
		current[it.key] = true
		if _, ok := st.Alerted[it.key]; ok {
			continue
		}
		st.Alerted[it.key] = now
		if _, gone := st.Dismissed[it.key]; !gone && !it.silent {
			tell = append(tell, it)
		}
	}
	changed := len(tell) > 0
	for _, m := range []map[string]time.Time{st.Alerted, st.Dismissed} {
		for k := range m {
			if !current[k] {
				delete(m, k)
				changed = true
			}
		}
	}
	if changed {
		b.approvals.save(path)
	}
	b.approvals.mu.Unlock()
	var msg string
	switch len(tell) {
	case 0:
		return
	case 1:
		msg = outboundmsg.ApprovalAlert(tell[0].msg, now)
	default:
		list := make([]outboundmsg.OwnerApproval, len(tell))
		for i, it := range tell {
			list[i] = it.msg
		}
		msg = outboundmsg.ApprovalsWaitingAlert(list, now)
	}
	if err := b.outbox.Text(ctx, target, msg, cfg.MaxChunkSize); err != nil {
		log.Printf("approvals: telling the owner: %v", err)
	}
}

// --- !approve and !deny

// ownerDecide answers !approve (approve) or !deny with no argument from
// by in chat jid.
func (b *App) ownerDecide(ctx context.Context, jid, by string, approve bool) string {
	items := b.waiting(ctx)
	now := time.Now()
	b.approvals.mu.Lock()
	c := b.approvals.confirm
	b.approvals.confirm = nil
	b.approvals.mu.Unlock()
	if c != nil && now.Sub(c.at) <= confirmWindow {
		if i := slices.IndexFunc(items, func(it pendingItem) bool { return it.key == c.key }); i >= 0 {
			return b.settle(ctx, items[i], by, approve, "confirmed")
		}
	}
	switch len(items) {
	case 0:
		b.auditOwner(pendingItem{}, by, "", "nothing waits")
		return outboundmsg.NothingPendingReply
	case 1:
		return b.settleOrAsk(ctx, items[0], by, approve, now)
	}
	return b.sendPick(ctx, jid, by, items, approve)
}

// settleOrAsk approves or denies it, unless an approval needs the owner
// to be sure first: it was not their doing, it waited too long, or they
// were never shown it.
func (b *App) settleOrAsk(ctx context.Context, it pendingItem, by string, approve bool, now time.Time) string {
	if !approve {
		return b.settle(ctx, it, by, false, "")
	}
	b.approvals.mu.Lock()
	b.approvals.load(b.ownerApprovalsPath())
	_, shown := b.approvals.state.Alerted[it.key]
	b.approvals.mu.Unlock()
	shown = shown || it.silent // a run's poll shows it
	var why string
	switch {
	case !shown:
		// Approve only what the owner has seen: this is it.
		b.markAlerted(it.key, now)
		b.setConfirm(it.key, now)
		b.auditOwner(it, by, "approve", "shown first")
		return outboundmsg.ApprovalAlert(it.msg, now)
	case !it.expires.IsZero() && now.After(it.expires):
		why = outboundmsg.ApprovalExpiredWhy(it.since, now)
	case it.msg.Unexpected != "":
		why = "This is unexpected: " + it.msg.Unexpected
	default:
		return b.settle(ctx, it, by, true, "")
	}
	b.setConfirm(it.key, now)
	b.auditOwner(it, by, "approve", "asked to confirm")
	return outboundmsg.ApprovalConfirmReply(it.msg, why, int(confirmWindow/time.Minute))
}

func (b *App) setConfirm(key string, now time.Time) {
	b.approvals.mu.Lock()
	b.approvals.confirm = &ownerConfirm{key: key, at: now}
	b.approvals.mu.Unlock()
}

func (b *App) markAlerted(key string, now time.Time) {
	b.approvals.mu.Lock()
	defer b.approvals.mu.Unlock()
	path := b.ownerApprovalsPath()
	b.approvals.load(path)
	b.approvals.state.Alerted[key] = now
	b.approvals.save(path)
}

// settle approves or denies it; how says what came before ("confirmed",
// "poll", "ref") for the audit log.
func (b *App) settle(ctx context.Context, it pendingItem, by string, approve bool, how string) string {
	do, verb := it.approve, "approve"
	if !approve {
		do, verb = it.deny, "deny"
	}
	reply, err := do(ctx, by)
	if err != nil {
		log.Printf("approvals: %s %s: %v", verb, it.key, err)
		b.auditOwner(it, by, verb+" "+how, "failed: "+err.Error())
		return outboundmsg.ApprovalFailedReply(it.msg.Label, err.Error())
	}
	outcome := "approved"
	if !approve {
		outcome = "denied"
		if it.dismissible {
			b.approvals.mu.Lock()
			path := b.ownerApprovalsPath()
			b.approvals.load(path)
			b.approvals.state.Dismissed[it.key] = time.Now()
			b.approvals.save(path)
			b.approvals.mu.Unlock()
		}
	}
	b.auditOwner(it, by, strings.TrimSpace(verb+" "+how), outcome)
	return reply
}

// auditOwner records the owner's decision on it.
func (b *App) auditOwner(it pendingItem, by, detail, outcome string) {
	kind := audit.OwnerApproval
	if it.kind == pendingNode {
		kind = audit.NodeApproved
		if strings.HasPrefix(detail, "deny") {
			kind = audit.NodeRevoked
		}
	}
	if it.kind != "" {
		detail = strings.TrimSpace(it.kind + " " + detail)
	}
	b.audit.Append(audit.Entry{Kind: kind, Chat: ownerChat(b.cfgs.Get()), By: by, ID: it.msg.Ref, Detail: detail, Outcome: outcome})
	log.Printf("approvals: %s %s by %s: %s", detail, it.msg.Ref, by, outcome)
}

// ownerDecideRef answers `!approve <ref>` and `!deny <ref>`: the one
// thing waiting with that ref, which the owner named. It reports false
// for a ref nothing waiting has.
func (b *App) ownerDecideRef(ctx context.Context, by, ref string, approve bool) (string, bool) {
	for _, it := range b.waiting(ctx) {
		if strings.EqualFold(it.msg.Ref, ref) {
			return b.settle(ctx, it, by, approve, "by ref"), true
		}
	}
	return "", false
}

// sendPick sends the poll that picks one of items.
func (b *App) sendPick(ctx context.Context, jid, by string, items []pendingItem, approve bool) string {
	items = items[:min(len(items), outboundmsg.MaxPollOptions)]
	pick := ownerPick{approve: approve, keys: map[string]string{}, at: time.Now()}
	options := make([]string, 0, len(items))
	for _, it := range items {
		o := textx.Truncate(cmp.Or(it.msg.Option, it.msg.Ref), 90)
		for n := 2; pick.keys[o] != ""; n++ {
			o = fmt.Sprintf("%s (%d)", textx.Truncate(cmp.Or(it.msg.Option, it.msg.Ref), 85), n)
		}
		pick.keys[o] = it.key
		options = append(options, o)
	}
	token := randomToken(9)
	b.approvals.mu.Lock()
	if b.approvals.picks == nil {
		b.approvals.picks = map[string]ownerPick{}
	}
	for t, p := range b.approvals.picks {
		if time.Since(p.at) > approvalFreshFor {
			delete(b.approvals.picks, t)
		}
	}
	b.approvals.picks[token] = pick
	b.approvals.mu.Unlock()
	if err := b.outbox.Poll(ctx, jid, outboundmsg.ApprovalPickQuestion(approve, len(items)), options, ownerPickPrefix+token); err != nil {
		log.Printf("approvals: sending the poll: %v", err)
		return outboundmsg.ChatErrGeneric
	}
	b.auditOwner(pendingItem{}, by, map[bool]string{true: "approve", false: "deny"}[approve], fmt.Sprintf("poll sent: %d options", len(options)))
	return outboundmsg.ApprovalPickNotice(approve)
}

// ownerPickVote takes a vote in the poll that picks one: only the
// owner's, in their direct chat, decides; it then goes as !approve alone
// would for that one.
func (b *App) ownerPickVote(token string, v pollVote) {
	voter := v.Voter.User
	entry := audit.Entry{Kind: audit.OwnerApproval, Chat: v.Poll.Chat, By: voter, Detail: "poll vote: " + strings.Join(v.Selected, ", ")}
	switch {
	case !b.isOwner(voter) || chat.IsGroupJID(v.Poll.Chat):
		entry.Outcome = "ignored: not the owner"
		b.audit.Append(entry)
		return
	case len(v.Selected) == 0:
		entry.Outcome = "ignored: vote taken back"
		b.audit.Append(entry)
		return
	}
	ctx := context.Background()
	b.approvals.mu.Lock()
	pick, ok := b.approvals.picks[token]
	key := pick.keys[v.Selected[0]]
	if ok && key != "" {
		delete(b.approvals.picks, token)
	}
	b.approvals.mu.Unlock()
	reply := outboundmsg.ApprovalPollStale
	var found *pendingItem
	if key != "" {
		for _, it := range b.waiting(ctx) {
			if it.key == key {
				found = &it
				break
			}
		}
	}
	if found == nil {
		entry.Outcome = "ignored: poll out of date"
		b.audit.Append(entry)
	} else {
		entry.ID, entry.Outcome = found.msg.Ref, "picked"
		b.audit.Append(entry)
		reply = b.settleOrAsk(ctx, *found, voter, pick.approve, time.Now())
	}
	if err := b.outbox.Text(ctx, v.Poll.Chat, reply, b.cfgs.Get().MaxChunkSize); err != nil {
		log.Printf("approvals: answering a vote: %v", err)
	}
}
