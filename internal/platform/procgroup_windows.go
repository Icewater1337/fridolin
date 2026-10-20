package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// IsolateProcessGroup starts cmd in a process group of its own and with a
// console of its own that has no window, so stopping a run also stops the
// commands Claude started, and nothing it starts can write keystrokes into
// the console of a bridge run in the foreground (WriteConsoleInput, the
// counterpart of TIOCSTI). Windows has no process-group kill: the group
// is stopped as the tree of processes under pid (KillProcessGroup).
func IsolateProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | createNoWindow}
	cmd.Cancel = func() error { return KillProcessGroup(cmd.Process.Pid) }
	cmd.WaitDelay = 5 * time.Second
}

// createNoWindow is CREATE_NO_WINDOW, which package syscall does not name.
const createNoWindow = 0x08000000

// taskkillNotFound is taskkill's exit status when there is no process pid.
const taskkillNotFound = 128

// KillProcessGroup kills pid and every process below it, with taskkill
// /T: unlike a Unix process group, the tree is found through each
// process's parent, so it misses what a process that has ended already
// started, and it finds nothing once pid itself has ended.
func KillProcessGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	taskkill := "taskkill.exe"
	if root := os.Getenv("SystemRoot"); root != "" {
		taskkill = filepath.Join(root, "System32", "taskkill.exe")
	}
	cmd := exec.Command(taskkill, "/T", "/F", "/PID", strconv.Itoa(pid))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	err := cmd.Run()
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == taskkillNotFound {
		return nil
	}
	return err
}
