package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
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
				_ = syscall.Kill(pid, syscall.SIGKILL)
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
			waitFor(t, 15*time.Second, "the answer", func() bool { return d.count() == 1 })
			if res := d.all()[0].res; res.Err != nil || !strings.HasSuffix(res.Text, "says: answer and leave") {
				t.Errorf("result = %+v, want the answer", res)
			}
			// With time for the fake CLI to start.
			if el := time.Since(start); el > tc.within+2*time.Second {
				t.Errorf("the answer took %v, want it within %v", el, tc.within)
			}
			if last := mgr.Get("a").Snapshot().Last; last == nil || last.Outcome != outcomeOK {
				t.Errorf("last run = %+v, want outcome ok", last)
			}

			// The one run slot is free for the next message, in another
			// brain.
			if err := os.Unsetenv("FRIDOLIN_FAKE_SETSID_CHILD"); err != nil {
				t.Fatal(err)
			}
			next := Inbound{JID: "120363000000000000@g.us", Sender: "15551234567", Text: "next"}
			if _, _, _, err := mgr.Submit(next); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 5*time.Second, "the next answer", func() bool { return d.count() == 2 })
			if res := d.all()[1].res; res.Err != nil || !strings.HasSuffix(res.Text, "says: next") {
				t.Errorf("next result = %+v, want the answer", res)
			}
		})
	}
}

// TestStopWhileOutputIsHeld: a stop ends a run whose output a process
// outside its group holds, within runWaitDelay, as an interrupted run.
// The bridge kept reading that output once the CLI was killed, and the
// stop gave up after 10 seconds with the brain still busy.
func TestStopWhileOutputIsHeld(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			prevDelay := runWaitDelay
			runWaitDelay = 300 * time.Millisecond
			t.Cleanup(func() { runWaitDelay = prevDelay })
			mgr, d, home := queueTestManager(t, 30000, "")
			br := mgr.Get("a")
			settings := *br.settings.Load()
			settings.provider, settings.codexPath = provider, settings.claudePath
			br.settings.Store(&settings)
			setsidChildren(t, home, "stdout")
			if _, _, _, err := mgr.Submit(dm("long task")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 5*time.Second, "the run's grandchild", func() bool {
				data, _ := os.ReadFile(filepath.Join(home, "pids"))
				return len(strings.Fields(string(data))) == 2
			})
			start := time.Now()
			if !br.StopCurrent(false) {
				t.Fatal("StopCurrent found no run")
			}
			if el := time.Since(start); el > 3*time.Second {
				t.Errorf("the stop took %v", el)
			}
			waitFor(t, 5*time.Second, "the stopped run", func() bool { return d.count() == 1 })
			if res := d.all()[0].res; !errors.Is(res.Err, errBrainInterrupted) {
				t.Errorf("result = %+v, want interrupted", res)
			}
		})
	}
}

// TestStopAfterTheRunWasReapedSendsNoSignal: once Wait has reaped the CLI,
// its PID is no longer the run's, and a stop that comes before the run is
// finished, StopCurrent racing its end, signals nothing. The run kept the
// PID until it was finished, and a stop killed that process group: by
// then, with the PID taken by another process, someone else's.
func TestStopAfterTheRunWasReapedSendsNoSignal(t *testing.T) {
	mgr, _, _ := queueTestManager(t, 0, "")
	br := mgr.Get("a")
	m := dm("quick")
	ctx, cancel := runContext(0)
	run := &brainRun{cancel: cancel, done: make(chan struct{}), msg: m, started: time.Now()}
	br.mu.Lock()
	br.current = run
	br.mu.Unlock()
	if res := br.invoke(run, ctx, br.settings.Load(), m); res.Err != nil {
		t.Fatal(res.Err)
	}
	// The run is over, but not yet finished: what the worker does next.
	if rt := br.Snapshot(); rt.Running == nil || rt.Running.PID != 0 {
		t.Errorf("running %+v once the CLI was reaped, want the run without a PID", rt.Running)
	}

	// As if the CLI's PID had gone to another process group's leader, and
	// the run still named it.
	other := exec.Command("sleep", "30")
	other.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_ = other.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = other.Process.Kill()
		<-exited
	})
	br.mu.Lock()
	run.pid = other.Process.Pid
	br.mu.Unlock()

	stopped := make(chan bool, 1)
	go func() { stopped <- br.StopCurrent(false) }()
	<-ctx.Done() // the stop cancelled the run, and waits for it to finish
	br.finishRun(run)
	if !<-stopped {
		t.Error("StopCurrent found no run")
	}
	select {
	case <-exited:
		t.Errorf("the stop killed process %d, which had the run's PID", other.Process.Pid)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestStopBeforeTheCLIStartsIsAnInterruption: a run stopped while it is
// prepared, before the CLI starts, is recorded as interrupted, as one
// stopped a moment later is. Start's "context canceled" made it a failed
// run, shown as an error on the dashboard.
func TestStopBeforeTheCLIStartsIsAnInterruption(t *testing.T) {
	mgr, d, _ := queueTestManager(t, 0, "")
	br := mgr.Get("a")
	mgr.SetHooks(BrainHooks{Deliver: d.add, ExtraArgs: func(string) []string {
		// The last thing before the CLI starts that a stop can come in.
		br.mu.Lock()
		cur := br.current
		br.mu.Unlock()
		br.interruptRun(cur)
		return nil
	}})
	if _, _, _, err := mgr.Submit(dm("stopped at once")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "the run", func() bool { return d.count() == 1 })
	if res := d.all()[0].res; !errors.Is(res.Err, errBrainInterrupted) {
		t.Errorf("result = %+v, want interrupted", res)
	}
	if last := br.Snapshot().Last; last == nil || last.Outcome != outcomeInterrupted || last.Error != "" {
		t.Errorf("last run = %+v, want outcome interrupted", last)
	}
}

// TestSiteSearchStartsClaudeAsBrainRunsDo: a site module's search runs
// claude through the same helper as a brain's runs: the same output and
// isolation arguments, its own read-only tools, and the prompt on stdin,
// not in argv, where every process listing showed it and ARG_MAX bounded
// it.
func TestSiteSearchStartsClaudeAsBrainRunsDo(t *testing.T) {
	tb := newTestBridge(t, "")
	tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A", Routing: BrainRouting{HandlesDMs: true}})
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
	tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A", Routing: BrainRouting{HandlesDMs: true}})
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

// A result is final even if a stop or deadline arrives while inherited
// output is still open. Keep both answers and the CLI's own error then.
func TestCancellationKeepsTheRecordedResult(t *testing.T) {
	cases := []struct {
		name     string
		cliError string
		timeout  bool
		empty    bool
	}{
		{"stop after answer", "", false, false},
		{"stop after empty result", "", false, true},
		{"stop after error", "API quota exhausted", false, false},
		{"timeout after error", "API quota exhausted", true, false},
	}
	for _, provider := range []string{"claude", "codex"} {
		for _, tc := range cases {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				prevGrace, prevDelay := postResultGrace, runWaitDelay
				postResultGrace, runWaitDelay = 30*time.Second, 3*time.Second
				t.Cleanup(func() { postResultGrace, runWaitDelay = prevGrace, prevDelay })
				mgr, _, home := queueTestManager(t, 0, "")
				setsidChildren(t, home, "stdout,stderr")
				const answer = "The recorded answer"
				br := mgr.Get("a")
				settings := *br.settings.Load()
				settings.provider = provider
				settings.codexPath = settings.claudePath // the same fake prints either stream below
				var output string
				if provider == "codex" {
					switch {
					case tc.empty:
						output = `{"type":"turn.completed"}`
					case tc.cliError != "":
						output = `{"type":"turn.failed","error":{"message":` + strconv.Quote(tc.cliError) + `}}`
					default:
						output = `{"type":"item.completed","item":{"type":"agent_message","text":` + strconv.Quote(answer) + `}}` + "\n" + `{"type":"turn.completed"}`
					}
				} else {
					switch {
					case tc.empty:
						output = `{"type":"result","result":""}`
					case tc.cliError != "":
						output = `{"type":"result","is_error":true,"result":` + strconv.Quote(tc.cliError) + `}`
					default:
						output = `{"type":"result","result":` + strconv.Quote(answer) + `}`
					}
				}
				t.Setenv("FRIDOLIN_FAKE_RESULT", output)
				ctx, cancel := context.WithCancel(context.Background())
				if tc.timeout {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
				}
				defer cancel()
				m := dm("answer before stopping")
				run := &brainRun{cancel: cancel, done: make(chan struct{}), msg: m}
				result := make(chan RunResult, 1)
				go func() { result <- br.invokeGuarded(run, ctx, &settings, m) }()
				waitFor(t, 5*time.Second, "the recorded result", func() bool {
					data, _ := os.ReadFile(filepath.Join(br.workDir, streamLogName))
					return strings.Contains(string(data), "✅ Result:") || strings.Contains(string(data), "❌ Error:")
				})
				if !tc.timeout {
					cancel()
				}
				select {
				case res := <-result:
					if tc.empty {
						if !errors.Is(res.Err, errNoResult) {
							t.Errorf("result = %+v, want errNoResult", res)
						}
					} else if tc.cliError == "" {
						if res.Err != nil || res.Text != answer {
							t.Errorf("result = %+v, want the recorded answer", res)
						}
					} else {
						var cliErr *cliError
						if !errors.As(res.Err, &cliErr) || cliErr.msg != tc.cliError || cliErr.provider != provider {
							t.Errorf("result = %+v, want CLI error %q", res, tc.cliError)
						}
					}
				case <-time.After(10 * time.Second):
					t.Fatal("the cancelled run kept waiting for inherited output")
				}
			})
		}
	}
}

// A second signal may exit the bridge as soon as KillRuns returns. Even
// with a delayed supervisor it must first signal every run, then wait for
// the acknowledgement, without waiting for blocked reply hooks/run.done.
func TestKillRunsWaitsForProcessCancellation(t *testing.T) {
	var runs []*brainRun
	var contexts []context.Context
	mgr := &BrainManager{}
	for range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		run := &brainRun{cancel: cancel, done: make(chan struct{}), process: &claudeProcess{stopped: make(chan struct{})}}
		runs = append(runs, run)
		contexts = append(contexts, ctx)
		mgr.order = append(mgr.order, &Brain{current: run})
		// Both cancellations must be requested before either supervisor
		// acknowledges stopping; otherwise one delayed run blocks another.
		t.Cleanup(func() {
			if ctx.Err() == nil {
				t.Error("run was not cancelled")
			}
		})
	}
	finished := make(chan struct{})
	go func() { mgr.KillRuns(); close(finished) }()
	defer func() {
		for _, run := range runs {
			run.process.acknowledgeStop()
		}
		<-finished
	}()
	for _, ctx := range contexts {
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("one delayed supervisor prevented cancelling the other run")
		}
	}
	select {
	case <-finished:
		t.Fatal("force-exit returned before either supervisor acknowledged the kill")
	case <-time.After(50 * time.Millisecond):
	}
	runs[0].process.acknowledgeStop()
	select {
	case <-finished:
		t.Fatal("force-exit returned before the second supervisor acknowledged the kill")
	case <-time.After(50 * time.Millisecond):
	}
	runs[1].process.acknowledgeStop()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("force-exit waited for output or delivery after both kills were acknowledged")
	}
}

// Force-exit also closes the gap before cmd.Start: once it returns,
// preparation that was still running must never create a new child.
func TestKillRunsPreventsALateProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := &brainRun{cancel: cancel, done: make(chan struct{})}
	mgr := &BrainManager{order: []*Brain{{current: run}}}
	mgr.KillRuns()
	// An independent context deliberately tests the startup barrier,
	// rather than relying on os/exec noticing the cancelled run context.
	marker := filepath.Join(t.TempDir(), "started")
	proc, err := run.startProcess(context.Background(), claudeRun{binary: "/bin/sh", args: []string{"-c", `echo started > "$1"`, "sh", marker}})
	if proc != nil {
		_ = proc.wait()
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("late start = %v, want cancellation", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("force-exit allowed a later process to start")
	}
	if ctx.Err() == nil {
		t.Error("run was not cancelled")
	}
}

// A run may still be delivering after its CLI exited. Force-exit must use
// the completed supervisor's acknowledgement, without another PID signal
// or waiting for that delivery to finish.
func TestKillRunsAfterCLIExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := &brainRun{cancel: cancel, done: make(chan struct{})}
	proc, err := run.startProcess(ctx, claudeRun{binary: "/bin/sh", args: []string{"-c", "exit 0"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.wait(); err != nil {
		t.Fatal(err)
	}
	mgr := &BrainManager{order: []*Brain{{current: run}}}
	stopped := make(chan struct{})
	go func() { mgr.KillRuns(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("force-exit waited for delivery after the CLI had exited")
	}
}

func TestKillRunsIncludesRetiringBrains(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proc := &claudeProcess{stopped: make(chan struct{})}
	proc.acknowledgeStop()
	old := &Brain{current: &brainRun{cancel: cancel, process: proc}}
	mgr := &BrainManager{retired: map[*Brain]struct{}{old: {}}}
	mgr.KillRuns()
	if ctx.Err() == nil {
		t.Fatal("force-exit missed the run removed from the current brain set")
	}
	if !old.retired {
		t.Fatal("force-exit left the retired brain's queue open")
	}
}

func TestKillRunsFreezesQueuedAndNewRuns(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			mgr, d, home := queueTestManager(t, 30000, "")
			br := mgr.Get("a")
			settings := *br.settings.Load()
			settings.provider, settings.codexPath = provider, settings.claudePath
			br.settings.Store(&settings)
			pidLog := filepath.Join(home, "run-pids")
			t.Setenv("FRIDOLIN_FAKE_PID_LOG", pidLog)
			delivering, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			releaseDelivery := sync.OnceFunc(func() { close(release) })
			defer releaseDelivery()
			mgr.SetHooks(BrainHooks{Deliver: func(br *Brain, m Inbound, res RunResult) {
				once.Do(func() { close(delivering) })
				<-release
				d.add(br, m, res)
			}})
			if _, _, _, err := mgr.Submit(dm("first")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 5*time.Second, "the CLI to start", func() bool {
				data, _ := os.ReadFile(pidLog)
				return len(strings.Fields(string(data))) == 1
			})
			if _, _, _, err := mgr.Submit(dm("queued")); err != nil {
				t.Fatal(err)
			}
			stopped := make(chan struct{})
			go func() { mgr.KillRuns(); close(stopped) }()
			select {
			case <-stopped:
			case <-time.After(5 * time.Second):
				t.Fatal("force-stop waited for delivery")
			}
			select {
			case <-delivering:
			case <-time.After(5 * time.Second):
				t.Fatal("cancelled run did not reach delivery")
			}
			if _, _, _, err := mgr.Submit(dm("new")); !errors.Is(err, errBrainsStopped) {
				t.Errorf("new message after force-stop: %v", err)
			}
			if _, _, err := br.Submit(dm("direct")); !errors.Is(err, errBrainRetired) {
				t.Errorf("direct message after force-stop: %v", err)
			}
			releaseDelivery()
			waitFor(t, 5*time.Second, "the worker to finish without another turn", func() bool {
				br.mu.Lock()
				defer br.mu.Unlock()
				return !br.working
			})
			data, _ := os.ReadFile(pidLog)
			if len(strings.Fields(string(data))) != 1 {
				t.Errorf("started more runs after force-stop: %s", data)
			}
			waiting := br.closeQueue()
			if len(waiting) != 1 || waiting[0].Text != "queued" {
				t.Errorf("pending message was lost: %+v", waiting)
			}
		})
	}
}

func TestKillRunsRejectsReloadAlreadyInProgress(t *testing.T) {
	mgr, _, home := queueTestManager(t, 0, "")
	cfg, err := LoadConfig(writeConfig(t, `{"data_dir": "`+home+`/data", "claude_path": "`+os.Args[0]+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	// Block reload after its initial stopped check, before publication.
	// Force-stop must not wait for reloadMu or allow that reload to publish.
	mgr.profiler.mu.Lock()
	release := sync.OnceFunc(mgr.profiler.mu.Unlock)
	defer release()
	reloaded := make(chan error, 1)
	go func() { reloaded <- mgr.reload(nil, nil, cfg, nil) }()
	waitFor(t, 5*time.Second, "reload to hold its serialization lock", func() bool {
		if mgr.reloadMu.TryLock() {
			mgr.reloadMu.Unlock()
			return false
		}
		return true
	})
	stopped := make(chan struct{})
	go func() { mgr.KillRuns(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("force-stop waited for a blocked reload")
	}
	release()
	if err := <-reloaded; !errors.Is(err, errBrainsStopped) {
		t.Fatalf("in-flight reload after force-stop = %v", err)
	}
	if mgr.Get("a") == nil {
		t.Fatal("reload published its replacement set after force-stop")
	}
}

func TestReloadTracksRetirementUntilDeliveryFinishes(t *testing.T) {
	mgr, _, home := queueTestManager(t, 0, "")
	old := mgr.Get("a")
	delivering, release := make(chan struct{}), make(chan struct{})
	releaseDelivery := sync.OnceFunc(func() { close(release) })
	defer releaseDelivery()
	mgr.SetHooks(BrainHooks{Deliver: func(*Brain, Inbound, RunResult) {
		close(delivering)
		<-release
	}})
	if _, _, _, err := mgr.Submit(dm("finish before reload")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-delivering:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not reach delivery")
	}
	cfg, err := LoadConfig(writeConfig(t, `{"data_dir": "`+home+`/data", "claude_path": "`+os.Args[0]+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.reload(nil, nil, cfg, nil); err != nil {
		t.Fatal(err)
	}
	mgr.mu.RLock()
	_, tracked := mgr.retired[old]
	mgr.mu.RUnlock()
	if !tracked {
		t.Error("removed brain disappeared before its worker finished")
	}
	mgr.KillRuns() // Must return despite the blocked retirement/delivery.
	releaseDelivery()
	mgr.retiring.Wait()
	mgr.mu.RLock()
	_, tracked = mgr.retired[old]
	mgr.mu.RUnlock()
	if tracked {
		t.Error("completed retirement remained in the force-stop set")
	}
}
