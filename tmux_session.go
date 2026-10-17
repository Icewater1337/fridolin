package main

import (
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

// findTmux locates tmux on PATH once and caches the result.
func findTmux() string {
	tmuxOnce.Do(func() {
		if p, err := exec.LookPath("tmux"); err == nil {
			tmuxPath = p
		}
	})
	return tmuxPath
}

// NewTmuxViewer creates a detached tmux session tailing the brain's activity
// log and opens a Terminal window attached to it, running both with env.
func NewTmuxViewer(name, streamLog, cwd string, env []string) (*TmuxViewer, error) {
	if findTmux() == "" {
		return nil, fmt.Errorf("tmux not found on PATH")
	}
	tv := &TmuxViewer{Name: name, StreamLog: streamLog, CWD: cwd, Env: env}
	if err := tv.start(); err != nil {
		return nil, err
	}
	return tv, nil
}

// command is name args… with the viewer's environment.
func (tv *TmuxViewer) command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Env = tv.Env
	return cmd
}

func (tv *TmuxViewer) start() error {
	tmux := findTmux()

	// Replace any stale session of the same name.
	_ = tv.command(tmux, "kill-session", "-t", tv.Name).Run()
	time.Sleep(300 * time.Millisecond)

	tailCmd := fmt.Sprintf(
		`printf '\033[1m=== %s — Claude activity ===\033[0m\n\n'; `+
			`printf '\033[90mtailing %s\033[0m\n\n'; `+
			`tail -n 100 -f %q 2>/dev/null`,
		tv.Name, tv.StreamLog, tv.StreamLog,
	)

	cmd := tv.command(tmux, "new-session", "-d", "-s", tv.Name, "-c", tv.CWD, "sh", "-c", tailCmd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("creating tmux viewer %s: %w", tv.Name, err)
	}
	_ = tv.command(tmux, "rename-window", "-t", tv.Name, tv.Name).Run()

	// Opening a window is macOS-specific and entirely optional — the tmux
	// session exists either way and can be attached to by hand.
	if runtime.GOOS == "darwin" {
		script := fmt.Sprintf(`tell application "Terminal"
	activate
	do script "%s attach -t %s"
end tell`, tmux, tv.Name)
		if err := tv.command(osascript, "-e", script).Run(); err != nil {
			log.Printf("[viewer] could not open Terminal for %s: %v (attach manually with: tmux attach -t %s)", tv.Name, err, tv.Name)
		}
	} else {
		log.Printf("[viewer] session %s ready — attach with: tmux attach -t %s", tv.Name, tv.Name)
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
	return tv.command(tmux, "has-session", "-t", tv.Name).Run() == nil
}

// Close terminates the tmux session.
func (tv *TmuxViewer) Close() {
	if tmux := findTmux(); tmux != "" {
		_ = tv.command(tmux, "kill-session", "-t", tv.Name).Run()
		log.Printf("[viewer] closed %s", tv.Name)
	}
}
