//go:build unix

package main

import (
	"os/exec"
	"syscall"
	"time"
)

// isolateProcessGroup starts cmd in its own process group, so stopping a run
// also stops everything Claude started (shell commands, scripts) instead of
// leaving them holding the output pipe.
func isolateProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
