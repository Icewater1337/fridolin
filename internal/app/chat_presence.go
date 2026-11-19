package app

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
)

// What the bridge shows of itself in its chats (whatsapp.presence):
// "typing…" while a run works on a chat's message, and read receipts for
// the messages a brain took, played ones for voice notes once its run
// answered them. The WhatsApp client shows the bridge online itself.

// receiptTimeout bounds one receipt, which nobody waits for.
const receiptTimeout = 30 * time.Second

// presence is the messenger's chat states and receipts, nil where it has
// none.
func (b *App) presence() outboundmsg.Presence {
	p, _ := b.wa.(outboundmsg.Presence)
	return p
}

// showsPresence reports whether the chat sees br work on m: a message
// someone wrote in a chat, not chatter the brain only reads, nothing
// handed in (a file, a schedule, a delegated task) and nothing whose
// answer stays out of the chat; whatsapp.presence on, and br not turning
// it off (brain.json's presence).
func (b *App) showsPresence(br *brain.Brain, m brain.Inbound) bool {
	if m.Silent || m.HandedIn() || m.AnswerOnly || !chat.IsDirectJID(m.JID) && !chat.IsGroupJID(m.JID) {
		return false
	}
	if !b.cfgs.Get().Live.WhatsApp.Presence {
		return false
	}
	if br != nil {
		if p := br.Manifest().Presence; p != nil && !*p {
			return false
		}
	}
	return true
}

// typing shows typing in m's chat while br's run for it works, and
// returns what ends it (nil for nothing to end).
func (b *App) typing(br *brain.Brain, m brain.Inbound) func() {
	if b.typist == nil || !b.showsPresence(br, m) {
		return nil
	}
	return b.typist.Start(m.JID)
}

// markRead marks the messages m is made of, which br took, as read; with
// played, once br's run answered them, the voice notes among them as
// played. It does not wait for WhatsApp.
func (b *App) markRead(br *brain.Brain, m brain.Inbound, played bool) {
	ids := m.MessageIDs()
	p := b.presence()
	if len(ids) == 0 || p == nil || !b.showsPresence(br, m) {
		return
	}
	mark, what := p.MarkRead, "read"
	if played {
		mark, what = p.MarkPlayed, "played"
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), receiptTimeout)
		defer cancel()
		if err := mark(ctx, m.JID, ids); err != nil && !errors.Is(err, outboundmsg.ErrOffline) {
			log.Printf("could not mark %d message(s) in %s as %s: %v", len(ids), m.JID, what, err)
		}
	}()
}

// tookMessage is the pipeline's Read: the brain br took the batch m.
func (b *App) tookMessage(br *brain.Brain, m brain.Inbound) { b.markRead(br, m, false) }
