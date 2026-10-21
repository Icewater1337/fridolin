package app

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
)

// openTerminal opens a terminal for by (a brain, or "dashboard"); node
// and folder as place takes them, or for the dashboard a node's id and an
// absolute folder.
func (w *workRuntime) openTerminal(ctx context.Context, by, run, want, folder string, cols, rows int) (*workTerm, nodeproto.TerminalInfo, error) {
	v, folder, err := w.place("terminal", want, folder)
	if err != nil {
		return nil, nodeproto.TerminalInfo{}, err
	}
	info, err := w.b.nodes.TerminalOpen(ctx, v.ID, nodeproto.TerminalOpenParams{Folder: folder, Cols: cols, Rows: rows})
	detail := fmt.Sprintf("terminal open on %s in %s", v.Name, folder)
	if err != nil {
		w.auditSession(audit.Entry{By: by, Run: run, Detail: detail, Outcome: "failed: " + err.Error()})
		return nil, info, err
	}
	w.mu.Lock()
	w.next++
	t := &workTerm{N: w.next, Node: v.ID, ID: info.ID, Folder: info.Folder, By: by, Started: time.Now()}
	w.terms[t.N] = t
	w.mu.Unlock()
	w.auditSession(audit.Entry{By: by, Run: run, ID: fmt.Sprintf("terminal#%d", t.N), Detail: detail + ", " + info.Shell + ", sandbox " + info.Sandbox, Outcome: "started"})
	return t, info, nil
}

// term is the record of terminal #n.
func (w *workRuntime) term(n int64) (*workTerm, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	t := w.terms[n]
	if t == nil {
		return nil, fmt.Errorf("there is no terminal #%d", n)
	}
	return t, nil
}

// send types data into terminal t, auditing each command line it ends.
func (w *workRuntime) send(ctx context.Context, t *workTerm, by, data string) (nodeproto.TerminalInfo, error) {
	if w.b.locked() {
		return nodeproto.TerminalInfo{}, errLockedNode
	}
	w.mu.Lock()
	lines := t.feed(data)
	w.mu.Unlock()
	for _, l := range lines {
		w.b.audit.Append(audit.Entry{Kind: audit.NodeCommand, By: by, ID: fmt.Sprintf("terminal#%d", t.N), Detail: w.nodeName(t.Node) + ": " + l})
	}
	return w.b.nodes.TerminalSend(ctx, t.Node, nodeproto.TerminalSendParams{ID: t.ID, Data: data})
}

// feed takes typed bytes and returns the command lines they end: Enter
// ends one, a backspace takes back a character, escape sequences (arrow
// keys) and other control characters are left out. What a shell's own
// history or completion fills in is not seen; the audit line is what was
// typed.
func (t *workTerm) feed(data string) []string {
	var out []string
	for _, r := range data {
		switch {
		case t.escape:
			// CSI and SS3 sequences end with a letter or ~.
			if r >= '@' && r <= '~' && r != '[' && r != 'O' {
				t.escape = false
			}
		case r == 0x1b:
			t.escape = true
		case r == '\r' || r == '\n':
			if len(t.line) > 0 {
				out = append(out, audit.Scrub(string(t.line)))
			}
			t.line = t.line[:0]
		case r == 0x7f || r == 0x08:
			if len(t.line) > 0 {
				t.line = t.line[:len(t.line)-1]
			}
		case r == 0x03 || r == 0x15: // ^C, ^U
			t.line = t.line[:0]
		case r < 0x20:
		default:
			if len(t.line) < 4096 {
				t.line = append(t.line, r)
			}
		}
	}
	return out
}

// closeTerm ends terminal t.
func (w *workRuntime) closeTerm(ctx context.Context, t *workTerm, by, why string) (nodeproto.TerminalInfo, error) {
	info, err := w.b.nodes.TerminalClose(ctx, t.Node, t.ID)
	w.ended(t, by, why)
	return info, err
}

// ended writes terminal t's last audit line, once.
func (w *workRuntime) ended(t *workTerm, by, why string) {
	w.mu.Lock()
	was := t.Ended
	t.Ended = true
	w.mu.Unlock()
	if !was {
		w.auditSession(audit.Entry{By: by, ID: fmt.Sprintf("terminal#%d", t.N), Detail: fmt.Sprintf("terminal end on %s in %s", w.nodeName(t.Node), t.Folder), Outcome: why, DurationMS: time.Since(t.Started).Milliseconds()})
	}
}

// stopAgent stops Agent a.
func (w *workRuntime) stopAgent(ctx context.Context, a *workAgent, why string) (nodeproto.AgentSession, error) {
	s, err := w.b.nodes.AgentStop(ctx, a.Node, a.ID)
	if err == nil {
		w.relayEnd(a, s, why)
	}
	return s, err
}

// relayEnd writes a stopped session's end once, as its news would.
func (w *workRuntime) relayEnd(a *workAgent, s nodeproto.AgentSession, why string) {
	w.mu.Lock()
	was := a.Ended
	a.Ended = true
	w.mu.Unlock()
	if !was {
		w.auditSession(audit.Entry{Brain: a.Brain, Run: a.Run, Chat: a.Chat, Thread: a.Chat, ID: fmt.Sprintf("agent#%d", a.N),
			Detail: fmt.Sprintf("agent end on %s in %s (%s), %d files changed", w.nodeName(a.Node), a.Folder, why, len(s.Changed)), Outcome: s.State,
			DurationMS: time.Since(a.Started).Milliseconds()})
	}
}

// stopAll stops every session the core knows of that runs: a lockdown.
// It returns how many it stopped.
func (w *workRuntime) stopAll(why string) int {
	w.mu.Lock()
	var agents []*workAgent
	var terms []*workTerm
	for _, a := range w.agents {
		if !a.Ended {
			agents = append(agents, a)
		}
	}
	for _, t := range w.terms {
		if !t.Ended {
			terms = append(terms, t)
		}
	}
	w.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	n := 0
	for _, a := range agents {
		if _, err := w.stopAgent(ctx, a, why); err != nil {
			log.Printf("workstation: stopping Agent #%d: %v", a.N, err)
			continue
		}
		n++
	}
	for _, t := range terms {
		if _, err := w.closeTerm(ctx, t, "policy", why); err != nil {
			log.Printf("workstation: closing terminal #%d: %v", t.N, err)
			continue
		}
		n++
	}
	return n
}

// lockdown stops what runs on nodes as the bridge locks down, and tells
// the owner when it stopped anything.
func (w *workRuntime) lockdown() {
	if n := w.stopAll("lockdown"); n > 0 {
		if target := ownerChat(w.b.cfgs.Get()); target != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := w.b.wa.SendChunked(ctx, target, outboundmsg.NodeSessionsStoppedNotice(n)); err != nil {
				log.Printf("workstation: telling the owner about the lockdown: %v", err)
			}
		}
	}
}

// orphan is a session running on a node that the core has no record of.
type orphan struct {
	Node, NodeName, Kind, ID, Folder string
	Started                          time.Time
}

// orphans lists, on every online node that offers agent or terminal, the
// sessions that run and that the core does not know (a restart forgot
// them, or the core never started them).
func (w *workRuntime) orphans(ctx context.Context) ([]orphan, []string) {
	views, err := w.b.nodes.Views()
	if err != nil {
		return nil, []string{"reading the node registry: " + err.Error()}
	}
	var out []orphan
	var errs []string
	for _, v := range views {
		if !v.Online || v.Device != nodeproto.DeviceApproved {
			continue
		}
		if v.Effective.Agent != nil {
			list, err := w.b.nodes.AgentList(ctx, v.ID)
			if err != nil {
				errs = append(errs, fmt.Sprintf("listing the agent sessions of %s: %v", v.Name, err))
			}
			for _, s := range list.Sessions {
				if !s.Finished() && w.agentByNode(v.ID, s.ID) == nil {
					out = append(out, orphan{Node: v.ID, NodeName: v.Name, Kind: "agent", ID: s.ID, Folder: s.Folder, Started: s.Started})
				}
			}
		}
		if v.Effective.Terminal != nil {
			list, err := w.b.nodes.TerminalList(ctx, v.ID)
			if err != nil {
				errs = append(errs, fmt.Sprintf("listing the terminals of %s: %v", v.Name, err))
			}
			for _, t := range list.Terminals {
				if !t.Closed && !w.knowsTerm(v.ID, t.ID) {
					out = append(out, orphan{Node: v.ID, NodeName: v.Name, Kind: "terminal", ID: t.ID, Folder: t.Folder, Started: t.Started})
				}
			}
		}
	}
	return out, errs
}

func (w *workRuntime) knowsTerm(node, id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, t := range w.terms {
		if t.Node == node && t.ID == id {
			return true
		}
	}
	return false
}

// stopOrphan stops an orphaned session on its node.
func (w *workRuntime) stopOrphan(ctx context.Context, o orphan) error {
	var err error
	if o.Kind == "agent" {
		_, err = w.b.nodes.AgentStop(ctx, o.Node, o.ID)
	} else {
		_, err = w.b.nodes.TerminalClose(ctx, o.Node, o.ID)
	}
	w.auditSession(audit.Entry{By: "housekeeping", ID: o.Kind + ":" + o.ID, Detail: fmt.Sprintf("%s end on %s in %s (orphaned)", o.Kind, o.NodeName, o.Folder), Outcome: outcomeOf(err)})
	return err
}

func outcomeOf(err error) string {
	if err != nil {
		return "failed: " + err.Error()
	}
	return "stopped"
}

// errLockedNode is a call a lockdown refuses.
var errLockedNode = &nodeproto.Error{Code: nodeproto.CodeDenied, Message: "the bridge is in lockdown: only reading goes ahead; unlock it in the dashboard or with !unlock"}
