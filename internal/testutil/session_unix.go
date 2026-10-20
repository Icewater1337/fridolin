//go:build unix

package testutil

import "syscall"

// newSessionAttr starts a process in a session of its own, outside the
// run's process group.
func newSessionAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

// PIDAlive reports whether the process pid exists, a zombie included.
func PIDAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// KillPID kills the process pid, if there is one.
func KillPID(pid int) { _ = syscall.Kill(pid, syscall.SIGKILL) }
