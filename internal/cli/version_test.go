package cli

import (
	"log"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/testutil"
	"github.com/Icewater1337/fridolin/internal/version"
)

func TestRunLogsItsVersionFirst(t *testing.T) {
	// A log excerpt in a bug report must say which build wrote it, even
	// when the bridge then refuses to start: the CONFIG_FILE warning that
	// configPath logs comes after it, in the console and on the dashboard.
	t.Setenv("HOME", t.TempDir())
	testutil.ClearEnv(t, "FRIDOLIN_CONFIG")
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "old.json"))
	prev := log.Writer()
	t.Cleanup(func() { log.SetOutput(prev) })
	var console strings.Builder
	ring, _ := runLog(&console, "")

	want := "Starting fridolin " + version.String() + " (" + runtime.Version() + ", " + runtime.GOOS + "/" + runtime.GOARCH + ")"
	lines := strings.Split(strings.TrimSpace(console.String()), "\n")
	if len(lines) < 2 || !strings.Contains(lines[0], want) || !strings.Contains(lines[1], "CONFIG_FILE") {
		t.Errorf("the console log does not start with %q, then the CONFIG_FILE warning:\n%s", want, console.String())
	}
	if got, _ := ring.Since(0, 50); len(got) == 0 || !strings.Contains(got[0].Text, want) {
		t.Errorf("the dashboard's log does not start with %q: %+v", want, got)
	}
}
