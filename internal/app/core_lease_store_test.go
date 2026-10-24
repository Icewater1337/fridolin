package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/lease"
)

// errCap is what Backblaze answers once a daily cap is used up.
var errCap = &cloud.RcloneError{Op: "rcd", Key: lease.Key, ExitCode: -1, Err: errors.New("rclone rcd answered 403"),
	Stderr: "failed to authorize account: Transaction cap exceeded (403 transaction_cap_exceeded)"}

// failingDir is a cloud.Dir whose calls fail with errCap while failing;
// failed counts the calls it refused.
func failingDir(t *testing.T) (dir *cloud.Dir, failing *atomic.Bool, failed *atomic.Int64) {
	failing, failed = new(atomic.Bool), new(atomic.Int64)
	return &cloud.Dir{Root: t.TempDir(), Fail: func(string, string) error {
		if failing.Load() {
			failed.Add(1)
			return errCap
		}
		return nil
	}}, failing, failed
}

// aheadClock is the system clock, moved ahead by what ahead holds.
type aheadClock struct{ ahead atomic.Int64 }

func (c *aheadClock) Now() time.Time { return time.Now().Add(time.Duration(c.ahead.Load())) }

func (c *aheadClock) Sleep(ctx context.Context, d time.Duration) error {
	return lease.SystemClock{}.Sleep(ctx, d)
}

// The lease store refusing at start (B2's cap used up): a device that
// never held the lease refuses; one that held it last starts unverified,
// keeps its channels, and is held again once the store answers.
func TestLeaseStoreRefusingAtStart(t *testing.T) {
	t.Parallel()
	st, failing, failedCalls := failingDir(t)
	failing.Store(true)
	opts := Options{LeaseStore: st, LeaseSettle: time.Millisecond, LeaseRetry: 20 * time.Millisecond}
	_, err := newRunningCore(t, leaseConfig, opts)
	if err == nil || !strings.Contains(err.Error(), "never held the lease") || !strings.Contains(err.Error(), "Caps & Alerts") {
		t.Fatalf("a device that never held the lease started on a refusing store: %v", err)
	}

	failing.Store(false)
	c, err := newRunningCore(t, leaseConfig, opts)
	if err != nil {
		t.Fatal(err)
	}
	c.stop() // released: the state file names this device
	failing.Store(true)
	cfg := filepath.Join(filepath.Dir(c.cfgs.Path()), "config.json")
	c2, err := startCoreAt(t, c.home, cfg, opts)
	if err != nil {
		t.Fatalf("the last holder did not start while the store refused: %v", err)
	}
	if ls := c2.leaseStatus(); ls == nil || ls.State != "unverified" || !strings.Contains(ls.Error, "transaction_cap_exceeded") {
		t.Fatalf("lease status = %+v", ls)
	}
	// The lease's retries run once the bridge has started.
	n := failedCalls.Load()
	waitUntil(t, "a retry", func() bool { return failedCalls.Load() > n+1 })
	h := c2.newHTTPHandler("127.0.0.1:8765")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://127.0.0.1:8765/ready", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"unverified"`) {
		t.Errorf("/ready while unverified: %d %s", rec.Code, rec.Body)
	}
	if ws := c2.coreWarnings(); len(ws) != 1 || !strings.Contains(ws[0], "could not be checked") {
		t.Errorf("warnings = %q", ws)
	}
	if err := c2.wa.SendMessage(context.Background(), "15551234567@s.whatsapp.net", "hi"); err != nil {
		t.Errorf("a send while unverified: %v", err)
	}
	failing.Store(false)
	waitUntil(t, "the lease to be verified", func() bool { ls := c2.leaseStatus(); return ls != nil && ls.State == "held" })
	if r := readLease(t, st); r.DeviceID != c2.leaseStatus().DeviceID || r.Released {
		t.Errorf("lease after the store came back: %+v", r)
	}
}

// Renewals that fail on the store leave the channels running, however
// long; only another device's record, once the store answers, stops them.
func TestLeaseRenewalFailuresKeepTheChannels(t *testing.T) {
	t.Parallel()
	st, failing, failed := failingDir(t)
	clock := &aheadClock{}
	c, err := newRunningCore(t, leaseConfig, Options{LeaseStore: st, LeaseSettle: time.Millisecond, LeaseRetry: 20 * time.Millisecond, LeaseClock: clock})
	if err != nil {
		t.Fatal(err)
	}
	failing.Store(true)
	waitUntil(t, "the lease to be unverified", func() bool { ls := c.leaseStatus(); return ls != nil && ls.State == "unverified" })
	// Past the staleness window (4 s) on the lease's clock, and renewals
	// fail there too.
	clock.ahead.Store(int64(5 * time.Second))
	n := failed.Load()
	waitUntil(t, "renewals past the staleness window", func() bool { return failed.Load() > n+2 })
	if c.core.lost.Load() {
		t.Fatal("store failures stopped the channels")
	}
	if err := c.wa.SendMessage(context.Background(), "15551234567@s.whatsapp.net", "hi"); err != nil {
		t.Errorf("a send while the store fails: %v", err)
	}
	// Meanwhile another device took the stale lease.
	other := lease.Record{DeviceID: "0123456789abcdef0123456789abcdef", Hostname: "core-b", StartedAt: time.Now().UTC(), RenewedAt: time.Now().UTC(), Version: "test"}
	data, _ := json.Marshal(other)
	failing.Store(false)
	if err := st.Put(context.Background(), lease.Key, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the lease to be lost", func() bool { ls := c.leaseStatus(); return ls != nil && ls.State == "lost" })
	if err := c.wa.SendMessage(context.Background(), "15551234567@s.whatsapp.net", "hi"); !errors.Is(err, errLeaseLost) {
		t.Errorf("a send after the takeover: %v", err)
	}
}
