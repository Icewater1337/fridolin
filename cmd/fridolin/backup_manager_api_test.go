package main

import (
	"errors"
	"testing"
)

// The service rejects path components before consulting storage or the caller's
// destination checker, even when used without an HTTP adapter.
func TestBackupRestoreRejectsInvalidDirectRequests(t *testing.T) {
	for _, req := range []BackupRestoreRequest{
		{Brain: "../other", Version: "current"},
		{Brain: "notes", Version: "../../other"},
		{Brain: "", Version: ""},
	} {
		m := &BackupManager{}
		target, err := m.Restore(req, func(string) (string, error) { t.Fatal("invalid request reached destination check"); return "", nil })
		if target != "" || err == nil || err.Error() != "invalid brain or version" || !errors.Is(err, ErrBackupRestoreRequest) {
			t.Errorf("Restore(%+v) = %q, %v", req, target, err)
		}
	}
}
