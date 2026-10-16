package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSandboxFailsClosed: with the sandbox on, a run whose sandbox cannot be
// set up is refused before anything starts, the tmux viewer included. It used
// to fall back to an unsandboxed spawn that still skipped every permission
// prompt.
func TestSandboxFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		breakIt func(sp *SandboxProfiler, home string)
	}{
		{"sandbox-exec missing", func(sp *SandboxProfiler, home string) {
			sp.execPath = filepath.Join(home, "no-sandbox-exec")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr, d, home := queueTestManager(t, 0, `, "viewer": true`)
			argsLog := filepath.Join(home, "args.log")
			t.Setenv("WCB_FAKE_ARGS_LOG", argsLog)
			tmuxCalls := fakeTmux(t)
			sp := mgr.profiler
			sp.mu.Lock()
			sp.enabled = true
			tc.breakIt(sp, home)
			sp.mu.Unlock()

			if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 10*time.Second, "the run", func() bool { return d.count() == 1 })

			res := d.all()[0].res
			if !errors.Is(res.Err, errSandboxUnavailable) {
				t.Fatalf("run result = %+v, want errSandboxUnavailable", res)
			}
			if strings.Contains(res.Err.Error(), home) {
				t.Errorf("the chat error names a local path: %v", res.Err)
			}
			if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
				t.Error("the CLI was started without the sandbox")
			}
			if _, err := os.Stat(tmuxCalls); !os.IsNotExist(err) {
				t.Error("a refused run started the tmux viewer")
			}
			if last := mgr.Get("a").Snapshot().Last; last == nil || last.Outcome != "error" {
				t.Errorf("last run = %+v, want outcome error", last)
			}

			// Control: with the sandbox off the same brain does start the
			// viewer, so the check above would have seen it.
			sp.mu.Lock()
			sp.enabled = false
			sp.mu.Unlock()
			if _, _, _, err := mgr.Submit(dm("again")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 10*time.Second, "the second run", func() bool { return d.count() == 2 })
			if _, err := os.Stat(tmuxCalls); err != nil {
				t.Errorf("the unsandboxed run did not start the viewer: %v", err)
			}
		})
	}
}

// fakeTmux makes the activity viewer use a tmux that only logs its calls to
// the returned file and then fails, so the viewer never gets as far as
// opening a Terminal window.
func fakeTmux(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls.log")
	t.Setenv("WCB_FAKE_TMUX_LOG", calls)
	script := filepath.Join(dir, "tmux")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\" >> \"$WCB_FAKE_TMUX_LOG\"\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	tmuxOnce.Do(func() {}) // keep findTmux from looking up the real one
	old := tmuxPath
	tmuxPath = script
	t.Cleanup(func() { tmuxPath = old })
	return calls
}
