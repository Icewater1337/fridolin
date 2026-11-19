package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// What the bridge shows of itself in its chats (whatsapp.presence): that
// it is online once it connects, which WhatsApp wants before a chat shows
// anyone typing; "typing…" while a run works on a chat's message; and
// read receipts for the messages a brain took, played ones for voice
// notes. The bridge decides when (outbound.Typist, internal/app); this
// says it to WhatsApp.

// Presencer is what presence.go needs from whatsmeow.Client; tests set a
// fake.
type Presencer interface {
	SendPresence(ctx context.Context, state types.Presence) error
	SendChatPresence(ctx context.Context, jid types.JID, state types.ChatPresence, media types.ChatPresenceMedia) error
	MarkRead(ctx context.Context, ids []types.MessageID, timestamp time.Time, chat, sender types.JID, receiptTypeExtra ...types.ReceiptType) error
}

// presencer is what chat states and receipts go out through.
func (wa *Client) presencer() Presencer {
	if wa.presence != nil {
		return wa.presence
	}
	return wa.cl()
}

// onlineTimeout bounds telling WhatsApp the bridge is online.
const onlineTimeout = 30 * time.Second

// showOnline tells WhatsApp the bridge is online, unless
// whatsapp.presence is off. It reports whether it did.
func (wa *Client) showOnline(ctx context.Context) bool {
	if !wa.cfg().Presence {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, onlineTimeout)
	defer cancel()
	if err := wa.presencer().SendPresence(ctx, types.PresenceAvailable); err != nil {
		wa.log.Warnf("Could not show the bridge online: %v", err)
		return false
	}
	wa.shownOnline.Store(true)
	return true
}

// Typing shows "typing…" in the chat jid (on), or takes it away. The
// bridge is shown online first if it is not yet: whatsapp.presence was
// turned on after it connected.
func (wa *Client) Typing(ctx context.Context, jid string, on bool) error {
	to, err := presenceChat(jid)
	if err != nil {
		return err
	}
	state := types.ChatPresencePaused
	if on {
		state = types.ChatPresenceComposing
		if !wa.shownOnline.Load() {
			wa.showOnline(ctx)
		}
	}
	return wa.classify(wa.presencer().SendChatPresence(ctx, to, state, types.ChatPresenceMediaText))
}

// MarkRead marks the messages ids in the chat jid as read: those the
// client handed on lately (see handledMessages), as WhatsApp addressed
// them. It knows no others, the ones a restart replayed say.
func (wa *Client) MarkRead(ctx context.Context, jid string, ids []string) error {
	return wa.receipt(ctx, jid, ids, types.ReceiptTypeRead)
}

// MarkPlayed marks the voice notes among the messages ids in the chat jid
// as played, as MarkRead does; the others it leaves.
func (wa *Client) MarkPlayed(ctx context.Context, jid string, ids []string) error {
	return wa.receipt(ctx, jid, ids, types.ReceiptTypePlayed)
}

func (wa *Client) receipt(ctx context.Context, jid string, ids []string, kind types.ReceiptType) error {
	var errs []error
	for _, b := range wa.handled.receipts(jid, ids, kind == types.ReceiptTypePlayed) {
		if err := wa.presencer().MarkRead(ctx, b.ids, time.Now(), b.chat, b.sender, kind); err != nil {
			errs = append(errs, err)
		}
	}
	return wa.classify(errors.Join(errs...))
}

// classify is Outbound.Classify, where there is one.
func (wa *Client) classify(err error) error {
	if err == nil || wa.outbound.Classify == nil {
		return err
	}
	return wa.outbound.Classify(err)
}

// presenceChat parses the chat a chat state goes to: a person's or a
// group's, never a broadcast list, a Status or a channel.
func presenceChat(jid string) (types.JID, error) {
	to, err := types.ParseJID(jid)
	if err != nil {
		return types.JID{}, fmt.Errorf("parsing JID %q: %w", jid, err)
	}
	switch to.Server {
	case types.DefaultUserServer, types.HiddenUserServer, types.GroupServer:
		return to.ToNonAD(), nil
	}
	return types.JID{}, fmt.Errorf("no chat state goes to %s", jid)
}

// isVoiceNote reports whether m is a voice note, recorded in the chat,
// rather than an audio file sent as one.
func isVoiceNote(m *waE2E.Message) bool { return m.GetAudioMessage().GetPTT() }

// handledKept is how many messages the client remembers where a receipt
// for goes: far more than wait in the brains' queues at once.
const handledKept = 2048

// handledMessages remembers, for the messages the client handed on
// lately, where a receipt for them goes: the chat, and in a group the
// sender, as WhatsApp addressed them, which can be a LID where the bridge
// goes by the number; and whether it was a voice note. They are known by
// the chat the bridge has them in and their id, so that a receipt only
// ever goes for a message of that chat.
type handledMessages struct {
	mu    sync.Mutex
	byKey map[string]handledMessage
	ring  [handledKept]string // the keys, the oldest at next once full
	next  int
}

type handledMessage struct {
	chat, sender types.JID
	voice        bool
}

func handledKey(chatJID, id string) string { return chatJID + "\x00" + id }

// add remembers the message id in chatJID, the bridge's name for its
// chat, the oldest one going once handledKept are kept.
func (h *handledMessages) add(chatJID string, id types.MessageID, m handledMessage) {
	if id == "" {
		return
	}
	key := handledKey(chatJID, string(id))
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.byKey == nil {
		h.byKey = map[string]handledMessage{}
	}
	if _, ok := h.byKey[key]; !ok {
		if old := h.ring[h.next]; old != "" {
			delete(h.byKey, old)
		}
		h.ring[h.next] = key
		h.next = (h.next + 1) % handledKept
	}
	h.byKey[key] = m
}

// receiptBatch is one receipt: messages of one sender in one chat, which
// WhatsApp takes together.
type receiptBatch struct {
	chat, sender types.JID
	ids          []types.MessageID
}

// receipts are the receipts for the messages ids in chatJID that it
// knows, the voice notes alone with voiceOnly, in the order of ids.
func (h *handledMessages) receipts(chatJID string, ids []string, voiceOnly bool) []receiptBatch {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []receiptBatch
	for _, id := range ids {
		m, ok := h.byKey[handledKey(chatJID, id)]
		if !ok || voiceOnly && !m.voice {
			continue
		}
		i := 0
		for i < len(out) && (out[i].chat != m.chat || out[i].sender != m.sender) {
			i++
		}
		if i == len(out) {
			out = append(out, receiptBatch{chat: m.chat, sender: m.sender})
		}
		out[i].ids = append(out[i].ids, types.MessageID(id))
	}
	return out
}
