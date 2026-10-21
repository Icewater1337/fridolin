package nodes

import (
	"context"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// The workstation family (docs/NODES.md, "Workstation"): agent sessions
// and terminals in granted folders on a node.

func init() {
	// Stopping a session lists what changed in its folder.
	slowCalls[nodeproto.MethodAgentStop] = 2 * time.Minute
	slowCalls[nodeproto.MethodTerminalClose] = 30 * time.Second
}

// AgentStart starts an agent session on node id. The folder is checked
// here against the approved roots before anything is sent, and on the
// node again with its symlinks resolved.
func (s *Service) AgentStart(ctx context.Context, id string, p nodeproto.AgentStartParams) (nodeproto.AgentSession, error) {
	var out nodeproto.AgentSession
	if err := s.checkWorkFolder(id, "agent", p.Folder); err != nil {
		return out, err
	}
	return out, s.Call(ctx, id, nodeproto.MethodAgentStart, p, &out)
}

// AgentStatus reads a session with its events after p.After.
func (s *Service) AgentStatus(ctx context.Context, id string, p nodeproto.AgentRef) (nodeproto.AgentSession, error) {
	var out nodeproto.AgentSession
	return out, s.Call(ctx, id, nodeproto.MethodAgentStatus, p, &out)
}

// AgentReply answers a session that waits.
func (s *Service) AgentReply(ctx context.Context, id string, p nodeproto.AgentReplyParams) (nodeproto.AgentSession, error) {
	var out nodeproto.AgentSession
	return out, s.Call(ctx, id, nodeproto.MethodAgentReply, p, &out)
}

// AgentStop stops a session.
func (s *Service) AgentStop(ctx context.Context, id, session string) (nodeproto.AgentSession, error) {
	var out nodeproto.AgentSession
	return out, s.Call(ctx, id, nodeproto.MethodAgentStop, nodeproto.AgentRef{ID: session}, &out)
}

// AgentList lists node id's sessions.
func (s *Service) AgentList(ctx context.Context, id string) (nodeproto.AgentList, error) {
	var out nodeproto.AgentList
	return out, s.Call(ctx, id, nodeproto.MethodAgentList, struct{}{}, &out)
}

// TerminalOpen opens a terminal on node id.
func (s *Service) TerminalOpen(ctx context.Context, id string, p nodeproto.TerminalOpenParams) (nodeproto.TerminalInfo, error) {
	var out nodeproto.TerminalInfo
	if err := s.checkWorkFolder(id, "terminal", p.Folder); err != nil {
		return out, err
	}
	return out, s.Call(ctx, id, nodeproto.MethodTerminalOpen, p, &out)
}

// TerminalSend types into a terminal.
func (s *Service) TerminalSend(ctx context.Context, id string, p nodeproto.TerminalSendParams) (nodeproto.TerminalInfo, error) {
	var out nodeproto.TerminalInfo
	return out, s.Call(ctx, id, nodeproto.MethodTerminalSend, p, &out)
}

// TerminalRead reads a terminal's output.
func (s *Service) TerminalRead(ctx context.Context, id string, p nodeproto.TerminalReadParams) (nodeproto.TerminalOutput, error) {
	var out nodeproto.TerminalOutput
	return out, s.Call(ctx, id, nodeproto.MethodTerminalRead, p, &out)
}

// TerminalClose ends a terminal.
func (s *Service) TerminalClose(ctx context.Context, id, term string) (nodeproto.TerminalInfo, error) {
	var out nodeproto.TerminalInfo
	return out, s.Call(ctx, id, nodeproto.MethodTerminalClose, nodeproto.TerminalRef{ID: term}, &out)
}

// TerminalList lists node id's terminals.
func (s *Service) TerminalList(ctx context.Context, id string) (nodeproto.TerminalList, error) {
	var out nodeproto.TerminalList
	return out, s.Call(ctx, id, nodeproto.MethodTerminalList, struct{}{}, &out)
}

// checkWorkFolder refuses a folder outside node id's approved roots of
// kind before anything is sent.
func (s *Service) checkWorkFolder(id, kind, folder string) error {
	n, err := s.reg.Get(id)
	if err != nil {
		return err
	}
	eff := n.Effective()
	w := eff.Terminal
	if kind == "agent" {
		w = eff.Agent
	}
	if w == nil {
		return nodeproto.Errorf(nodeproto.CodeDenied, "node %s: %s is not approved", id, kind)
	}
	if !w.AllowsFolder(folder) {
		return nodeproto.Errorf(nodeproto.CodeOutsideRoot, "node %s: %s is not under a folder granted for %s", id, folder, kind)
	}
	return nil
}
