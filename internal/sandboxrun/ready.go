package sandboxrun

import (
	"context"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// MaxAge is how old a look at the node may be before it is looked at
// again.
const MaxAge = 30 * time.Second

// State is what a look at the node found.
type State struct {
	// Node and Name are the node's id and name; "" when none was found.
	Node, Name string
	// Info is what sandbox.info answered.
	Info nodeproto.SandboxInfo
	// Why says why the node's sandboxes cannot run brains now, "" when
	// they can (a provider's login aside: Info.Ready says that).
	Why string
	// At is when the look was made.
	At time.Time
}

// Cache keeps the last look at the node, MaxAge old at most.
type Cache struct {
	look func(ctx context.Context) State
	now  func() time.Time

	mu      sync.Mutex
	last    State
	looking bool
}

// NewCache makes a cache that looks with look; now is the clock (nil:
// the system's).
func NewCache(look func(ctx context.Context) State, now func() time.Time) *Cache {
	if now == nil {
		now = time.Now
	}
	return &Cache{look: look, now: now}
}

// Fresh is a look at most MaxAge old, made now when the last is older.
func (c *Cache) Fresh(ctx context.Context) State {
	c.mu.Lock()
	last := c.last
	c.mu.Unlock()
	if !last.At.IsZero() && c.now().Sub(last.At) < MaxAge {
		return last
	}
	return c.Look(ctx)
}

// Peek is the last look without waiting: the dashboard's GET must not
// wait for a node. When it is older than MaxAge, a new look starts in the
// background. Before any look, it is the zero State.
func (c *Cache) Peek() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	if (c.last.At.IsZero() || c.now().Sub(c.last.At) >= MaxAge) && !c.looking {
		c.looking = true
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			c.Look(ctx)
			c.mu.Lock()
			c.looking = false
			c.mu.Unlock()
		}()
	}
	return c.last
}

// Change is what an observation changed about an outage.
type Change int

const (
	// Same: nothing to tell the owner.
	Same Change = iota
	// Down: the sandboxes stopped being ready; tell the owner once.
	Down
	// Back: they are ready again after an outage; tell the owner once.
	Back
)

// Outages follows whether the sandboxes are out, so the owner hears of
// an outage once, and once that it is over.
type Outages struct {
	mu    sync.Mutex
	down  bool
	since time.Time
	why   string
}

// Observe records whether the sandboxes were ready at now, and why not.
func (o *Outages) Observe(ready bool, why string, now time.Time) Change {
	o.mu.Lock()
	defer o.mu.Unlock()
	switch {
	case !ready && !o.down:
		o.down, o.since, o.why = true, now, why
		return Down
	case !ready:
		o.why = why
	case o.down:
		o.down, o.since, o.why = false, time.Time{}, ""
		return Back
	}
	return Same
}

// Since is when the outage began, zero when there is none.
func (o *Outages) Since() time.Time {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.since
}

// Look looks at the node now.
func (c *Cache) Look(ctx context.Context) State {
	s := c.look(ctx)
	s.At = c.now()
	c.mu.Lock()
	c.last = s
	c.mu.Unlock()
	return s
}
