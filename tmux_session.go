package main

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// TmuxViewer is an optional monitoring window: a tmux session tailing one
// brain's activity log, opened in Terminal.app so you can watch what the brain
// is doing. It is read-only and has no part in the actual Claude interaction —
// if tmux or Terminal.app is missing, the bridge runs exactly the same.
type TmuxViewer struct {
	Name      string // tmux session name, e.g. "claude-personal"
	StreamLog string // path to the brain's claude-stream.log, which must exist
	CWD       string
	// Env is what tmux and osascript run with (see childEnv). A tmux server
	// this starts keeps it, and any brain can read it from there, so it
	// must never be the bridge's whole environment. Every tmux call gets the
	// same, so that each finds the same server.
	Env []string
}

var (
	tmuxOnce sync.Once
	tmuxPath string
)

// osascript opens the Terminal window, a variable so tests need not open one.
var osascript = "osascript"

// findTmux locates tmux once, in the folders of PATH that helperPath
// keeps, and caches its path, which every tmux call then runs it by.
func findTmux() string {
	tmuxOnce.Do(func() { tmuxPath = lookPathIn("tmux", helperPath()) })
	return tmuxPath
}

// NewTmuxViewer creates a detached tmux session tailing the brain's activity
// log and opens a Terminal window attached to it, running both with env.
func NewTmuxViewer(name, streamLog, cwd string, env []string) (*TmuxViewer, error) {
	if findTmux() == "" {
		return nil, fmt.Errorf("tmux not found (looked in %q)", helperPath())
	}
	tv := &TmuxViewer{Name: name, StreamLog: streamLog, CWD: cwd, Env: env}
	if err := tv.start(); err != nil {
		return nil, err
	}
	return tv, nil
}

// viewerTimeout bounds each tmux and osascript call. osascript can wait on
// an Automation consent prompt, or for a GUI session there is none of under
// launchd; a call that hangs must not keep the next viewer from starting.
var viewerTimeout = 5 * time.Second

// run runs name args… with the viewer's environment and viewerTimeout.
func (tv *TmuxViewer) run(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), viewerTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = tv.Env
	cmd.WaitDelay = time.Second
	return cmd.Run()
}

// target names the viewer's session to tmux exactly. A plain name also
// matches a session it is the start of, so that a brain "work" would kill,
// or take for its own, the viewer of a brain "work-archive".
func (tv *TmuxViewer) target() string { return "=" + tv.Name }

// viewerScript is what the session runs: a heading, then the log. The
// session name and the log's path reach it as $1 and $2, never as part of
// the script: a brain folder may be called anything, "Alex's 100% $(x)"
// included.
const viewerScript = `printf '\033[1m=== %s — Claude activity ===\033[0m\n\n' "$1"; ` +
	`printf '\033[90mtailing %s\033[0m\n\n' "$2"; ` +
	// -F: by name, so that it follows the log on to a new file when the
	// bridge rotates it.
	`exec tail -n 100 -F "$2" 2>/dev/null`

func (tv *TmuxViewer) start() error {
	tmux := findTmux()

	// Replace a stale session of the same name, and only that one.
	_ = tv.run(tmux, "kill-session", "-t", tv.target())

	// With more than one argument tmux runs the command itself, no shell
	// in between (tmux 3.0 or newer).
	if err := tv.run(tmux, "new-session", "-d", "-s", tv.Name, "-n", tv.Name, "-c", tv.CWD,
		"sh", "-c", viewerScript, "sh", tv.Name, tv.StreamLog); err != nil {
		return fmt.Errorf("creating tmux viewer %s: %w", tv.Name, err)
	}

	// Opening a window is macOS-specific and entirely optional — the tmux
	// session exists either way and can be attached to by hand. The command
	// goes to the script as an argument, and Terminal does not come to the
	// front: a viewer started again by a run must not take the focus.
	if runtime.GOOS == "darwin" {
		attach := shellQuote(tmux) + " attach -t " + shellQuote(tv.target())
		if err := tv.run(osascript, "-e", "on run argv", "-e", `tell application "Terminal" to do script (item 1 of argv)`, "-e", "end run", attach); err != nil {
			log.Printf("[viewer] could not open Terminal for %s: %v (attach by hand with: tmux attach -t %s)", tv.Name, err, tv.target())
		}
	} else {
		log.Printf("[viewer] session %s ready — attach with: tmux attach -t %s", tv.Name, tv.target())
	}

	log.Printf("[viewer] started %s (stream: %s)", tv.Name, tv.StreamLog)
	return nil
}

// IsAlive reports whether the tmux session still exists.
func (tv *TmuxViewer) IsAlive() bool {
	tmux := findTmux()
	if tmux == "" {
		return false
	}
	return tv.run(tmux, "has-session", "-t", tv.target()) == nil
}

// Close ends the tmux session, and with it the Terminal window attached.
func (tv *TmuxViewer) Close() {
	if tmux := findTmux(); tmux != "" {
		_ = tv.run(tmux, "kill-session", "-t", tv.target())
		log.Printf("[viewer] closed %s", tv.Name)
	}
}
