package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Lock is held by the bridge that uses a data directory.
type Lock struct {
	mu sync.Mutex
	f  *os.File
}

// AcquireLock makes sure only one bridge uses a data directory, by locking
// path, its bridge.lock (config.Layout.Lock). Two bridges on one WhatsApp
// session knock each other offline and can get the device logged out. The
// flock belongs to the open file, so a second bridge in the same process
// is refused too, and the kernel drops it when the process dies.
// lock_unix.go and lock_windows.go take it: flock, and LockFileEx on
// Windows.
func AcquireLock(path string) (*Lock, error) {
	dataDir := filepath.Dir(path)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		var owner string
		if lockHeld(err) {
			var buf [32]byte
			n, _ := f.ReadAt(buf[:], 0)
			if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(buf[:n]))); parseErr == nil && pid > 0 {
				owner = fmt.Sprintf(" (PID %d)", pid)
			}
			f.Close()
			return nil, fmt.Errorf("another bridge is already running with data dir %s%s: %w", dataDir, owner, err)
		}
		f.Close()
		return nil, fmt.Errorf("locking data dir %s: %w", dataDir, err)
	}
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, fmt.Errorf("recording lock owner: %w", err)
	}
	if _, err := fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
		f.Close()
		return nil, fmt.Errorf("recording lock owner: %w", err)
	}
	return &Lock{f: f}, nil
}

// Release lets a restarted bridge take the lock again. Calling it twice is
// harmless.
func (l *Lock) Release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		unlockFile(l.f)
		l.f.Close()
		l.f = nil
	}
}
