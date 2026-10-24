package app

import (
	"context"
	"errors"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/audit"
	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/httpapi"
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
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.asks == nil {
		r.asks = map[string][]time.Time{}
	}
	cut := now.Add(-time.Hour)
	recent := slices.DeleteFunc(r.asks[command], func(t time.Time) bool { return !t.After(cut) })
	if len(recent) >= ownerAsksPerHour {
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
	kind := map[string]string{"!join": audit.NodeJoinCode, "!dashboard": audit.DashboardLink, "!approve": audit.NodeApproved, "!deny": audit.NodeRevoked, "!revoke": audit.NodeRevoked}[command]
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
	if command != "!revoke" {
		if reply, ok := b.ownerDecideRef(ctx, by, args[0], approve); ok {
			return reply
		}
	}
	if command == "!deny" {
		command = "!revoke"
	}
	return b.nodeCommand(command, args[0], record)
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
	sh, ps := httpapi.JoinCommands(code, core)
	bsh, bps := httpapi.BoxCommands(code, core, b.boxSource())
	record("", "ok")
	return outboundmsg.JoinCodeReply(code, core, sh, ps, bsh, bps, int(nodes.JoinCodeTTL/time.Minute))
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
	coreHost := false
	if cur, err := b.nodes.Registry().Get(id); err == nil {
		coreHost = cur.Declared.CoreHost != nil && !nodeproto.Covers(cur.Approved, nodeproto.Capabilities{CoreHost: cur.Declared.CoreHost})
	}
	if command == "!revoke" {
		n, err = b.nodes.Revoke(id)
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
		return outboundmsg.NodeRevokedReply(n.Name, n.ID)
	}
	reply := outboundmsg.NodeApprovedReply(n.Name, n.ID, n.Effective().Plain())
	if coreHost && n.Effective().CoreHost != nil {
		reply += "\n\n" + outboundmsg.NodeCoreHostApprovedNote(nodeproto.CoreHostApproval)
	}
	return reply
}

// nodeCoreHost tells the owner, in their direct chat, that node n asks
// for core-host, or changed its core-host.json, which waits for approval:
// as every approval, in alertPendingApprovals.
func (b *App) nodeCoreHost(*nodes.Node) { b.alertPendingApprovals(context.Background()) }

// nodeJoined tells the owner, in their direct chat, that a machine joined
// with a join code and waits for approval.
func (b *App) nodeJoined(*nodes.Node) { b.alertPendingApprovals(context.Background()) }
