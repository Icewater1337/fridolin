package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// setsidChildren has the fake CLI start a grandchild that leaves the run's
// process group holding holds ("stdout" or "stdout,stderr"), and kills
// those grandchildren when the test ends: nothing the bridge does reaches
// them.
func setsidChildren(t *testing.T, home, holds string) {
	t.Helper()
	pidLog := filepath.Join(home, "pids")
	t.Setenv("FRIDOLIN_FAKE_PID_LOG", pidLog)
	t.Setenv("FRIDOLIN_FAKE_SETSID_CHILD", holds)
	t.Cleanup(func() {
		data, _ := os.ReadFile(pidLog)
		// Each run logs the CLI's PID, then its grandchild's.
		for i, line := range strings.Fields(string(data)) {
			if pid, err := strconv.Atoi(line); err == nil && i%2 == 1 {
				testutil.KillPID(pid)
			}
		}
	})
}

// TestDetachedChildDoesNotHoldTheBrain: a process the CLI started that left
// the run's process group, which no kill of the group reaches, and that
// holds the CLI's output open does not keep the brain or its run slot
// busy: the answer goes out within postResultGrace and runWaitDelay, and
// the next message runs. The bridge read the output until that process
// let go of it, for as long as it ran, past run_timeout and !stop. Holding
// stderr too, it turned the answer that had come into a "took too long"
// error once run_timeout came.
func TestDetachedChildDoesNotHoldTheBrain(t *testing.T) {
	cases := []struct {
		name         string
		holds        string
		grace, delay time.Duration // postResultGrace, runWaitDelay
		minutes      string        // run_timeout_minutes, in runTimeoutUnit
		within       time.Duration // when the answer is in, at the latest
	}{
		{"holding stdout", "stdout", 300 * time.Millisecond, 300 * time.Millisecond, "30", 600 * time.Millisecond},
		// The deadline comes while Wait still waits for the output: the
		// CLI exited after its answer, so runWaitDelay is what ends it.
		{"holding stdout and stderr past run_timeout", "stdout,stderr", 10 * time.Second, 3 * time.Second, "20", 3 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prevGrace, prevDelay, prevUnit := postResultGrace, runWaitDelay, runTimeoutUnit
			postResultGrace, runWaitDelay, runTimeoutUnit = tc.grace, tc.delay, 100*time.Millisecond
			t.Cleanup(func() { postResultGrace, runWaitDelay, runTimeoutUnit = prevGrace, prevDelay, prevUnit })
			mgr, d, home := queueTestManager(t, 0, `, "max_concurrent_runs": 1, "run_timeout_minutes": `+tc.minutes)
			setsidChildren(t, home, tc.holds)

			start := time.Now()
			if _, _, _, err := mgr.Submit(dm("answer and leave")); err != nil {
				t.Fatal(err)
			}
			testutil.WaitFor(t, 15*time.Second, "the answer", func() bool { return d.count() == 1 })
			if res := d.all()[0].res; res.Err != nil || !strings.HasSuffix(res.Text, "says: answer and leave") {
				t.Errorf("result = %+v, want the answer", res)
			}
			// With time for the fake CLI to start.
			if el := time.Since(start); el > tc.within+2*time.Second {
				t.Errorf("the answer took %v, want it within %v", el, tc.within)
			}
			if last := mgr.Get("a").Snapshot().Last; last == nil || last.Outcome != "ok" {
				t.Errorf("last run = %+v, want outcome ok", last)
			}

			// The one run slot is free for the next message, in another
			// brain.
			if err := os.Unsetenv("FRIDOLIN_FAKE_SETSID_CHILD"); err != nil {
				t.Fatal(err)
			}
			next := brain.Inbound{Inbound: chat.Inbound{JID: "120363000000000000@g.us", Sender: "15551234567", Text: "next"}}
			if _, _, _, err := mgr.Submit(next); err != nil {
				t.Fatal(err)
			}
			testutil.WaitFor(t, 5*time.Second, "the next answer", func() bool { return d.count() == 2 })
			if res := d.all()[1].res; res.Err != nil || !strings.HasSuffix(res.Text, "says: next") {
				t.Errorf("next result = %+v, want the answer", res)
			}
		})
	}
}

// TestSiteSearchStartsClaudeAsBrainRunsDo: a site module's search runs
// claude through the same helper as a brain's runs: the same output and
// isolation arguments, its own read-only tools, and the prompt on stdin,
// not in argv, where every process listing showed it and ARG_MAX bounded
// it.
func TestSiteSearchStartsClaudeAsBrainRunsDo(t *testing.T) {
	tb := newTestBridge(t, "")
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A", Routing: manifest.Routing{HandlesDMs: true}})
	argsLog := filepath.Join(tb.home, "args.log")
	t.Setenv("FRIDOLIN_FAKE_ARGS_LOG", argsLog)
	const prompt = "SEARCH-PROMPT where is the letter?"
	var out bytes.Buffer
	if err := tb.runBrainSearch(context.Background(), "a", prompt, &out); err != nil {
		t.Fatalf("search: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), prompt) {
		t.Errorf("search output %q, want the prompt echoed from stdin", out.String())
	}
	data, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.TrimSpace(string(data))
	if strings.Contains(args, "SEARCH-PROMPT") {
		t.Errorf("args %q hold the prompt", args)
	}
	if want := "--output-format stream-json --verbose --setting-sources project,local --strict-mcp-config --settings "; !strings.HasPrefix(args, want) {
		t.Errorf("args %q, want them to start with %q", args, want)
	}
	if want := " --tools Read,Grep,Glob -p"; !strings.HasSuffix(args, want) {
		t.Errorf("args %q, want them to end in %q, a bare -p last", args, want)
	}
}

// TestSiteSearchKeepsOnlyTheEndOfStderr: what a search's claude writes to
// stderr is kept up to its end only, however much it is, and that end
// names the error. It was all kept in memory.
func TestSiteSearchKeepsOnlyTheEndOfStderr(t *testing.T) {
	tb := newTestBridge(t, "")
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A", Routing: manifest.Routing{HandlesDMs: true}})
	const flood = 16 << 20
	t.Setenv("FRIDOLIN_FAKE_STDERR_BYTES", strconv.Itoa(flood))
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	err := tb.runBrainSearch(context.Background(), "a", "where is the letter?", io.Discard)
	runtime.ReadMemStats(&after)
	if err == nil || !strings.Contains(err.Error(), "fatal: the last thing the CLI said") || len(err.Error()) > 500 {
		t.Errorf("err = %.600v, want the end of stderr", err)
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > flood/4 {
		t.Errorf("a search that wrote %d MiB to stderr allocated %d MiB", flood>>20, n>>20)
	}
}

func TestKillRunsRejectsReloadAlreadyInProgress(t *testing.T) {
	mgr, _, home := queueTestManager(t, 0, "")
	cfg, err := LoadConfig(writeConfig(t, `{"data_dir": "`+home+`/data", "claude_path": "`+os.Args[0]+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	// Block command reload while it computes sandbox policy. Diagnostics and
	// force-stop must not wait for that work or let the reload publish later.
	mgr.profiler.mu.Lock()
	release := sync.OnceFunc(mgr.profiler.mu.Unlock)
	defer release()
	reloaded := make(chan error, 1)
	go func() { reloaded <- mgr.reload(nil, nil, cfg, nil) }()
	testutil.WaitFor(t, 5*time.Second, "reload to hold its serialization lock", func() bool {
		if mgr.applyMu.TryLock() {
			mgr.applyMu.Unlock()
			return false
		}
		return true
	})
	readIssues := make(chan struct{})
	go func() { mgr.Issues(); close(readIssues) }()
	select {
	case <-readIssues:
	case <-time.After(5 * time.Second):
		t.Fatal("reading diagnostics waited for a blocked reload")
	}
	stopped := make(chan struct{})
	go func() { mgr.KillRuns(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("force-stop waited for a blocked reload")
	}
	release()
	if err := <-reloaded; !errors.Is(err, brain.ErrStopped) {
		t.Fatalf("in-flight reload after force-stop = %v", err)
	}
	if mgr.Get("a") == nil {
		t.Fatal("reload published its replacement set after force-stop")
	}
}
