package sandboxrun_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/sandboxrun"
)

// A look serves for MaxAge; Peek never waits and looks again in the
// background once the last look is stale.
func TestCache(t *testing.T) {
	t.Parallel()
	var looks atomic.Int32
	var now atomic.Int64
	clock := func() time.Time { return time.Unix(now.Load(), 0) }
	c := sandboxrun.NewCache(func(context.Context) sandboxrun.State {
		looks.Add(1)
		return sandboxrun.State{Node: "n1", Why: ""}
	}, clock)
	if s := c.Fresh(context.Background()); s.Node != "n1" || looks.Load() != 1 {
		t.Fatalf("first look: %+v, %d looks", s, looks.Load())
	}
	now.Add(29)
	c.Fresh(context.Background())
	if c.Peek(); looks.Load() != 1 {
		t.Errorf("a look 29 s old was made again: %d looks", looks.Load())
	}
	now.Add(2)
	c.Peek()
	deadline := time.Now().Add(5 * time.Second)
	for looks.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if looks.Load() != 2 {
		t.Errorf("a stale Peek did not look again: %d looks", looks.Load())
	}
}

// The owner hears of an outage once, and once that it is over.
func TestOutages(t *testing.T) {
	t.Parallel()
	var o sandboxrun.Outages
	t0 := time.Unix(1000, 0)
	steps := []struct {
		ready bool
		want  sandboxrun.Change
	}{{true, sandboxrun.Same}, {false, sandboxrun.Down}, {false, sandboxrun.Same}, {true, sandboxrun.Back}, {true, sandboxrun.Same}, {false, sandboxrun.Down}}
	for i, s := range steps {
		if got := o.Observe(s.ready, "offline", t0.Add(time.Duration(i)*time.Minute)); got != s.want {
			t.Errorf("step %d: %v, want %v", i, got, s.want)
		}
	}
	if !o.Since().Equal(t0.Add(5 * time.Minute)) {
		t.Errorf("since %v", o.Since())
	}
}
