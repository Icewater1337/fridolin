package platform

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"
)

// Viewer is an optional monitoring window: a tmux session tailing one
// brain's activity log, opened in Terminal.app so you can watch what the brain
// is doing. It is read-only and has no part in the actual Claude interaction —
// if tmux or Terminal.app is missing, the bridge runs exactly the same.
type Viewer struct {
	Name      string // tmux session name, e.g. "claude-personal"
	StreamLog string // path to the brain's claude-stream.log, which must exist
	CWD       string
	// Env is the allowlisted environment tmux and osascript run with, with PATH
	// replaced by SystemPATH (see run). A tmux server this starts keeps it,
	// and any brain can read it from there, so it must never be the
	// bridge's whole environment. Every tmux call gets the same, so that
	// each finds the same server.
	Env []string
	// These are resolved by the caller so a viewer owns its process settings.
	TmuxPath  string
	Osascript string
	Timeout   time.Duration
}

// run runs name args… with the viewer's environment and timeout.
// tmux runs the session's sh, and sh its tail, by name, outside the
// sandbox, and a tmux server passes the PATH it gets on to its windows: so
// whatever PATH the environment has (a nil Env is the bridge's own, as for
// exec.Cmd), they all get SystemPATH, which has no folder a brain can
// write to.
func (tv *Viewer) run(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), tv.timeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = tv.Env
	env := slices.DeleteFunc(cmd.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "PATH=") })
	cmd.Env = append(env, "PATH="+SystemPATH)
	cmd.WaitDelay = time.Second
	return cmd.Run()
}

// target names the viewer's session to tmux exactly. A plain name also
// matches a session it is the start of, so that a brain "work" would kill,
// or take for its own, the viewer of a brain "work-archive".
func (tv *Viewer) target() string { return "=" + tv.Name }

// viewerScript is what the session runs: a heading, then the log. The
// session name and the log's path reach it as $1 and $2, never as part of
// the script: a brain folder may be called anything, "Alex's 100% $(x)"
// included.
const viewerScript = `printf '\033[1m=== %s — Claude activity ===\033[0m\n\n' "$1"; ` +
	`printf '\033[90mtailing %s\033[0m\n\n' "$2"; ` +
	// -F: by name, so that it follows the log on to a new file when the
	// bridge rotates it.
	`exec tail -n 100 -F "$2" 2>/dev/null`

// Start creates the session and opens its optional terminal window.
func (tv *Viewer) Start() error {
	tmux := tv.TmuxPath
	if runtime.GOOS == "windows" {
		// The session runs sh and tail, and tmux is no Windows program.
		return fmt.Errorf("creating tmux viewer %s: the viewer is not available on Windows", tv.Name)
	}

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
		attach := viewerShellQuote(tmux) + " attach -t " + viewerShellQuote(tv.target())
		if err := tv.run(tv.osascript(), "-e", "on run argv", "-e", `tell application "Terminal" to do script (item 1 of argv)`, "-e", "end run", attach); err != nil {
			log.Printf("[viewer] could not open Terminal for %s: %v (attach by hand with: tmux attach -t %s)", tv.Name, err, tv.target())
		}
	} else {
		log.Printf("[viewer] session %s ready — attach with: tmux attach -t %s", tv.Name, tv.target())
	}

	log.Printf("[viewer] started %s (stream: %s)", tv.Name, tv.StreamLog)
	return nil
}

// IsAlive reports whether the tmux session still exists.
func (tv *Viewer) IsAlive() bool {
	tmux := tv.TmuxPath
	if tmux == "" {
		return false
	}
	return tv.run(tmux, "has-session", "-t", tv.target()) == nil
}

// Close ends the tmux session, and with it the Terminal window attached.
func (tv *Viewer) Close() {
	if tmux := tv.TmuxPath; tmux != "" {
		_ = tv.run(tmux, "kill-session", "-t", tv.target())
		log.Printf("[viewer] closed %s", tv.Name)
	}
}

// SystemPATH is the fixed PATH for the system programs a viewer runs.
const SystemPATH = "/usr/bin:/bin:/usr/sbin:/sbin"

func (tv *Viewer) timeout() time.Duration {
	if tv.Timeout == 0 {
		return 5 * time.Second
	}
	return tv.Timeout
}

func (tv *Viewer) osascript() string {
	if tv.Osascript == "" {
		return "/usr/bin/osascript"
	}
	return tv.Osascript
}

func viewerShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
