//go:build unix

package platform

import (
	"errors"
	"os"
	"syscall"
)

// lockFile takes the bridge's flock on f without waiting.
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockFile(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

// lockHeld reports whether lockFile failed because another holds the lock.
func lockHeld(err error) bool { return errors.Is(err, syscall.EWOULDBLOCK) }
