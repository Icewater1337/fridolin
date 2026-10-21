// Package lease keeps the core lease: one object in the vault bucket that
// names the device running the core. Exactly one device may run the core,
// because the core holds the WhatsApp session, and two bridges on one
// session fight over it (docs/CORE.md).
//
// The holder renews the object every renew interval. A device that finds it
// held by another device, renewed within the staleness window, refuses to
// start; one that finds it stale, released or missing takes it. A holder
// that reads another device's record stops its channels at once: that is
// a takeover, and the only thing that stops them.
//
// The store being unreachable is not a takeover. Backblaze refuses every
// call once a daily transaction cap is used up, a network goes down, B2
// answers 5xx: a holder that cannot read or write the lease keeps its
// channels and says the lease is unverified, retrying with a backoff, and
// stops only if the store, once it answers, shows another device holding
// it. A device that starts while the store cannot be read starts too
// (Options.Resume), unverified, when its own records say it was the last
// holder; one that never held it refuses, so that two bridges never run
// one session because of an outage.
//
// The object store has no conditional write (Backblaze B2 has none), so two
// devices that start within the same few seconds could both write. Acquire
// writes, waits a settle delay and reads again: the device whose record
// is not the one read back gives up. What remains is a race between two
// writes that both land before either re-read, which needs two devices to
// take a free lease within the settle delay of each other. Taking a lease
// that is held needs it to be stale first, which takes minutes of silence
// from the holder, and moving the core is a step a person does on one
// device at a time (export, import, promote), so in practice the window
// is never hit.
//
// A renewal costs one read (on B2 two Class B transactions through the
// rclone daemon: a HEAD and a GET) and one write (Class A, free), so the
// default five-minute renewal is 576 capped transactions a day.
package lease

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/cloud"
)

// Key is the lease object's key in the vault bucket.
const Key = "core/lease.json"

// Defaults of core.lease (docs/CONFIGURATION.md).
const (
	DefaultRenew  = 300 * time.Second
	DefaultStale  = 1200 * time.Second
	DefaultSettle = 3 * time.Second
	// DefaultRetry is the first wait before a failed renewal is tried
	// again; it doubles up to the renew interval.
	DefaultRetry = 30 * time.Second
)

// States of a lease, as Lease.State reports them.
const (
	StateHeld       = "held"       // the store confirmed this device's record at the last try
	StateUnverified = "unverified" // the store could not be read or written since
)

// Store is the vault bucket the lease lives in. The lease uses Get and Put;
// Get of a key that does not exist returns an error that matches
// fs.ErrNotExist, as cloud.ErrNotFound does.
type Store = cloud.ObjectStore

// Clock is the time the lease reads and waits on; tests use a fake one.
type Clock interface {
	Now() time.Time
	// Sleep waits d, or until ctx ends, which it returns as an error.
	Sleep(ctx context.Context, d time.Duration) error
}

// Record is the lease object's content.
type Record struct {
	DeviceID  string    `json:"device_id"`
	Hostname  string    `json:"hostname"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	RenewedAt time.Time `json:"renewed_at"`
	Version   string    `json:"version"`
	// Released is set by a clean shutdown: the lease is free at once,
	// without waiting for it to go stale.
	Released bool `json:"released,omitempty"`
}

// Age is how long ago the record was renewed, at now.
func (r Record) Age(now time.Time) time.Duration { return now.Sub(r.RenewedAt) }

// Live reports whether the record holds the lease at now: not released and
// renewed within stale.
func (r Record) Live(now time.Time, stale time.Duration) bool {
	return r.DeviceID != "" && !r.Released && r.Age(now) < stale
}

// same reports whether r and o are one acquisition: the same device and
// process, started at the same time.
func (r Record) same(o Record) bool {
	return r.DeviceID == o.DeviceID && r.PID == o.PID && r.StartedAt.Equal(o.StartedAt)
}

// HeldError is Acquire's refusal: another device holds the lease.
type HeldError struct {
	Holder Record
	Age    time.Duration
	// Race is set when the other device wrote its record between this
	// one's write and its check.
	Race bool
}

func (e *HeldError) Error() string {
	host := e.Holder.Hostname
	if host == "" {
		host = "unknown host"
	}
	what := "holds"
	if e.Race {
		what = "took"
	}
	return fmt.Sprintf("the core lease is held: device %s (%s, version %s) %s it, last renewed %s ago; "+
		"stop the core there first, or wait until it has not renewed for the staleness window",
		e.Holder.DeviceID, host, e.Holder.Version, what, e.Age.Round(time.Second))
}

// Options configure a lease. Store, DeviceID and Hostname are required.
type Options struct {
	Store    Store
	DeviceID string
	Hostname string
	PID      int
	Version  string
	// Renew is how often the holder renews (core.lease.renew_seconds);
	// Stale how long an unrenewed lease stays held (stale_seconds).
	Renew, Stale time.Duration
	// Settle is how long Acquire waits before reading its write back.
	Settle time.Duration
	// Retry is the first wait before a renewal that failed on the store is
	// tried again (default 30s); it doubles up to Renew.
	Retry time.Duration
	// Resume says this device was the last holder by its own records (it
	// held, renewed or released the lease last and was not taken over):
	// when the store cannot be read at start, Acquire then returns an
	// unverified lease instead of an error.
	Resume bool
	Clock  Clock
	// OnLost is called once, from Run, when another device took the
	// lease. The app stops its channels there.
	OnLost func(error)
	// OnRenew, when set, is called after each renewal that wrote, with
	// the record written: the app shows and records it.
	OnRenew func(Record)
	// OnUnverified, when set, is called after each renewal that failed on
	// the store, with its error: the channels keep running, the lease is
	// unverified until a renewal succeeds.
	OnUnverified func(error)
	Logf         func(format string, args ...any)
}

func (o *Options) defaults() error {
	if o.Store == nil || o.DeviceID == "" {
		return errors.New("lease: a store and a device id are required")
	}
	if o.Renew <= 0 {
		o.Renew = DefaultRenew
	}
	if o.Stale <= 0 {
		o.Stale = DefaultStale
	}
	if o.Stale <= o.Renew {
		return fmt.Errorf("lease: stale (%s) must be longer than renew (%s)", o.Stale, o.Renew)
	}
	if o.Settle <= 0 {
		o.Settle = DefaultSettle
	}
	if o.Retry <= 0 {
		o.Retry = DefaultRetry
	}
	o.Retry = min(o.Retry, o.Renew)
	if o.Clock == nil {
		o.Clock = SystemClock{}
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.OnLost == nil {
		o.OnLost = func(error) {}
	}
	return nil
}

// Lease is a held core lease, verified or not.
type Lease struct {
	mu     sync.Mutex // the fields below, for Record and State from other goroutines
	opts   Options
	record Record
	// confirmed is set once the store showed this lease's record in
	// this process; until then (a start while the store was down) a
	// record of this device from an earlier run is taken as this one's.
	confirmed bool
	// verified is whether the last try reached the store; lastErr is
	// that try's error when it did not.
	verified bool
	lastErr  error
}

// StoreError is a renewal or an acquisition that failed because the store
// could not be read or written: the lease is unverified, not lost.
type StoreError struct{ Err error }

func (e *StoreError) Error() string { return e.Err.Error() }
func (e *StoreError) Unwrap() error { return e.Err }

// Read returns the lease record in store, and false when there is none.
func Read(ctx context.Context, store Store) (Record, bool, error) {
	rc, err := store.Get(ctx, Key)
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, &StoreError{Err: fmt.Errorf("reading the core lease: %w", err)}
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, 64<<10))
	if err != nil {
		return Record{}, false, &StoreError{Err: fmt.Errorf("reading the core lease: %w", err)}
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return Record{}, false, fmt.Errorf("the core lease object %s is not a lease record: %w", Key, err)
	}
	return r, true, nil
}

func write(ctx context.Context, store Store, r Record) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := store.Put(ctx, Key, bytes.NewReader(append(data, '\n'))); err != nil {
		return &StoreError{Err: fmt.Errorf("writing the core lease: %w", err)}
	}
	return nil
}

// isStoreError reports whether err is the store failing (unreachable,
// refusing, 5xx), not a refusal of the lease.
func isStoreError(err error) bool {
	_, ok := errors.AsType[*StoreError](err)
	return ok
}

// Acquire takes the lease for this device, or returns a *HeldError naming
// the device that holds it. A record of this same device is taken over: it
// is a previous run here that did not release it.
//
// When the store cannot be read or written and opts.Resume is set, it
// returns an unverified lease (State is StateUnverified, Err says why):
// Run keeps trying and calls OnLost if the store then shows another
// device holding the lease. Without Resume that is an error.
func Acquire(ctx context.Context, opts Options) (*Lease, error) {
	if err := opts.defaults(); err != nil {
		return nil, err
	}
	now := opts.Clock.Now()
	mine := Record{DeviceID: opts.DeviceID, Hostname: opts.Hostname, PID: opts.PID,
		StartedAt: now.UTC(), RenewedAt: now.UTC(), Version: opts.Version}
	unverified := func(err error) (*Lease, error) {
		if !opts.Resume || !isStoreError(err) {
			return nil, err
		}
		opts.Logf("The core lease store does not answer (%v); starting unverified, as device %s held the lease last", err, opts.DeviceID)
		return &Lease{opts: opts, record: mine, lastErr: err}, nil
	}
	cur, ok, err := Read(ctx, opts.Store)
	if err != nil {
		return unverified(err)
	}
	if ok && cur.DeviceID != opts.DeviceID && cur.Live(now, opts.Stale) {
		return nil, &HeldError{Holder: cur, Age: cur.Age(now)}
	}
	if ok && cur.DeviceID != opts.DeviceID && cur.DeviceID != "" {
		state := "stale"
		if cur.Released {
			state = "released"
		}
		opts.Logf("Taking the core lease from device %s (%s), %s for %s", cur.DeviceID, cur.Hostname, state, cur.Age(now).Round(time.Second))
	}
	if err := write(ctx, opts.Store, mine); err != nil {
		return unverified(err)
	}
	// No conditional put: read back after a while to see whether another
	// device wrote over this record meanwhile (the package comment says
	// what race is left).
	if err := opts.Clock.Sleep(ctx, opts.Settle); err != nil {
		return nil, err
	}
	back, ok, err := Read(ctx, opts.Store)
	if err != nil {
		return unverified(err)
	}
	if !ok {
		return nil, errors.New("the core lease vanished right after it was written: is another device releasing it?")
	}
	if !back.same(mine) {
		return nil, &HeldError{Holder: back, Age: back.Age(opts.Clock.Now()), Race: true}
	}
	opts.Logf("Holding the core lease as device %s (%s)", mine.DeviceID, mine.Hostname)
	return &Lease{opts: opts, record: mine, confirmed: true, verified: true}, nil
}

// Record returns the record this lease last wrote (or would write).
func (l *Lease) Record() Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.record
}

// State is StateHeld while the store confirmed the lease at the last try,
// StateUnverified while it could not be reached since.
func (l *Lease) State() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.verified {
		return StateHeld
	}
	return StateUnverified
}

// Err is why the lease is unverified, nil while it is held.
func (l *Lease) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.verified {
		return nil
	}
	return l.lastErr
}

// ours reports whether cur is this lease's record: the same acquisition,
// or, before the store ever confirmed this one, any record of this device
// (an earlier run here).
func (l *Lease) ours(cur Record) bool {
	if l.confirmed {
		return cur.same(l.record)
	}
	return cur.DeviceID == l.opts.DeviceID
}

func (l *Lease) failed(err error) error {
	l.mu.Lock()
	l.verified, l.lastErr = false, err
	l.mu.Unlock()
	if l.opts.OnUnverified != nil {
		l.opts.OnUnverified(err)
	}
	return err
}

// Renew renews the lease once. It returns a *LostError when another
// device holds it, and a *StoreError when the store could not be read or
// written: the lease is unverified then, not lost.
func (l *Lease) Renew(ctx context.Context) error {
	now := l.opts.Clock.Now()
	cur, ok, err := Read(ctx, l.opts.Store)
	if err != nil {
		if !isStoreError(err) {
			// The object is there but not a lease record: something else
			// wrote it. Not a device's takeover; keep trying.
			err = &StoreError{Err: err}
		}
		return l.failed(err)
	}
	l.mu.Lock()
	confirmed := l.confirmed
	l.mu.Unlock()
	if ok && cur.DeviceID != "" && !l.ours(cur) {
		// Another device's record. While this process held the lease,
		// any is a takeover. Before the store ever confirmed it (a start
		// while the store was down), the lease is taken as Acquire takes
		// it: a live holder is a takeover, a stale or released record is
		// free.
		if confirmed || cur.Live(now, l.opts.Stale) {
			return &LostError{Holder: cur}
		}
	}
	next := l.Record()
	next.RenewedAt = now.UTC()
	if err := write(ctx, l.opts.Store, next); err != nil {
		return l.failed(err)
	}
	if !confirmed {
		// The first write since an unverified start: read back after the
		// settle delay, as Acquire does.
		if err := l.opts.Clock.Sleep(ctx, l.opts.Settle); err != nil {
			return err
		}
		back, ok, err := Read(ctx, l.opts.Store)
		if err != nil {
			return l.failed(err)
		}
		if ok && !back.same(next) && back.DeviceID != l.opts.DeviceID {
			return &LostError{Holder: back}
		}
		l.opts.Logf("The core lease is verified again: device %s (%s) holds it", next.DeviceID, next.Hostname)
	}
	l.mu.Lock()
	l.record, l.confirmed, l.verified, l.lastErr = next, true, true, nil
	l.mu.Unlock()
	if l.opts.OnRenew != nil {
		l.opts.OnRenew(next)
	}
	return nil
}

// LostError says why the lease was lost: another device took it.
type LostError struct {
	// Holder is the device that took it.
	Holder Record
}

func (e *LostError) Error() string {
	return fmt.Sprintf("the core lease was taken by device %s (%s): stopping the channels", e.Holder.DeviceID, e.Holder.Hostname)
}

// Run renews the lease every renew interval until ctx ends, then returns
// nil, or until another device takes it: it calls OnLost then and returns
// the *LostError. A renewal that fails on the store is tried again after
// Retry, then twice as long each time, up to the renew interval; the
// lease is unverified meanwhile and the channels keep running.
func (l *Lease) Run(ctx context.Context) error {
	backoff := l.opts.Retry
	for {
		wait := l.opts.Renew
		if l.State() == StateUnverified {
			wait = backoff
			backoff = min(2*backoff, l.opts.Renew)
		} else {
			backoff = l.opts.Retry
		}
		if err := l.opts.Clock.Sleep(ctx, wait); err != nil {
			return nil
		}
		err := l.Renew(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if lost, ok := errors.AsType[*LostError](err); ok {
			l.opts.Logf("%v", lost)
			l.opts.OnLost(lost)
			return lost
		}
		if err != nil {
			l.opts.Logf("warning: the core lease is unverified: %v (the channels keep running; retrying)", err)
		}
	}
}

// Release frees the lease for the next device at once, if it is still this
// one's. A clean shutdown calls it after the channels have stopped.
func (l *Lease) Release(ctx context.Context) error {
	cur, ok, err := Read(ctx, l.opts.Store)
	if err != nil {
		return err
	}
	if !ok || !l.ours(cur) {
		return nil
	}
	rel := l.Record()
	rel.Released = true
	rel.RenewedAt = l.opts.Clock.Now().UTC()
	return write(ctx, l.opts.Store, rel)
}

// SystemClock is the real clock.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

func (SystemClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// DeviceIDFile is where a data dir keeps its device id. core/ is not part
// of an export, so a data dir imported elsewhere gets an id of its own.
const DeviceIDFile = "core/device-id"

// DeviceID returns the device id of the data dir dataDir, making one on
// first use: 128 random bits as hex.
func DeviceID(dataDir string) (string, error) {
	path := filepath.Join(dataDir, filepath.FromSlash(DeviceIDFile))
	if data, err := os.ReadFile(path); err == nil {
		id := strings.TrimSpace(string(data))
		if len(id) != 32 {
			return "", fmt.Errorf("%s does not hold a device id", path)
		}
		if _, err := hex.DecodeString(id); err != nil {
			return "", fmt.Errorf("%s does not hold a device id", path)
		}
		return id, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b[:])
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return DeviceID(dataDir) // made meanwhile by another process
	}
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(id + "\n"); err != nil {
		f.Close()
		return "", err
	}
	return id, f.Close()
}
