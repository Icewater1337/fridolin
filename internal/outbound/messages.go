package outbound

import (
	"fmt"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/textx"
)

// The messages the bridge itself sends into chats: its answers to the
// chat commands, its notices about a message and its run, and its alerts
// to the owner. A brain's answers are its own; these are the bridge's,
// and they live here, in one place, so that they are read, reworded and
// pinned (testdata/golden/messages.golden) together. Each starts with an
// emoji that says what kind of message it is, for the people who read
// it; code compares with the message itself, never with its emoji.

// NoBrainNotice answers a message in a chat no brain takes.
const NoBrainNotice = "🧠 No brain is set up for this chat yet. Open the bridge dashboard and add one."

// HeldBrainNotice answers a message in a chat whose brain cannot run: it
// goes to no other brain instead.
const HeldBrainNotice = "🧠 The brain for this chat cannot run right now. The Brains page of the bridge dashboard says why."

// ResetReply answers !reset. afresh says that the chat starts afresh,
// which only a chat a brain answers can: its next message is read without
// the ones before.
func ResetReply(afresh bool) string {
	if afresh {
		return "✅ Reset. I stopped what this chat had running or waiting, and the next message starts without its earlier messages. What I noted in my memory files stays."
	}
	return "✅ Cleared anything this chat had running or waiting."
}

// StopReply answers !stop, which stopped a run or not and dropped that
// many waiting messages.
func StopReply(stopped bool, dropped int) string {
	switch {
	case stopped:
		return "🛑 Stopped, and cleared anything this chat had waiting."
	case dropped > 0:
		return "🛑 Nothing was running for this chat; cleared what it had waiting."
	}
	return "🛑 Nothing was running for this chat."
}

// HelpReply answers !help. groupTrigger is the trigger that addresses the
// bridge, in a group; "" in a direct chat.
func HelpReply(groupTrigger string) string {
	help := "🤖 *Fridolin*\n\n" +
		"Send a message to talk to Claude. Images, documents, audio, and video are passed along as files.\n\n" +
		"*Commands:*\n" +
		"• `!help` — show this help\n" +
		"• `!stop` — stop what I'm doing for this chat and drop its queued messages\n" +
		"• `!reset` — like `!stop`, and the next message starts without this chat's earlier messages\n" +
		"• `!fleet` — the machines connected to me as nodes (direct chats only)\n" +
		"• `!move <thread> <node>` — move FrIDE's coding work to another node (direct chats only)\n" +
		"• `!lockdown` — owner only: pause all work but reading until `!unlock`\n" +
		"• `!join` — owner only, direct chat: a join code for a new node\n" +
		"• `!dashboard` — owner only, direct chat: a sign-in link for the dashboard\n" +
		"• `!approve <id>`, `!revoke <id>` — owner only, direct chat: let a node in, or shut it out"
	if groupTrigger != "" {
		help += "\n\nIn this group, start your message with `" + groupTrigger + "` or reply to one of mine to address me, commands included."
	}
	return help
}

// QueuedNotice tells the sender their message waits, at position pos.
func QueuedNotice(pos int) string {
	return fmt.Sprintf("⏳ Queued (#%d) — I'll get to this right after the current task.", pos)
}

// QueueFullNotice tells the sender their message was dropped because the
// brain's queue is full; stop is the !stop that addresses the bridge in
// the chat.
func QueueFullNotice(stop string) string {
	return "⏳ I'm still busy with earlier messages and my queue is full. Try again in a bit, or send `" + stop + "` to drop what this chat has waiting."
}

// OtpCaughtNotice tells the sender of a code the OTP guard caught what
// became of it.
const OtpCaughtNotice = "🔐 That looks like the one-time code this chat was asked for, so I kept it out of the conversation: the assistant does not see it."

// A run that takes longer than workingNoticeAfter tells the chat it is
// working on it, and questions it asks mid-run go out marked as such.
const (
	WorkingNotice  = "⏳ Working on it..."
	QuestionPrefix = "❓ "
)

// What a chat is told about a run that failed (see chatErrorText). The
// error itself goes to the log and the dashboard: wrapped, it can hold the
// CLI's stderr, with local paths and sandbox denials, and in a group every
// member would read it.
const (
	ChatErrCodexNotSignedIn = "🔑 Codex is not signed in for this brain. See the bridge dashboard for its sign-in command, then send your message again."
	ChatErrCodexLimit       = "⏳ Codex's usage limit is reached for now. Try again later."
	ChatErrNotSignedIn      = "🔑 Claude is not signed in on the bridge machine. Run `claude` there once and log in, then send your message again."
	ChatErrLimit            = "⏳ Claude's usage limit is reached for now. Try again later."
	ChatErrTimeout          = "⌛ That took too long and was stopped. Try again, perhaps with a smaller request."
	ChatErrGeneric          = "❌ Something went wrong — details on the bridge dashboard."
)

// ChatErrSandbox is what a chat is told about a run its sandbox kept from
// starting: err says why, without a path.
func ChatErrSandbox(err error) string { return "❌ " + capitalize(err.Error()) + "." }

// ChatErrClaude is the CLI's own short account of a failed run, msg, as a
// direct chat is told it.
func ChatErrClaude(msg string) string { return "❌ Claude: " + textx.Truncate(msg, 300) }

// ChatErrCodex is Codex's own short account, shown only in a direct chat.
func ChatErrCodex(msg string) string { return "❌ Codex: " + textx.Truncate(msg, 300) }

// BuildChatError is what a build chat on the Site's Build page shows for
// a message the builder brain could not take.
func BuildChatError(err error) string { return "❌ " + err.Error() }

// ViewOnceNotice is what a chat is told about a view-once message.
const ViewOnceNotice = "👁️ I don't open view-once photos, videos or voice notes: I would keep them. Send it as a normal one if I should see it."

// MediaTooLargeNotice is what a chat is told about an attachment over the
// limit of maxBytes.
func MediaTooLargeNotice(maxBytes int64) string {
	return fmt.Sprintf("⚠️ That file is over the %d MB limit, so I skipped it.", maxBytes>>20)
}

// MediaFailedNotice is what a chat is told about an attachment that could
// not be downloaded.
const MediaFailedNotice = "⚠️ I couldn't download that file. Please send it again."

// PollAsText is a poll as a numbered list, for when WhatsApp refuses it
// as a poll.
func PollAsText(p Poll) string {
	text := "📊 *" + p.Question + "*\n"
	for i, opt := range p.Options {
		text += fmt.Sprintf("%d. %s\n", i+1, opt)
	}
	return strings.TrimSpace(text)
}

// StartupCheckAlert tells the owner what the startup check found missing,
// msg.
func StartupCheckAlert(msg string) string { return "⚠️ Bridge startup check: " + msg }

// BackupRefusedAlert tells the owner that the scheduled backup of the
// brain named name refused to run, and why.
func BackupRefusedAlert(name, why string) string {
	return fmt.Sprintf("⚠️ Backup of %s: %s\n\nCheck the brain folder, then the Backup page in the Fridolin dashboard. "+
		"Scheduled backups of this brain keep refusing until one succeeds; this is the only message about it.", name, why)
}

// PlaudGivenUpAlert tells the chat a brain's Plaud summaries go to that
// the recording name will not reach brain, and why.
func PlaudGivenUpAlert(name, brain, why string) string {
	return fmt.Sprintf("⚠️ Plaud: the recording “%s” was not handed to brain %s: %s. It is still in Plaud, and the dashboard lists it (Integrations → Plaud).", name, brain, why)
}

// PlaudExpiredAlert tells the owner that the Plaud login expired.
const PlaudExpiredAlert = "⚠️ Plaud: the login expired, so new recordings are not arriving. Reconnect in the Fridolin dashboard (Integrations → Plaud)."

// What the bridge tells a chat about its own restart.
const (
	// RestartedNotice tells a chat, once the bridge is back, that a stop
	// cut short the run for its message. The run is not made again: it
	// may have done some of what it was asked already, an event created or
	// a message sent, and would do it twice.
	RestartedNotice = "🔄 I restarted while I was working on your message. Send it again if you still need an answer."
)

// FleetGroupNotice answers !fleet in a group: the fleet is the owner's
// business.
const FleetGroupNotice = "🖥️ !fleet answers only in a direct chat."

// FleetReply answers !fleet: the nodes of the core's fleet, whether each
// is online and what it may do. enabled is nodes.enabled.
func FleetReply(enabled bool, nodes []FleetNode, now time.Time) string {
	if !enabled {
		return NodesOffNotice
	}
	if len(nodes) == 0 {
		return "🖥️ No nodes yet. Send `!join` here for a join code, and paste the line it sends on the new machine."
	}
	online := 0
	for _, n := range nodes {
		if n.Online {
			online++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🖥️ *Fleet* — %s, %d online\n", countNodes(len(nodes)), online)
	for _, n := range nodes {
		fmt.Fprintf(&b, "\n• *%s* (%s) — %s", n.Name, osLabel(n.OS), seen(n, now))
		switch {
		case n.Device == "revoked":
			b.WriteString(" · revoked")
		case n.Device == "pending":
			b.WriteString(" · waiting for approval: `!approve " + n.ID + "`")
		case n.Commands == "pending":
			b.WriteString(" · offers more than approved: `!approve " + n.ID + "`")
		}
		if n.Device == "approved" {
			b.WriteString("\n  may run: " + n.Offers)
		}
		b.WriteString("\n  id " + n.ID)
		if len(n.Tags) > 0 {
			b.WriteString(" · " + strings.Join(n.Tags, ", "))
		}
	}
	return b.String()
}

// NodesOffNotice answers !fleet and !join while nodes.enabled is off.
const NodesOffNotice = "🖥️ Nodes are off. Turn on nodes.enabled in the config and restart the bridge to connect other machines (docs/NODES.md)."

// OwnerCommandGroupNotice answers !join, !dashboard, !approve or !revoke
// in a group: what they hand out or change is the owner's alone.
const OwnerCommandGroupNotice = "🔒 !join, !dashboard, !approve and !revoke answer only in the owner's direct chat."

// OwnerCommandOwnerOnly answers !join, !dashboard, !approve or !revoke
// from someone who is not the owner (a number in allowed_numbers).
const OwnerCommandOwnerOnly = "🔒 Only the owner can ask for join codes and sign-in links, or approve and revoke nodes."

// OwnerCommandRateLimited answers !join or !dashboard asked for more than
// max times in the last hour.
func OwnerCommandRateLimited(command string, max int) string {
	return fmt.Sprintf("⏳ %s answered %d times in the last hour, the most allowed. Try again later, or use the dashboard.", command, max)
}

// NodeCommandUsage answers !approve or !revoke without one node id.
const NodeCommandUsage = "🖥️ Send `!approve <id>` or `!revoke <id>` with a node's id; `!fleet` lists them."

// JoinCodeReply answers !join: a single-use join code, valid for minutes,
// the lines to paste on the new machine for sh and PowerShell, and the
// core's node address. boxSh and boxPowerShell, when set, install a node
// in a box instead, and come first.
func JoinCodeReply(code, core, sh, powershell, boxSh, boxPowerShell string, minutes int) string {
	box := ""
	if boxSh != "" {
		box = fmt.Sprintf("*A node in a box* (Docker; it asks for the rest):\n\n*macOS, Linux:*\n```%s```\n\n*Windows (PowerShell):*\n```%s```\n\n"+
			"*Or natively*, with fridolin installed on it:\n\n", boxSh, boxPowerShell)
	}
	return fmt.Sprintf("🔑 Join code for a new node, single-use, valid %d minutes:\n\n%s\n\n"+
		"On the new machine, paste one line.\n\n%s*macOS, Linux (sh):*\n```%s```\n\n*Windows (PowerShell):*\n```%s```\n\n"+
		"Core: %s. Add `--root PATH:rw` to share a folder, `--no-exec` to allow no commands (docs/NODES.md). "+
		"Once it connects I'll tell you here, and `!approve <id>` lets it in.", minutes, code, box, sh, powershell, core)
}

// DashboardOffNotice answers !dashboard while the dashboard is off.
const DashboardOffNotice = "🔗 The dashboard is turned off (dashboard.disabled or http_enabled), so there is nothing to sign in to."

// DashboardLinkReply answers !dashboard: the sign-in link at the core's
// tailnet address (tailnet, "" when the core is not on the tailnet) and
// on the core machine itself (local). Both carry one code, which signs
// one browser in, once, within minutes.
func DashboardLinkReply(tailnet, local string, minutes int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🔗 Dashboard sign-in link, works once within %d minutes:\n\n", minutes)
	if tailnet != "" {
		b.WriteString("*From any device on your tailnet:*\n" + tailnet + "\n\n*On the core machine:*\n" + local)
	} else {
		b.WriteString(local + "\n\nThe core is not on the tailnet, so it works only on the core machine.")
	}
	b.WriteString("\n\nIt signs that browser in for 30 days; send `!dashboard` again for another link. Do not forward it.")
	return b.String()
}

// NodeJoinedAlert tells the owner that a machine joined with a join code
// and waits for approval.
func NodeJoinedAlert(name, goos, id string) string {
	return fmt.Sprintf("🖥️ *%s* (%s) wants to join as a node — approve it on the Nodes page or reply `!approve %s`. "+
		"Not yours? `!revoke %s` shuts it out.", name, osLabel(goos), id, id)
}

// NodeApprovedReply answers !approve: the device, and what it offers
// (offers), are approved.
func NodeApprovedReply(name, id, offers string) string {
	return fmt.Sprintf("✅ *%s* (%s) is approved: it may serve %s. `!fleet` lists it; `!revoke %s` shuts it out again.", name, id, offers, id)
}

// NodeCoreHostAlert tells the owner that a node asks for core-host, or
// that its core-host.json changed (changed), which waits for approval
// again: what approving means (meaning, nodeproto.CoreHostApproval), the
// device key's fingerprint, and the targets it trusts.
func NodeCoreHostAlert(name, id, fingerprint string, trusts []string, changed bool, meaning string) string {
	var b strings.Builder
	if changed {
		fmt.Fprintf(&b, "🔐 *%s* (%s) changed its core-host settings, which wait for your approval again.", name, id)
	} else {
		fmt.Fprintf(&b, "🔐 *%s* (%s) offers core-host.", name, id)
	}
	b.WriteString("\n\n" + meaning)
	b.WriteString("\n\nDevice key: `" + fingerprint + "`")
	if len(trusts) > 0 {
		b.WriteString("\nSends secrets only to: " + strings.Join(trusts, ", "))
	}
	fmt.Fprintf(&b, "\n\nYou did not just run `fridolin node core-host` there? Do not approve; `!revoke %s` shuts it out. Otherwise `!approve %s`.", id, id)
	return b.String()
}

// NodeCoreHostApprovedNote follows NodeApprovedReply when the approval
// took in core-host: what that means.
func NodeCoreHostApprovedNote(meaning string) string {
	return "🔐 That includes core-host. " + meaning + " `!revoke <id>` takes it back."
}

// NodeRevokedReply answers !revoke.
func NodeRevokedReply(name, id string) string {
	return fmt.Sprintf("🚫 *%s* (%s) is revoked: it is disconnected and refused from now on. To bring it back, it joins again with a new code (`!join`).", name, id)
}

// NodeIsRevokedReply answers !approve for a node that was revoked.
func NodeIsRevokedReply(name, id string) string {
	return fmt.Sprintf("🚫 *%s* (%s) was revoked, so it cannot be approved. It joins again with a new code (`!join`), as a new node.", name, id)
}

// NodeUnknownReply answers !approve or !revoke with an id no node has.
func NodeUnknownReply(id string) string {
	return "❓ No node has the id " + textx.Truncate(id, 40) + ". `!fleet` lists them."
}

// The approval poll: a run's request_approval asks the owner with
// ApprovalQuestion(action) and these options, of which "Show me the
// draft" sends ApprovalDraftReply and leaves the poll open.
const (
	ApprovalYes   = "✅ Yes"
	ApprovalNo    = "❌ No"
	ApprovalDraft = "📝 Show me the draft"
)

// ApprovalQuestion is the question of an approval poll.
func ApprovalQuestion(action string) string {
	return "🔐 Approve? " + textx.Truncate(action, 200)
}

// AskQuestion is the question of an ask_user poll, or the message that
// asks it without options.
func AskQuestion(question string) string { return QuestionPrefix + textx.Truncate(question, 1000) }

// ApprovalDraftReply answers a "Show me the draft" vote with what the run
// gave as its draft.
func ApprovalDraftReply(action, details string) string {
	if strings.TrimSpace(details) == "" {
		return "📝 There is no draft for “" + textx.Truncate(action, 200) + "” beyond what the poll says."
	}
	return "📝 The draft for “" + textx.Truncate(action, 200) + "”:\n\n" + details + "\n\nVote Yes or No in the poll above."
}

// LockdownReply answers !lockdown (on) and !unlock (off); already says
// the bridge was in that state.
func LockdownReply(on, already bool) string {
	switch {
	case on && already:
		return "🔒 The bridge is in lockdown already. Send !unlock, or unlock it in the dashboard, to end it."
	case on:
		return "🔒 Lockdown: only read-only work runs now. Nothing that writes, sends or submits goes ahead, and no approval is asked. Send !unlock, or unlock it in the dashboard, to end it."
	case already:
		return "🔓 The bridge is not in lockdown."
	}
	return "🔓 Unlocked: the brains work as before."
}

// LockdownOwnerOnly answers !lockdown or !unlock from someone who is not
// the owner (a number in allowed_numbers).
const LockdownOwnerOnly = "🔒 Only the owner can lock the bridge down or unlock it."

// LockdownRefusedNotice tells a chat that the lockdown kept its request,
// one of a skill that writes or submits, from running.
const LockdownRefusedNotice = "🔒 The bridge is in lockdown, so this was not run: it needs more than reading. Unlock it in the dashboard or with !unlock, then send it again."

// RateLimitedNotice tells a chat that it started max runs in the last
// hour, the most policy.max_runs_per_hour allows.
func RateLimitedNotice(max int) string {
	return fmt.Sprintf("⏳ This chat started %d runs in the last hour, the most allowed. Try again later.", max)
}

// FrideNewsMessage says FrIDE's news in the chat.
func FrideNewsMessage(n FrideNews) string {
	body := frideBody(n.Text)
	head := fmt.Sprintf("*FrIDE #%d*", n.Thread)
	work := ""
	if n.Task != "" {
		work = ": task " + n.Task
		if n.Branch != "" {
			work += ", branch " + n.Branch
		}
	}
	var b strings.Builder
	switch n.Kind {
	case "question":
		fmt.Fprintf(&b, "❓ %s asks (on %s):\n\n%s\n\nAnswer here, and I'll pass it on.", head, n.Node, body)
		return b.String()
	case "started":
		fmt.Fprintf(&b, "🛠️ %s started on %s%s.", head, n.Node, work)
	case "done":
		fmt.Fprintf(&b, "✅ %s is done on %s%s.", head, n.Node, work)
		if n.Rationale != "" {
			b.WriteString("\nRationale: " + n.Rationale)
		}
	case "failed":
		fmt.Fprintf(&b, "⚠️ %s failed on %s.", head, n.Node)
		if body != "" {
			b.WriteString("\n" + body)
		}
		b.WriteString("\nAnswer here to make it try again.")
		return b.String()
	default:
		fmt.Fprintf(&b, "🛠️ %s on %s:", head, n.Node)
	}
	if body != "" {
		b.WriteString("\n" + body)
	}
	return b.String()
}

// FrideQueuedNotice says a thread waits for a node whose FrIDE can take
// it, and why none can now.
func FrideQueuedNotice(thread int64, why string) string {
	return fmt.Sprintf("⏳ *FrIDE #%d* waits: no node with FrIDE can take it now (%s). It starts as soon as one can, and I'll say so here.", thread, why)
}

// FridePlacedNotice says a waiting thread went to node.
func FridePlacedNotice(thread int64, node string) string {
	return fmt.Sprintf("🛠️ *FrIDE #%d* went to %s now.", thread, node)
}

// FrideMoveUsage answers a !move that names no thread and node.
const FrideMoveUsage = "🚚 Use `!move <thread> <node>` to move FrIDE's work to another node, e.g. `!move 3 laptop`."

// FrideMoveGroupNotice answers !move in a group: the fleet is the owner's
// business.
const FrideMoveGroupNotice = "🚚 !move answers only in a direct chat."

// FrideMovingNotice says a move began: it may take a while.
func FrideMovingNotice(thread int64, node string) string {
	return fmt.Sprintf("🚚 Moving *FrIDE #%d* to %s…", thread, node)
}

// FrideMovedNotice says a move is done.
func FrideMovedNotice(thread int64, from, to, task string) string {
	return fmt.Sprintf("🚚 *FrIDE #%d* moved from %s to %s: task %s continues there.", thread, from, to, task)
}

// FrideMoveFailedNotice says a move did not happen, and why.
func FrideMoveFailedNotice(thread int64, why string) string {
	return fmt.Sprintf("⚠️ *FrIDE #%d* did not move: %s", thread, why)
}

// HousekeepingSummary tells the owner what the housekeeping job did
// (done: what it stopped or removed) and what it found but could not fix
// (problems), a line each. It is sent only when either has something.
func HousekeepingSummary(done, problems []string) string {
	var b strings.Builder
	b.WriteString("🧹 Housekeeping")
	if len(done) > 0 {
		fmt.Fprintf(&b, " stopped or removed %d:", len(done))
		for _, l := range done {
			b.WriteString("\n• " + textx.Truncate(l, 200))
		}
	} else {
		b.WriteString(" found what it cannot fix:")
	}
	if len(problems) > 0 {
		if len(done) > 0 {
			b.WriteString("\n\nIt could not fix:")
		}
		for _, l := range problems {
			b.WriteString("\n• " + textx.Truncate(l, 200))
		}
	}
	b.WriteString("\n\nThe Overview page of the dashboard has the whole report.")
	return b.String()
}

// HousekeepingStopResult tells the owner how stopping what they approved
// went.
func HousekeepingStopResult(what, failure string) string {
	if failure != "" {
		return "⚠️ Housekeeping could not stop " + textx.Truncate(what, 200) + ": " + failure
	}
	return "🧹 Housekeeping stopped " + textx.Truncate(what, 200) + "."
}

// NodeAgentNews is a node agent session's news for the chat that started
// it (docs/NODES.md, "Workstation").
type NodeAgentNews struct {
	// Session is the core's number for it (Agent #2), Node the node's name.
	Session int64
	Node    string
	Folder  string
	// Kind is question, irreversible, permission, done, failed or stopped.
	Kind    string
	Text    string
	Changed []string
}

// NodeAgentNewsMessage says a node agent session's news in the chat.
func NodeAgentNewsMessage(n NodeAgentNews) string {
	head := fmt.Sprintf("*Agent #%d*", n.Session)
	body := strings.TrimSpace(n.Text)
	if r := []rune(body); len(r) > 1500 {
		body = string(r[:1500]) + "…"
	}
	var b strings.Builder
	switch n.Kind {
	case "question":
		fmt.Fprintf(&b, "❓ %s asks (on %s, in %s):\n\n%s\n\nAnswer here, and I'll pass it on.", head, n.Node, n.Folder, body)
		return b.String()
	case "irreversible", "permission":
		fmt.Fprintf(&b, "✋ %s (on %s, in %s) wants to take a step: %s\nI've asked for your approval in a poll.", head, n.Node, n.Folder, body)
		return b.String()
	case "done":
		fmt.Fprintf(&b, "✅ %s is done on %s, in %s.", head, n.Node, n.Folder)
	case "failed":
		fmt.Fprintf(&b, "⚠️ %s failed on %s, in %s.", head, n.Node, n.Folder)
	default:
		fmt.Fprintf(&b, "⏹️ %s stopped on %s, in %s.", head, n.Node, n.Folder)
	}
	if body != "" {
		b.WriteString("\n\n" + body)
	}
	if len(n.Changed) > 0 {
		list := n.Changed
		more := ""
		if len(list) > 15 {
			list, more = list[:15], fmt.Sprintf("\n… and %d more", len(n.Changed)-15)
		}
		b.WriteString("\n\nChanged:\n" + strings.Join(list, "\n") + more)
	}
	return b.String()
}

// NodeSessionsStoppedNotice tells the owner a lockdown stopped the node
// sessions that ran.
func NodeSessionsStoppedNotice(n int) string {
	return fmt.Sprintf("🔒 The lockdown stopped %d session(s) on nodes (agents and terminals).", n)
}

// ScheduleFailedNotice tells the owner that a run of scheduled agent
// agent of brain failed, and why, without the CLI's stderr (that is in
// the run's log).
func ScheduleFailedNotice(agent, brain, why string) string {
	return fmt.Sprintf("⏰ Scheduled agent *%s* (brain %s) failed: %s. Its log is on the Schedules page of the dashboard.", agent, brain, textx.Truncate(why, 300))
}

// ScheduleCommandResult is what a scheduled agent's program sends with
// report "always": how it ended on node, and the end of its output.
func ScheduleCommandResult(agent, node string, exitCode int, output string) string {
	head := fmt.Sprintf("⏰ Scheduled agent *%s* ran on %s: exit %d.", agent, node, exitCode)
	if output = strings.TrimSpace(output); output != "" {
		head += "\n\n" + textx.Tail(output, 1500)
	}
	return head
}
