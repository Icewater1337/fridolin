package app

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// dashboardCall serves the dashboard's node calls of the workstation
// family (POST /api/nodes/{id}/call), so that its terminals are the
// core's like the core brain's: numbered, audited command line by command
// line, stopped by a lockdown. Agent sessions are the core brain's to
// start, since their news goes to a chat; the dashboard may read and
// stop them. It reports false for a method it does not serve.
func (w *workRuntime) dashboardCall(ctx context.Context, node, method string, params, out any) (bool, error) {
	if !slices.Contains(nodeproto.TerminalMethods, method) && method != nodeproto.MethodAgentStart {
		return false, nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return true, err
	}
	decode := func(v any) error {
		if err := json.Unmarshal(raw, v); err != nil {
			return nodeproto.Errorf(nodeproto.CodeBadRequest, "%s params: %v", method, err)
		}
		return nil
	}
	var res any
	switch method {
	case nodeproto.MethodAgentStart:
		return true, nodeproto.Errorf(nodeproto.CodeDenied, "agent sessions are started by the core brain, whose chat hears their questions; the dashboard can follow and stop them")
	case nodeproto.MethodTerminalOpen:
		var p nodeproto.TerminalOpenParams
		if err := decode(&p); err != nil {
			return true, err
		}
		if w.b.locked() {
			return true, errLockedNode
		}
		_, info, err := w.openTerminal(ctx, "dashboard", "", node, p.Folder, p.Cols, p.Rows)
		if err != nil {
			if _, ok := err.(*nodeproto.Error); !ok {
				err = nodeproto.Errorf(nodeproto.CodeDenied, "%v", err)
			}
			return true, err
		}
		res = info
	case nodeproto.MethodTerminalList:
		res, err = w.b.nodes.TerminalList(ctx, node)
	default:
		var ref struct {
			ID string `json:"id"`
		}
		if err := decode(&ref); err != nil {
			return true, err
		}
		t := w.termByNode(node, ref.ID)
		if t == nil {
			return true, nodeproto.Errorf(nodeproto.CodeNotFound, "terminal %s on %s was not opened through this core", ref.ID, node)
		}
		switch method {
		case nodeproto.MethodTerminalSend:
			var p nodeproto.TerminalSendParams
			if err := decode(&p); err != nil {
				return true, err
			}
			res, err = w.send(ctx, t, "dashboard", p.Data)
		case nodeproto.MethodTerminalRead:
			var p nodeproto.TerminalReadParams
			if err := decode(&p); err != nil {
				return true, err
			}
			var o nodeproto.TerminalOutput
			o, err = w.b.nodes.TerminalRead(ctx, node, p)
			if err == nil && o.Closed {
				w.ended(t, "dashboard", "exited")
			}
			res = o
		case nodeproto.MethodTerminalResize:
			var p nodeproto.TerminalResizeParams
			if err := decode(&p); err != nil {
				return true, err
			}
			err = w.b.nodes.Call(ctx, node, method, p, &res)
		case nodeproto.MethodTerminalClose:
			res, err = w.closeTerm(ctx, t, "dashboard", "closed")
		}
	}
	if err != nil {
		return true, err
	}
	data, err := json.Marshal(res)
	if err != nil {
		return true, err
	}
	return true, json.Unmarshal(data, out)
}

// termByNode is the record of a node's terminal.
func (w *workRuntime) termByNode(node, id string) *workTerm {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, t := range w.terms {
		if t.Node == node && t.ID == id {
			return t
		}
	}
	return nil
}
