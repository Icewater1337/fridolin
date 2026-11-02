package whatsapp

import (
	"strings"
	"sync"
	"testing"

	"github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// lockedBuf is a log meowcaller may write to from its goroutines.
type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// TestCallStackFitsWhatsmeow: meowcaller reaches into whatsmeow's own
// node handlers to see the calls' raw stanzas. A whatsmeow update that
// changes them would leave calls half working with only a line in the
// log; this fails instead, so the update waits until meowcaller fits.
func TestCallStackFitsWhatsmeow(t *testing.T) {
	t.Parallel()
	var logged lockedBuf
	meowcaller.NewClient(whatsmeow.NewClient(&store.Device{}, waLog.Noop), meowcaller.WithLogger(zerolog.New(&logged)))
	logged.mu.Lock()
	defer logged.mu.Unlock()
	if strings.Contains(logged.b.String(), "raw call adapter is unavailable") {
		t.Fatalf("meowcaller cannot hook this whatsmeow: %s", logged.b.String())
	}
}

// TestPlaceCallWithCallsOff: without calls.enabled nothing is placed.
func TestPlaceCallWithCallsOff(t *testing.T) {
	t.Parallel()
	tw := newTestWhatsApp(t)
	if _, err := tw.PlaceCall(t.Context(), "15551234567"); err != ErrCallsOff {
		t.Errorf("PlaceCall: %v, want ErrCallsOff", err)
	}
}
