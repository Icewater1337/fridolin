package devchat

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/textx"
)

// ── dev mode ─────────────────────────────────────────────────────────────

// Messenger stands in for WhatsApp in --no-whatsapp mode. Outgoing
// messages are kept in memory and shown on the dashboard; incoming ones are
// injected through the dev API.
type Messenger struct {
	chunk   func(string) []string
	mu      sync.Mutex
	outbox  []Message
	signals []Signal     // what Signals returns
	ids     atomic.Int64 // numbers the ids of polls and notices
}

// New takes the current chunking policy. Each SendChunked call
// evaluates it once, retaining live MaxChunkSize without a Config dependency.
func New(chunk func(string) []string) *Messenger { return &Messenger{chunk: chunk} }

// Message is one message the bridge "sent" in dev mode.
type Message struct {
	TS   time.Time `json:"ts"`
	JID  string    `json:"jid"`
	Text string    `json:"text"`
	Poll []string  `json:"poll,omitempty"`
	// ID is a poll's message id, which a vote names (POST /api/dev/vote),
	// or a notice's (SendMessageWithID), which a reply may quote.
	ID string `json:"id,omitempty"`
}

// devConnected is always closed: dev mode can always send.
var devConnected = func() chan struct{} { c := make(chan struct{}); close(c); return c }()

func (d *Messenger) Connected() <-chan struct{} { return devConnected }
func (d *Messenger) Connect() error             { return nil }
func (d *Messenger) Disconnect()                {}
func (d *Messenger) State() chat.LinkState {
	return chat.LinkState{Mode: "disabled", Detail: "Dev mode (--no-whatsapp): messages are simulated"}
}
func (d *Messenger) StartPairing() error {
	return fmt.Errorf("WhatsApp is disabled in dev mode")
}
func (d *Messenger) JoinedGroups(context.Context) ([]chat.Group, error) { return nil, nil }

func (d *Messenger) record(m Message) {
	m.TS = time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.outbox = append(d.outbox, m)
	if len(d.outbox) > 200 {
		d.outbox = d.outbox[len(d.outbox)-200:]
	}
	log.Printf("[dev] → %s: %s", m.JID, textx.Truncate(m.Text, 200))
}

func (d *Messenger) SendMessage(_ context.Context, jid, text string) error {
	d.record(Message{JID: jid, Text: text})
	return nil
}

func (d *Messenger) SendChunked(ctx context.Context, jid, text string) error {
	for _, c := range d.chunk(text) {
		d.record(Message{JID: jid, Text: c})
	}
	return nil
}

// SendMessageWithID shows text in the dev chat with an id of its own,
// which sending has first.
func (d *Messenger) SendMessageWithID(_ context.Context, jid, text string, sending func(id string)) error {
	id := fmt.Sprintf("dev-msg-%d-%d", time.Now().UnixNano(), d.ids.Add(1))
	if sending != nil {
		sending(id)
	}
	d.record(Message{JID: jid, Text: text, ID: id})
	return nil
}

// SendPoll shows the poll in the dev chat, where it can be voted on at
// once: sending has its id first.
func (d *Messenger) SendPoll(_ context.Context, jid, q string, opts []string, _ int, sending func(id string)) (string, error) {
	id := fmt.Sprintf("dev-poll-%d-%d", time.Now().UnixNano(), d.ids.Add(1))
	if sending != nil {
		sending(id)
	}
	d.record(Message{JID: jid, Text: q, Poll: opts, ID: id})
	return id, nil
}

// Signal is a chat state or receipt the bridge "sent" in dev mode
// (outbound.Presence).
type Signal struct {
	JID string
	// Kind is SignalTyping, SignalPaused, SignalRead or SignalPlayed.
	Kind string
	IDs  []string // a receipt's messages
}

// The kinds of Signal.
const (
	SignalTyping = "typing"
	SignalPaused = "paused"
	SignalRead   = "read"
	SignalPlayed = "played"
)

func (d *Messenger) signal(s Signal) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.signals = append(d.signals, s)
	if len(d.signals) > 200 {
		d.signals = d.signals[len(d.signals)-200:]
	}
}

// Typing records that the bridge shows typing in jid, or stopped.
func (d *Messenger) Typing(_ context.Context, jid string, on bool) error {
	kind := SignalPaused
	if on {
		kind = SignalTyping
	}
	d.signal(Signal{JID: jid, Kind: kind})
	return nil
}

// MarkRead records that the bridge marked ids in jid as read.
func (d *Messenger) MarkRead(_ context.Context, jid string, ids []string) error {
	d.signal(Signal{JID: jid, Kind: SignalRead, IDs: slices.Clone(ids)})
	return nil
}

// MarkPlayed records that the bridge marked the voice notes among ids in
// jid as played; dev mode cannot tell which they are.
func (d *Messenger) MarkPlayed(_ context.Context, jid string, ids []string) error {
	d.signal(Signal{JID: jid, Kind: SignalPlayed, IDs: slices.Clone(ids)})
	return nil
}

// Signals returns the recorded chat states and receipts, oldest first.
func (d *Messenger) Signals() []Signal {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.signals)
}

// Outbox returns the recorded messages, newest first.
func (d *Messenger) Outbox() []Message {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Message, 0, len(d.outbox))
	for i := len(d.outbox) - 1; i >= 0; i-- {
		out = append(out, d.outbox[i])
	}
	return out
}
