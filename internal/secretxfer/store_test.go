package secretxfer

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var origCreateFile = createFile

const crashName = "google_calendar_token_personal.json"

// A crash after the temp file is written but before it is linked into place
// (the seam panics there) leaves nothing at the final path, so the next
// store writes the file instead of keeping a truncated one (#612).
func TestStoreFilesCrashLeavesNothingAtPath(t *testing.T) {
	dir := t.TempDir()
	files := map[string][]byte{crashName: []byte(`{"t":"x"}`)}
	t.Cleanup(func() { createFile = origCreateFile })
	createFile = func(p string, data []byte, _ os.FileMode) error {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("before the file was finished, %s exists: %v", p, err)
		}
		_ = os.WriteFile(filepath.Join(filepath.Dir(p), "."+filepath.Base(p)+".1.tmp"), data[:3], 0o600)
		panic("crash")
	}
	func() {
		defer func() { _ = recover() }()
		_, _, _ = StoreFiles(dir, files)
	}()
	createFile = origCreateFile
	if _, err := os.Lstat(filepath.Join(dir, crashName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a crashed store left the file in place: %v", err)
	}
	stored, kept, err := StoreFiles(dir, files)
	if err != nil || len(stored) != 1 || len(kept) != 0 {
		t.Fatalf("after the crash: stored %v kept %v err %v", stored, kept, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, crashName)); string(got) != `{"t":"x"}` {
		t.Fatalf("stored %q", got)
	}
}

func TestStoreFilesKeepsFileAndSymlink(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	const old = `{"old":true}`
	if err := os.WriteFile(filepath.Join(dir, "dashboard-users.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "target")
	if err := os.Symlink(target, filepath.Join(dir, "google_oauth_web_credentials.json")); err != nil {
		t.Fatal(err)
	}
	stored, kept, err := StoreFiles(dir, map[string][]byte{
		"dashboard-users.json":              []byte(`[]`),
		"google_oauth_web_credentials.json": []byte(`{"web":{}}`),
	})
	if err != nil || len(stored) != 0 || strings.Join(kept, ",") != "dashboard-users.json,google_oauth_web_credentials.json" {
		t.Fatalf("stored %v kept %v err %v", stored, kept, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "dashboard-users.json")); string(got) != old {
		t.Fatalf("a kept file changed: %q", got)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the symlink was followed: %v", err)
	}
	if fi, err := os.Lstat(filepath.Join(dir, "google_oauth_web_credentials.json")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink was replaced: %v", err)
	}
}

func TestStoreFilesModeAndNoTemp(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := StoreFiles(dir, map[string][]byte{crashName: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, crashName)); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("stored file: %v %v", fi.Mode(), err)
	}
	noTemp(t, dir)

	// A failed store (the folder is read-only) leaves no temp file either.
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return
	}
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	if _, _, err := StoreFiles(ro, map[string][]byte{crashName: []byte(`{}`)}); err == nil {
		t.Fatal("storing into a read-only folder succeeded")
	}
	noTemp(t, ro)
}

func noTemp(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file %s left in %s", e.Name(), dir)
		}
	}
}
