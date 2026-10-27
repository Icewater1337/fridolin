// Package sandboxruntest is a fake node with Docker Sandboxes for tests:
// it answers the sandbox methods from memory and streams canned output.
package sandboxruntest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// Node is a fake node's sandbox capability. Set its fields before the
// calls; it records what it was asked.
type Node struct {
	mu sync.Mutex
	// Info is what sandbox.info answers.
	Info nodeproto.SandboxInfo
	// EnsureErr fails sandbox.ensure; Offline fails every call as a node
	// that is not connected.
	EnsureErr error
	Offline   bool
	// Output is what each run writes to stdout, ExitCode how it ends.
	Output   string
	ExitCode int
	// Hang keeps a run going after its output until it is stopped.
	Hang bool

	Ensured []nodeproto.SandboxEnsureParams
	Runs    []nodeproto.SandboxRunParams
	Stops   []nodeproto.SandboxStopParams
	runs    map[string]*run
}

type run struct {
	out     string
	done    bool
	changed chan struct{}
}

// Ready is the SandboxInfo of a node whose sandboxes run providers.
func Ready(providers ...string) nodeproto.SandboxInfo {
	return nodeproto.SandboxInfo{SandboxCap: nodeproto.SandboxCap{Version: "0.50.0", SignedIn: true, Daemon: true, Settings: true, Template: "fridolin-brain:test", Providers: providers},
		BinPath: "/opt/fridolin/bin/fridolin", ToolsPath: "/opt/fridolin/tools", FreeMemoryMB: 8192}
}

// Set changes the node under its lock.
func (n *Node) Set(f func(n *Node)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	f(n)
}

// Unavailable is the error of a node that is not connected.
func Unavailable() error {
	return nodeproto.Errorf(nodeproto.CodeUnavailable, "node mini is not connected")
}

func decode(params, out any) {
	data, _ := json.Marshal(params)
	_ = json.Unmarshal(data, out)
}

func answer(v, out any) error {
	if out == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// Call answers method as the node would: a sandboxrun.Caller.
func (n *Node) Call(ctx context.Context, method string, params, out any) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.Offline {
		return Unavailable()
	}
	if n.runs == nil {
		n.runs = map[string]*run{}
	}
	switch method {
	case nodeproto.MethodSandboxInfo:
		return answer(n.Info, out)
	case nodeproto.MethodSandboxEnsure:
		var p nodeproto.SandboxEnsureParams
		decode(params, &p)
		n.Ensured = append(n.Ensured, p)
		if n.EnsureErr != nil {
			return n.EnsureErr
		}
		return answer(nodeproto.SandboxEnsureResult{Name: nodeproto.SandboxName(p.Brain), State: "ready"}, out)
	case nodeproto.MethodSandboxRun:
		var p nodeproto.SandboxRunParams
		decode(params, &p)
		n.Runs = append(n.Runs, p)
		n.runs[p.Run] = &run{out: n.Output, done: !n.Hang, changed: make(chan struct{})}
		return answer(nodeproto.SandboxRunResult{Run: p.Run}, out)
	case nodeproto.MethodSandboxRead:
		var p nodeproto.SandboxReadParams
		decode(params, &p)
		return n.read(ctx, p, out)
	case nodeproto.MethodSandboxStop:
		var p nodeproto.SandboxStopParams
		decode(params, &p)
		n.Stops = append(n.Stops, p)
		if r := n.runs[p.Run]; r != nil && !r.done {
			r.done = true
			close(r.changed)
		}
		return answer(struct{}{}, out)
	case nodeproto.MethodSandboxRemove:
		return answer(struct{}{}, out)
	}
	return nodeproto.Errorf(nodeproto.CodeUnsupported, "unknown method %s", method)
}

// read answers sandbox.read, waiting while a hanging run has nothing new;
// the caller holds n.mu.
func (n *Node) read(ctx context.Context, p nodeproto.SandboxReadParams, out any) error {
	r := n.runs[p.Run]
	if r == nil {
		return nodeproto.Errorf(nodeproto.CodeNotFound, "no run %s", p.Run)
	}
	if !r.done && p.Offset >= int64(len(r.out)) {
		changed := r.changed
		n.mu.Unlock()
		wait := time.NewTimer(time.Duration(p.WaitMS) * time.Millisecond)
		select {
		case <-changed:
		case <-ctx.Done():
		case <-wait.C:
		}
		wait.Stop()
		n.mu.Lock()
	}
	res := nodeproto.SandboxReadResult{Next: int64(len(r.out)), Done: r.done}
	if p.Offset < int64(len(r.out)) {
		res.Data = r.out[p.Offset:]
	}
	if r.done {
		res.ExitCode, res.StderrTail = n.ExitCode, fmt.Sprintf("run %s ended", p.Run)
	}
	return answer(res, out)
}
