package claudecli

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// remoteProcess is a run elsewhere, as Spawn.Launch makes one: its output
// comes from a string, and it ends once that is written.
type remoteProcess struct {
	done    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func (p *remoteProcess) PID() int                 { return 0 }
func (p *remoteProcess) ID() string               { return "run r1 on mini" }
func (p *remoteProcess) Wait() error              { <-p.done; p.stop(); return nil }
func (p *remoteProcess) StderrTail() string       { return "a warning" }
func (p *remoteProcess) Stopped() <-chan struct{} { return p.stopped }
func (p *remoteProcess) stop()                    { p.once.Do(func() { close(p.stopped) }) }

// A run whose Spawn has Launch starts through it: Run parses what it
// writes, and the callbacks get its ID and no PID.
func TestRunThroughLaunch(t *testing.T) {
	t.Parallel()
	var prompt string
	launch := func(ctx context.Context, s Spawn) (Process, error) {
		prompt = s.Prompt
		p := &remoteProcess{done: make(chan struct{}), stopped: make(chan struct{})}
		go func() {
			defer close(p.done)
			_, _ = io.Copy(s.Stdout, strings.NewReader(`{"type":"result","subtype":"success","result":"hi from the sandbox"}`+"\n"))
		}()
		return p, nil
	}
	cb := quietRunCallbacks()
	pid := -1
	var ids []string
	cb.Started = func(p int) func() { pid = p; return nil }
	open := cb.OpenStream
	cb.OpenStream = func(id string, result func(Result)) (StreamSink, func()) {
		ids = append(ids, id)
		return open(id, result)
	}
	cb.Stderr = func(id, text string) { ids = append(ids, id+": "+text) }
	res := Run(context.Background(), Invocation{Provider: "claude", Run: Spawn{Prompt: "hello", Launch: launch}, NoticeAfter: time.Hour, ExitGrace: time.Second}, cb)
	if res.Err != nil || res.Text != "hi from the sandbox" {
		t.Fatalf("got %+v", res)
	}
	if prompt != "hello" || pid != 0 {
		t.Errorf("prompt %q, pid %d", prompt, pid)
	}
	if want := []string{"run r1 on mini", "run r1 on mini: a warning"}; strings.Join(ids, "|") != strings.Join(want, "|") {
		t.Errorf("ids %q, want %q", ids, want)
	}
}
