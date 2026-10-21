package devchat

import (
	"context"
	"fmt"
	"log"
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
	chunk  func(string) []string
	mu     sync.Mutex
	outbox []Message
	polls  atomic.Int64
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
	// ID is a poll's message id, which a vote names (POST /api/dev/vote).
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

// SendPoll shows the poll in the dev chat, where it can be voted on at
// once: sending has its id first.
func (d *Messenger) SendPoll(_ context.Context, jid, q string, opts []string, _ int, sending func(id string)) (string, error) {
	id := fmt.Sprintf("dev-poll-%d-%d", time.Now().UnixNano(), d.polls.Add(1))
	if sending != nil {
		sending(id)
	}
	d.record(Message{JID: jid, Text: q, Poll: opts, ID: id})
	return id, nil
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
