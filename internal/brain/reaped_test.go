package brain

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

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
