package outbound

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

// Presence is a Transport that also shows what the bridge does in a
// chat: that it types while a run works on the chat's message, and which
// messages it read.
type Presence interface {
	// Typing shows "typing…" in the chat jid (on), or takes it away.
	Typing(ctx context.Context, jid string, on bool) error
	// MarkRead marks the messages ids in the chat jid as read.
	MarkRead(ctx context.Context, jid string, ids []string) error
	// MarkPlayed marks the voice notes among the messages ids in the
	// chat jid as played; the others it leaves.
	MarkPlayed(ctx context.Context, jid string, ids []string) error
}

// TypingEvery is how often a Typist says again that the bridge types:
// WhatsApp takes "typing…" away by itself after about 25 seconds.
const TypingEvery = 20 * time.Second

// presenceTimeout bounds one chat state or receipt, which nobody waits
// for.
const presenceTimeout = 10 * time.Second

// Typist keeps "typing…" up in the chats where runs work, from the first
// run's start to the last one's end there, and takes it away then.
type Typist struct {
	presence func() Presence // nil, or one returning nil: nothing shows
	every    time.Duration

	mu    sync.Mutex
	chats map[string]*typing
}

// typing is one chat's: how many runs work there, and the wake that tells
// its goroutine when that fell to none.
type typing struct {
	runs int
	wake chan struct{}
}

// NewTypist makes a Typist that shows typing through what presence
// returns at the time, saying it again every every.
func NewTypist(presence func() Presence, every time.Duration) *Typist {
	return &Typist{presence: presence, every: every, chats: map[string]*typing{}}
}

// Start shows typing in the chat jid until end is called. Runs in the
// same chat share it: it stays until the last of them ended. Calling end
// more than once ends it once.
func (t *Typist) Start(jid string) (end func()) {
	t.mu.Lock()
	c, ok := t.chats[jid]
	if !ok {
		c = &typing{wake: make(chan struct{}, 1)}
		t.chats[jid] = c
		go t.keep(jid, c)
	}
	c.runs++
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			c.runs--
			if c.runs == 0 {
				select {
				case c.wake <- struct{}{}:
				default:
				}
			}
			t.mu.Unlock()
		})
	}
}

// keep is one chat's goroutine: it says the bridge types while runs work
// there, again every t.every, and that it stopped once none does. A run
// that starts while it says so takes it on again; with none left, it
// ends, in order, so that no later typing is taken away by an earlier
// pause.
func (t *Typist) keep(jid string, c *typing) {
	tick := time.NewTicker(t.every)
	defer tick.Stop()
	on, failed := false, false
	for {
		t.mu.Lock()
		want := c.runs > 0
		if !want && !on {
			delete(t.chats, jid)
			t.mu.Unlock()
			return
		}
		t.mu.Unlock()
		if err := t.send(jid, want); err != nil && !failed && !errors.Is(err, ErrOffline) {
			// Once in a stretch: it would say the same every t.every.
			log.Printf("could not show typing in %s: %v", jid, err)
			failed = true
		}
		on = want
		if !want {
			continue
		}
		select {
		case <-tick.C:
		case <-c.wake:
		}
	}
}

func (t *Typist) send(jid string, on bool) error {
	if t.presence == nil {
		return nil
	}
	p := t.presence()
	if p == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), presenceTimeout)
	defer cancel()
	return p.Typing(ctx, jid, on)
}
