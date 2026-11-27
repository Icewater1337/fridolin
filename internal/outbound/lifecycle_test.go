package outbound

import (
	"context"
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

// TestPollRefusesMoreOptionsThanWhatsAppTakes: #829. The nil store
// shows nothing was queued.
func TestPollRefusesMoreOptionsThanWhatsAppTakes(t *testing.T) {
	o := NewPausedOutbox(nil, nil, 0, nil)
	if err := o.Poll(context.Background(), "x@s.whatsapp.net", "Q?", make([]string, MaxPollOptions+1), "r"); err == nil {
		t.Fatal("a poll of 13 options was not refused")
	}
}
