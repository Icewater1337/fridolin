//go:build unix

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// instanceLock is held by the bridge that uses a data directory.
type instanceLock struct {
	mu sync.Mutex
	f  *os.File
}

// acquireInstanceLock makes sure only one bridge uses a data directory. Two
// bridges on one WhatsApp session knock each other offline and can get the
// device logged out. The flock belongs to the open file, so a second bridge
// in the same process is refused too, and the kernel drops it when the
// process dies.
func acquireInstanceLock(dataDir string) (*instanceLock, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dataDir, "bridge.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another bridge is already running with data dir %s", dataDir)
	}
	_ = f.Truncate(0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	return &instanceLock{f: f}, nil
}

// Release lets a restarted bridge take the lock again. Calling it twice is
// harmless.
func (l *instanceLock) Release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
		l.f.Close()
		l.f = nil
	}
}
