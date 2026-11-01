package brain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

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
			settings.Provider, settings.CodexPath = provider, settings.ClaudePath
			br.settings.Store(&settings)
			setsidChildren(t, home, "stdout")
			if _, _, _, err := mgr.Submit(dm("long task")); err != nil {
				t.Fatal(err)
			}
			testutil.WaitFor(t, 5*time.Second, "the run's grandchild", func() bool {
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
			testutil.WaitFor(t, 5*time.Second, "the stopped run", func() bool { return d.count() == 1 })
			if res := d.all()[0].res; !errors.Is(res.Err, claudecli.ErrInterrupted) {
				t.Errorf("result = %+v, want interrupted", res)
			}
		})
	}
}

// TestStopBeforeTheCLIStartsIsAnInterruption: a run stopped while it is
// prepared, before the CLI starts, is recorded as interrupted, as one
// stopped a moment later is. Start's "context canceled" made it a failed
// run, shown as an error on the dashboard.
func TestStopBeforeTheCLIStartsIsAnInterruption(t *testing.T) {
	mgr, d, _ := queueTestManager(t, 0, "")
	br := mgr.Get("a")
	original := br.runner
	br.runner = brainRunnerFunc(func(ctx context.Context, spec RunSpec, callbacks claudecli.Callbacks) claudecli.Result {
		br.mu.Lock()
		cur := br.lane(LaneChat).current
		br.mu.Unlock()
		br.interruptRun(cur)
		return original.Run(ctx, spec, callbacks)
	})
	if _, _, _, err := mgr.Submit(dm("stopped at once")); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 5*time.Second, "the run", func() bool { return d.count() == 1 })
	if res := d.all()[0].res; !errors.Is(res.Err, claudecli.ErrInterrupted) {
		t.Errorf("result = %+v, want interrupted", res)
	}
	if last := br.Snapshot().Last; last == nil || last.Outcome != "interrupted" || last.Error != "" {
		t.Errorf("last run = %+v, want outcome interrupted", last)
	}
}

// A result is final even if a stop or deadline arrives while inherited
// output is still open. Keep both answers and the CLI's own error then.
// The output stays open until the test releases it, after the stop or the
// deadline: runWaitDelay is longer than the test waits, so nothing else
// can end the run first, and the run ends as soon as it is released.
func TestCancellationKeepsTheRecordedResult(t *testing.T) {
	cases := []struct {
		name      string
		errorText string
		timeout   bool
		empty     bool
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
				postResultGrace, runWaitDelay = 30*time.Second, time.Minute
				t.Cleanup(func() { postResultGrace, runWaitDelay = prevGrace, prevDelay })
				mgr, _, home := queueTestManager(t, 0, "")
				setsidChildren(t, home, "stdout,stderr")
				const answer = "The recorded answer"
				br := mgr.Get("a")
				settings := *br.settings.Load()
				settings.Provider = provider
				settings.CodexPath = settings.ClaudePath // the same fake prints either stream below
				var output string
				if provider == "codex" {
					switch {
					case tc.empty:
						output = `{"type":"turn.completed"}`
					case tc.errorText != "":
						output = `{"type":"turn.failed","error":{"message":` + strconv.Quote(tc.errorText) + `}}`
					default:
						output = `{"type":"item.completed","item":{"type":"agent_message","text":` + strconv.Quote(answer) + `}}` + "\n" + `{"type":"turn.completed"}`
					}
				} else {
					switch {
					case tc.empty:
						output = `{"type":"result","result":""}`
					case tc.errorText != "":
						output = `{"type":"result","is_error":true,"result":` + strconv.Quote(tc.errorText) + `}`
					default:
						output = `{"type":"result","result":` + strconv.Quote(answer) + `}`
					}
				}
				t.Setenv("FRIDOLIN_FAKE_RESULT", output)
				var ctx context.Context
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				deadline := &deadlineCtx{Context: context.Background(), done: make(chan struct{})}
				if tc.timeout {
					ctx = deadline
				}
				m := dm("answer before stopping")
				run := &brainRun{cancel: cancel, done: make(chan struct{}), msg: m}
				result := make(chan claudecli.Result, 1)
				go func() { result <- br.invokeGuarded(run, ctx, &settings, m) }()
				testutil.WaitFor(t, 5*time.Second, "the recorded result", func() bool {
					data, _ := os.ReadFile(filepath.Join(br.workDir, StreamLogName))
					return strings.Contains(string(data), "✅ Result:") || strings.Contains(string(data), "❌ Error:")
				})
				var pids []string
				testutil.WaitFor(t, 5*time.Second, "the grandchild holding the output", func() bool {
					data, _ := os.ReadFile(filepath.Join(home, "pids"))
					pids = strings.Fields(string(data))
					return len(pids) == 2
				})
				if tc.timeout {
					close(deadline.done)
				} else {
					cancel()
				}
				// The stop or deadline is in while the output is held: now
				// let it go, as the grandchild would once it ends.
				grandchild, err := strconv.Atoi(pids[1])
				if err != nil {
					t.Fatal(err)
				}
				testutil.KillPID(grandchild)
				select {
				case res := <-result:
					if tc.empty {
						if !errors.Is(res.Err, claudecli.ErrNoResult) {
							t.Errorf("result = %+v, want errNoResult", res)
						}
					} else if tc.errorText == "" {
						if res.Err != nil || res.Text != answer {
							t.Errorf("result = %+v, want the recorded answer", res)
						}
					} else {
						var cliErr *claudecli.Error
						if !errors.As(res.Err, &cliErr) || cliErr.Message != tc.errorText || cliErr.Provider != provider {
							t.Errorf("result = %+v, want CLI error %q", res, tc.errorText)
						}
					}
				case <-time.After(10 * time.Second):
					t.Fatal("the cancelled run kept waiting once its output was released")
				}
			})
		}
	}
}

// deadlineCtx is a run's deadline that the test sets off itself, by
// closing done: Err is then context.DeadlineExceeded, as at run_timeout.
type deadlineCtx struct {
	context.Context
	done chan struct{}
}

func (c *deadlineCtx) Done() <-chan struct{} { return c.done }

func (c *deadlineCtx) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func TestKillRunsFreezesQueuedAndNewRuns(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			mgr, d, home := queueTestManager(t, 30000, "")
			br := mgr.Get("a")
			settings := *br.settings.Load()
			settings.Provider, settings.CodexPath = provider, settings.ClaudePath
			br.settings.Store(&settings)
			pidLog := filepath.Join(home, "run-pids")
			t.Setenv("FRIDOLIN_FAKE_PID_LOG", pidLog)
			delivering, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			releaseDelivery := sync.OnceFunc(func() { close(release) })
			defer releaseDelivery()
			mgr.SetSink(&testSink{Deliver: func(br *Brain, m Inbound, res claudecli.Result) {
				once.Do(func() { close(delivering) })
				<-release
				d.add(br, m, res)
			}})
			if _, _, _, err := mgr.Submit(dm("first")); err != nil {
				t.Fatal(err)
			}
			testutil.WaitFor(t, 5*time.Second, "the CLI to start", func() bool {
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
			if _, _, _, err := mgr.Submit(dm("new")); !errors.Is(err, ErrStopped) {
				t.Errorf("new message after force-stop: %v", err)
			}
			if _, _, err := br.Submit(dm("direct")); !errors.Is(err, ErrRetired) {
				t.Errorf("direct message after force-stop: %v", err)
			}
			releaseDelivery()
			testutil.WaitFor(t, 5*time.Second, "the worker to finish without another turn", func() bool {
				br.mu.Lock()
				defer br.mu.Unlock()
				return !br.lane(LaneChat).working
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

func TestReloadTracksRetirementUntilDeliveryFinishes(t *testing.T) {
	mgr, _, _ := queueTestManager(t, 0, "")
	old := mgr.Get("a")
	delivering, release := make(chan struct{}), make(chan struct{})
	releaseDelivery := sync.OnceFunc(func() { close(release) })
	defer releaseDelivery()
	mgr.SetSink(&testSink{Deliver: func(*Brain, Inbound, claudecli.Result) {
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
	if _, err := mgr.Apply(nil, Globals{}, ApplyOptions{}); err != nil {
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
