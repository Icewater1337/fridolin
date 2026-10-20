package cli

import "golang.org/x/sys/windows"

// dup2 makes oldfd's file the process's standard error, which is the only
// newfd the rotating log passes: Windows has no dup2, but the Go runtime
// asks for the standard error handle each time it writes there (a panic,
// say), so it follows the new file too. os.Stderr keeps the handle it had.
func dup2(oldfd, _ int) error {
	return windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(oldfd))
}

// logRotates is false on Windows: Go opens files there without
// FILE_SHARE_DELETE, so the open log cannot be renamed to <path>.1, and
// the file grows. Nothing on Windows passes --log-file yet; the service
// that will comes with its own log.
const logRotates = false
