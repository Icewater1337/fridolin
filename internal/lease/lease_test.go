package lease

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/cloud"
)

// memStore is an object store in memory; failGet and failPut make its
// calls fail.
type memStore struct {
	cloud.ObjectStore // Stat, List and Delete, which the lease never calls
	mu                sync.Mutex
	objs              map[string][]byte
	failGet, failPut  error
	puts              int
}

func newMemStore() *memStore { return &memStore{objs: map[string][]byte{}} }

func (s *memStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failGet != nil {
		return nil, s.failGet
	}
	b, ok := s.objs[key]
	if !ok {
		return nil, &fs.PathError{Op: "get", Path: key, Err: fs.ErrNotExist}
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (s *memStore) Put(_ context.Context, key string, r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failPut != nil {
		return s.failPut
	}
	s.puts++
	s.objs[key] = b
	return nil
}

func (s *memStore) record(t *testing.T) Record {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var r Record
	if err := json.Unmarshal(s.objs[Key], &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func (s *memStore) set(t *testing.T, r Record) {
	t.Helper()
	b, _ := json.Marshal(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objs[Key] = b
}

// fakeClock moves on only when slept on; onSleep runs after each sleep.
type fakeClock struct {
	now     time.Time
	onSleep func(d time.Duration)
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.now = c.now.Add(d)
	if c.onSleep != nil {
		c.onSleep(d)
	}
	return ctx.Err()
}

var t0 = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func options(store Store, clock Clock, device string) Options {
	return Options{Store: store, Clock: clock, DeviceID: device, Hostname: "host-" + device, PID: 42,
		Version: "v1.0.0", Renew: time.Minute, Stale: 5 * time.Minute, Settle: 3 * time.Second}
}

func TestAcquireFreeLease(t *testing.T) {
	store, clock := newMemStore(), &fakeClock{now: t0}
	l, err := Acquire(context.Background(), options(store, clock, "aaaa"))
	if err != nil {
		t.Fatal(err)
	}
	got := store.record(t)
	if got.DeviceID != "aaaa" || got.Hostname != "host-aaaa" || got.PID != 42 || got.Version != "v1.0.0" || !got.StartedAt.Equal(t0) {
		t.Errorf("record %+v", got)
	}
	if l.Record() != got {
		t.Errorf("lease's record %+v, stored %+v", l.Record(), got)
	}
}

func TestAcquireRefusesALiveHolder(t *testing.T) {
	store, clock := newMemStore(), &fakeClock{now: t0}
	store.set(t, Record{DeviceID: "bbbb", Hostname: "mac-mini", Version: "v0.9", RenewedAt: t0.Add(-2 * time.Minute)})
	_, err := Acquire(context.Background(), options(store, clock, "aaaa"))
	held, ok := errors.AsType[*HeldError](err)
	if !ok {
		t.Fatalf("error %v, want a HeldError", err)
	}
	if held.Holder.DeviceID != "bbbb" || held.Age != 2*time.Minute {
		t.Errorf("held %+v", held)
	}
	for _, want := range []string{"bbbb", "mac-mini", "2m0s ago"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
	if store.puts != 0 {
		t.Errorf("wrote the lease %d times while refusing", store.puts)
	}
}

func TestAcquireTakesAStaleOrReleasedLease(t *testing.T) {
	for name, held := range map[string]Record{
		"stale":    {DeviceID: "bbbb", RenewedAt: t0.Add(-5 * time.Minute)},
		"released": {DeviceID: "bbbb", RenewedAt: t0.Add(-time.Second), Released: true},
		"ours":     {DeviceID: "aaaa", PID: 7, RenewedAt: t0.Add(-time.Second)},
	} {
		t.Run(name, func(t *testing.T) {
			store, clock := newMemStore(), &fakeClock{now: t0}
			store.set(t, held)
			var logs []string
			opts := options(store, clock, "aaaa")
			opts.Logf = func(f string, a ...any) { logs = append(logs, f) }
			if _, err := Acquire(context.Background(), opts); err != nil {
				t.Fatal(err)
			}
			if got := store.record(t); got.DeviceID != "aaaa" || got.PID != 42 {
				t.Errorf("record %+v, want ours", got)
			}
		})
	}
}

func TestAcquireLosesARace(t *testing.T) {
	store := newMemStore()
	clock := &fakeClock{now: t0}
	// Another device writes during the settle delay.
	clock.onSleep = func(time.Duration) {
		store.set(t, Record{DeviceID: "cccc", Hostname: "linux-vm", StartedAt: clock.now, RenewedAt: clock.now})
	}
	_, err := Acquire(context.Background(), options(store, clock, "aaaa"))
	held, ok := errors.AsType[*HeldError](err)
	if !ok || !held.Race || held.Holder.DeviceID != "cccc" {
		t.Fatalf("error %v, want a race lost to cccc", err)
	}
	if !strings.Contains(err.Error(), "took it") {
		t.Errorf("error %q", err)
	}
}

func TestRunRenews(t *testing.T) {
	store, clock := newMemStore(), &fakeClock{now: t0}
	l, err := Acquire(context.Background(), options(store, clock, "aaaa"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	renewals := 0
	clock.onSleep = func(time.Duration) {
		if renewals++; renewals == 4 {
			cancel()
		}
	}
	if err := l.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := store.record(t)
	if !got.StartedAt.Equal(t0) || !got.RenewedAt.Equal(t0.Add(3*time.Second+3*time.Minute)) {
		t.Errorf("record %+v after three renewals", got)
	}
}

// errCapExceeded is what B2 answers once a daily cap is used up.
var errCapExceeded = errors.New("cloud: rclone rcd: failed to authorize account: Transaction cap exceeded (403 transaction_cap_exceeded)")

// A store that keeps failing never stops the channels: the lease is
// unverified, renewals are retried with a backoff up to the renew
// interval, and it is held again once the store answers.
func TestRunKeepsRunningWhileTheStoreFails(t *testing.T) {
	store, clock := newMemStore(), &fakeClock{now: t0}
	opts := options(store, clock, "aaaa")
	opts.Retry = 10 * time.Second
	var unverified []error
	opts.OnLost = func(err error) { t.Errorf("lost: %v", err) }
	opts.OnUnverified = func(err error) { unverified = append(unverified, err) }
	l, err := Acquire(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	store.failPut = errCapExceeded
	ctx, cancel := context.WithCancel(context.Background())
	var waits []time.Duration
	clock.onSleep = func(d time.Duration) {
		waits = append(waits, d)
		switch {
		case clock.now.Sub(t0) > 2*opts.Stale && store.failPut != nil:
			store.failPut = nil // long past the staleness window
		case store.failPut == nil && l.State() == StateHeld:
			cancel()
		}
	}
	if err := l.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(unverified) < 5 || !errors.Is(unverified[0], errCapExceeded) {
		t.Fatalf("OnUnverified got %v", unverified)
	}
	// One renew interval, then retries after 10s, 20s, 40s, 60s, 60s...
	if waits[0] != time.Minute || waits[1] != 10*time.Second || waits[2] != 20*time.Second || waits[3] != 40*time.Second || waits[4] != time.Minute {
		t.Errorf("waits %v", waits[:5])
	}
	if l.State() != StateHeld || l.Err() != nil {
		t.Errorf("state %s, err %v after the store came back", l.State(), l.Err())
	}
	if got := store.record(t); !got.RenewedAt.Equal(clock.now) && got.RenewedAt.Before(t0.Add(opts.Stale)) {
		t.Errorf("not renewed after the outage: %+v", got)
	}
}

// A takeover during an outage is found once the store answers again.
func TestTakeoverDuringAnOutageStops(t *testing.T) {
	store, clock := newMemStore(), &fakeClock{now: t0}
	opts := options(store, clock, "aaaa")
	var lost []error
	opts.OnLost = func(err error) { lost = append(lost, err) }
	l, err := Acquire(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	store.failGet = errCapExceeded
	n := 0
	clock.onSleep = func(time.Duration) {
		if n++; n == 20 {
			// Another device took the stale lease (and stopped since):
			// still a takeover for the device that held it.
			store.failGet = nil
			store.set(t, Record{DeviceID: "bbbb", Hostname: "vm", RenewedAt: clock.now.Add(-time.Hour), Released: true})
		}
	}
	err = l.Run(context.Background())
	le, ok := errors.AsType[*LostError](err)
	if !ok || le.Holder.DeviceID != "bbbb" || len(lost) != 1 {
		t.Fatalf("Run returned %v, OnLost %v", err, lost)
	}
}

func TestRunRecoversFromAShortOutage(t *testing.T) {
	store, clock := newMemStore(), &fakeClock{now: t0}
	opts := options(store, clock, "aaaa")
	opts.OnLost = func(err error) { t.Errorf("lost: %v", err) }
	l, err := Acquire(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	clock.onSleep = func(time.Duration) {
		n++
		switch n {
		case 1:
			store.failPut = errors.New("timeout")
		case 3:
			store.failPut = nil
		case 8:
			cancel()
		}
	}
	if err := l.Run(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRunStopsWhenAnotherDeviceTookTheLease(t *testing.T) {
	store, clock := newMemStore(), &fakeClock{now: t0}
	opts := options(store, clock, "aaaa")
	l, err := Acquire(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	clock.onSleep = func(time.Duration) {
		store.set(t, Record{DeviceID: "bbbb", Hostname: "linux-vm", RenewedAt: clock.now})
	}
	err = l.Run(context.Background())
	le, ok := errors.AsType[*LostError](err)
	if !ok || le.Holder.DeviceID != "bbbb" {
		t.Fatalf("Run returned %v", err)
	}
	if got := store.record(t); got.DeviceID != "bbbb" {
		t.Errorf("the old holder wrote over the new one: %+v", got)
	}
}

func TestTakeoverAfterStaleness(t *testing.T) {
	// Device A holds the lease and goes silent; B is refused until the
	// lease is stale, then takes it, and A's next renewal stops A.
	store := newMemStore()
	clockA, clockB := &fakeClock{now: t0}, &fakeClock{now: t0}
	a, err := Acquire(context.Background(), options(store, clockA, "aaaa"))
	if err != nil {
		t.Fatal(err)
	}
	clockB.now = t0.Add(4 * time.Minute)
	if _, err := Acquire(context.Background(), options(store, clockB, "bbbb")); err == nil {
		t.Fatal("B took a lease renewed 4 minutes ago")
	}
	clockB.now = t0.Add(3*time.Second + 5*time.Minute)
	if _, err := Acquire(context.Background(), options(store, clockB, "bbbb")); err != nil {
		t.Fatalf("B could not take a stale lease: %v", err)
	}
	clockA.now = clockB.now
	if _, ok := errors.AsType[*LostError](a.Renew(context.Background())); !ok {
		t.Error("A renewed over B")
	}
}

func TestRelease(t *testing.T) {
	store, clock := newMemStore(), &fakeClock{now: t0}
	l, err := Acquire(context.Background(), options(store, clock, "aaaa"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !store.record(t).Released {
		t.Fatal("not released")
	}
	// Free at once for another device.
	if _, err := Acquire(context.Background(), options(store, clock, "bbbb")); err != nil {
		t.Fatal(err)
	}
	// A late release of the old holder leaves the new one alone.
	if err := l.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := store.record(t); got.DeviceID != "bbbb" || got.Released {
		t.Errorf("record %+v", got)
	}
}

func TestOptionsValidate(t *testing.T) {
	store := newMemStore()
	opts := options(store, &fakeClock{now: t0}, "aaaa")
	opts.Stale = opts.Renew
	if _, err := Acquire(context.Background(), opts); err == nil {
		t.Error("accepted stale == renew")
	}
	if _, err := Acquire(context.Background(), Options{Store: store}); err == nil {
		t.Error("accepted no device id")
	}
}

func TestReadErrors(t *testing.T) {
	store := newMemStore()
	if _, ok, err := Read(context.Background(), store); ok || err != nil {
		t.Errorf("empty store: ok %v err %v", ok, err)
	}
	store.objs[Key] = []byte("not json")
	if _, _, err := Read(context.Background(), store); err == nil {
		t.Error("read a broken record")
	}
	store.failGet = errors.New("offline")
	if _, err := Acquire(context.Background(), options(store, &fakeClock{now: t0}, "aaaa")); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Errorf("error %v", err)
	}
	// A broken record is not the store failing: Resume does not start on it.
	store.failGet = nil
	opts := options(store, &fakeClock{now: t0}, "aaaa")
	opts.Resume = true
	if _, err := Acquire(context.Background(), opts); err == nil {
		t.Error("resumed on a broken record")
	}
}

// The store refusing at start (B2's cap used up): a device that held the
// lease last starts unverified; one that did not refuses.
func TestAcquireWhileTheStoreRefuses(t *testing.T) {
	for _, fail := range []string{"get", "put"} {
		store, clock := newMemStore(), &fakeClock{now: t0}
		store.set(t, Record{DeviceID: "aaaa", Hostname: "host-aaaa", PID: 7, StartedAt: t0.Add(-time.Hour), RenewedAt: t0.Add(-time.Minute)})
		if fail == "get" {
			store.failGet = errCapExceeded
		} else {
			store.failPut = errCapExceeded
		}
		opts := options(store, clock, "aaaa")
		if _, err := Acquire(context.Background(), opts); !errors.Is(err, errCapExceeded) {
			t.Fatalf("%s: never-resumed Acquire = %v, want the store's error", fail, err)
		}
		opts.Resume = true
		l, err := Acquire(context.Background(), opts)
		if err != nil {
			t.Fatalf("%s: resumed Acquire = %v", fail, err)
		}
		if l.State() != StateUnverified || !errors.Is(l.Err(), errCapExceeded) {
			t.Fatalf("%s: state %s err %v", fail, l.State(), l.Err())
		}
		// The store answers again and shows this device's earlier record:
		// the lease is verified and the record is this run's.
		store.failGet, store.failPut = nil, nil
		if err := l.Renew(context.Background()); err != nil {
			t.Fatalf("%s: Renew after the store came back: %v", fail, err)
		}
		if got := store.record(t); l.State() != StateHeld || got.PID != 42 || !got.StartedAt.Equal(t0) {
			t.Errorf("%s: state %s, record %+v", fail, l.State(), got)
		}
	}
}

// A device that started unverified stops when the store then shows
// another device holding the lease, and takes a stale one.
func TestUnverifiedStartMeetsAnotherDevice(t *testing.T) {
	store, clock := newMemStore(), &fakeClock{now: t0}
	store.failGet = errCapExceeded
	opts := options(store, clock, "aaaa")
	opts.Resume = true
	l, err := Acquire(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	store.failGet = nil
	store.set(t, Record{DeviceID: "bbbb", Hostname: "vm", RenewedAt: t0.Add(-time.Minute)})
	if _, ok := errors.AsType[*LostError](l.Renew(context.Background())); !ok {
		t.Fatal("an unverified start renewed over a live holder")
	}
	if got := store.record(t); got.DeviceID != "bbbb" {
		t.Fatalf("wrote over the live holder: %+v", got)
	}

	// A stale record of another device is free, as for Acquire.
	store2, clock2 := newMemStore(), &fakeClock{now: t0}
	store2.failGet = errCapExceeded
	opts2 := options(store2, clock2, "aaaa")
	opts2.Resume = true
	l2, err := Acquire(context.Background(), opts2)
	if err != nil {
		t.Fatal(err)
	}
	store2.failGet = nil
	store2.set(t, Record{DeviceID: "bbbb", Hostname: "vm", RenewedAt: t0.Add(-time.Hour)})
	if err := l2.Renew(context.Background()); err != nil || l2.State() != StateHeld {
		t.Fatalf("Renew = %v, state %s", err, l2.State())
	}
	if got := store2.record(t); got.DeviceID != "aaaa" {
		t.Fatalf("record %+v", got)
	}
}

func TestDeviceID(t *testing.T) {
	dir := t.TempDir()
	id, err := DeviceID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 32 {
		t.Errorf("id %q", id)
	}
	again, err := DeviceID(dir)
	if err != nil || again != id {
		t.Errorf("second call %q, %v; want %q", again, err, id)
	}
	st, err := os.Stat(filepath.Join(dir, "core", "device-id"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("device-id file: %v %v", st, err)
	}
	other, _ := DeviceID(t.TempDir())
	if other == id {
		t.Error("two data dirs got one id")
	}
	if err := os.WriteFile(filepath.Join(dir, "core", "device-id"), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DeviceID(dir); err == nil {
		t.Error("accepted a broken device id")
	}
}

func TestReleaseDevice(t *testing.T) {
	ctx := context.Background()
	st, err := cloud.NewDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := ReleaseDevice(ctx, st, "a", time.Now()); ok || err != nil {
		t.Fatalf("no lease: %v %v", ok, err)
	}
	if err := write(ctx, st, Record{DeviceID: "a", RenewedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if ok, err := ReleaseDevice(ctx, st, "b", time.Now()); ok || err != nil {
		t.Fatalf("another device's: %v %v", ok, err)
	}
	if ok, err := ReleaseDevice(ctx, st, "a", time.Now()); !ok || err != nil {
		t.Fatalf("its own: %v %v", ok, err)
	}
	if r, _, _ := Read(ctx, st); !r.Released || r.DeviceID != "a" {
		t.Fatalf("after: %+v", r)
	}
}
