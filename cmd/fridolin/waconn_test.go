package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/testutil"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// Every item that ends a pairing leads to a state the dashboard offers to
// pair again from, or that says why not: an item without an error used to
// leave the state at "pairing" with a stale code.
func TestPairingState(t *testing.T) {
	cases := []struct {
		name string
		item whatsmeow.QRChannelItem
		mode string
		has  string // part of the detail
	}{
		{"success", whatsmeow.QRChannelSuccess, modeConnected, "Paired"},
		{"timeout", whatsmeow.QRChannelTimeout, modeQRExpired, "expired"},
		{"client outdated", whatsmeow.QRChannelClientOutdated, modeOutdated, "Update Fridolin"},
		{"no multi-device", whatsmeow.QRChannelScannedWithoutMultidevice, modePairing, "multi-device"},
		{"unexpected state", whatsmeow.QRChannelErrUnexpectedEvent, modeQRExpired, "start pairing again"},
		{"an error", whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventError, Error: errors.New("bad signature")}, modeQRExpired, "bad signature"},
		{"a passkey", whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventPasskeyRequest}, modeQRExpired, "does not support"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mode, detail, ok := pairingState(c.item)
			if !ok || mode != c.mode || !strings.Contains(detail, c.has) {
				t.Errorf("pairingState = %q, %q, %v; want %q with %q", mode, detail, ok, c.mode, c.has)
			}
		})
	}
}

// The events after which nothing works say so, with what to do: they used
// to leave the dashboard at "connecting" or "connected".
func TestConnectionState(t *testing.T) {
	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.Local)
	cases := []struct {
		name  string
		evt   any
		retry time.Duration
		mode  string
		has   string
	}{
		{"connected", &events.Connected{}, 0, modeConnected, ""},
		{"disconnected", &events.Disconnected{}, 0, modeDisconnected, "reconnecting"},
		{"keepalive timeout", &events.KeepAliveTimeout{ErrorCount: 2, LastSuccess: now.Add(-time.Minute)}, 0, modeConnecting, "08:59"},
		{"keepalive restored", &events.KeepAliveRestored{}, 0, modeConnected, ""},
		{"replaced", &events.StreamReplaced{}, 0, modeReplaced, "second bridge"},
		{"logged out", &events.LoggedOut{Reason: events.ConnectFailureLoggedOut}, 0, modeLoggedOut, "bridge.db can stay"},
		{"outdated", &events.ClientOutdated{}, 0, modeOutdated, "git pull && ./setup.sh"},
		{"banned", &events.TemporaryBan{Code: events.TempBanBlockedByUsers, Expire: 2 * time.Hour}, 0, modeBanned, "Oct 18 11:00"},
		{"refused", &events.ConnectFailure{Reason: events.ConnectFailureBadUserAgent, Message: "nope"}, 0, modeFailed, "Restart the bridge"},
		// whatsmeow dispatches no ConnectFailure for a 500 or a 503,
		// which it reconnects after by itself; a 501 it hands on.
		{"refused, WhatsApp's fault", &events.ConnectFailure{Reason: events.ConnectFailureExperimental}, 2 * time.Minute, modeFailed, "tries again in 2m0s"},
		{"another permanent disconnect", &events.CATRefreshError{Error: errors.New("x")}, 0, modeFailed, "CAT refresh failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mode, detail, ok := connectionState(c.evt, now, c.retry)
			if !ok || mode != c.mode || !strings.Contains(detail, c.has) {
				t.Errorf("connectionState = %q, %q, %v; want %q with %q", mode, detail, ok, c.mode, c.has)
			}
		})
	}
	if _, _, ok := connectionState(&events.Receipt{}, now, 0); ok {
		t.Error("a receipt changed the state")
	}
}

func TestFailureRetryAfter(t *testing.T) {
	cases := map[time.Duration]time.Duration{
		0:                      connectFailureRetry,
		time.Minute:            2 * time.Minute,
		16 * time.Minute:       connectFailureMaxRetry,
		connectFailureMaxRetry: connectFailureMaxRetry,
	}
	for prev, want := range cases {
		if got := failureRetryAfter(prev); got != want {
			t.Errorf("failureRetryAfter(%v) = %v, want %v", prev, got, want)
		}
	}
}

// Failures on WhatsApp's side that go on wait longer each time before the
// bridge connects again, with one attempt scheduled at a time, and none
// once it is connected: each used to start a timer of its own, a minute
// apart forever.
func TestConnectFailuresBackOff(t *testing.T) {
	tw := newTestWhatsApp(t)
	fail := &events.ConnectFailure{Reason: events.ConnectFailureExperimental}
	tw.connectionEvent(fail)
	if st := tw.State(); st.Mode != modeFailed || !strings.Contains(st.Detail, "tries again in 1m0s") {
		t.Errorf("state = %+v, want a retry in a minute", st)
	}
	tw.mu.Lock()
	first := tw.retry
	tw.mu.Unlock()
	tw.connectionEvent(fail)
	if st := tw.State(); !strings.Contains(st.Detail, "tries again in 2m0s") {
		t.Errorf("state = %+v, want the wait doubled", st)
	}
	if first.Stop() {
		t.Error("the first attempt is still scheduled next to the second")
	}
	tw.mu.Lock()
	second := tw.retry
	tw.mu.Unlock()
	// A connection made otherwise ends the wait: its attempt used to stay
	// scheduled, and fire at a connected client.
	tw.client = whatsmeow.NewClient(&store.Device{}, waLog.Noop) // the account Connected reads
	tw.connectionEvent(&events.Connected{})
	if second.Stop() {
		t.Error("the attempt is still scheduled after a connection")
	}
	tw.connectionEvent(fail)
	if st := tw.State(); !strings.Contains(st.Detail, "tries again in 1m0s") {
		t.Errorf("state = %+v, want the wait started over", st)
	}
	tw.Disconnect()
	tw.mu.Lock()
	last := tw.retry
	tw.mu.Unlock()
	if last.Stop() {
		t.Error("the attempt is still scheduled after Disconnect")
	}
}

// After a logout the dashboard shows no number and offers to pair; the
// number used to stay, which hid the Pair button.
func TestLogoutClearsTheOwnNumber(t *testing.T) {
	tw := newTestWhatsApp(t)
	tw.setOwn(types.NewJID(otherNumber, types.DefaultUserServer), types.NewJID(bridgeLID, types.HiddenUserServer))
	tw.setState(modeConnected, "")
	if got := tw.State().OwnNumber; got != otherNumber {
		t.Fatalf("own number = %q, want %s", got, otherNumber)
	}
	tw.connectionEvent(&events.LoggedOut{})
	if st := tw.State(); st.Mode != modeLoggedOut || st.OwnNumber != "" {
		t.Errorf("state = %+v, want logged out without a number", st)
	}
	if own := tw.ownMentionJIDs(); len(own) != 0 {
		t.Errorf("own JIDs = %v, want none", own)
	}
}

// Connected is closed while the state is "connected", and open otherwise.
func TestConnectedFollowsTheState(t *testing.T) {
	tw := newTestWhatsApp(t)
	if isConnected(tw) {
		t.Fatal("connected before any event")
	}
	ch := tw.Connected()
	tw.setState(modeConnected, "")
	select {
	case <-ch:
	default:
		t.Fatal("the channel from before did not close on connecting")
	}
	tw.setState(modeConnected, "again")
	if !isConnected(tw) {
		t.Error("not connected after a second connected state")
	}
	tw.setState(modeDisconnected, "")
	if isConnected(tw) {
		t.Error("still connected after disconnecting")
	}
}

// A pairing ends with its context, which a new one or the shutdown
// cancels, and so does the goroutine that follows its codes: whatsmeow
// does not close the channel of a pairing that a disconnect replaced.
func TestFollowPairingEndsWithItsContext(t *testing.T) {
	tw := newTestWhatsApp(t)
	tw.pairing = true
	ctx, cancel := context.WithCancel(context.Background())
	qr := make(chan whatsmeow.QRChannelItem, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		tw.followPairing(ctx, cancel, qr)
	}()
	qr <- whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventCode, Code: "2@code"}
	testutil.WaitFor(t, 2*time.Second, "the code", func() bool { return tw.State().QRCode == "2@code" })
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("following the pairing did not end with its context")
	}
	tw.pairMu.Lock()
	defer tw.pairMu.Unlock()
	if tw.pairing {
		t.Error("still marked as pairing")
	}
}

// A second pairing is refused while one runs: both would follow the same
// events, and the first one's goroutine and handler would linger.
func TestStartPairingRefusesASecondPairing(t *testing.T) {
	tw := newTestWhatsApp(t)
	tw.pairing = true
	if err := tw.StartPairing(); !errors.Is(err, errPairingLive) {
		t.Errorf("StartPairing = %v, want %v", err, errPairingLive)
	}
}

// After a logout whatsmeow deletes the device, and its client refuses to
// connect again: pairing again gets a client on a new device instead of
// failing until a restart.
func TestNewSessionAfterLogout(t *testing.T) {
	s := openOutboxStore(t, t.TempDir())
	cfg := &config.Config{LogLevel: "error"}
	wa, err := NewWhatsAppClient(func() *config.Config { return cfg }, s, func(context.Context, Inbound) {}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wa.Disconnect)
	old := wa.cl()
	old.Store.Deleted = true // as Device.Delete leaves it
	wa.setOwn(types.NewJID(otherNumber, types.DefaultUserServer), types.EmptyJID)

	cli := wa.newSession()
	if cli == old || wa.cl() != cli || cli.Store.Deleted || cli.Store.ID != nil {
		t.Errorf("new client %p (old %p), deleted %v, id %v: want a fresh, unpaired one", cli, old, cli.Store.Deleted, cli.Store.ID)
	}
	if st := wa.State(); st.OwnNumber != "" {
		t.Errorf("own number = %q after a new session", st.OwnNumber)
	}
}

// connectMessenger is the dev messenger whose Connect fails with err.
type connectMessenger struct {
	*devMessenger
	err   error
	mu    sync.Mutex
	tries int
}

func (c *connectMessenger) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tries++
	return c.err
}

// An error that another try cannot fix ends the connect loop at once.
func TestConnectLoopStopsOnPermanentErrors(t *testing.T) {
	for _, err := range []error{store.ErrDeviceDeleted, errPairingLive} {
		t.Run(err.Error(), func(t *testing.T) {
			m := &connectMessenger{devMessenger: &devMessenger{}, err: err}
			b := &Bridge{wa: m}
			done := make(chan struct{})
			go func() {
				defer close(done)
				b.connectLoop(context.Background())
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("the connect loop kept retrying")
			}
			if m.tries != 1 {
				t.Errorf("%d tries, want 1", m.tries)
			}
		})
	}
}
