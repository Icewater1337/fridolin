package claudecli

import (
	"context"
	"errors"
	"sync"
	"time"
)

// A live session is one interactive Claude Code process a brain keeps
// running, which the bridge wakes with each message (docs/BRAINS.md,
// "Live session"). The message reaches it through a Claude Code channel:
// the session's MCP server (fridolin mcp --live) asks the bridge for the
// next event and passes it on as a channel notification. The end of its
// turn, and its answer, come back through the session's Stop hook. The
// Hub is where the bridge's side of that meets the two.

// LiveEvent is one message for the session: Content is what the model
// reads, Meta the attributes of its <channel> tag (names of letters,
// digits and underscores).
type LiveEvent struct {
	Content string            `json:"content"`
	Meta    map[string]string `json:"meta,omitempty"`
}

// ErrLiveBusy is a turn started while another one is under way.
var ErrLiveBusy = errors.New("the live session is busy with another turn")

// ErrLiveFull is an event the session's channel has no room for.
var ErrLiveFull = errors.New("the live session is not taking events")

// LiveHub passes one session's events to its MCP server and its turn's end
// back. It serves one turn at a time.
type LiveHub struct {
	events chan LiveEvent

	mu   sync.Mutex
	turn chan string // the armed turn's answer; nil without one

	connected     chan struct{}
	connectedOnce sync.Once
}

// NewLiveHub makes the hub of a session.
func NewLiveHub() *LiveHub {
	return &LiveHub{events: make(chan LiveEvent, 4), connected: make(chan struct{})}
}

// Connected closes once the session's MCP server first asked for events,
// which shows the CLI started it.
func (h *LiveHub) Connected() <-chan struct{} { return h.connected }

// Next waits up to wait for the next event for the session's MCP server.
// It returns false when there is none by then, or ctx ends.
func (h *LiveHub) Next(ctx context.Context, wait time.Duration) (LiveEvent, bool) {
	h.connectedOnce.Do(func() { close(h.connected) })
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case ev := <-h.events:
		return ev, true
	case <-t.C:
	case <-ctx.Done():
	}
	return LiveEvent{}, false
}

// Begin arms the next turn and returns the channel its answer comes on.
// Call it before Deliver, so that an answer cannot come before anyone
// waits for it. End disarms it.
func (h *LiveHub) Begin() (<-chan string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.turn != nil {
		return nil, ErrLiveBusy
	}
	h.turn = make(chan string, 1)
	return h.turn, nil
}

// Abort disarms the turn Begin armed, when it ended some other way.
func (h *LiveHub) Abort() {
	h.mu.Lock()
	h.turn = nil
	h.mu.Unlock()
}

// Deliver queues ev for the session's MCP server.
func (h *LiveHub) Deliver(ev LiveEvent) error {
	select {
	case h.events <- ev:
		return nil
	default:
		return ErrLiveFull
	}
}

// End is the session's Stop hook: the model finished a turn, whose last
// text was text. It reports whether a turn waited for it; a stop nobody
// waits for, such as one a background task caused, is not an answer.
func (h *LiveHub) End(text string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.turn == nil {
		return false
	}
	h.turn <- text
	h.turn = nil
	return true
}
