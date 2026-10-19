package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/testutil"
)

// The dashboard shows the required files missing now: one restored
// stops being reported, one deleted later is, without a restart.
func TestWarningsFollowTheRequiredFiles(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	tb := newTestBridge(t, `, "startup_checks": {"required_files": ["`+a+`", "`+b+`"], "hint": "restore them"}`)
	if err := os.WriteFile(b, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if w := tb.Warnings(); len(w) != 1 || !strings.Contains(w[0], a) || strings.Contains(w[0], b) || !strings.Contains(w[0], "restore them") {
		t.Errorf("warnings = %q, want %s missing", w, a)
	}
	if err := os.WriteFile(a, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if w := tb.Warnings(); len(w) != 0 {
		t.Errorf("warnings = %q after the file came back", w)
	}
	if err := os.Remove(b); err != nil {
		t.Fatal(err)
	}
	if w := tb.Warnings(); len(w) != 1 || !strings.Contains(w[0], b) {
		t.Errorf("warnings = %q, want %s missing", w, b)
	}
}

// The alert goes out once per change of what is missing, not on every
// look, and again once a file goes missing after all were back.
func TestRequiredFilesAlertOncePerChange(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	tb := newTestBridge(t, `, "startup_checks": {"required_files": ["`+a+`"], "notify_whatsapp": true}`)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		tb.watchRequiredFiles(ctx, 20*time.Millisecond)
	}()
	alerts := func() int {
		n := 0
		for _, s := range tb.sentTo(testDMChat) {
			if strings.HasPrefix(s, "⚠️ Bridge startup check:") {
				n++
			}
		}
		return n
	}
	testutil.WaitFor(t, 5*time.Second, "the alert", func() bool { return alerts() == 1 })
	time.Sleep(150 * time.Millisecond) // several more looks
	if n := alerts(); n != 1 {
		t.Errorf("%d alerts for one change, want 1", n)
	}
	if err := os.WriteFile(a, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 5*time.Second, "the second alert", func() bool { return alerts() == 2 })
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the watcher did not end with its context")
	}
	if got := tb.sentTo(testDMChat); !slices.ContainsFunc(got, func(s string) bool { return strings.Contains(s, a) }) {
		t.Errorf("sent %q, want the file named", got)
	}
}
