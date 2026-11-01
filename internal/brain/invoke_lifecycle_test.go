package brain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/claudecli"
)

// A second signal may exit the bridge as soon as KillRuns returns. Even
// with a delayed supervisor it must first signal every run, then wait for
// the acknowledgement, without waiting for blocked reply hooks/run.done.
func TestKillRunsWaitsForProcessCancellation(t *testing.T) {
	var runs []*brainRun
	var contexts []context.Context
	mgr := &Manager{}
	for range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		run := &brainRun{cancel: cancel, done: make(chan struct{}), process: &fakeProcessStop{stopped: make(chan struct{})}}
		runs = append(runs, run)
		contexts = append(contexts, ctx)
		mgr.order = append(mgr.order, &Brain{lanes: [laneCount]lane{{current: run}}})
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
			run.process.(*fakeProcessStop).acknowledgeStop()
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
	runs[0].process.(*fakeProcessStop).acknowledgeStop()
	select {
	case <-finished:
		t.Fatal("force-exit returned before the second supervisor acknowledged the kill")
	case <-time.After(50 * time.Millisecond):
	}
	runs[1].process.(*fakeProcessStop).acknowledgeStop()
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
	mgr := &Manager{order: []*Brain{{lanes: [laneCount]lane{{current: run}}}}}
	mgr.KillRuns()
	// An independent context deliberately tests the startup barrier,
	// rather than relying on os/exec noticing the cancelled run context.
	marker := filepath.Join(t.TempDir(), "started")
	proc, err := run.startProcess(context.Background(), claudecli.Spawn{WaitDelay: 250 * time.Millisecond, Binary: "/bin/sh", Args: []string{"-c", `echo started > "$1"`, "sh", marker}})
	if proc != nil {
		_ = proc.Wait()
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
	proc, err := run.startProcess(ctx, claudecli.Spawn{WaitDelay: 250 * time.Millisecond, Binary: "/bin/sh", Args: []string{"-c", "exit 0"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatal(err)
	}
	mgr := &Manager{order: []*Brain{{lanes: [laneCount]lane{{current: run}}}}}
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
	proc := &fakeProcessStop{stopped: make(chan struct{})}
	proc.acknowledgeStop()
	old := &Brain{lanes: [laneCount]lane{{current: &brainRun{cancel: cancel, process: proc}}}}
	mgr := &Manager{retired: map[*Brain]struct{}{old: {}}}
	mgr.KillRuns()
	if ctx.Err() == nil {
		t.Fatal("force-exit missed the run removed from the current brain set")
	}
	if !old.retired {
		t.Fatal("force-exit left the retired brain's queue open")
	}
}

// fakeProcessStop lets manager tests delay the supervisor acknowledgement
// without exposing or mutating the real process owner's internal state.
type fakeProcessStop struct {
	stopped chan struct{}
	once    sync.Once
}

func (p *fakeProcessStop) Stopped() <-chan struct{} { return p.stopped }
func (p *fakeProcessStop) acknowledgeStop()         { p.once.Do(func() { close(p.stopped) }) }
