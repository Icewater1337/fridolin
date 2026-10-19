package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/platform"
)

var (
	tmuxOnce sync.Once
	tmuxPath string
)

// osascript opens the Terminal window: the system's, by its path, since it
// runs outside the sandbox. A variable so tests need not open one.
var osascript = "/usr/bin/osascript"

// findTmux locates tmux once, in the folders of PATH that helperPath
// keeps, and caches its path, which every tmux call then runs it by.
func findTmux() string {
	tmuxOnce.Do(func() { tmuxPath = lookPathIn("tmux", helperPath()) })
	return tmuxPath
}

// NewTmuxViewer creates a detached tmux session tailing the brain's activity
// log and opens a Terminal window attached to it, running both with env.
func NewTmuxViewer(name, streamLog, cwd string, env []string) (*platform.Viewer, error) {
	if findTmux() == "" {
		return nil, fmt.Errorf("tmux not found (looked in %q)", helperPath())
	}
	tv := &platform.Viewer{Name: name, StreamLog: streamLog, CWD: cwd, Env: env, TmuxPath: findTmux(), Osascript: osascript, Timeout: viewerTimeout}
	if err := tv.Start(); err != nil {
		return nil, err
	}
	return tv, nil
}

// viewerTimeout bounds each tmux and osascript call. osascript can wait on
// an Automation consent prompt, or for a GUI session there is none of under
// launchd; a call that hangs must not keep the next viewer from starting.
var viewerTimeout = 5 * time.Second
