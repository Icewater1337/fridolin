package platform

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockedByte is the one byte of bridge.lock that LockFileEx locks, at
// offset 2^62, far past the PID at the start: a lock on Windows is
// mandatory, so one over the PID would keep a second bridge from reading
// who holds it. A lock may lie past the end of the file.
func lockedByte() *windows.Overlapped { return &windows.Overlapped{OffsetHigh: 1 << 30} }

// lockFile takes the bridge's lock on f without waiting. Windows drops it
// when the handle closes or the process dies.
func lockFile(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, lockedByte())
}

func unlockFile(f *os.File) { _ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, lockedByte()) }

// lockHeld reports whether lockFile failed because another holds the lock.
func lockHeld(err error) bool { return errors.Is(err, windows.ERROR_LOCK_VIOLATION) }
