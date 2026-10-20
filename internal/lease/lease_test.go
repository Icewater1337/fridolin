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

func TestRunStopsWhenRenewalKeepsFailing(t *testing.T) {
	store, clock := newMemStore(), &fakeClock{now: t0}
	opts := options(store, clock, "aaaa")
	var lost []error
	opts.OnLost = func(err error) { lost = append(lost, err) }
	l, err := Acquire(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	store.failPut = errors.New("b2: 503")
	err = l.Run(context.Background())
	le, ok := errors.AsType[*LostError](err)
	if !ok || len(lost) != 1 {
		t.Fatalf("Run returned %v, OnLost called %d times", err, len(lost))
	}
	// Stopped one renew interval before the window, so another device
	// that takes the lease once it is stale finds this one quiet.
	if le.Since < 4*time.Minute || le.Since >= 5*time.Minute || le.Holder.DeviceID != "" {
		t.Errorf("lost %+v", le)
	}
	if age := clock.now.Sub(store.record(t).RenewedAt); age >= opts.Stale {
		t.Errorf("stopped only when the lease was %s old, past the staleness window", age)
	}
	if !strings.Contains(err.Error(), "b2: 503") {
		t.Errorf("error %q does not name the cause", err)
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
