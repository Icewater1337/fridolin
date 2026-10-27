package sandboxrun_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/sandboxrun"
	"github.com/Icewater1337/fridolin/internal/sandboxrun/sandboxruntest"
)

// A run's output comes through the reads, and its exit code ends it.
func TestProcessCopiesTheOutput(t *testing.T) {
	t.Parallel()
	node := &sandboxruntest.Node{Info: sandboxruntest.Ready("claude"), Output: "line one\nline two\n", ExitCode: 3}
	var out bytes.Buffer
	p, err := sandboxrun.Start(context.Background(), node.Call, nodeproto.SandboxRunParams{Brain: "main", Run: "r1", Stdin: "hi"}, &out, sandboxrun.Options{Label: "sandbox run r1 on mini"})
	if err != nil {
		t.Fatal(err)
	}
	err = p.Wait()
	if err == nil || err.Error() != "exit status 3" {
		t.Errorf("Wait: %v", err)
	}
	if out.String() != "line one\nline two\n" || p.StderrTail() != "run r1 ended" || p.ID() != "sandbox run r1 on mini" || p.PID() != 0 {
		t.Errorf("out %q, stderr %q, id %q", out.String(), p.StderrTail(), p.ID())
	}
	if len(node.Stops) != 0 {
		t.Errorf("a run that ended was stopped: %+v", node.Stops)
	}
}

// A run whose context ends is stopped on the node, inside the VM.
func TestProcessStopsOnTheNode(t *testing.T) {
	t.Parallel()
	node := &sandboxruntest.Node{Info: sandboxruntest.Ready("claude"), Output: "partial", Hang: true}
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	p, err := sandboxrun.Start(ctx, node.Call, nodeproto.SandboxRunParams{Brain: "main", Run: "r2"}, &out, sandboxrun.Options{Grace: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-p.Stopped():
	case <-time.After(10 * time.Second):
		t.Fatal("the stop was not acknowledged")
	}
	_ = p.Wait()
	node.Set(func(n *sandboxruntest.Node) {
		if len(n.Stops) != 1 || n.Stops[0].Run != "r2" || n.Stops[0].GraceS != 2 {
			t.Errorf("stops: %+v", n.Stops)
		}
	})
	if out.String() != "partial" {
		t.Errorf("out %q", out.String())
	}
}

// A node that stops answering loses the run; one that refuses the start
// starts nothing; a prompt over the bound is refused here.
func TestProcessFailures(t *testing.T) {
	t.Parallel()
	node := &sandboxruntest.Node{Info: sandboxruntest.Ready("claude"), Hang: true}
	p, err := sandboxrun.Start(context.Background(), node.Call, nodeproto.SandboxRunParams{Run: "r3"}, &bytes.Buffer{}, sandboxrun.Options{ReadWait: 50 * time.Millisecond, Retry: 10 * time.Millisecond, GiveUp: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	node.Set(func(n *sandboxruntest.Node) { n.Offline = true })
	if err := p.Wait(); !errors.Is(err, sandboxrun.ErrLost) {
		t.Errorf("Wait: %v", err)
	}
	if _, err := sandboxrun.Start(context.Background(), node.Call, nodeproto.SandboxRunParams{Run: "r4"}, &bytes.Buffer{}, sandboxrun.Options{}); err == nil {
		t.Error("an offline node started a run")
	}
	big := nodeproto.SandboxRunParams{Run: "r5", Stdin: strings.Repeat("x", nodeproto.MaxSandboxStdinBytes+1)}
	if _, err := sandboxrun.Start(context.Background(), node.Call, big, &bytes.Buffer{}, sandboxrun.Options{}); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("a big prompt: %v", err)
	}
}
