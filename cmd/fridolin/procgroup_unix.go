//go:build unix

package main

import (
	"os/exec"
	"syscall"
	"time"
)

// isolateProcessGroup starts cmd in a session of its own, which makes it the
// leader of its own process group, so stopping a run also stops everything
// Claude started (shell commands, scripts) instead of leaving them holding
// the output pipe. The new session has no controlling terminal: a run
// started by a bridge in your terminal (dev mode, or `fridolin` in the
// foreground) could otherwise open /dev/tty and push keystrokes into your
// shell with TIOCSTI, which then ran them outside any sandbox.
func isolateProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process.Pid) }
	cmd.WaitDelay = 5 * time.Second
}

// killProcessGroup kills the process group led by pid.
func killProcessGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}
