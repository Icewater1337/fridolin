package testutil

import (
	"os"
	"syscall"
)

// newSessionAttr starts a process in a process group of its own, the
// nearest Windows has to a session of its own.
func newSessionAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// stillActive is STILL_ACTIVE, the exit code of a process still running.
const stillActive = 259

// PIDAlive reports whether the process pid is still running.
func PIDAlive(pid int) bool {
	const processQueryLimitedInformation = 0x1000
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	return syscall.GetExitCodeProcess(h, &code) == nil && code == stillActive
}

// KillPID kills the process pid, if there is one.
func KillPID(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
