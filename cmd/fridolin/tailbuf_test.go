package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/textx"
)

// A CLI that floods stderr cannot make the bridge hold all of it: the
// run's error keeps a short tail, and the activity log gets the end, with
// the line that says what went wrong.
func TestRunStderrIsBounded(t *testing.T) {
	mgr, d, _ := queueTestManager(t, 0, "")
	t.Setenv("FRIDOLIN_FAKE_STDERR_BYTES", "1000000")
	if _, _, _, err := mgr.Submit(dm("fail loudly")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the failed run", func() bool { return d.count() == 1 })
	res := d.all()[0].res
	if res.Err == nil || !strings.Contains(res.Err.Error(), "fatal: the last thing the CLI said") {
		t.Fatalf("err = %v, want the end of stderr", res.Err)
	}
	if len(res.Err.Error()) > 1000 {
		t.Errorf("the error holds %d bytes of stderr, want a short tail", len(res.Err.Error()))
	}
	data, err := os.ReadFile(filepath.Join(mgr.Get("a").Dir(), streamLogName))
	if err != nil {
		t.Fatal(err)
	}
	// The block names its run, as the run's heading does.
	if !strings.Contains(string(data), "stderr of the run for "+testDMChat+" from 15551234567, pid ") {
		t.Errorf("activity log = %q, want the stderr block to name its run", textx.Tail(string(data), 300))
	}
	if !strings.Contains(string(data), ":\n…") || !strings.Contains(string(data), "fatal: the last thing the CLI said") {
		t.Errorf("activity log = %q, want the end of stderr", textx.Tail(string(data), 300))
	}
	if len(data) > 2*stderrLogBytes {
		t.Errorf("activity log holds %d bytes, want at most the last %d of stderr", len(data), stderrLogBytes)
	}
}
