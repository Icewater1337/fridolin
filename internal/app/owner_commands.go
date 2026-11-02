package app

import (
	"context"
	"errors"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/audit"
	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/logx"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
)

// The owner's chat commands (pipeline.OwnerCommands): !join hands out a
// join code for a new node, !dashboard a dashboard sign-in link that
// works on the tailnet too, and !approve and !revoke let a node in or
// shut it out. They are what the Nodes page does, for a machine where
// the owner has no dashboard token at hand (AGENTS.md, path B).
//
// Only the owner (a number in allowed_numbers) may send them, and only
// in their direct chat: a code or a link sent into a group would reach
// everyone in it. !join and !dashboard answer ownerAsksPerHour times an
// hour each. Every one, refused or not, goes into the audit log, without
// the code. The answers go out as notices, which no brain's history
// records, so no run ever reads a code or a link.

// ownerAsksPerHour is how often !join and !dashboard each answer in an
// hour.
const ownerAsksPerHour = 5

// ownerCommandRate counts the owner's !join and !dashboard in the last
// hour, per command.
type ownerCommandRate struct {
	mu   sync.Mutex
	asks map[string][]time.Time
}

// allow counts command at now, unless it answered ownerAsksPerHour
// times in the hour before.
func (r *ownerCommandRate) allow(command string, now time.Time) bool {
	return r.allowN(command, ownerAsksPerHour, now)
}

// allowN counts command at now, unless it answered limit times in the
// hour before.
func (r *ownerCommandRate) allowN(command string, limit int, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.asks == nil {
		r.asks = map[string][]time.Time{}
	}
	cut := now.Add(-time.Hour)
	recent := slices.DeleteFunc(r.asks[command], func(t time.Time) bool { return !t.After(cut) })
	if len(recent) >= limit {
		r.asks[command] = recent
		return false
	}
	r.asks[command] = append(recent, now)
	return true
}

// ownerCommand answers command (one of pipeline.OwnerCommands) with args
// from m.
func (b *App) ownerCommand(m brainruntime.Inbound, command string, args []string) string {
	by := m.Sender
	if n, err := chat.PhoneNumber(m.Sender); err == nil {
		by = n
	}
	kind := map[string]string{"!join": audit.NodeJoinCode, "!dashboard": audit.DashboardLink, "!approve": audit.NodeApproved, "!deny": audit.NodeRevoked, "!revoke": audit.NodeRevoked, "!movecore": audit.CoreMoveRequested, "!unblock": audit.Errand}[command]
	record := func(id, outcome string) {
		b.audit.Append(audit.Entry{Kind: kind, Chat: m.JID, By: by, ID: id, Detail: command, Outcome: outcome})
		log.Printf("owner command: %s from %s in %s: %s", command, by, m.JID, outcome)
	}
	switch {
	case m.IsGroup():
		record("", "refused: a group")
		return outboundmsg.OwnerCommandGroupNotice
	case !b.isOwner(m.Sender):
		record("", "refused: not the owner")
		return outboundmsg.OwnerCommandOwnerOnly
	}
	switch command {
	case "!join":
		return b.joinCommand(record)
	case "!dashboard":
		return b.dashboardCommand(record)
	case "!movecore":
		return b.moveCoreCommand(args, record)
	case "!unblock":
		return b.unblockCommand(args, record)
	}
	ctx := context.Background()
	approve := command == "!approve"
	switch len(args) {
	case 0:
		// !approve or !deny alone, and !revoke alone as !deny: what waits.
		return b.ownerDecide(ctx, m.JID, by, approve)
	case 1:
	default:
		return outboundmsg.NodeCommandUsage
	}
	// !revoke <ref> shuts a node out, approved or not; !approve and !deny
	// <ref> settle what waits with that ref, a node's too.
	ref := args[0]
	if command != "!revoke" {
		if reply, ok := b.ownerDecideRef(ctx, by, ref, approve); ok {
			return reply
		}
	}
	// A node's name stands for its ref (#251).
	n, err := b.nodes.FindNode(ref)
	if reply, ok := ambiguousNodeReply(command, err); ok {
		record(ref, "refused: ambiguous name")
		return reply
	}
	if err == nil && n.ID != ref {
		ref = n.ID
		if command != "!revoke" {
			if reply, ok := b.ownerDecideRef(ctx, by, ref, approve); ok {
				return reply
			}
		}
	}
	if command == "!deny" {
		// Only a node still waiting to join is shut out; an approved one
		// keeps its approval, as only !revoke <ref> takes that away.
		if cur, err := b.nodes.Registry().Get(ref); err == nil && cur.Device != nodeproto.DevicePending {
			record(ref, "refused: nothing waits")
			return outboundmsg.NodeNothingWaitsReply(ref)
		}
		command = "!revoke"
	}
	return b.nodeCommand(command, ref, record)
}

// ambiguousNodeReply answers command when err is a name several nodes
// have (nodes.AmbiguousNodeError), and reports whether it is.
func ambiguousNodeReply(command string, err error) (string, bool) {
	amb, ok := errors.AsType[*nodes.AmbiguousNodeError](err)
	if !ok {
		return "", false
	}
	list := make([]outboundmsg.FleetNode, len(amb.Matches))
	for i, n := range amb.Matches {
		list[i] = outboundmsg.FleetNode{ID: n.ID, Name: n.Name, OS: n.OS}
	}
	return outboundmsg.NodeAmbiguousReply(command, amb.Name, list), true
}

// joinCommand answers !join: a join code, as `fridolin nodes join-code`
// makes one.
func (b *App) joinCommand(record func(id, outcome string)) string {
	if !b.nodesEnabled {
		record("", "refused: nodes are off")
		return outboundmsg.NodesOffNotice
	}
	if !b.ownerAsks.allow("!join", time.Now()) {
		record("", "refused: rate limit")
		return outboundmsg.OwnerCommandRateLimited("!join", ownerAsksPerHour)
	}
	code, _, err := b.nodes.NewJoinCodeFrom(nodes.JoinedFromChat)
	if err != nil {
		log.Printf("nodes: a join code for !join: %v", err)
		record("", "failed")
		return outboundmsg.ChatErrGeneric
	}
	core := b.joinCore()
	record("", "ok")
	return outboundmsg.JoinCodeReply(code, core, joinLines(code, core, b.boxSource()), int(nodes.JoinCodeTTL/time.Minute))
}

// joinLines are the lines !join offers for code: natively and in a box
// where src says where their scripts are published, and the plain join.
func joinLines(code, core string, src httpapi.BoxSource) outboundmsg.JoinLines {
	var l outboundmsg.JoinLines
	l.NativeSh, l.NativePowerShell = httpapi.NativeCommands(code, core, src)
	l.BoxSh, l.BoxPowerShell = httpapi.BoxCommands(code, core, src)
	l.Sh, l.PowerShell = httpapi.JoinCommands(code, core)
	return l
}

// dashboardCommand answers !dashboard: a login code that lasts
// httpapi.RemoteLoginCodeLifetime, as links to the dashboard on the
// tailnet and on the core machine.
func (b *App) dashboardCommand(record func(id, outcome string)) string {
	if b.listen == "" || b.cfgs.Get().Dashboard.Disabled {
		record("", "refused: the dashboard is off")
		return outboundmsg.DashboardOffNotice
	}
	if !b.ownerAsks.allow("!dashboard", time.Now()) {
		record("", "refused: rate limit")
		return outboundmsg.OwnerCommandRateLimited("!dashboard", ownerAsksPerHour)
	}
	code, _ := b.sessions.NewCodeFor(httpapi.RemoteLoginCodeLifetime)
	tailnet := ""
	if base := b.tailnetDashboardURL(); base != "" {
		tailnet = httpapi.TailnetLoginURL(base, code)
	}
	record("", "ok")
	return outboundmsg.DashboardLinkReply(tailnet, httpapi.LoginURL(b.listen, code), int(httpapi.RemoteLoginCodeLifetime/time.Minute))
}

// nodeCommand answers !approve id, which approves the device and what it
// offers in one go, and !revoke id.
func (b *App) nodeCommand(command, id string, record func(id, outcome string)) string {
	var n *nodes.Node
	var err error
	fride := false
	if cur, err := b.nodes.Registry().Get(id); err == nil {
		fride = nodes.FridePending(cur)
	}
	var forgot *nodes.CoreForget
	if command == "!revoke" {
		n, forgot, err = b.nodes.RevokeForget(id)
	} else if n, err = b.nodes.ApproveDevice(id); err == nil {
		n, err = b.nodes.ApproveCommands(id)
	}
	switch {
	case errors.Is(err, nodes.ErrUnknownNode):
		record(id, "refused: unknown node")
		return outboundmsg.NodeUnknownReply(id)
	case errors.Is(err, nodes.ErrRevoked):
		record(id, "refused: revoked")
		name := id
		if cur, err := b.nodes.Registry().Get(id); err == nil {
			name = cur.Name
		}
		return outboundmsg.NodeIsRevokedReply(name, id)
	case err != nil:
		log.Printf("nodes: %s %s: %v", command, id, err)
		record(id, "failed")
		return outboundmsg.ChatErrGeneric
	}
	record(id, "ok")
	if command == "!revoke" {
		reply := outboundmsg.NodeRevokedReply(n.Name, n.ID)
		if forgot != nil {
			reply += "\n\n" + outboundmsg.NodeRevokedCoreSecretsNote(forgetReport(*forgot))
		}
		return reply
	}
	reply := outboundmsg.NodeApprovedReply(n.Name, n.ID, n.Effective().Plain())
	if fride && n.Effective().Fride != nil {
		reply += "\n\n" + outboundmsg.NodeFrideApprovedNote(nodeproto.FrideApproval)
	}
	return reply
}

// nodeJoined tells the owner, in their direct chat, that a machine joined
// with a join code and waits for approval.
func (b *App) nodeJoined(*nodes.Node) { b.alertPendingApprovals(context.Background()) }

// unblockCommand answers !unblock <number>: Fridolin hears again someone
// it stopped listening to in an errand (errand_runtime.go), and asks the
// owner before it writes to them.
func (b *App) unblockCommand(args []string, record func(id, outcome string)) string {
	number, err := chat.PhoneNumber(strings.Join(args, ""))
	if len(args) == 0 || err != nil {
		record("", "refused: no number")
		return outboundmsg.UnblockUsage
	}
	masked := logx.MaskJID(number, "", "")
	unblocked, err := b.unblockErrandContact(context.Background(), number)
	switch {
	case err != nil:
		record(masked, "failed: "+err.Error())
		return outboundmsg.UnblockUsage
	case !unblocked:
		record(masked, "nothing blocked")
		return outboundmsg.NotBlockedReply(number)
	}
	record(masked, "unblocked")
	return outboundmsg.UnblockedReply(number)
}
