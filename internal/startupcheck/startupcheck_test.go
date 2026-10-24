package startupcheck

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// sent is one alert handed to Send.
type sent struct {
	chat, text string
	max        int
}

// fakeWatcher is a watcher whose settings the test sets and whose sends it
// records; failing makes the next send fail.
type fakeWatcher struct {
	watcher
	s       Settings
	sent    []sent
	failing bool
}

func newFakeWatcher(s Settings) *fakeWatcher {
	f := &fakeWatcher{s: s}
	f.options = Options{
		Settings: func() Settings { return f.s },
		Send: func(_ context.Context, chat, text string, max int) error {
			if f.failing {
				f.failing = false
				return errors.New("not connected")
			}
			f.sent = append(f.sent, sent{chat, text, max})
			return nil
		},
	}
	return f
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func remove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func TestWarningsNameTheMissingFilesWithTheHint(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	s := Settings{RequiredFiles: []string{a, b}, Hint: "restore them"}
	want := "2 required file(s) are missing: " + a + ", " + b + " — restore them"
	if got := Warnings(s); !slices.Equal(got, []string{want}) {
		t.Errorf("Warnings = %q, want %q", got, want)
	}
	touch(t, b)
	s.Hint = ""
	want = "1 required file(s) are missing: " + a
	if got := Warnings(s); !slices.Equal(got, []string{want}) {
		t.Errorf("Warnings = %q, want %q", got, want)
	}
	touch(t, a)
	if got := Warnings(s); got != nil {
		t.Errorf("Warnings = %q with every file there, want nil", got)
	}
	if got := Warnings(Settings{}); got != nil {
		t.Errorf("Warnings = %q without required files, want nil", got)
	}
}

// The alert goes out once per change of what is missing, not on every
// look, and again once a file goes missing after all were back.
func TestAlertOncePerChange(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	f := newFakeWatcher(Settings{RequiredFiles: []string{a, b}, Hint: "restore them", Notify: true, Chat: "15551234567@s.whatsapp.net", MaxChunkSize: 1234})
	touch(t, b)
	ctx := context.Background()
	f.look(ctx)
	f.look(ctx)
	want := sent{"15551234567@s.whatsapp.net", "⚠️ Bridge startup check: 1 required file(s) are missing: " + a + " — restore them", 1234}
	if !slices.Equal(f.sent, []sent{want}) {
		t.Fatalf("sent %v after two looks, want %v", f.sent, want)
	}
	remove(t, b) // others are missing now
	f.look(ctx)
	if len(f.sent) != 2 || f.sent[1].text != "⚠️ Bridge startup check: 2 required file(s) are missing: "+a+", "+b+" — restore them" {
		t.Fatalf("sent %v, want a second alert naming both", f.sent)
	}
	touch(t, a)
	touch(t, b)
	f.look(ctx)
	f.look(ctx)
	if len(f.sent) != 2 {
		t.Fatalf("sent %v once the files were back, want nothing more", f.sent)
	}
	remove(t, a)
	f.look(ctx)
	if len(f.sent) != 3 || f.sent[2] != f.sent[0] {
		t.Fatalf("sent %v, want the first alert again once a went missing again", f.sent)
	}
}

// A failed send is tried again on the next look.
func TestFailedAlertIsSentOnTheNextLook(t *testing.T) {
	a := filepath.Join(t.TempDir(), "a.json")
	f := newFakeWatcher(Settings{RequiredFiles: []string{a}, Notify: true, Chat: "15551234567@s.whatsapp.net"})
	f.failing = true
	f.look(context.Background())
	if len(f.sent) != 0 {
		t.Fatalf("sent %v, want the send to fail", f.sent)
	}
	f.look(context.Background())
	if len(f.sent) != 1 {
		t.Fatalf("sent %v, want the alert on the second look", f.sent)
	}
}

// Without notify_whatsapp, or with no chat to tell, nothing is sent.
func TestNoAlertWithoutNotifyOrChat(t *testing.T) {
	a := filepath.Join(t.TempDir(), "a.json")
	for _, s := range []Settings{
		{RequiredFiles: []string{a}, Chat: "15551234567@s.whatsapp.net"},
		{RequiredFiles: []string{a}, Notify: true},
	} {
		f := newFakeWatcher(s)
		f.look(context.Background())
		if len(f.sent) != 0 {
			t.Errorf("settings %+v: sent %v, want nothing", s, f.sent)
		}
	}
}

// Watch looks at once, again on each tick, and ends with its context.
func TestWatchLooksUntilTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	looks := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		Watch(ctx, time.Millisecond, Options{Settings: func() Settings {
			if looks++; looks == 3 {
				cancel()
			}
			return Settings{}
		}})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Watch did not end with its context")
	}
	// A tick due with the cancellation may still be taken first.
	if looks < 3 {
		t.Errorf("%d looks, want at least 3", looks)
	}
}
