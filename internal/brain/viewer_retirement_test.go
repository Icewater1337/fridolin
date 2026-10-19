package brain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// The activity viewer's tmux session, and so its Terminal window, ends
// when its brain is removed or the viewer setting is turned off. It stays
// when the bridge stops, for the next start to take over by its name.
func TestViewerClosesWithItsBrain(t *testing.T) {
	cases := []struct {
		name   string
		change func(t *testing.T, mgr *Manager, globals Globals, specs []manifest.Spec)
		closed bool
	}{
		{"brain removed", func(t *testing.T, mgr *Manager, globals Globals, specs []manifest.Spec) {
			if _, err := mgr.Apply(nil, globals, ApplyOptions{}); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"viewer turned off", func(t *testing.T, mgr *Manager, globals Globals, specs []manifest.Spec) {
			globals.Defaults.Viewer = false
			if _, err := mgr.Apply(specs, globals, ApplyOptions{}); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"bridge stopped", func(t *testing.T, mgr *Manager, _ Globals, _ []manifest.Spec) {
			mgr.Stop()
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mgr, _, home := queueTestManager(t, 0, "")
			calls := filepath.Join(home, "tmux-calls")
			tmux := filepath.Join(home, "tmux")
			if err := os.WriteFile(tmux, []byte("#!/bin/sh\necho \"$*\" >> \""+calls+"\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			specs := []manifest.Spec{{Dir: mgr.Get("a").Dir(), Manifest: mgr.Get("a").Manifest()}}
			globals := Globals{Defaults: Settings{Viewer: true}}
			if _, err := mgr.Apply(specs, globals, ApplyOptions{}); err != nil {
				t.Fatal(err)
			}
			br := mgr.Get("a")
			br.tmuxViewer = &platform.Viewer{Name: "claude-a", TmuxPath: tmux}

			logs := captureLog(t)

			c.change(t, mgr, globals, specs)
			if c.closed {
				// The line comes last, from the goroutine that retires the
				// brain, which is then done with the fake tmux.
				testutil.WaitFor(t, 5*time.Second, "the viewer to close", func() bool { return logContains(logs, "[viewer] closed claude-a") })
			}
			data, _ := os.ReadFile(calls)
			if killed := strings.Contains(string(data), "kill-session -t =claude-a"); killed != c.closed {
				t.Errorf("tmux calls %q, want the session killed: %v", data, c.closed)
			}
		})
	}
}
