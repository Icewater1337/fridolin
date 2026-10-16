//go:build unix

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

var (
	lockMu   sync.Mutex
	lockFile *os.File
)

// acquireInstanceLock makes sure only one bridge uses a data directory. Two
// bridges on one WhatsApp session knock each other offline and can get the
// device logged out.
func acquireInstanceLock(dataDir string) error {
	lockMu.Lock()
	defer lockMu.Unlock()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dataDir, "bridge.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return fmt.Errorf("another bridge is already running with data dir %s", dataDir)
	}
	_ = f.Truncate(0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	lockFile = f
	return nil
}

// releaseInstanceLock lets a restarted bridge take the lock again.
func releaseInstanceLock() {
	lockMu.Lock()
	defer lockMu.Unlock()
	if lockFile != nil {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		lockFile.Close()
		lockFile = nil
	}
}
