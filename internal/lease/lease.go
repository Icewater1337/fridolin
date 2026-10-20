// Package lease keeps the core lease: one object in the vault bucket that
// names the device running the core. Exactly one device may run the core,
// because the core holds the WhatsApp session, and two bridges on one
// session fight over it (docs/CORE.md).
//
// The holder renews the object every renew interval. A device that finds it
// held by another device, renewed within the staleness window, refuses to
// start; one that finds it stale, released or missing takes it. A holder
// that cannot renew stops its channels before the window runs out, so that
// by the time another device may take the lease the old one is quiet.
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
// is never hit. A renewing holder that reads another device's record stops
// at once.
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
	DefaultRenew  = 60 * time.Second
	DefaultStale  = 300 * time.Second
	DefaultSettle = 3 * time.Second
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
	Clock  Clock
	// OnLost is called once, from Run, when the lease is lost: renewal
	// failed for too long, or another device took it. The app stops its
	// channels there.
	OnLost func(error)
	// OnRenew, when set, is called after each renewal that
	// wrote, with the record written: the app shows and records it.
	OnRenew func(Record)
	Logf    func(format string, args ...any)
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

// Lease is a held core lease.
type Lease struct {
	mu     sync.Mutex // record, for Record from other goroutines
	opts   Options
	record Record
	lastOK time.Time
}

// Read returns the lease record in store, and false when there is none.
func Read(ctx context.Context, store Store) (Record, bool, error) {
	rc, err := store.Get(ctx, Key)
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("reading the core lease: %w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, 64<<10))
	if err != nil {
		return Record{}, false, fmt.Errorf("reading the core lease: %w", err)
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
		return fmt.Errorf("writing the core lease: %w", err)
	}
	return nil
}

// Acquire takes the lease for this device, or returns a *HeldError naming
// the device that holds it. A record of this same device is taken over: it
// is a previous run here that did not release it.
func Acquire(ctx context.Context, opts Options) (*Lease, error) {
	if err := opts.defaults(); err != nil {
		return nil, err
	}
	now := opts.Clock.Now()
	cur, ok, err := Read(ctx, opts.Store)
	if err != nil {
		return nil, err
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
	mine := Record{DeviceID: opts.DeviceID, Hostname: opts.Hostname, PID: opts.PID,
		StartedAt: now.UTC(), RenewedAt: now.UTC(), Version: opts.Version}
	if err := write(ctx, opts.Store, mine); err != nil {
		return nil, err
	}
	// No conditional put: read back after a while to see whether another
	// device wrote over this record meanwhile (the package comment says
	// what race is left).
	if err := opts.Clock.Sleep(ctx, opts.Settle); err != nil {
		return nil, err
	}
	back, ok, err := Read(ctx, opts.Store)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("the core lease vanished right after it was written: is another device releasing it?")
	}
	if !back.same(mine) {
		return nil, &HeldError{Holder: back, Age: back.Age(opts.Clock.Now()), Race: true}
	}
	opts.Logf("Holding the core lease as device %s (%s)", mine.DeviceID, mine.Hostname)
	return &Lease{opts: opts, record: mine, lastOK: now}, nil
}

// Record returns the record this lease last wrote.
func (l *Lease) Record() Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.record
}

// lostAfter is how long renewal may fail before the holder gives the lease
// up: one renew interval short of the staleness window, so the channels
// are stopped before another device may take the lease.
func (l *Lease) lostAfter() time.Duration { return l.opts.Stale - l.opts.Renew }

// Renew renews the lease once. It returns a *LostError when the lease is
// lost: another device holds it, or renewal has failed for too long.
func (l *Lease) Renew(ctx context.Context) error {
	now := l.opts.Clock.Now()
	cur, ok, err := Read(ctx, l.opts.Store)
	if err == nil && ok && !cur.same(l.record) {
		return &LostError{Holder: cur}
	}
	if err == nil {
		next := l.record
		next.RenewedAt = now.UTC()
		if err = write(ctx, l.opts.Store, next); err == nil {
			l.mu.Lock()
			l.record, l.lastOK = next, now
			l.mu.Unlock()
			if l.opts.OnRenew != nil {
				l.opts.OnRenew(next)
			}
			return nil
		}
	}
	if since := now.Sub(l.lastOK); since >= l.lostAfter() {
		return &LostError{Err: err, Since: since}
	}
	return err
}

// LostError says why the lease was lost.
type LostError struct {
	// Holder is the device that took it, when one did.
	Holder Record
	// Err is the last renewal's error, and Since how long renewals had
	// failed, when renewal failed.
	Err   error
	Since time.Duration
}

func (e *LostError) Error() string {
	if e.Holder.DeviceID != "" {
		return fmt.Sprintf("the core lease was taken by device %s (%s): stopping the channels", e.Holder.DeviceID, e.Holder.Hostname)
	}
	return fmt.Sprintf("the core lease could not be renewed for %s (%v): stopping the channels before another device may take it", e.Since.Round(time.Second), e.Err)
}

func (e *LostError) Unwrap() error { return e.Err }

// Run renews the lease every renew interval until ctx ends, then returns
// nil, or until the lease is lost: it calls OnLost then and returns the
// *LostError.
func (l *Lease) Run(ctx context.Context) error {
	for {
		if err := l.opts.Clock.Sleep(ctx, l.opts.Renew); err != nil {
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
			l.opts.Logf("warning: renewing the core lease: %v (will retry)", err)
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
	if !ok || !cur.same(l.record) {
		return nil
	}
	rel := l.record
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
