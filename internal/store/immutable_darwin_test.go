//go:build darwin

package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewStoreStartsWhenChmodFails(t *testing.T) {
	// A leftover file the bridge cannot chmod must not keep it from
	// starting: an upgrade would take a working bridge offline.
	logs := captureLog(t)
	dbPath := filepath.Join(t.TempDir(), "bridge.db")
	wal := dbPath + "-wal"
	if err := os.WriteFile(wal, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(wal, 0o644); err != nil {
		t.Fatal(err)
	}
	makeImmutable(t, wal)

	openTestStore(t, dbPath)

	if got := permOf(t, wal); got != 0o644 {
		t.Errorf("immutable bridge.db-wal has mode %04o, want 0644 unchanged", got)
	}
	if !logContains(logs, "warning: could not make "+wal+" private") {
		t.Error("the failed chmod was not logged")
	}
}
