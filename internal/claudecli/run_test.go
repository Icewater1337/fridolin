package claudecli

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// A completed parse closes its activity log before making the result
// available to supervision. The next runtime turn may rotate that log.
func TestRunWaitsForStreamClose(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			dir := t.TempDir()
			output := "{\"type\":\"result\",\"result\":\"done\"}\n"
			if provider == "codex" {
				output = "{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"done\"}}\n{\"type\":\"turn.completed\"}\n"
			}
			path := filepath.Join(dir, "output")
			if err := os.WriteFile(path, []byte(output), 0600); err != nil {
				t.Fatal(err)
			}
			closing, release, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
			releaseClose := sync.OnceFunc(func() { close(release) })
			defer releaseClose()
			callbacks := quietRunCallbacks()
			callbacks.OpenStream = func(_ int, result func(Result)) (StreamSink, func()) {
				return &testRunSink{recordingSink: &recordingSink{}, result: result}, func() {
					close(closing)
					<-release
					close(closed)
				}
			}
			done := make(chan struct{})
			var got Result
			go func() {
				got = Run(context.Background(), Invocation{
					Run:      Spawn{Binary: "/bin/cat", Args: []string{path}, Dir: dir, Env: []string{"HOME=" + dir}, WaitDelay: time.Second},
					Provider: provider, ExitGrace: time.Hour, NoticeAfter: time.Hour,
				}, callbacks)
				close(done)
			}()
			select {
			case <-closing:
			case <-time.After(5 * time.Second):
				t.Fatal("parser never began stream cleanup")
			}
			select {
			case <-done:
				t.Error("run returned before its stream close completed")
			case <-time.After(250 * time.Millisecond):
			}
			releaseClose()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("run did not finish after stream close")
			}
			<-closed
			if got.Err != nil || got.Text != "done" {
				t.Errorf("result = %+v", got)
			}
		})
	}
}

type testRunSink struct {
	*recordingSink
	result func(Result)
}

func (s *testRunSink) Result(r Result) { s.recordingSink.Result(r); s.result(r) }

func quietRunCallbacks() Callbacks {
	return Callbacks{
		Start:   Start,
		Started: func(int) func() { return nil },
		Reaped:  func() {},
		OpenStream: func(_ int, result func(Result)) (StreamSink, func()) {
			return &testRunSink{recordingSink: &recordingSink{}, result: result}, func() {}
		},
		ReadPanic:    func(any, []byte) {},
		Notice:       func() {},
		GraceExpired: func() {},
		SlowResponse: func(time.Duration) {},
		Stderr:       func(int, string) {},
	}
}
