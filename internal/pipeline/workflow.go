package pipeline

import (
	"context"
	"errors"
	"log"
	"slices"
	"strings"
	"time"

	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
)

// Settings is one operation's live policy snapshot. Runtime routing
// stays with the Manager; transport and host capabilities are supplied below.
type Settings struct {
	FirstTrigger string
	QueueNotify  bool
	OwnerChat    string
}

// Workflow owns message decisions independently of bridge lifecycle.
// The command intake wrapper still owns admission and persistence on shutdown.
type Workflow struct {
	Brains       *brainruntime.Manager
	Debouncer    *Debouncer
	Settings     func() Settings
	Notice       func(context.Context, string, string) error
	HandOffCode  func(*brainruntime.Brain, brainruntime.Inbound, brainruntime.OTPCatch)
	Chats        func(brainruntime.Inbound) []string
	ReplyChat    func(*brainruntime.Brain, string) string
	SendChunked  func(context.Context, string, string) error
	AppendSent   func(chat.Principal, string, string, string)
	ResolveMedia func(string, []string, bool) ([]string, error)
	HandOff      func(*brainruntime.Brain, string, string, string, []string)
}

// handleInbound is the entry point for every allowed message. Access control
// already happened, for WhatsApp and dev messages alike: groups against
// allowed_groups and their senders against group_members (whom
// allowed_numbers admits, for a group without an entry), DMs against
// allowed_numbers. Group messages without the trigger arrive Silent, and go
// on to their brain like the rest: it keeps them in its history. Once the
// bridge stops, a message is kept for the next start instead: no command
// runs and no notice goes out (see intake).
func (p *Workflow) Handle(ctx context.Context, m brainruntime.Inbound) {
	m.Text = strings.TrimSpace(m.Text)
	settings := p.Settings()

	// Commands come from a chat. A message handed to a brain (/ingest, Plaud,
	// a dev message that names one) carries none: its text is for that brain.
	// A command must also be addressed to the bridge. In a group, an untagged
	// "!stop" is someone talking to the others: it must not stop anything,
	// and the bridge must not answer it.
	if m.Source != chat.SourceIngest && m.Brain == "" && !m.Silent && p.handleCommand(ctx, m, settings) {
		return
	}
	if m.Text == "" && len(m.Media) == 0 && m.Quoted == nil {
		return
	}

	// Resolved like the run itself (BrainManager.Submit), so the OTP guard
	// checks the brain that would read the message.
	br := p.Brains.Route(m)
	if br == nil && m.Brain != "" {
		log.Printf("brain %s is not configured; dropping the message for %s", m.Brain, m.JID)
		return
	}
	if br == nil {
		reply := outboundmsg.NoBrainNotice
		if id := p.Brains.HeldFor(m.JID, m.IsGroup()); id != "" {
			// Not the default brain instead: what was meant for this
			// brain, your DMs say, must not land in another one.
			log.Printf("brain %s, which handles %s, cannot run (see its issue on the Brains page); the message goes to no other brain", id, m.JID)
			reply = outboundmsg.HeldBrainNotice
		} else {
			log.Printf("No brain configured for %s — add one in the dashboard", m.JID)
		}
		if !m.Silent && m.Source != chat.SourceIngest {
			_ = p.Notice(ctx, m.JID, reply)
		}
		return
	}
	// The OTP guard runs before anything is written to history, so a
	// two-factor code the chat was asked for is neither logged nor sent to
	// the model. Whoever sent it is told, unless it was untagged chatter,
	// which the bridge never answers: a code that vanished without a word
	// looks lost.
	if code, ok := br.CatchesCode(m, time.Now()); ok {
		log.Printf("brain %s: kept a one-time code out of the conversation (OTP guard)", br.ID())
		p.HandOffCode(br, m, code)
		if !m.Silent {
			_ = p.Notice(ctx, m.JID, outboundmsg.OtpCaughtNotice)
		}
		return
	}

	p.Debouncer.Add(m)
}

// handleCommand runs !help, !reset and !stop. It reports whether text was one.
func (p *Workflow) handleCommand(ctx context.Context, m brainruntime.Inbound, settings Settings) bool {
	switch strings.ToLower(m.Text) {
	case "!reset":
		// A reset stops what !stop stops for the chat, hand-offs from other
		// brains included, and starts the chat afresh: a brain run never
		// resumes a session, each starts from the chat's recent history,
		// which the next one reads only from the reset on. Say only what
		// happened: the brain's memory files stay, and a chat no brain
		// answers has no history to start afresh.
		p.stopChat(m)
		_ = p.Notice(ctx, m.JID, outboundmsg.ResetReply(p.resetChat(m)))
		return true

	case "!stop":
		_ = p.Notice(ctx, m.JID, outboundmsg.StopReply(p.stopChat(m)))
		return true

	case "!help":
		trigger := ""
		if m.IsGroup() {
			trigger = settings.FirstTrigger
		}
		_ = p.Notice(ctx, m.JID, outboundmsg.HelpReply(trigger))
		return true
	}
	return false
}

// stopChat stops what is going on for m's chat, for !stop and !reset. It
// reports whether a run was stopped and how many waiting messages were
// dropped.
func (p *Workflow) stopChat(m brainruntime.Inbound) (stopped bool, dropped int) {
	// Only this chat's work: a brain serves several chats, and one chat must
	// not cancel what another asked for. A DM goes by two JIDs (see
	// chatJIDs), and its run may have come in under either.
	chats := p.Chats(m)
	for _, jid := range chats {
		p.Debouncer.Drop(jid)
	}
	if br := p.Brains.BrainFor(m.JID, m.IsGroup()); br != nil {
		for _, jid := range chats {
			s, d := br.StopChat(jid)
			stopped, dropped = stopped || s, dropped+d
		}
	}
	// A file or recording handed to another brain may answer here too, and
	// the chat's own brain knows nothing about it. What waits for one counts
	// as this chat's, for the reply.
	for _, jid := range chats {
		s, d := p.Brains.StopHandOffs(jid)
		stopped, dropped = stopped || s, dropped+d
	}
	return stopped, dropped
}

// resetChat starts m's chat afresh (Brain.ResetChat) in the brain that
// answers there and in every brain whose reply chat it is, which answers
// files handed to it there: !reset is about the chat, whichever brain
// speaks in it. It reports whether any brain does.
func (p *Workflow) resetChat(m brainruntime.Inbound) bool {
	settings := p.Settings()
	chats := p.Chats(m)
	owner := p.Brains.BrainFor(m.JID, m.IsGroup())
	reset := false
	for _, br := range p.Brains.Brains() {
		if br == owner || slices.Contains(chats, p.ReplyChat(br, settings.OwnerChat)) {
			br.ResetChat(chats, m.Sender)
			reset = true
		}
	}
	return reset
}

// dispatch hands a (debounced) message to its brain.
func (p *Workflow) Dispatch(m brainruntime.Inbound) {
	ctx := context.Background()
	settings := p.Settings()
	br, pos, busy, err := p.Brains.Submit(m)
	switch {
	case errors.Is(err, brainruntime.ErrQueueFull):
		log.Printf("brain %s: queue full, rejecting message from %s", br.ID(), m.Sender)
		if !m.Silent {
			// !stop drops only this chat's work, and in a group it needs the
			// trigger like any command.
			stop := "!stop"
			if t := settings.FirstTrigger; m.IsGroup() && t != "" {
				stop = t + " !stop"
			}
			_ = p.Notice(ctx, m.JID, outboundmsg.QueueFullNotice(stop))
		}
	case err != nil:
		log.Printf("routing message for %s: %v", m.JID, err)
	case busy && !m.Silent && settings.QueueNotify:
		// Interrupt mode queues too: behind other chats and other people.
		_ = p.Notice(ctx, m.JID, outboundmsg.QueuedNotice(pos))
	}
}
