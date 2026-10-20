package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// LockFileEx refuses a second handle in the same process, as flock does,
// and the lock covers no byte of the PID, which a second bridge reads.
func TestLockContentionNamesOwnerOnWindows(t *testing.T) {
	dir := t.TempDir()
	owner, err := AcquireLock(filepath.Join(dir, "bridge.lock"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = AcquireLock(filepath.Join(dir, "bridge.lock"))
	if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		t.Fatalf("lost contention cause: %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("PID %d", os.Getpid())) {
		t.Fatalf("missing owner: %v", err)
	}
	owner.Release()
	owner.Release()
	again, err := AcquireLock(filepath.Join(dir, "bridge.lock"))
	if err != nil {
		t.Fatalf("after Release: %v", err)
	}
	again.Release()
}

func TestExecOfAMissingProgramOnWindows(t *testing.T) {
	if err := Exec(`C:\no\such\program.exe`, []string{"program"}, nil); err == nil {
		t.Fatal("Exec of a missing program returned no error")
	}
}

func TestKillProcessGroupOfNoProcess(t *testing.T) {
	if err := KillProcessGroup(0); err != nil {
		t.Fatal(err)
	}
}
