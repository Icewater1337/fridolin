package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	prevDelay := runWaitDelay
	runWaitDelay = 300 * time.Millisecond
	t.Cleanup(func() { runWaitDelay = prevDelay })
	mgr, d, home := queueTestManager(t, 30000, "")
	setsidChildren(t, home, "stdout")
	if _, _, _, err := mgr.Submit(dm("long task")); err != nil {
		t.Fatal(err)
	}
	br := mgr.Get("a")
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
