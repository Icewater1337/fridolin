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
		"• `!approve`, `!deny` — owner only, direct chat: approve, or turn down, what waits for you (a poll asks which when several wait)\n" +
		"• `!revoke <ref>` — owner only, direct chat: shut a node out"
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

// CalendarGrantBrokenAlert tells the owner that the Google Calendar of the
// brain named name stopped working: revoked says Google refused its
// sign-in, else the sign-in needs a new consent. No ids: the owner finds
// the brain by its name on the Brains page.
func CalendarGrantBrokenAlert(name string, revoked bool) string {
	why := "its Google sign-in has to be renewed"
	if revoked {
		why = "Google no longer accepts its sign-in (the access was removed, it expired, or the password changed)"
	}
	return fmt.Sprintf("⚠️ Google Calendar stopped working for %s: %s. Open the Fridolin dashboard, Brains → %s → Calendar, and press Reconnect. "+
		"This is the only message about it until it works again.", name, why, name)
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
			b.WriteString(" · shut out")
		case n.Device == "pending":
			b.WriteString("\n  ⏳ wants to join — reply !approve")
		case n.FridePending:
			b.WriteString("\n  ⏳ asks to let FrIDE's agents run anything on it (fride) — reply !approve")
		case n.Commands == "pending":
			b.WriteString("\n  ⏳ asks to do more than you approved — reply !approve")
		}
		if n.Device == "approved" && len(n.Can) > 0 {
			b.WriteString("\n  can: " + strings.Join(n.Can, "; "))
		}
		if n.Device == "approved" && n.Fride {
			b.WriteString("\n  ⚠️ fride: FrIDE's agents can run anything on it")
		}
		b.WriteString("\n  _ref " + n.ID)
		if len(n.Tags) > 0 {
			b.WriteString(" · " + strings.Join(n.Tags, ", "))
		}
		b.WriteString("_")
	}
	return b.String()
}

// FleetWaitingNote follows FleetReply while n things that are not nodes
// wait for the owner's approval; "" for none.
func FleetWaitingNote(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "\n\n🔐 One more thing waits for your approval — reply !approve."
	}
	return fmt.Sprintf("\n\n🔐 %d more things wait for your approval — reply !approve.", n)
}

// NodesOffNotice answers !fleet and !join while nodes.enabled is off.
const NodesOffNotice = "🖥️ Nodes are off. Turn on nodes.enabled in the config and restart the bridge to connect other machines (docs/NODES.md)."

// OwnerCommandGroupNotice answers !join, !dashboard, !approve or !revoke
// in a group: what they hand out or change is the owner's alone.
const OwnerCommandGroupNotice = "🔒 !join, !dashboard, !approve, !deny, !revoke and !movecore answer only in the owner's direct chat."

// OwnerCommandOwnerOnly answers !join, !dashboard, !approve or !revoke
// from someone who is not the owner (a number in allowed_numbers).
const OwnerCommandOwnerOnly = "🔒 Only the owner can ask for join codes and sign-in links, approve and turn things down, or move the core."

// OwnerCommandRateLimited answers !join or !dashboard asked for more than
// max times in the last hour.
func OwnerCommandRateLimited(command string, max int) string {
	return fmt.Sprintf("⏳ %s answered %d times in the last hour, the most allowed. Try again later, or use the dashboard.", command, max)
}

// NodeCommandUsage answers !approve, !deny or !revoke with more than one
// word after it.
const NodeCommandUsage = "🔐 Send !approve or !deny alone: I ask which one when several things wait. `!revoke <ref>` shuts out a node `!fleet` lists."

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
		"Once it connects I'll tell you here, and !approve lets it in.", minutes, code, box, sh, powershell, core)
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

// NodeApprovedReply answers !approve of a node: it is approved, and can
// now do what can says, one sentence each.
func NodeApprovedReply(name, id string, can []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "✅ *%s* is approved.", name)
	if len(can) > 0 {
		b.WriteString(" It can now:")
		for _, c := range can {
			b.WriteString("\n• " + c)
		}
	}
	fmt.Fprintf(&b, "\n\n`!fleet` lists it. To shut it out again: `!revoke %s`.", id)
	return b.String()
}

// NodeRevokedReply answers !revoke.
func NodeRevokedReply(name, id string) string {
	return fmt.Sprintf("🚫 *%s* is shut out: it is disconnected and refused from now on. To bring it back, it joins again with a new code (`!join`).\n\n_ref %s_", name, id)
}

// NodeIsRevokedReply answers !approve for a node that was revoked.
func NodeIsRevokedReply(name, id string) string {
	return fmt.Sprintf("🚫 *%s* was shut out, so it cannot be approved. It joins again with a new code (`!join`), as a new node.\n\n_ref %s_", name, id)
}

// NodeUnknownReply answers !approve or !revoke with an id no node has.
func NodeUnknownReply(id string) string {
	return "❓ Nothing waits with the ref " + textx.Truncate(id, 40) + ", and no node has it. Send !approve alone to see what waits; `!fleet` lists the nodes."
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

// The owner's approvals (approvals.go): the alert, the multi-select poll
// that picks, and the replies to !approve and !deny.

// ApprovalAlert tells the owner that a waits for their approval.
func ApprovalAlert(a OwnerApproval, now time.Time) string {
	var b strings.Builder
	b.WriteString("🔐 " + a.Subject + ".\n")
	if !a.When.IsZero() {
		b.WriteString("\n• When: " + when(a.When, now))
	}
	for _, f := range a.Facts {
		b.WriteString("\n• " + f)
	}
	if len(a.Allows) > 0 {
		b.WriteString("\n\nIf you approve, it can:")
		for _, s := range a.Allows {
			b.WriteString("\n• " + s)
		}
	}
	if a.Note != "" {
		b.WriteString("\n\n" + a.Note)
	}
	if a.Unexpected != "" {
		b.WriteString("\n\n⚠️ *Unexpected:* " + a.Unexpected)
	}
	b.WriteString("\n\nWas this you? Reply !approve — or !deny if not.")
	if a.Ref != "" {
		b.WriteString("\n\n_ref " + a.Ref + "_")
	}
	return b.String()
}

// ApprovalsWaitingAlert tells the owner that several things wait for
// their approval at once, which one message lists.
func ApprovalsWaitingAlert(list []OwnerApproval, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🔐 %d things wait for your approval:\n", len(list))
	for i, a := range list {
		fmt.Fprintf(&b, "\n%d. %s", i+1, a.Subject)
		if !a.When.IsZero() {
			b.WriteString(" — " + when(a.When, now))
		}
		if a.Unexpected != "" {
			b.WriteString("\n   ⚠️ Unexpected: " + a.Unexpected)
		}
	}
	b.WriteString("\n\nReply !approve to tick the ones to approve, or !deny to tick the ones to turn down.")
	return b.String()
}

// ApprovalPickQuestion is the question of the multi-select poll !approve
// (approve true) or !deny sends while n things wait.
func ApprovalPickQuestion(approve bool, n int) string {
	if approve {
		return fmt.Sprintf("🔐 %d things wait for you. Tick the ones you approve.", n)
	}
	return fmt.Sprintf("🚫 %d things wait for you. Tick the ones you turn down.", n)
}

// ApprovalPickNotice follows that poll: how to vote, the refs of its
// options ("option: `ref`"), and how many more wait than it has room for.
func ApprovalPickNotice(approve bool, refs []string, more int) string {
	var b strings.Builder
	cmd := "!approve"
	if approve {
		b.WriteString("🗳️ Tick every one you approve in the poll above; each tick approves it. Nobody else's vote counts.")
	} else {
		cmd = "!deny"
		b.WriteString("🗳️ Tick every one you turn down in the poll above; each tick turns it down. Nobody else's vote counts.")
	}
	if len(refs) > 0 {
		b.WriteString("\n\nTheir refs, for `" + cmd + " <ref>`:")
		for _, r := range refs {
			b.WriteString("\n• " + r)
		}
	}
	if more > 0 {
		fmt.Fprintf(&b, "\n\n%d more wait than a poll has room for: send %s again for them.", more, cmd)
	}
	return b.String()
}

// NothingPendingReply answers !approve or !deny while nothing waits.
const NothingPendingReply = "✅ Nothing waits for your approval."

// ApprovalConfirmReply answers !approve (or a vote) for a that the owner
// may not have started, or that waited too long (why): it goes ahead
// only on a second !approve within minutes.
func ApprovalConfirmReply(a OwnerApproval, why string, minutes int) string {
	return fmt.Sprintf("⚠️ %s.\n\n%s\n\nTo be sure: reply !approve again within %d minutes to approve it, or !deny to turn it down.", a.Subject, why, minutes)
}

// ApprovalsConfirmReply answers a vote that ticked several of list, each
// asking for a second !approve for its reason in whys.
func ApprovalsConfirmReply(list []OwnerApproval, whys []string, minutes int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "⚠️ To be sure, %d of them wait for a second word:\n", len(list))
	for i, a := range list {
		fmt.Fprintf(&b, "\n%d. %s.\n   %s", i+1, a.Subject, whys[i])
	}
	fmt.Fprintf(&b, "\n\nReply !approve again within %d minutes to approve them all, or !deny to turn them all down.", minutes)
	return b.String()
}

// ApprovedReply answers an approval of what label names, and what it
// changes now (done).
func ApprovedReply(label, done string) string {
	s := "✅ Approved: " + label + "."
	if done != "" {
		s += " " + done
	}
	return s
}

// DeniedReply answers a denial of what label names, and what it
// changes now (done).
func DeniedReply(label, done string) string {
	s := "🚫 Turned down: " + label + "."
	if done != "" {
		s += " " + done
	}
	return s
}

// ApprovalPollStale answers a vote in a poll whose list changed since,
// or that the bridge no longer knows (it restarted).
const ApprovalPollStale = "⌛ That poll is out of date. Send !approve (or !deny) again for what waits now."

// ApprovalPollGone answers the ticks of a vote whose options no longer
// wait: approved or turned down elsewhere since.
func ApprovalPollGone(options []string) string {
	return "⌛ No longer waiting, so nothing done: " + strings.Join(options, "; ") + "."
}

// ApprovalFailedReply answers an approval or denial of what label names
// that did not go through: why.
func ApprovalFailedReply(label, why string) string {
	return "❌ That did not work for " + label + ": " + textx.Truncate(why, 300)
}

// The core's move from the owner's chat (#187 §3): !movecore <node>
// checks the move and asks with a poll whose options are CoreMoveNow and
// CoreMoveCancel; only the owner's vote counts, and no vote in time is a
// no. A move that ended asks whether the machine it left keeps the host
// secrets (CoreSecretsQuestion).
const (
	CoreMoveNow    = "🚚 Move now"
	CoreMoveCancel = "✋ Cancel"
)

// CoreMoveLockdownNotice answers !movecore, or a "Move now" vote, during
// a lockdown.
const CoreMoveLockdownNotice = "🔒 The bridge is in lockdown: the core does not move. `!unlock` first, then `!movecore` again."

// CoreMoveRunningNotice answers !movecore while a move runs or waits for
// its vote.
const CoreMoveRunningNotice = "🚚 A core move runs or waits for your vote already: one at a time."

// CoreMoveUsage answers !movecore with more than one word after it.
const CoreMoveUsage = "🚚 Send `!movecore <node>` to move the core to that machine, or `!movecore` alone to see where it can go."

// CoreMoveChecking answers !movecore <node> at once: the check takes a
// moment, and the report follows.
func CoreMoveChecking(target string) string {
	return fmt.Sprintf("🔎 Checking whether the core can move to *%s*…", target)
}

// CoreMoveHost is a machine !movecore lists.
type CoreMoveHost struct {
	Name, ID string
	Online   bool
	// New is a machine the owner has not confirmed as a core host: the
	// first move there lets it hold the secrets.
	New bool
	// Problem is the first thing that keeps the core from moving there,
	// "" when it can.
	Problem string
}

// CoreMoveTargets answers !movecore alone: where the core runs (runsOn,
// "" when no core host holds the lease) and where it can go.
func CoreMoveTargets(runsOn string, hosts []CoreMoveHost) string {
	var b strings.Builder
	if runsOn == "" {
		b.WriteString("🚚 I can't tell which machine runs the core: no core host says it holds the lease. A core that runs outside a node (a native install that is no core host) cannot move by itself; docs/CORE.md, \"The core moves itself\".")
	} else {
		fmt.Fprintf(&b, "🚚 The core runs on *%s*.", runsOn)
	}
	if len(hosts) == 0 {
		b.WriteString("\n\nNo other machine can take it yet. Every node you approved can, unless core-host is off there: `!join` adds one (a node in a box is the easiest).")
		return b.String()
	}
	b.WriteString("\n")
	for _, h := range hosts {
		fmt.Fprintf(&b, "\n• *%s* — ", h.Name)
		switch {
		case !h.Online:
			b.WriteString("offline")
		case h.Problem != "":
			b.WriteString("not now: " + textx.Truncate(h.Problem, 300))
		case runsOn == "":
			b.WriteString("a core host")
		case h.New:
			fmt.Fprintf(&b, "ready: `!movecore %s` (it never held the core: moving there lets it hold all of Fridolin's secrets)", h.Name)
		default:
			fmt.Fprintf(&b, "ready: `!movecore %s`", h.Name)
		}
	}
	return b.String()
}

// CoreMoveCheck is a move's pre-flight as the owner reads it.
type CoreMoveCheck struct {
	Source, Target, TargetID string
	TargetOS, TargetArch     string
	Box                      bool
	Fingerprint              string
	// Outage is the expected time offline, in words.
	Outage string
	// NotOnTarget is what stays on the source; Site what becomes of the
	// site ("" when it is off); Warnings the rest worth knowing.
	NotOnTarget []string
	Site        string
	Warnings    []string
	// Blocking keeps the move from running: no poll then.
	Blocking []string
	// PollMinutes is how long the poll waits for a vote.
	PollMinutes int
	// FirstTime is a target the owner has not confirmed as a core host:
	// the vote is what lets it hold the secrets, and FirstTimeNote says
	// what that means.
	FirstTime     bool
	FirstTimeNote string
}

// CoreMoveReport is !movecore's report: source → target, the expected
// time offline, what does not work on the target; and either what
// keeps the move from running or that the poll below asks.
func CoreMoveReport(c CoreMoveCheck) string {
	var b strings.Builder
	if len(c.Blocking) > 0 {
		fmt.Fprintf(&b, "🚫 The core cannot move from *%s* to *%s* now:\n", c.Source, c.Target)
		for _, p := range c.Blocking {
			b.WriteString("\n• " + textx.Truncate(p, 400))
		}
		fmt.Fprintf(&b, "\n\nFix that and send `!movecore %s` again. Nothing changed.", c.Target)
		return b.String()
	}
	kind := "a node"
	if c.Box {
		kind = "a box"
	}
	fmt.Fprintf(&b, "🚚 *Move the core from %s to %s?*\n\n", c.Source, c.Target)
	fmt.Fprintf(&b, "*%s* is %s on %s %s, id %s, device key `%s`.\n", c.Target, kind, osLabel(c.TargetOS), c.TargetArch, c.TargetID, c.Fingerprint)
	fmt.Fprintf(&b, "Offline: %s. Messages you send meanwhile wait and are answered from %s. If %s does not come up, %s starts again by itself.", c.Outage, c.Target, c.Target, c.Source)
	if c.FirstTime {
		fmt.Fprintf(&b, "\n\n🔐 *%s has never held the core.* %s", c.Target, c.FirstTimeNote)
	}
	if len(c.NotOnTarget) > 0 {
		fmt.Fprintf(&b, "\n\n*Not working on %s:*", c.Target)
		for _, m := range c.NotOnTarget {
			b.WriteString("\n• " + textx.Truncate(m, 300))
		}
	}
	if c.Site != "" {
		b.WriteString("\n\n🌐 *The site:* " + c.Site + ".")
	}
	if len(c.Warnings) > 0 {
		b.WriteString("\n\n*Good to know:*")
		for _, w := range c.Warnings {
			b.WriteString("\n• " + textx.Truncate(w, 300))
		}
	}
	fmt.Fprintf(&b, "\n\nWas this you? Vote in the poll below, or reply !approve — or !deny if not. No vote within %d minutes is a no. `!movecore %s` moves it back later.", c.PollMinutes, c.Source)
	return b.String()
}

// CoreMoveQuestion is the move poll's question.
func CoreMoveQuestion(source, target string) string {
	return fmt.Sprintf("🚚 Move the core from %s to %s now?", textx.Truncate(source, 60), textx.Truncate(target, 60))
}

// CoreMoveHereAlready answers !movecore for the machine that runs the
// core.
func CoreMoveHereAlready(name string) string {
	return fmt.Sprintf("🚚 The core runs on *%s* already.", name)
}

// CoreMoveNoSource answers !movecore <node> when no core host holds the
// lease.
const CoreMoveNoSource = "🚚 I can't tell which machine runs the core: no core host says it holds the lease, so the core cannot move by itself. `!movecore` lists the core hosts."

// CoreMoveStarted answers a "Move now" vote: the move runs.
func CoreMoveStarted(source, target, move string) string {
	return fmt.Sprintf("🚚 Moving the core from *%s* to *%s* (move %s). First %s gets ready while the core keeps running here; I'll say when the core stops.", source, target, move, target)
}

// CoreMoveNotStarted says a voted move did not start: why, from the
// check run again.
func CoreMoveNotStarted(target, why string) string {
	return fmt.Sprintf("🚫 The core did not move to *%s*: %s. Nothing changed.", target, textx.Truncate(why, 400))
}

// CoreMoveCancelled says a move's poll ended without a move: the owner
// cancelled (expired false), or did not vote in time.
func CoreMoveCancelled(target string, expired bool) string {
	if expired {
		return fmt.Sprintf("⌛ No vote in time: the core stays where it is and does not move to *%s*. Send `!movecore %s` again when you want it.", target, target)
	}
	return fmt.Sprintf("✋ Cancelled: the core stays where it is and does not move to *%s*.", target)
}

// CoreMoveStopping says the core stops on source now: the outage begins.
func CoreMoveStopping(source, target, outage string) string {
	return fmt.Sprintf("⏸️ %s is ready. Stopping the core on *%s* now. Expect %s offline; messages you send meanwhile wait and are answered from *%s*.", target, source, outage, target)
}

// CoreMoveEnded tells the owner how a core move ended (#187): state is
// the record's (done, rolled_back, failed, unknown), detail what the
// hosts said, which names no secret. notOnTarget is what the move's
// report said does not work on the target, for a move that is done.
func CoreMoveEnded(move, source, target, state, detail string, notOnTarget []string) string {
	switch state {
	case "done":
		var b strings.Builder
		fmt.Fprintf(&b, "🚚 Running on *%s* now (move %s): %s.", target, move, detail)
		if len(notOnTarget) > 0 {
			b.WriteString("\n\nNot working here:")
			for _, m := range notOnTarget {
				b.WriteString("\n• " + textx.Truncate(m, 300))
			}
		}
		fmt.Fprintf(&b, "\n\n`!movecore %s` moves the core back.", source)
		return b.String()
	case "rolled_back":
		return fmt.Sprintf("🔙 The core move to *%s* failed and the core runs on *%s* again (move %s): %s", target, source, move, detail)
	case "failed":
		return fmt.Sprintf("⚠️ The core move from *%s* to *%s* failed (move %s): %s", source, target, move, detail)
	}
	return fmt.Sprintf("❓ The core move from *%s* to *%s* (move %s) did not say how it ended: %s", source, target, move, detail)
}

// CoreSecretsQuestion asks, after a move, whether host keeps its copy of
// the host secrets; CoreSecretsKeep and CoreSecretsForget are the
// options. No vote in hours counts as forget.
func CoreSecretsQuestion(host string, hours int) string {
	return fmt.Sprintf("🔐 Keep the host secrets on %s? It no longer runs the core. Keeping them makes the next move faster; forgetting removes what moves brought it: the host secrets, the secret files and the core's data dir with the WhatsApp session, never what it held before. No vote within %d hours forgets them. Or reply !approve to keep them, !deny to forget them.", textx.Truncate(host, 60), hours)
}

// CoreSecretsKeep is the keep option of CoreSecretsQuestion.
func CoreSecretsKeep(host string) string {
	return "🔐 Keep them on " + textx.Truncate(host, 60)
}

// CoreSecretsForget is the forget option of CoreSecretsQuestion.
func CoreSecretsForget(host string) string {
	return "🧹 Forget them on " + textx.Truncate(host, 60)
}

// CoreSecretsKept answers a keep vote.
func CoreSecretsKept(host string) string {
	return fmt.Sprintf("🔐 *%s* keeps its copy of the host secrets, and what else moves brought it. Revoking it (`!revoke`) makes it forget them first.", host)
}

// CoreSecretsForgotten says what forgetting what moves brought host did
// (r), and whether no vote decided it (expired).
func CoreSecretsForgotten(host string, r CoreForgetReport, expired bool) string {
	why := ""
	if expired {
		why = "No vote in time, so: "
	}
	lines := r.removed()
	var msg string
	switch {
	case len(lines) == 0 && r.Failure != "":
		msg = fmt.Sprintf("⚠️ %s*%s* could not forget what moves brought it: %s. Revoking it while it is online (`!revoke`) makes it try again; docs/CORE.md, \"Forgetting a former core host\", says how to remove it by hand and which keys to rotate.", why, host, textx.Truncate(r.Failure, 300))
	case len(lines) == 0:
		msg = fmt.Sprintf("🧹 %s*%s* held nothing a move brought it: what it holds was there before, and stays.", why, host)
	default:
		msg = fmt.Sprintf("🧹 %s*%s* forgot what moves brought it:\n%s", why, host, strings.Join(lines, "\n"))
		if r.Failure != "" {
			msg += fmt.Sprintf("\n⚠️ Not all of it: %s. docs/CORE.md, \"Forgetting a former core host\", says how to remove the rest by hand.", textx.Truncate(r.Failure, 300))
		}
	}
	return msg + r.dataNote()
}

// CoreMoveSiteAnswers says the site answers at its domain once the core
// runs on target.
func CoreMoveSiteAnswers(domain, target string) string {
	return fmt.Sprintf("🌐 The site answers at %s, from *%s*.", domain, target)
}

// CoreMoveSiteDown says the site does not answer at its domain after the
// move: why, as the domain check says.
func CoreMoveSiteDown(domain, target, why string) string {
	return fmt.Sprintf("🌐 The site does not answer at %s since the core runs on *%s*: %s", domain, target, textx.Truncate(why, 300))
}

// NodeFrideApprovedNote follows !approve's answer when it approved fride:
// what that means (meaning, nodeproto.FrideApproval).
func NodeFrideApprovedNote(meaning string) string {
	return "⚠️ That includes fride. " + meaning + " `!revoke <id>` takes it back."
}

// NodeRevokedCoreSecretsNote follows !revoke's answer for a core host:
// what forgetting what moves brought it did there first (r).
func NodeRevokedCoreSecretsNote(r CoreForgetReport) string {
	lines := r.removed()
	switch {
	case len(lines) == 0 && r.Failure != "":
		return fmt.Sprintf("🔐 It is a core host and could not forget what moves brought it first (%s). If it held host secrets, rotate them: docs/CORE.md, \"A core host that is gone\".", textx.Truncate(r.Failure, 200)) + r.dataNote()
	case len(lines) == 0:
		return "🔐 It is a core host; it held nothing a move brought it." + r.dataNote()
	}
	msg := "🔐 It is a core host, and forgot what moves brought it first:\n" + strings.Join(lines, "\n")
	if r.Failure != "" {
		msg += fmt.Sprintf("\n⚠️ Not all of it: %s. docs/CORE.md, \"Forgetting a former core host\", says how to remove the rest by hand.", textx.Truncate(r.Failure, 200))
	}
	return msg + r.dataNote()
}
