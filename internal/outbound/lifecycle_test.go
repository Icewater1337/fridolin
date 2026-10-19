package outbound

import (
	"sync"
	"testing"
)

func TestPausedOutboxStopsWithoutStartingDelivery(t *testing.T) {
	// A nil store is deliberate: touching it would mean delivery began before
	// Start, or that calling Start after Stop resurrected the delivery loop.
	o := NewPausedOutbox(nil, nil, 0, nil)
	var calls sync.WaitGroup
	for range 8 {
		calls.Go(o.Stop)
	}
	calls.Wait()
	o.Start()
	select {
	case <-o.done:
	default:
		t.Fatal("Stop did not complete")
	}
}
