package main

import (
	"fmt"
	"strings"

	"github.com/Icewater1337/fridolin/internal/textx"
)

// The messages the bridge itself sends into chats: its answers to the
// chat commands, its notices about a message and its run, and its alerts
// to the owner. A brain's answers are its own; these are the bridge's,
// and they live here, in one place, so that they are read, reworded and
// pinned (testdata/golden/messages.golden) together. Each starts with an
// emoji that says what kind of message it is, for the people who read
// it; code compares with the message itself, never with its emoji.

// noBrainNotice answers a message in a chat no brain takes.
const noBrainNotice = "🧠 No brain is set up for this chat yet. Open the bridge dashboard and add one."

// heldBrainNotice answers a message in a chat whose brain cannot run: it
// goes to no other brain instead.
const heldBrainNotice = "🧠 The brain for this chat cannot run right now. The Brains page of the bridge dashboard says why."

// resetReply answers !reset. afresh says that the chat starts afresh,
// which only a chat a brain answers can: its next message is read without
// the ones before.
func resetReply(afresh bool) string {
	if afresh {
		return "✅ Reset. I stopped what this chat had running or waiting, and the next message starts without its earlier messages. What I noted in my memory files stays."
	}
	return "✅ Cleared anything this chat had running or waiting."
}

// stopReply answers !stop, which stopped a run or not and dropped that
// many waiting messages.
func stopReply(stopped bool, dropped int) string {
	switch {
	case stopped:
		return "🛑 Stopped, and cleared anything this chat had waiting."
	case dropped > 0:
		return "🛑 Nothing was running for this chat; cleared what it had waiting."
	}
	return "🛑 Nothing was running for this chat."
}

// helpReply answers !help. groupTrigger is the trigger that addresses the
// bridge, in a group; "" in a direct chat.
func helpReply(groupTrigger string) string {
	help := "🤖 *Fridolin*\n\n" +
		"Send a message to talk to Claude. Images, documents, audio, and video are passed along as files.\n\n" +
		"*Commands:*\n" +
		"• `!help` — show this help\n" +
		"• `!stop` — stop what I'm doing for this chat and drop its queued messages\n" +
		"• `!reset` — like `!stop`, and the next message starts without this chat's earlier messages"
	if groupTrigger != "" {
		help += "\n\nIn this group, start your message with `" + groupTrigger + "` or reply to one of mine to address me, commands included."
	}
	return help
}

// queuedNotice tells the sender their message waits, at position pos.
func queuedNotice(pos int) string {
	return fmt.Sprintf("⏳ Queued (#%d) — I'll get to this right after the current task.", pos)
}

// queueFullNotice tells the sender their message was dropped because the
// brain's queue is full; stop is the !stop that addresses the bridge in
// the chat.
func queueFullNotice(stop string) string {
	return "⏳ I'm still busy with earlier messages and my queue is full. Try again in a bit, or send `" + stop + "` to drop what this chat has waiting."
}

// otpCaughtNotice tells the sender of a code the OTP guard caught what
// became of it.
const otpCaughtNotice = "🔐 That looks like the one-time code this chat was asked for, so I kept it out of the conversation: the assistant does not see it."

// A run that takes longer than workingNoticeAfter tells the chat it is
// working on it, and questions it asks mid-run go out marked as such.
const (
	workingNotice  = "⏳ Working on it..."
	questionPrefix = "❓ "
)

// What a chat is told about a run that failed (see chatErrorText). The
// error itself goes to the log and the dashboard: wrapped, it can hold the
// CLI's stderr, with local paths and sandbox denials, and in a group every
// member would read it.
const (
	chatErrCodexNotSignedIn = "🔑 Codex is not signed in for this brain. See the bridge dashboard for its sign-in command, then send your message again."
	chatErrCodexLimit       = "⏳ Codex's usage limit is reached for now. Try again later."
	chatErrNotSignedIn      = "🔑 Claude is not signed in on the bridge machine. Run `claude` there once and log in, then send your message again."
	chatErrLimit            = "⏳ Claude's usage limit is reached for now. Try again later."
	chatErrTimeout          = "⌛ That took too long and was stopped. Try again, perhaps with a smaller request."
	chatErrGeneric          = "❌ Something went wrong — details on the bridge dashboard."
)

// chatErrSandbox is what a chat is told about a run its sandbox kept from
// starting: err says why, without a path.
func chatErrSandbox(err error) string { return "❌ " + capitalize(err.Error()) + "." }

// chatErrClaude is the CLI's own short account of a failed run, msg, as a
// direct chat is told it.
func chatErrClaude(msg string) string { return "❌ Claude: " + textx.Truncate(msg, 300) }

// chatErrCodex is Codex's own short account, shown only in a direct chat.
func chatErrCodex(msg string) string { return "❌ Codex: " + textx.Truncate(msg, 300) }

// buildChatError is what a build chat on the Site's Build page shows for
// a message the builder brain could not take.
func buildChatError(err error) string { return "❌ " + err.Error() }

// viewOnceNotice is what a chat is told about a view-once message.
const viewOnceNotice = "👁️ I don't open view-once photos, videos or voice notes: I would keep them. Send it as a normal one if I should see it."

// mediaTooLargeNotice is what a chat is told about an attachment over the
// limit of maxBytes.
func mediaTooLargeNotice(maxBytes int64) string {
	return fmt.Sprintf("⚠️ That file is over the %d MB limit, so I skipped it.", maxBytes>>20)
}

// mediaFailedNotice is what a chat is told about an attachment that could
// not be downloaded.
const mediaFailedNotice = "⚠️ I couldn't download that file. Please send it again."

// pollAsText is a poll as a numbered list, for when WhatsApp refuses it
// as a poll.
func pollAsText(p pollData) string {
	text := "📊 *" + p.Question + "*\n"
	for i, opt := range p.Options {
		text += fmt.Sprintf("%d. %s\n", i+1, opt)
	}
	return strings.TrimSpace(text)
}

// startupCheckAlert tells the owner what the startup check found missing,
// msg.
func startupCheckAlert(msg string) string { return "⚠️ Bridge startup check: " + msg }

// backupRefusedAlert tells the owner that the scheduled backup of the
// brain named name refused to run, and why.
func backupRefusedAlert(name, why string) string {
	return fmt.Sprintf("⚠️ Backup of %s: %s\n\nCheck the brain folder, then the Backup page in the Fridolin dashboard. "+
		"Scheduled backups of this brain keep refusing until one succeeds; this is the only message about it.", name, why)
}

// plaudGivenUpAlert tells the chat a brain's Plaud summaries go to that
// the recording name will not reach brain, and why.
func plaudGivenUpAlert(name, brain, why string) string {
	return fmt.Sprintf("⚠️ Plaud: the recording “%s” was not handed to brain %s: %s. It is still in Plaud, and the dashboard lists it (Integrations → Plaud).", name, brain, why)
}

// plaudExpiredAlert tells the owner that the Plaud login expired.
const plaudExpiredAlert = "⚠️ Plaud: the login expired, so new recordings are not arriving. Reconnect in the Fridolin dashboard (Integrations → Plaud)."

// What the bridge tells a chat about its own restart.
const (
	// restartedNotice tells a chat, once the bridge is back, that a stop
	// cut short the run for its message. The run is not made again: it
	// may have done some of what it was asked already, an event created or
	// a message sent, and would do it twice.
	restartedNotice = "🔄 I restarted while I was working on your message. Send it again if you still need an answer."
)
