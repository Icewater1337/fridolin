package brain

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

func TestRetirementKeepsItsQueueWhenStopHasBegun(t *testing.T) {
	mgr, _, _ := queueTestManager(t, 10, "")
	var started, kept atomic.Int32
	mgr.SetSink(&testSink{Keep: func(_ *Brain, ms []Inbound) { kept.Add(int32(len(ms))) }})
	mgr.Get("a").runner = brainRunnerFunc(func(context.Context, RunSpec, claudecli.Callbacks) claudecli.Result {
		started.Add(1)
		return claudecli.Result{}
	})
	// A retirement whose old worker is still delivering holds Stop at
	// its retirement barrier. Its queue arrives at requeue afterward.
	mgr.retiring.Add(1)
	retired := sync.OnceFunc(mgr.retiring.Done)
	defer retired()
	stopped := make(chan struct{})
	go func() { mgr.Stop(); close(stopped) }()
	testutil.WaitFor(t, 5*time.Second, "the stop to begin", func() bool {
		mgr.reloadMu.Lock()
		defer mgr.reloadMu.Unlock()
		return mgr.stopped
	})
	mgr.requeue(mgr.Get("a"), []Inbound{dm("still waiting")})
	testutil.WaitFor(t, 5*time.Second, "the retired queue to be handled", func() bool { return started.Load()+kept.Load() > 0 })
	if started.Load() != 0 || kept.Load() != 1 {
		t.Errorf("retirement during stop: started=%d, kept=%d; want zero runs and one kept message", started.Load(), kept.Load())
	}
	retired()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not finish after retirement")
	}
}
