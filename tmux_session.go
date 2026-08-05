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
	StreamLog string // path to the brain's claude-stream.log
	CWD       string
}

var (
	tmuxOnce sync.Once
	tmuxPath string
)

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
// log and opens a Terminal window attached to it.
func NewTmuxViewer(name, streamLog, cwd string) (*TmuxViewer, error) {
	if findTmux() == "" {
		return nil, fmt.Errorf("tmux not found on PATH")
	}
	tv := &TmuxViewer{Name: name, StreamLog: streamLog, CWD: cwd}
	if err := tv.start(); err != nil {
		return nil, err
	}
	return tv, nil
}

func (tv *TmuxViewer) start() error {
	tmux := findTmux()

	// Replace any stale session of the same name.
	_ = exec.Command(tmux, "kill-session", "-t", tv.Name).Run()
	time.Sleep(300 * time.Millisecond)

	// tail fails on a file that does not exist yet.
	_ = exec.Command("touch", tv.StreamLog).Run()

	tailCmd := fmt.Sprintf(
		`printf '\033[1m=== %s — Claude activity ===\033[0m\n\n'; `+
			`printf '\033[90mtailing %s\033[0m\n\n'; `+
			`tail -n 100 -f %q 2>/dev/null`,
		tv.Name, tv.StreamLog, tv.StreamLog,
	)

	cmd := exec.Command(tmux, "new-session", "-d", "-s", tv.Name, "-c", tv.CWD, "sh", "-c", tailCmd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("creating tmux viewer %s: %w", tv.Name, err)
	}
	_ = exec.Command(tmux, "rename-window", "-t", tv.Name, tv.Name).Run()

	// Opening a window is macOS-specific and entirely optional — the tmux
	// session exists either way and can be attached to by hand.
	if runtime.GOOS == "darwin" {
		script := fmt.Sprintf(`tell application "Terminal"
	activate
	do script "%s attach -t %s"
end tell`, tmux, tv.Name)
		if err := exec.Command("osascript", "-e", script).Run(); err != nil {
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
	return exec.Command(tmux, "has-session", "-t", tv.Name).Run() == nil
}

// Close terminates the tmux session.
func (tv *TmuxViewer) Close() {
	if tmux := findTmux(); tmux != "" {
		_ = exec.Command(tmux, "kill-session", "-t", tv.Name).Run()
		log.Printf("[viewer] closed %s", tv.Name)
	}
}
