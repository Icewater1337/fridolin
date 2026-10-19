package brain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// TestViewerClosedWhileItStarts: a viewer whose start was under way when
// closeViewer ran (the setting went off, or the brain did) is closed as
// soon as it is up, and not kept; one nothing closed is kept.
func TestViewerClosedWhileItStarts(t *testing.T) {
	cases := []struct {
		name   string
		closed bool // closeViewer runs while the viewer starts
	}{
		{"closed while starting", true},
		{"left alone", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A tmux that only notes what it is told: Close kills the
			// session through it.
			bin := t.TempDir()
			calls := filepath.Join(bin, "calls")
			if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\necho \"$*\" >> \""+calls+"\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}

			starting, release := make(chan struct{}), make(chan struct{})
			br := bareBrain(t)
			br.viewerStart = func(name, streamLog, cwd string, env []string) (*platform.Viewer, error) {
				close(starting)
				<-release
				return &platform.Viewer{TmuxPath: filepath.Join(bin, "tmux"), Osascript: "/usr/bin/false", Timeout: 5 * time.Second, Name: name, StreamLog: streamLog, CWD: cwd, Env: env}, nil
			}

			logs := captureLog(t)

			br.ensureViewer(&Settings{Viewer: true})
			<-starting
			if tc.closed {
				br.closeViewer()
			}
			close(release)
			viewerIdle(t, br)
			if tc.closed {
				// Close logs last, after its tmux call: waiting for that
				// also orders the start's use of tmuxPath before the
				// cleanup that puts it back.
				testutil.WaitFor(t, 5*time.Second, "the viewer to be closed", func() bool {
					return strings.Contains(strings.Join(logs(), "\n"), "[viewer] closed claude-a")
				})
			}
			data, _ := os.ReadFile(calls)
			if killed := strings.Contains(string(data), "kill-session -t =claude-a"); killed != tc.closed {
				t.Errorf("session killed = %v, want %v (tmux calls: %q)", killed, tc.closed, data)
			}
			br.viewerMu.Lock()
			kept := br.tmuxViewer != nil
			br.viewerMu.Unlock()
			if kept == tc.closed {
				t.Errorf("viewer kept = %v, want %v", kept, !tc.closed)
			}
		})
	}
}

// TestViewerStartDoesNotHoldUpTheRun: the viewer starts in the background,
// and each tmux or osascript call has viewerTimeout, so a tmux that hangs
// holds up neither the run nor the next start for long.
func TestViewerStartDoesNotHoldUpTheRun(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	const viewerTimeout = time.Second
	br := bareBrain(t)
	br.viewerStart = func(name, streamLog, cwd string, env []string) (*platform.Viewer, error) {
		v := &platform.Viewer{TmuxPath: filepath.Join(bin, "tmux"), Osascript: "/usr/bin/false", Timeout: viewerTimeout, Name: name, StreamLog: streamLog, CWD: cwd, Env: env}
		return v, v.Start()
	}

	start := time.Now()
	br.ensureViewer(&Settings{Viewer: true})
	// Well under one call's timeout: a start in the run's path, bounded
	// as it is, would wait for at least that one.
	if el := time.Since(start); el > viewerTimeout/2 {
		t.Errorf("ensureViewer took %v", el)
	}
	viewerIdle(t, br)
	if el := time.Since(start); el > 10*time.Second {
		t.Errorf("the start took %v with each call bounded by %v", el, viewerTimeout)
	}
}
