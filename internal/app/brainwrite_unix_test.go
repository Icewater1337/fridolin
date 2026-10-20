//go:build unix

package app

import (
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

// A short write must not publish part of a new manifest: a transaction
// cannot roll back a file whose creation never reported success.
func TestNewManifestWriteFailureLeavesNoFile(t *testing.T) {
	const child = "FRIDOLIN_TEST_PARTIAL_MANIFEST"
	if os.Getenv(child) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNewManifestWriteFailureLeavesNoFile$")
		cmd.Env = append(os.Environ(), child+"=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("limited writer: %v\n%s", err, out)
		}
		return
	}
	dir := t.TempDir()
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatal(err)
	}
	original := limit
	t.Cleanup(func() {
		// The instrumented child writes its coverage after the test returns.
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
			t.Errorf("restoring file-size limit: %v", err)
		}
	})
	limit.Cur = 1024
	signal.Ignore(syscall.SIGXFSZ)
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatal(err)
	}
	err := manifest.Write(dir, &manifest.Manifest{ID: "new", Name: strings.Repeat("n", 4096)}, false)
	if err == nil {
		t.Fatal("manifest write exceeded the file-size limit without failing")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed creation left files: %v, %v", entries, err)
	}
}
