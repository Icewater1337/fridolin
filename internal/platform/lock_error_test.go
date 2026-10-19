//go:build unix

package platform

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestLockContentionPreservesCauseAndNamesOwner(t *testing.T) {
	dir := t.TempDir()
	owner, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	_, err = AcquireLock(dir)
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("lost contention cause: %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("PID %d", os.Getpid())) {
		t.Fatalf("missing owner: %v", err)
	}
}
