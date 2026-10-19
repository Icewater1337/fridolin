package store

import (
	"os/exec"
	"testing"
)

// makeImmutable sets and later clears the user immutable file flag.
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
