package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/logx"
	mediafiles "github.com/Icewater1337/fridolin/internal/media"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

const (
	servicePlaudOld1 = "of_0000000000000000000001"
	servicePlaudOld2 = "of_0000000000000000000002"
	servicePlaudNew  = "of_0000000000000000000003"
)

type plaudServiceFixture struct {
	plaud    *PlaudManager
	settings atomic.Pointer[PlaudSnapshot]
	home     string
	dev      *plaudServiceNotices
	options  PlaudOptions
}

type plaudServiceNotice struct{ JID, Text string }
type plaudServiceNotices struct {
	mu       sync.Mutex
	messages []plaudServiceNotice
}

func (n *plaudServiceNotices) Send(_ context.Context, jid, text string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.messages = append(n.messages, plaudServiceNotice{jid, text})
	return nil
}
func (n *plaudServiceNotices) Outbox() []plaudServiceNotice {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.messages)
}

// plaudService supplies only owned settings, target callbacks and isolated
// process inputs. It neither constructs a Bridge nor changes process globals.
func plaudService(t *testing.T, extra string) (*plaudServiceFixture, *testutil.PlaudFixture) {
	t.Helper()
	fp := testutil.NewPlaudFixture(t)
	root := t.TempDir()
	f := &plaudServiceFixture{home: filepath.Join(root, "home"), dev: &plaudServiceNotices{}}
	settings := PlaudSettings{Enabled: true, CLIPath: os.Args[0]}
	if err := json.Unmarshal([]byte(`{"enabled":true`+extra+`}`), &settings); err != nil {
		t.Fatal(err)
	}
	interval := settings.IntervalSeconds
	if interval <= 0 {
		interval = 60
	}
	f.settings.Store(&PlaudSnapshot{Plaud: settings, DataDir: filepath.Join(root, "data"), MediaDir: filepath.Join(root, "media"), QueueMaxPending: 10, OwnerChat: "15551234567@s.whatsapp.net", Interval: time.Duration(interval) * time.Second})
	migration := &mediafiles.Migration{Started: time.Now()}
	target := &PlaudTarget{BrainID: "p", ReplyChat: func() string { return "" }, OtherOwner: func(string) string { return "p" }, Submit: func(_, _, _ string, _ []string, done func(error)) error { done(nil); return nil }}
	f.options = PlaudOptions{
		PlaudDir: filepath.Join(root, "data", "plaud"), Home: f.home, Settings: f.settings.Load,
		Target: func(id string) *PlaudTarget {
			if id == "" || id == "p" {
				return target
			}
			return nil
		}, HeldTarget: func() string { return "" }, Notify: f.dev.Send,
		FindCLI: func(bin string) (string, error) {
			if _, err := os.Stat(bin); err != nil {
				return "", err
			}
			return bin, nil
		}, CLIVersion: func(string) string { return plaudCLITested },
		Environ: func(_ context.Context, _, home string) []string {
			return []string{"HOME=" + home, "PATH=/usr/bin:/bin", "FRIDOLIN_FAKE_PLAUD=1", "FRIDOLIN_FAKE_PLAUD_DIR=" + fp.Dir, "GORACE=atexit_sleep_ms=0"}
		},
		DesktopContext: func(ctx context.Context) context.Context { return ctx },
		Inbox:          func(dir string) mediafiles.Inbox { return mediafiles.Inbox{Dir: dir, Migration: migration} },
		RunInterrupted: func(err error) bool { return errors.Is(err, context.Canceled) },
	}
	f.plaud = newPlaudManager(f.options)
	t.Cleanup(f.plaud.Stop)
	return f, fp
}

func plaudServiceSignIn(t *testing.T, f *plaudServiceFixture) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.plaud.tokenPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.plaud.tokenPath(), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
}
func plaudServiceDeliver(t *testing.T, f *plaudServiceFixture, fp *testutil.PlaudFixture) error {
	t.Helper()
	plaudServiceSignIn(t, f)
	if err := f.plaud.saveState(plaudServiceSeededState("", servicePlaudOld1)); err != nil {
		t.Fatal(err)
	}
	fp.Files(servicePlaudNew, servicePlaudOld1)
	fp.Recording(servicePlaudNew, true)
	return f.plaud.Poll(context.Background())
}
func plaudServiceAfterRestart(t *testing.T, f *plaudServiceFixture) *PlaudManager {
	t.Helper()
	p := newPlaudManager(f.options)
	t.Cleanup(p.Stop)
	return p
}
func plaudServiceSeededState(account string, seen ...string) *plaudState {
	st := newPlaudState()
	st.Seeded, st.Account, st.Seen = true, account, seen
	return st
}
func plaudServiceWaiting(st *plaudState, id string, at time.Time) *plaudState {
	st.Pending[id] = &plaudPending{FirstSeen: at}
	return st
}
func (f *plaudServiceFixture) plaudForwarded() bool {
	files, _ := filepath.Glob(filepath.Join(f.settings.Load().MediaDir, "*", "plaud", "*.txt"))
	return len(files) > 0
}
func (f *plaudServiceFixture) plaudSeenAll(ids ...string) bool {
	st, _ := f.plaud.loadState()
	for _, id := range ids {
		if st == nil || st.Pending[id] != nil || !slices.Contains(st.Seen, id) {
			return false
		}
	}
	return true
}
func (f *plaudServiceFixture) waitPlaudSeen(t *testing.T, id string) {
	t.Helper()
	testutil.WaitFor(t, 10*time.Second, id+" to be seen", func() bool { return f.plaudSeenAll(id) })
}
func (f *plaudServiceFixture) plaudSeeded() bool {
	st, _ := f.plaud.loadState()
	return st.seededFor("")
}
func plaudServiceCaptureLog(t *testing.T) func() []string {
	t.Helper()
	ring := logx.NewRing(50)
	prev := log.Writer()
	log.SetOutput(ring)
	t.Cleanup(func() { log.SetOutput(prev) })
	return func() []string {
		lines, _ := ring.Since(0, 50)
		var out []string
		for _, l := range lines {
			out = append(out, l.Text)
		}
		return out
	}
}

type plaudServiceRecorder struct {
	mu     sync.Mutex
	handed []string
	done   map[string]func(error)
	refuse error
}

func (r *plaudServiceRecorder) submit(_ *PlaudTarget, _, _, _ string, media []string, done func(error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refuse != nil {
		return r.refuse
	}
	id := strings.TrimSuffix(media[0][strings.LastIndex(media[0], " - ")+3:], ".txt")
	r.handed = append(r.handed, id)
	if r.done == nil {
		r.done = map[string]func(error){}
	}
	r.done[id] = done
	return nil
}
func (r *plaudServiceRecorder) end(id string, err error) {
	r.mu.Lock()
	done := r.done[id]
	r.mu.Unlock()
	done(err)
}
func (r *plaudServiceRecorder) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.handed) }
func plaudServiceIDs(prefix string, n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("of_%s%021x", prefix, n-i)
	}
	return ids
}
