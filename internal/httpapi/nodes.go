package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
)

// NodesService is what the Nodes page and `fridolin nodes` ask of the
// core's fleet (internal/nodes).
type NodesService interface {
	Views() ([]nodes.View, error)
	ApproveDevice(id string) (*nodes.Node, error)
	ApproveCommands(id string) (*nodes.Node, error)
	Revoke(id string) (*nodes.Node, error)
	SetTags(id string, tags []string) (*nodes.Node, error)
	NewJoinCode() (string, time.Time, error)
	Call(ctx context.Context, id, method string, params, out any) error
}

// NodesAPI serves /api/nodes.
type NodesAPI struct {
	Nodes func() NodesService
	// Enabled is nodes.enabled as the bridge started, and Listen the node
	// port it serves ("" while it is not open).
	Enabled bool
	Listen  func() string
	// CoreAddress is the core's node address a new machine joins
	// (host:port).
	CoreAddress func() string
}

// NodesResponse is GET /api/nodes.
type NodesResponse struct {
	Enabled bool         `json:"enabled"`
	Listen  string       `json:"listen"`
	Nodes   []nodes.View `json:"nodes"`
}

// JoinCodeResponse is POST /api/nodes/join-code.
type JoinCodeResponse struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
	// Core is the core's node address, host:port.
	Core string `json:"core"`
	// Command is the line to paste on a macOS or Linux machine,
	// CommandPowerShell the one for PowerShell on Windows, where the
	// runbook installs fridolin.exe in Program Files (AGENTS.md).
	Command           string `json:"command"`
	CommandPowerShell string `json:"command_powershell"`
}

// JoinCommands are the lines to paste on a new machine for code: for sh,
// and for PowerShell.
func JoinCommands(code, core string) (sh, powershell string) {
	args := " node join " + code + " --core " + core
	return "fridolin" + args, `& "$env:ProgramFiles\fridolin\fridolin.exe"` + args
}

// NodeResponse is a node after a change.
type NodeResponse struct {
	Node *nodes.Node `json:"node"`
}

// List answers GET /api/nodes.
func (a *NodesAPI) List(w http.ResponseWriter, r *http.Request) {
	views, err := a.Nodes().Views()
	if err != nil && views == nil {
		WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSONIfChanged(w, r, NodesResponse{Enabled: a.Enabled, Listen: a.Listen(), Nodes: views})
}

// JoinCode answers POST /api/nodes/join-code.
func (a *NodesAPI) JoinCode(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if err := DecodeOptionalJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !a.Enabled {
		WriteErr(w, http.StatusConflict, "nodes are off: turn on nodes.enabled and restart the bridge, so nodes can connect")
		return
	}
	code, exp, err := a.Nodes().NewJoinCode()
	if err != nil {
		WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	core := a.CoreAddress()
	sh, ps := JoinCommands(code, core)
	WriteJSON(w, http.StatusOK, JoinCodeResponse{Code: code, ExpiresAt: exp, Core: core, Command: sh, CommandPowerShell: ps})
}

func (a *NodesAPI) change(w http.ResponseWriter, r *http.Request, fn func(string) (*nodes.Node, error)) {
	var req struct{}
	if err := DecodeOptionalJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	n, err := fn(r.PathValue("id"))
	if err != nil {
		writeNodeErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, NodeResponse{Node: n})
}

func writeNodeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, nodes.ErrUnknownNode):
		WriteErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, nodes.ErrRevoked), errors.Is(err, nodes.ErrNotApproved):
		WriteErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, nodes.ErrInvalidInput):
		WriteErr(w, http.StatusUnprocessableEntity, err.Error())
	default:
		WriteErr(w, http.StatusInternalServerError, err.Error())
	}
}

// ApproveDevice answers POST /api/nodes/{id}/approve: the first stage.
func (a *NodesAPI) ApproveDevice(w http.ResponseWriter, r *http.Request) {
	a.change(w, r, a.Nodes().ApproveDevice)
}

// ApproveCommands answers POST /api/nodes/{id}/approve-commands: what the
// node declares now, the second stage.
func (a *NodesAPI) ApproveCommands(w http.ResponseWriter, r *http.Request) {
	a.change(w, r, a.Nodes().ApproveCommands)
}

// Revoke answers POST /api/nodes/{id}/revoke.
func (a *NodesAPI) Revoke(w http.ResponseWriter, r *http.Request) {
	a.change(w, r, a.Nodes().Revoke)
}

// Tags answers PUT /api/nodes/{id}/tags.
func (a *NodesAPI) Tags(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tags []string `json:"tags"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	n, err := a.Nodes().SetTags(r.PathValue("id"), req.Tags)
	if err != nil {
		writeNodeErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, NodeResponse{Node: n})
}

// NodeCallResponse is POST /api/nodes/{id}/call: the method's result as
// the node sent it.
type NodeCallResponse struct {
	Result json.RawMessage `json:"result"`
}

// Call answers POST /api/nodes/{id}/call: one capability call, checked
// against the approvals on the core and again on the node.
func (a *NodesAPI) Call(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !slices.Contains(nodeproto.Methods, req.Method) {
		WriteErr(w, http.StatusBadRequest, "method must be one of exec, fs.list, fs.read, fs.write, host.info, notify")
		return
	}
	params := req.Params
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	var out json.RawMessage
	err := a.Nodes().Call(r.Context(), r.PathValue("id"), req.Method, params, &out)
	if pe, ok := errors.AsType[*nodeproto.Error](err); ok {
		status := http.StatusBadGateway
		switch pe.Code {
		case nodeproto.CodeDenied, nodeproto.CodeOutsideRoot:
			status = http.StatusForbidden
		case nodeproto.CodeNotFound:
			status = http.StatusNotFound
		case nodeproto.CodeBadRequest, nodeproto.CodeUnsupported:
			status = http.StatusBadRequest
		case nodeproto.CodeUnavailable:
			status = http.StatusServiceUnavailable
		case nodeproto.CodeTimeout:
			status = http.StatusGatewayTimeout
		}
		WriteJSON(w, status, NodeCallError{Error: pe.Message, Code: pe.Code})
		return
	}
	if err != nil {
		writeNodeErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, NodeCallResponse{Result: out})
}

// NodeCallError is a call the core or the node refused, with the
// protocol's error code.
type NodeCallError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}
