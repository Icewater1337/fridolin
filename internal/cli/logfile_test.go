//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingLogKeepsTheFileSmall(t *testing.T) {
	// launchd's StandardErrorPath grew for as long as the service ran.
	path := filepath.Join(t.TempDir(), "stderr.log")
	// Stands in for the stderr launchd opened for the bridge.
	stderr, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	r, err := openRotatingLog(path, int(stderr.Fd()), 100)
	if err != nil {
		t.Fatal(err)
	}
	line := func(c string) string { return strings.Repeat(c, 59) + "\n" }
	for _, c := range []string{"a", "b"} {
		if _, err := r.Write([]byte(line(c))); err != nil {
			t.Fatal(err)
		}
	}
	// What the runtime writes to stderr itself follows the new file.
	if _, err := stderr.WriteString("panic: c\n"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path+".1"); got != line("a") {
		t.Errorf("stderr.log.1 = %q, want the first line", got)
	}
	if got := readFile(t, path); got != line("b")+"panic: c\n" {
		t.Errorf("stderr.log = %q, want the second line and the panic", got)
	}
	if _, err := r.Write([]byte(line("d"))); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path+".1"); got != line("b")+"panic: c\n" {
		t.Errorf("after the second rotation stderr.log.1 = %q", got)
	}
	if got := readFile(t, path); got != line("d") {
		t.Errorf("after the second rotation stderr.log = %q", got)
	}
	// The logs name chats and senders, whatever mode launchd gave the file.
	for _, p := range []string{path, path + ".1"} {
		if m := mode(t, p); m != 0o600 {
			t.Errorf("%s mode %v, want 0600", filepath.Base(p), m)
		}
	}
}

func TestRotatingLogRotatesAFileThatIsAlreadyTooLarge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stderr.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 500)), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	r, err := openRotatingLog(path, int(stderr.Fd()), 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Write([]byte("Starting fridolin\n")); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != "Starting fridolin\n" {
		t.Errorf("stderr.log = %q", got)
	}
	if got := len(readFile(t, path+".1")); got != 500 {
		t.Errorf("stderr.log.1 holds %d bytes, want the old 500", got)
	}
}
