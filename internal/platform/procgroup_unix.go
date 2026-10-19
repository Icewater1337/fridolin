//go:build unix

package platform

import (
	"os/exec"
	"syscall"
	"time"
)

// IsolateProcessGroup starts cmd in a session of its own, which makes it the
// leader of its own process group, so stopping a run also stops everything
// Claude started (shell commands, scripts) instead of leaving them holding
// the output pipe. The new session has no controlling terminal: a run
// started by a bridge in your terminal (dev mode, or `fridolin` in the
// foreground) could otherwise open /dev/tty and push keystrokes into your
// shell with TIOCSTI, which then ran them outside any sandbox.
func IsolateProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return KillProcessGroup(cmd.Process.Pid) }
	cmd.WaitDelay = 5 * time.Second
}

// KillProcessGroup kills the process group led by pid.
func KillProcessGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}
