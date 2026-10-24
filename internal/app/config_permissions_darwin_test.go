//go:build darwin

package app

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
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

func TestProtectDataDirWarnsWhenChmodFails(t *testing.T) {
	// Serial: protectDataDir's warnings go to the process's standard logger
	// (captureLog).
	// The same goes for a data dir the bridge cannot chmod: it is logged,
	// and protectDataDir has no error for the caller to stop on.
	home := t.TempDir()
	setenv(t, "HOME", home)
	data := filepath.Join(home, "data")
	mkdirMode(t, data, 0o755)
	makeImmutable(t, data)
	logs := captureLog(t)

	protectDataDir(resolvedSettings(t, &config.File{DataDir: data, DBPath: filepath.Join(data, "bridge.db")}))

	if got := permOf(t, data); got != 0o755 {
		t.Errorf("immutable data dir has mode %04o, want 0755 unchanged", got)
	}
	if !logContains(logs, "warning: could not make "+data+" private") {
		t.Error("the failed chmod was not logged")
	}
}
