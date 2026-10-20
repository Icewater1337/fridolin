package vault

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/cloud"
)

// clock is a fake clock the tests move by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// start is the fake clock's first instant: mid-month, so "the current
// month" is stable across a few days of advancing.
var start = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

type fixture struct {
	t     *testing.T
	v     *Vault
	cold  *cloud.Dir
	root  string
	db    string
	clock *clock
	opts  Options
}

func newFixture(t *testing.T, adjust ...func(*Options)) *fixture {
	t.Helper()
	base := t.TempDir()
	cold, err := cloud.NewDir(filepath.Join(base, "cold"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, cold: cold, root: filepath.Join(base, "hot"), clock: &clock{t: start}}
	f.db = filepath.Join(f.root, "vault.db")
	f.opts = Options{Root: f.root, DBPath: f.db, Store: cold, Now: f.clock.Now, HeadroomPercent: 10, Logger: slog.New(slog.DiscardHandler)}
	for _, a := range adjust {
		a(&f.opts)
	}
	f.open()
	return f
}

func (f *fixture) open() {
	f.t.Helper()
	v, err := New(f.opts)
	if err != nil {
		f.t.Fatal(err)
	}
	f.v = v
	f.t.Cleanup(func() { _ = v.Close() })
}

// write puts content at rel, last modified at the given age before the
// fake clock's now.
func (f *fixture) write(rel, content string, age time.Duration) {
	f.t.Helper()
	p := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
	m := f.clock.Now().Add(-age)
	if err := os.Chtimes(p, m, m); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) read(rel string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(f.root, filepath.FromSlash(rel)))
	if err != nil {
		return "", false
	}
	return string(data), true
}

func (f *fixture) coldBytes(rel string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(f.cold.Root, filepath.FromSlash(rel)))
	if err != nil {
		return "", false
	}
	return string(data), true
}

func (f *fixture) sync() SyncReport {
	f.t.Helper()
	rep, err := f.v.Sync(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return rep
}

func (f *fixture) evict() EvictReport {
	f.t.Helper()
	rep, err := f.v.Evict(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return rep
}

func (f *fixture) entry(rel string) Entry {
	f.t.Helper()
	e, err := f.v.Stat(context.Background(), rel)
	if err != nil {
		f.t.Fatalf("Stat(%s): %v", rel, err)
	}
	return e
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func nDays(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

func readAll(t *testing.T, p string) string {
	t.Helper()
	fh, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fh.Close() }()
	data, err := io.ReadAll(fh)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
