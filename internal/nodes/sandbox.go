package nodes

import (
	"context"
	"fmt"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// The sandbox family: brains' runs in Docker Sandboxes (sbx) on a node.
// Each call is checked here before it is sent, and on the node again.

func init() {
	// The first create pulls the agent's image (about 40 s), and a
	// recreate removes the old sandbox first.
	slowCalls[nodeproto.MethodSandboxEnsure] = 5 * time.Minute
	slowCalls[nodeproto.MethodSandboxInfo] = 2 * time.Minute
	slowCalls[nodeproto.MethodSandboxRun] = 2 * time.Minute
	slowCalls[nodeproto.MethodSandboxStop] = nodeproto.MaxSandboxGraceS*time.Second + time.Minute
	slowCalls[nodeproto.MethodSandboxRemove] = 2 * time.Minute
}

// SandboxInfo asks node id about sbx and its sandboxes.
func (s *Service) SandboxInfo(ctx context.Context, id string) (nodeproto.SandboxInfo, error) {
	var out nodeproto.SandboxInfo
	return out, s.Call(ctx, id, nodeproto.MethodSandboxInfo, struct{}{}, &out)
}

// SandboxEnsure makes sure the brain's sandbox on node id is as p asks.
func (s *Service) SandboxEnsure(ctx context.Context, id string, p nodeproto.SandboxEnsureParams) (nodeproto.SandboxEnsureResult, error) {
	var out nodeproto.SandboxEnsureResult
	if err := p.Check(); err != nil {
		return out, fmt.Errorf("%w: %v", ErrCall, err)
	}
	return out, s.Call(ctx, id, nodeproto.MethodSandboxEnsure, p, &out)
}

// SandboxRun starts a run in the brain's sandbox on node id.
func (s *Service) SandboxRun(ctx context.Context, id string, p nodeproto.SandboxRunParams) (nodeproto.SandboxRunResult, error) {
	var out nodeproto.SandboxRunResult
	if err := p.Check(); err != nil {
		return out, fmt.Errorf("%w: %v", ErrCall, err)
	}
	return out, s.Call(ctx, id, nodeproto.MethodSandboxRun, p, &out)
}

// SandboxRead reads a run's output on node id, waiting up to p.WaitMS.
func (s *Service) SandboxRead(ctx context.Context, id string, p nodeproto.SandboxReadParams) (nodeproto.SandboxReadResult, error) {
	var out nodeproto.SandboxReadResult
	if err := p.Check(); err != nil {
		return out, fmt.Errorf("%w: %v", ErrCall, err)
	}
	return out, s.Call(ctx, id, nodeproto.MethodSandboxRead, p, &out)
}

// SandboxStop ends a run on node id inside its VM.
func (s *Service) SandboxStop(ctx context.Context, id string, p nodeproto.SandboxStopParams) error {
	if err := p.Check(); err != nil {
		return fmt.Errorf("%w: %v", ErrCall, err)
	}
	return s.Call(ctx, id, nodeproto.MethodSandboxStop, p, nil)
}

// SandboxRemove removes the brain's sandbox on node id.
func (s *Service) SandboxRemove(ctx context.Context, id, brain string) error {
	p := nodeproto.SandboxRemoveParams{Brain: brain}
	if err := p.Check(); err != nil {
		return fmt.Errorf("%w: %v", ErrCall, err)
	}
	return s.Call(ctx, id, nodeproto.MethodSandboxRemove, p, nil)
}
