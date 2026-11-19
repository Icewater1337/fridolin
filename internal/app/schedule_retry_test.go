package app

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/sched"
)

// TestARunARestartCutShortRunsOnceMore: a scheduled agent's brain run
// that a restart cuts short is interrupted, and the first tick after the
// restart runs it once more, with a message that says to check what the
// cut run did first. An agent whose retry_interrupted is false, whose
// schedule changed since, or that was paused meanwhile gets no retry;
// nor does the retry itself, cut short in turn.
func TestARunARestartCutShortRunsOnceMore(t *testing.T) {
	t.Parallel()
	runs := fakeRuns(t)
	h := newHarness(t, "")
	h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}, Schedules: []manifest.Schedule{
		{Name: "digest", When: "every 6h", Prompt: "Write the weekly digest.", Report: manifest.ReportAlways, JitterMinutes: new(0)},
		{Name: "careful", When: "every 6h", Prompt: "Send the invoices.", Report: manifest.ReportSilent, RetryInterrupted: new(false), JitterMinutes: new(0)},
		{Name: "edited", When: "every 6h", Prompt: "Look around.", Report: manifest.ReportSilent, JitterMinutes: new(0)},
		{Name: "paused", When: "every 6h", Prompt: "Look around.", Report: manifest.ReportSilent, JitterMinutes: new(0)},
	}})
	names := []string{"digest", "careful", "edited", "paused"}
	setenv(t, "FRIDOLIN_FAKE_SLEEP_MS", "30000")
	for _, name := range names {
		if w := h.Do("POST", "/api/schedules/main/"+name+"/run", nil); w.Code != http.StatusOK {
			t.Fatalf("run %s: %d %s", name, w.Code, w.Body.String())
		}
	}
	h.WaitFor("the agents to run", func() bool {
		for _, a := range h.schedules().Agents {
			if !a.Running {
				return false
			}
		}
		return true
	})
	if w := h.Do("PUT", "/api/schedules/main/edited", map[string]any{"when": "every 6h", "prompt": "Look elsewhere.", "report": "silent", "jitter_minutes": 0}); w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("POST", "/api/schedules/main/paused/pause", nil); w.Code != http.StatusOK {
		t.Fatalf("pause: %d %s", w.Code, w.Body.String())
	}
	h.WaitFor("the edit to apply", func() bool { return h.agent("main", "edited").Schedule.Prompt == "Look elsewhere." })

	h.Restart()
	setenv(t, "FRIDOLIN_FAKE_SLEEP_MS", "")
	for _, name := range names {
		got := h.agentRuns("main", name)
		if len(got) != 1 || got[0].Status != sched.StatusInterrupted || got[0].Rev == "" {
			t.Fatalf("%s after the restart: %+v", name, got)
		}
	}
	cut := h.agentRuns("main", "digest")[0]
	before := len(runs())
	h.b.agents.tick(time.Now())
	r := h.waitRunEnded("main", "digest", 2)
	if r.Trigger != sched.TriggerRestartRetry || r.RetryOf != cut.ID || r.Status != sched.StatusOK || !r.Sent {
		t.Errorf("the retry: %+v", r)
	}
	all := runs()
	if len(all) != before+1 {
		t.Fatalf("%d runs after the restart, want the one retry", len(all)-before)
	}
	for _, want := range []string{"runs once more", "interrupted by a restart", "schedule:digest", "Do not repeat work or deliveries", "Write the weekly digest."} {
		if !strings.Contains(all[len(all)-1].Prompt, want) {
			t.Errorf("the retry's prompt lacks %q:\n%s", want, all[len(all)-1].Prompt)
		}
	}
	for _, name := range []string{"careful", "edited", "paused"} {
		if got := h.agentRuns("main", name); len(got) != 1 {
			t.Errorf("%s was retried: %+v", name, got)
		}
	}
	// Once: later ticks start none.
	h.b.agents.tick(time.Now().Add(time.Minute))
	if got := h.agentRuns("main", "digest"); len(got) != 2 {
		t.Errorf("a second retry: %+v", got)
	}

	// The retry cut short in turn is not retried again.
	setenv(t, "FRIDOLIN_FAKE_SLEEP_MS", "30000")
	h.b.agents.mu.Lock()
	h.b.agents.retries = []sched.Run{cut}
	h.b.agents.mu.Unlock()
	h.b.agents.retryInterrupted(h.b.cfgs.Get().Live.Schedules, nil)
	h.WaitFor("the second retry to run", func() bool { return h.agent("main", "digest").Running })
	h.Restart()
	setenv(t, "FRIDOLIN_FAKE_SLEEP_MS", "")
	h.b.agents.tick(time.Now())
	if got := h.agentRuns("main", "digest"); len(got) != 3 || got[0].Status != sched.StatusInterrupted || got[0].Trigger != sched.TriggerRestartRetry {
		t.Errorf("a cut retry: %+v", got)
	}
}

// TestAnInterruptionIsNoFailure: a run a restart cut short neither counts
// toward an agent's failures in a row nor ends them.
func TestAnInterruptionIsNoFailure(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "")
	st := h.b.agents.store
	t0 := time.Now().Add(-time.Hour)
	for i, status := range []string{sched.StatusOK, sched.StatusFailed, sched.StatusInterrupted, sched.StatusFailed, sched.StatusInterrupted} {
		at := t0.Add(time.Duration(i) * time.Minute)
		if err := st.Finish(sched.Run{ID: sched.NewRunID(at), Brain: "main", Agent: "x", Kind: sched.KindBrain, Started: at, Ended: at, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.b.agents.opsAgentOf(scheduleView{Brain: "main", Name: "x"}).ConsecutiveFailures; got != 2 {
		t.Errorf("consecutive failures %d, want 2", got)
	}
}
