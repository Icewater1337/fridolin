package nodeagent

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// staleCore is the source's CoreRunner once the target's lease went
// stale: Status says what the test sets, and taking the lease back fails
// with promoteErr.
type staleCore struct {
	CoreRunner // the other steps, which these tests never run
	status     nodeproto.CoreRuntime
	promoteErr error
}

func (f *staleCore) Status(context.Context) (nodeproto.CoreRuntime, error) { return f.status, nil }
func (f *staleCore) Import(context.Context) error                          { return nil }
func (f *staleCore) Promote(context.Context) error                         { return f.promoteErr }
func (f *staleCore) Start(context.Context) error                           { return nil }

// TestSourceWatchStaleLease: once the target stopped renewing the lease,
// the source logs the stale step however the move ends, so the core can
// tell that the target's core may still run (#591).
func TestSourceWatchStaleLease(t *testing.T) {
	for _, c := range []struct {
		name       string
		latest     string // the move of the latest export, by the target
		promoteErr error
		phase      string
	}{
		{"no export of this move", "m-20261024-0800-0001", nil, nodeproto.MoveFailed},
		{"rolled back on its export", testMove, nil, nodeproto.MoveRolledBack},
		{"the rollback fails", testMove, errors.New("the lease is held"), nodeproto.MoveFailed},
	} {
		a, _ := coreHostAgent(t, "mini")
		core := &staleCore{promoteErr: c.promoteErr, status: nodeproto.CoreRuntime{Device: thisDevice,
			Lease:  &nodeproto.CoreLease{Device: otherDevice, State: nodeproto.LeaseStale},
			Latest: &nodeproto.CoreExportInfo{Move: c.latest, Device: otherDevice}}}
		m := &moveRun{State: nodeproto.CoreMoveState{Move: testMove, Role: "source", Phase: nodeproto.MoveWatching},
			plan: nodeproto.MovePlan{Move: testMove, Target: "icebase", HandoffDeadline: time.Now()}}
		a.sourceWatch(context.Background(), core, m)
		if m.State.Phase != c.phase {
			t.Errorf("%s: ended %s: %s", c.name, m.State.Phase, m.State.Detail)
		}
		if !slices.ContainsFunc(m.State.Steps, func(s nodeproto.CoreMoveStep) bool { return s.Step == nodeproto.StepLeaseStale }) {
			t.Errorf("%s: no %q step: %+v", c.name, nodeproto.StepLeaseStale, m.State.Steps)
		}
	}
}
