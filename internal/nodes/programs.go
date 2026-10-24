package nodes

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// Programs the core adds to a node's exec allowlist (nodeproto's
// exec_programs.go): the core sends the node the programs its approved
// command agents run there, signed, and approves what the node kept,
// because the owner approved the agents that run it.

// ProgramsStatus is how the last list sent to a node went, for the Nodes
// page and the agent wizard.
type ProgramsStatus struct {
	At       time.Time                  `json:"at"`
	Sent     []nodeproto.ExecProgram    `json:"sent"`
	Accepted []nodeproto.ExecProgram    `json:"accepted"`
	Refused  []nodeproto.RefusedProgram `json:"refused"`
	// Error is why the list did not reach the node or was refused whole.
	Error string `json:"error,omitempty"`
}

// programsState is the service's side of the lists it sent.
type programsState struct {
	// send lets one list go out at a time; mu guards the rest.
	send    sync.Mutex
	mu      sync.Mutex
	lastSeq int64
	status  map[string]ProgramsStatus
}

// ErrNoPrograms is a node that accepts no programs from the core, or
// whose owner did not approve that it does.
var ErrNoPrograms = errors.New("the node accepts no programs from the core")

// nextSeq is a seq newer than every one sent before: the time, which a
// node keeps the last of, so that a core started again or moved goes on
// from where the last one stopped.
func (p *programsState) nextSeq(now time.Time) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	seq := max(now.UnixNano(), p.lastSeq+1)
	p.lastSeq = seq
	return seq
}

// SetPrograms sends node id the whole list of programs the core's
// approved command agents run on it, and approves what the node kept.
// What the core added before and the list leaves out goes.
func (s *Service) SetPrograms(ctx context.Context, id string, programs []nodeproto.ExecProgram) (ProgramsStatus, error) {
	s.progs.send.Lock()
	defer s.progs.send.Unlock()
	st := ProgramsStatus{At: time.Now().UTC(), Sent: programs, Accepted: []nodeproto.ExecProgram{}, Refused: []nodeproto.RefusedProgram{}}
	if st.Sent == nil {
		st.Sent = []nodeproto.ExecProgram{}
	}
	fail := func(err error) (ProgramsStatus, error) {
		st.Error = err.Error()
		s.recordPrograms(id, st)
		return st, err
	}
	n, err := s.reg.Get(id)
	if err != nil {
		return st, err
	}
	if !n.Effective().Allows(nodeproto.MethodExecPrograms) {
		return fail(ErrNoPrograms)
	}
	key, err := s.updateKey()
	if err != nil {
		return fail(fmt.Errorf("the core's key: %w", err))
	}
	before := coreRules(n.Approved)
	// Approved before the node declares them, so that its declaration
	// does not wait for the owner meanwhile; only what the node kept
	// stays approved below.
	if _, err := s.reg.SetCoreRules(id, rulesOf(programs)); err != nil {
		return fail(err)
	}
	p := nodeproto.ExecProgramsParams{Node: id, Seq: s.progs.nextSeq(time.Now()), Programs: st.Sent}
	p.Signature = nodeproto.SignExecPrograms(key, p.Node, p.Seq, p.Programs)
	var res nodeproto.ExecProgramsResult
	if err := s.Call(ctx, id, nodeproto.MethodExecPrograms, p, &res); err != nil {
		if _, rerr := s.reg.SetCoreRules(id, before); rerr != nil {
			s.logf("nodes: putting back the programs approved on %s: %v", id, rerr)
		}
		return fail(err)
	}
	// Only what was sent: a node cannot approve more for itself.
	for _, a := range res.Accepted {
		if slices.ContainsFunc(st.Sent, func(x nodeproto.ExecProgram) bool { return sameProgram(x, a) }) {
			st.Accepted = append(st.Accepted, a)
		}
	}
	st.Refused = append(st.Refused, res.Refused...)
	n, err = s.reg.SetCoreRules(id, rulesOf(st.Accepted))
	if err != nil {
		return fail(err)
	}
	s.recordPrograms(id, st)
	s.logf("nodes: %s (%s) allows %d programs from the core, refused %d", n.Name, n.ID, len(st.Accepted), len(st.Refused))
	s.pushState(n)
	return st, nil
}

func (s *Service) recordPrograms(id string, st ProgramsStatus) {
	s.progs.mu.Lock()
	defer s.progs.mu.Unlock()
	if s.progs.status == nil {
		s.progs.status = map[string]ProgramsStatus{}
	}
	s.progs.status[id] = st
}

// Programs is how the last list sent to node id went; false when none was
// sent since the core started.
func (s *Service) Programs(id string) (ProgramsStatus, bool) {
	s.progs.mu.Lock()
	defer s.progs.mu.Unlock()
	st, ok := s.progs.status[id]
	return st, ok
}

func sameProgram(a, b nodeproto.ExecProgram) bool {
	return a.Name == b.Name && slices.Equal(a.Args, b.Args)
}

func rulesOf(programs []nodeproto.ExecProgram) []nodeproto.ExecRule {
	out := make([]nodeproto.ExecRule, 0, len(programs))
	for _, p := range programs {
		out = append(out, p.Rule())
	}
	return out
}

// coreRules are the rules of c the core added.
func coreRules(c nodeproto.Capabilities) []nodeproto.ExecRule {
	if c.Exec == nil {
		return nil
	}
	var out []nodeproto.ExecRule
	for _, r := range c.Exec.Allow {
		if r.Core {
			out = append(out, r)
		}
	}
	return out
}

// SetCoreRules makes rules the approved rules of node id that the core
// added, in place of those before. The owner's approval of the node's
// commands stays as it is otherwise.
func (r *Registry) SetCoreRules(id string, rules []nodeproto.ExecRule) (*Node, error) {
	return r.change(id, func(n *Node, _ time.Time) error {
		if n.Device != nodeproto.DeviceApproved {
			return ErrNotApproved
		}
		e := n.Approved.Exec
		if e == nil {
			if len(rules) == 0 {
				return nil
			}
			return ErrNoPrograms
		}
		allow := slices.DeleteFunc(slices.Clone(e.Allow), func(x nodeproto.ExecRule) bool { return x.Core })
		for _, x := range rules {
			if len(allow) >= nodeproto.MaxExecRules {
				break
			}
			allow = append(allow, x)
		}
		c := *e
		c.Allow = allow
		n.Approved.Exec = &c
		return nil
	})
}
