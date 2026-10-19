package brain

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/Icewater1337/fridolin/internal/claudecli"
)

type brainRunnerFunc func(context.Context, RunSpec, claudecli.Callbacks) claudecli.Result

func (f brainRunnerFunc) Run(ctx context.Context, spec RunSpec, callbacks claudecli.Callbacks) claudecli.Result {
	return f(ctx, spec, callbacks)
}

func TestBrainRunnerPreservesOrderPositionAndSettingsSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mgr, d, _ := queueTestManager(t, 0, "")
		br := mgr.Get("a")
		started := make(chan RunSpec, 3)
		release := make(chan struct{})
		defer close(release)
		runs := 0
		br.runner = brainRunnerFunc(func(ctx context.Context, spec RunSpec, callbacks claudecli.Callbacks) claudecli.Result {
			runs++
			spec.BeforeStart()
			if cleanup := callbacks.Started(runs); cleanup != nil {
				defer cleanup()
			}
			defer callbacks.Reaped()
			started <- spec
			select {
			case <-release:
				return claudecli.Result{Text: fmt.Sprintf("answer %d", runs)}
			case <-ctx.Done():
				return claudecli.Result{Err: claudecli.ErrInterrupted}
			}
		})
		original := br.settings.Load()
		if _, _, busy, err := mgr.Submit(dm("first")); busy || err != nil {
			t.Fatalf("first submit: busy=%v err=%v", busy, err)
		}
		synctest.Wait()
		first := <-started
		if rt := br.Snapshot(); rt.Running == nil || rt.Running.PID != 1 {
			t.Fatalf("started callback did not publish the run: %+v", rt.Running)
		}
		updated := *original
		updated.Model = "updated-model"
		br.settings.Store(&updated)
		for i, text := range []string{"second", "third"} {
			if _, pos, busy, err := mgr.Submit(dm(text)); !busy || pos != i+1 || err != nil {
				t.Fatalf("%s submit: busy=%v pos=%d err=%v", text, busy, pos, err)
			}
		}
		if first.Settings != original || first.Settings.Model == "updated-model" || first.ID != br.ID() || first.Dir != br.workDir || !strings.Contains(first.Prompt, "first") {
			t.Fatalf("the first run lost its snapshot or input: %+v", first)
		}
		for _, text := range []string{"second", "third"} {
			release <- struct{}{}
			synctest.Wait()
			next := <-started
			if next.Settings.Model != "updated-model" || !strings.Contains(next.Prompt, text) {
				t.Fatalf("queued run did not take current settings/input: %+v", next)
			}
		}
		release <- struct{}{}
		synctest.Wait()
		if d.count() != 3 || br.Snapshot().Running != nil {
			t.Fatalf("finished runs=%d running=%+v", d.count(), br.Snapshot().Running)
		}
		for i, got := range d.all() {
			if got.res.Text != fmt.Sprintf("answer %d", i+1) {
				t.Fatalf("delivery %d: %+v", i, got)
			}
		}
	})
}
