package brain

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// Capacity eviction must finish the accepted message even though its history
// stays available. The paused worker makes eviction, rather than superseding or
// a race with a running message, the only possible completion path.
func TestCapacityDisplacementCompletesAcceptedMessage(t *testing.T) {
	for _, mode := range []string{"queue", QueueInterrupt} {
		for _, finish := range []string{"drain", "shutdown"} {
			t.Run(mode+"/"+finish, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					mgr := NewManager(Options{
						Runner: brainRunnerFunc(func(_ context.Context, spec RunSpec, _ claudecli.Callbacks) claudecli.Result {
							spec.BeforeStart()
							return claudecli.Result{Text: "answer"}
						}),
						Input:         inputRules(),
						ClaimMedia:    func(string, string, []string) map[string]string { return nil },
						ShutdownGrace: func() time.Duration { return 0 },
					})
					defer mgr.Stop()
					release := mgr.PauseRuns()
					defer release()
					spec := manifest.Spec{Dir: t.TempDir(), Manifest: manifest.Manifest{ID: "a", Observe: manifest.ObserveRun}}
					if failures, err := mgr.Apply([]manifest.Spec{spec}, Globals{Defaults: Settings{QueueMode: mode, MaxPending: 2}}, ApplyOptions{}); err != nil || len(failures) != 0 {
						t.Fatalf("apply: %v %v", failures, err)
					}
					br := mgr.Get("a")
					type completion struct {
						id  string
						err error
					}
					completed := make(chan completion, 6)
					for i, id := range []string{"chatter", "request", "arrival"} {
						m := Inbound{Inbound: chat.Inbound{ID: id, JID: id, Sender: "sender", Text: id, Silent: i == 0}}
						m.Done = func(err error) {
							if id == "chatter" {
								if !br.mu.TryLock() {
									t.Error("displaced callback ran while the queue lock was held")
								} else {
									br.mu.Unlock()
								}
							}
							completed <- completion{id, err}
						}
						if _, _, err := br.Submit(m); err != nil {
							t.Fatalf("submit %s: %v", id, err)
						}
						synctest.Wait()
					}
					select {
					case got := <-completed:
						if got.id != "chatter" || !errors.Is(got.err, ErrDropped) {
							t.Fatalf("capacity completion = %+v; want chatter dropped", got)
						}
					default:
						t.Fatal("capacity-displaced message never received its completion")
					}
					br.mu.Lock()
					var pending []string
					for _, m := range br.lane(LaneChat).pending {
						pending = append(pending, m.ID)
					}
					br.mu.Unlock()
					if !slices.Equal(pending, []string{"request", "arrival"}) {
						t.Fatalf("pending = %v", pending)
					}
					var recorded []string
					for _, entry := range br.Tail(10, 1<<20) {
						recorded = append(recorded, entry.ID)
					}
					if !slices.Equal(recorded, []string{"chatter", "request", "arrival"}) {
						t.Fatalf("history lost accepted messages: %v", recorded)
					}
					var wantErr error
					if finish == "shutdown" {
						wantErr = ErrDropped
						mgr.Stop()
					} else {
						release()
						synctest.Wait()
					}
					for _, id := range []string{"request", "arrival"} {
						select {
						case got := <-completed:
							if got.id != id || !errors.Is(got.err, wantErr) {
								t.Fatalf("completion = %+v; want %s, %v", got, id, wantErr)
							}
						default:
							t.Fatalf("no completion for %s", id)
						}
					}
					mgr.Stop()
					if len(completed) != 0 {
						t.Fatalf("duplicate completions after drain/shutdown: %d", len(completed))
					}
				})
			})
		}
	}
}
