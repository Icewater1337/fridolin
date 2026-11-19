package outbound

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// fakePresence records the chat states a Typist sends, with the time
// since the test began.
type fakePresence struct {
	start time.Time
	err   error

	mu     sync.Mutex
	states []string // "<seconds>s <jid> on|off"
}

func (f *fakePresence) Typing(_ context.Context, jid string, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	state := "off"
	if on {
		state = "on"
	}
	f.states = append(f.states, fmt.Sprintf("%vs %s %s", time.Since(f.start).Seconds(), jid, state))
	return f.err
}
func (f *fakePresence) MarkRead(context.Context, string, []string) error   { return nil }
func (f *fakePresence) MarkPlayed(context.Context, string, []string) error { return nil }

// captureStdLog sends the standard logger to a buffer for the test, and
// returns what it holds.
func captureStdLog(t *testing.T) func() string {
	t.Helper()
	var mu sync.Mutex
	var b strings.Builder
	prev := log.Writer()
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return b.Write(p)
	}))
	t.Cleanup(func() { log.SetOutput(prev) })
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return b.String()
	}
}

type writerFunc func([]byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) { return w(p) }

func (f *fakePresence) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.states)
}

// A Typist shows typing from a chat's first run to its last one's end,
// says so again every TypingEvery, and keeps the chats apart.
func TestTypistKeepsTypingWhileRunsWork(t *testing.T) {
	cases := []struct {
		name  string
		steps func(ty *Typist)
		want  []string
	}{
		{"one run", func(ty *Typist) {
			end := ty.Start("a")
			time.Sleep(50 * time.Second)
			end()
		}, []string{"0s a on", "20s a on", "40s a on", "50s a off"}},
		{"two runs in one chat", func(ty *Typist) {
			endA := ty.Start("a")
			time.Sleep(10 * time.Second)
			endB := ty.Start("a")
			time.Sleep(15 * time.Second)
			endA()
			endA() // once is enough
			time.Sleep(5 * time.Second)
			endB()
		}, []string{"0s a on", "20s a on", "30s a off"}},
		{"two chats", func(ty *Typist) {
			endA := ty.Start("a")
			time.Sleep(5 * time.Second)
			endB := ty.Start("b")
			time.Sleep(5 * time.Second)
			endA()
			time.Sleep(5 * time.Second)
			endB()
		}, []string{"0s a on", "5s b on", "10s a off", "15s b off"}},
		{"a run that ended before typing showed, then another", func(ty *Typist) {
			ty.Start("a")()
			synctest.Wait()
			time.Sleep(time.Second)
			end := ty.Start("a")
			time.Sleep(time.Second)
			end()
		}, []string{"1s a on", "2s a off"}},
		{"a run after the last one ended", func(ty *Typist) {
			end := ty.Start("a")
			synctest.Wait()
			end()
			synctest.Wait()
			time.Sleep(time.Second)
			end = ty.Start("a")
			time.Sleep(time.Second)
			end()
		}, []string{"0s a on", "0s a off", "1s a on", "2s a off"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := &fakePresence{start: time.Now()}
				ty := NewTypist(func() Presence { return f }, TypingEvery)
				c.steps(ty)
				synctest.Wait()
				if got := f.sent(); !slices.Equal(got, c.want) {
					t.Errorf("sent %q, want %q", got, c.want)
				}
				ty.mu.Lock()
				defer ty.mu.Unlock()
				if len(ty.chats) != 0 {
					t.Errorf("chats left behind: %v", ty.chats)
				}
			})
		})
	}
}

// A run that starts while the last one's pause is on its way keeps the
// chat typing: the pause never comes after it.
func TestTypistNeverPausesAfterALaterStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakePresence{start: time.Now()}
		ty := NewTypist(func() Presence { return f }, TypingEvery)
		ty.Start("a")()
		end := ty.Start("a")
		synctest.Wait()
		got := f.sent()
		if len(got) == 0 || !strings.HasSuffix(got[len(got)-1], " on") {
			t.Fatalf("sent %q, want typing on last", got)
		}
		end()
		synctest.Wait()
		if got := f.sent(); !strings.HasSuffix(got[len(got)-1], " off") {
			t.Errorf("sent %q, want typing off last", got)
		}
	})
}

// Without a transport that shows presence, nothing is sent and nothing
// is left running; a failure is logged once a stretch, and an offline
// WhatsApp not at all.
func TestTypistWithoutPresenceOrWithErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, p := range []func() Presence{nil, func() Presence { return nil }} {
			ty := NewTypist(p, TypingEvery)
			end := ty.Start("a")
			time.Sleep(time.Minute)
			end()
			synctest.Wait()
			if len(ty.chats) != 0 {
				t.Errorf("chats left behind: %v", ty.chats)
			}
		}
	})
	for _, c := range []struct {
		err  error
		logs int
	}{{errors.New("refused"), 1}, {fmt.Errorf("%w: down", ErrOffline), 0}} {
		synctest.Test(t, func(t *testing.T) {
			logs := captureStdLog(t)
			f := &fakePresence{start: time.Now(), err: c.err}
			ty := NewTypist(func() Presence { return f }, TypingEvery)
			end := ty.Start("a")
			time.Sleep(time.Minute)
			end()
			synctest.Wait()
			if n := strings.Count(logs(), "could not show typing"); n != c.logs {
				t.Errorf("%v: logged %d times, want %d", c.err, n, c.logs)
			}
		})
	}
}
