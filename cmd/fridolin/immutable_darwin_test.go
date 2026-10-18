//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// makeImmutable sets the user immutable flag on path, so that not even its
// owner can chmod it, and clears it when the test ends so the temp dir can
// be removed.
func makeImmutable(t *testing.T, path string) {
	t.Helper()
	if out, err := exec.Command("chflags", "uchg", path).CombinedOutput(); err != nil {
		t.Skipf("chflags uchg: %v: %s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("chflags", "nouchg", path).CombinedOutput(); err != nil {
			t.Errorf("chflags nouchg %s: %v: %s", path, err, out)
		}
	})
}

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

func TestProtectDataDirWarnsWhenChmodFails(t *testing.T) {
	// The same goes for a data dir the bridge cannot chmod: it is logged,
	// and protectDataDir has no error for the caller to stop on.
	home := t.TempDir()
	t.Setenv("HOME", home)
	data := filepath.Join(home, "data")
	mkdirMode(t, data, 0o755)
	makeImmutable(t, data)
	logs := captureLog(t)

	protectDataDir(&Config{DataDir: data, DBPath: filepath.Join(data, "bridge.db")})

	if got := permOf(t, data); got != 0o755 {
		t.Errorf("immutable data dir has mode %04o, want 0755 unchanged", got)
	}
	if !logContains(logs, "warning: could not make "+data+" private") {
		t.Error("the failed chmod was not logged")
	}
}
