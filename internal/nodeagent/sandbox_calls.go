package nodeagent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// sandboxCall serves a sandbox.* method.
func (a *Agent) sandboxCall(ctx context.Context, method string, raw json.RawMessage) (any, *nodeproto.Error) {
	a.pruneRuns(time.Now())
	decode := func(v any) *nodeproto.Error {
		if len(raw) == 0 {
			return nil
		}
		if err := json.Unmarshal(raw, v); err != nil {
			return nodeproto.Errorf(nodeproto.CodeBadRequest, "%s params: %v", method, err)
		}
		return nil
	}
	switch method {
	case nodeproto.MethodSandboxInfo:
		info, err := a.sandboxProbe(ctx)
		if err != nil {
			return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "sbx does not run on this node: %v", err)
		}
		return info, nil
	case nodeproto.MethodSandboxEnsure:
		var p nodeproto.SandboxEnsureParams
		if perr := decode(&p); perr != nil {
			return nil, perr
		}
		return a.sandboxEnsure(ctx, p)
	case nodeproto.MethodSandboxRun:
		var p nodeproto.SandboxRunParams
		if perr := decode(&p); perr != nil {
			return nil, perr
		}
		return a.sandboxRunStart(ctx, p)
	case nodeproto.MethodSandboxRead:
		var p nodeproto.SandboxReadParams
		if perr := decode(&p); perr != nil {
			return nil, perr
		}
		return a.sandboxRead(ctx, p)
	case nodeproto.MethodSandboxStop:
		var p nodeproto.SandboxStopParams
		if perr := decode(&p); perr != nil {
			return nil, perr
		}
		if err := p.Check(); err != nil {
			return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
		}
		r := a.getRun(p.Run)
		if r == nil {
			return nil, nodeproto.Errorf(nodeproto.CodeNotFound, "no run %s on this node", p.Run)
		}
		a.stopRun(r, p.Grace())
		return struct{}{}, nil
	case nodeproto.MethodSandboxRemove:
		var p nodeproto.SandboxRemoveParams
		if perr := decode(&p); perr != nil {
			return nil, perr
		}
		return a.sandboxRemove(ctx, p)
	}
	return nil, nodeproto.Errorf(nodeproto.CodeUnsupported, "no method %q", method)
}

// addRun registers r; a run id is used once.
func (a *Agent) addRun(r *sandboxRun) *nodeproto.Error {
	sb := &a.sandbox
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.runs == nil {
		sb.runs = map[string]*sandboxRun{}
	}
	if _, ok := sb.runs[r.id]; ok {
		return nodeproto.Errorf(nodeproto.CodeBadRequest, "run %s exists already", r.id)
	}
	sb.runs[r.id] = r
	return nil
}

func (a *Agent) dropRun(id string) {
	a.sandbox.mu.Lock()
	delete(a.sandbox.runs, id)
	a.sandbox.mu.Unlock()
}

func (a *Agent) getRun(id string) *sandboxRun {
	a.sandbox.mu.Lock()
	defer a.sandbox.mu.Unlock()
	return a.sandbox.runs[id]
}

// sandboxBusy reports whether a run is going on in brain's sandbox.
func (a *Agent) sandboxBusy(brain string) bool {
	a.sandbox.mu.Lock()
	runs := make([]*sandboxRun, 0, len(a.sandbox.runs))
	for _, r := range a.sandbox.runs {
		if r.brain == brain {
			runs = append(runs, r)
		}
	}
	a.sandbox.mu.Unlock()
	for _, r := range runs {
		if !r.isDone() {
			return true
		}
	}
	return false
}

// pruneRuns forgets the runs that ended SandboxRunForget ago and were
// not read since.
func (a *Agent) pruneRuns(now time.Time) {
	a.sandbox.mu.Lock()
	defer a.sandbox.mu.Unlock()
	for id, r := range a.sandbox.runs {
		r.mu.Lock()
		last := r.ended
		if r.lastRead.After(last) {
			last = r.lastRead
		}
		old := r.done && now.Sub(last) >= nodeproto.SandboxRunForget
		r.mu.Unlock()
		if old {
			delete(a.sandbox.runs, id)
		}
	}
}

// sandboxRead is sandbox.read: what the run wrote from the offset,
// waiting up to WaitMS for something while there is nothing new.
func (a *Agent) sandboxRead(ctx context.Context, p nodeproto.SandboxReadParams) (any, *nodeproto.Error) {
	if err := p.Check(); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	r := a.getRun(p.Run)
	if r == nil {
		return nil, nodeproto.Errorf(nodeproto.CodeNotFound, "no run %s on this node", p.Run)
	}
	wait := time.NewTimer(time.Duration(p.WaitMS) * time.Millisecond)
	defer wait.Stop()
	for {
		res, changed, ok := r.readFrom(p.Offset)
		if !ok {
			return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "offset %d is past the run's output", p.Offset)
		}
		if res.Data != "" || res.Done || p.WaitMS == 0 {
			return res, nil
		}
		select {
		case <-changed:
		case <-wait.C:
			return res, nil
		case <-ctx.Done():
			return res, nil
		}
	}
}

// stopRun ends r inside its VM: TERM to its process group, KILL after
// grace, and the sbx client on the host last, should neither end it.
func (a *Agent) stopRun(r *sandboxRun, grace time.Duration) {
	if r.isDone() {
		return
	}
	if d := a.sbx(); d != nil {
		signal := func(sig string) {
			ctx, cancel := context.WithTimeout(context.Background(), sandboxProbeTimeout)
			defer cancel()
			_, _, _ = d.run(ctx, nil, nil, "exec", r.name, "sh", "-c", `kill -`+sig+` -"$(cat "$0")"`, nodeproto.SandboxPidFile(r.id))
		}
		signal("TERM")
		if r.waitDone(grace) {
			return
		}
		signal("KILL")
		if r.waitDone(5 * time.Second) {
			return
		}
	}
	r.mu.Lock()
	cmd := r.cmd
	r.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// waitDone waits up to d for r to end and reports whether it did.
func (r *sandboxRun) waitDone(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		r.mu.Lock()
		done, changed := r.done, r.changed
		r.mu.Unlock()
		if done {
			return true
		}
		select {
		case <-changed:
		case <-t.C:
			return false
		}
	}
}
