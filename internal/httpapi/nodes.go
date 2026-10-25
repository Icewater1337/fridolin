package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
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
	NewJoinCodeFrom(origin string) (string, time.Time, error)
	Call(ctx context.Context, id, method string, params, out any) error
	StartUpdate(id string, o nodes.UpdateOptions) (string, error)
	SetUpdateAuto(id string, auto bool) (*nodes.Node, error)
	CoreSecretsTransfer(ctx context.Context, src, dst string, o nodes.CoreTransferOptions) (nodes.CoreTransferReport, error)
	MoveCheck(ctx context.Context, src, dst string) (nodes.MoveReport, error)
	StartMove(ctx context.Context, src, dst string, o nodes.MoveOptions) (nodes.MoveRecord, error)
	Move(id string) (nodes.MoveRecord, error)
}

// CoreSecretsTransferRequest is POST /api/nodes/core-secrets-transfer:
// the host secrets from one core host to another (#187), a dry run
// unless Run.
type CoreSecretsTransferRequest struct {
	Source  string `json:"source"`
	Target  string `json:"target"`
	Run     bool   `json:"run"`
	Replace bool   `json:"replace"`
}

// CoreSecretsTransferResponse is its answer: the report, and the error
// that stopped a transfer that was to run.
type CoreSecretsTransferResponse struct {
	nodes.CoreTransferReport
	Error string `json:"error,omitempty"`
}

// CoreSecretsTransfer answers POST /api/nodes/core-secrets-transfer. A
// transfer that cannot run, or fails, answers 409 with the report.
func (a *NodesAPI) CoreSecretsTransfer(w http.ResponseWriter, r *http.Request) {
	var req CoreSecretsTransferRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Source == "" || req.Target == "" {
		WriteErr(w, http.StatusBadRequest, "source and target are the ids of two nodes")
		return
	}
	rep, err := a.Nodes().CoreSecretsTransfer(r.Context(), req.Source, req.Target, nodes.CoreTransferOptions{Run: req.Run, Replace: req.Replace})
	switch {
	case errors.Is(err, nodes.ErrUnknownNode), errors.Is(err, nodes.ErrInvalidInput):
		writeNodeErr(w, err)
	case err != nil:
		WriteJSON(w, http.StatusConflict, CoreSecretsTransferResponse{CoreTransferReport: rep, Error: err.Error()})
	default:
		WriteJSON(w, http.StatusOK, CoreSecretsTransferResponse{CoreTransferReport: rep})
	}
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
	// Version is the core's version, which nodes are updated to.
	Version string
	// Box is where a node in a box comes from.
	Box BoxSource
}

// NodesResponse is GET /api/nodes.
type NodesResponse struct {
	Enabled bool   `json:"enabled"`
	Listen  string `json:"listen"`
	// Version is the core's: a node's update_to names it.
	Version string       `json:"version"`
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
	// BoxCommand and BoxCommandPowerShell install a node in a box
	// instead (docs/NODES.md, "Node in a box"); empty when the core does
	// not know where its box script is published (a local build).
	BoxCommand           string `json:"box_command,omitempty"`
	BoxCommandPowerShell string `json:"box_command_powershell,omitempty"`
}

// BoxSource is where a node in a box of the core's version comes from:
// the published box.sh and box.ps1 (Source, a URL their container/
// folder is under, at the core's commit) and the registry of the images
// (Registry, ghcr.io/<owner>). The core's image carries both
// (FRIDOLIN_BOX_SOURCE, FRIDOLIN_BOX_REGISTRY); either empty is none.
type BoxSource struct {
	Source, Registry, Version string
}

// BoxSourceFromEnv is the BoxSource of the core's image, for version.
func BoxSourceFromEnv(getenv func(string) string, version string) BoxSource {
	return BoxSource{Source: strings.TrimRight(strings.TrimSpace(getenv("FRIDOLIN_BOX_SOURCE")), "/"), Registry: strings.TrimSpace(getenv("FRIDOLIN_BOX_REGISTRY")), Version: version}
}

// BoxCommands are the lines that install a node in a box for code: for
// sh, and for PowerShell; empty when src is not known.
func BoxCommands(code, core string, src BoxSource) (sh, powershell string) {
	if src.Source == "" || src.Registry == "" || src.Version == "" {
		return "", ""
	}
	sh = "curl -fsSL " + src.Source + "/container/box.sh | sh -s -- --registry " + src.Registry +
		" --version " + src.Version + " --core " + core + " --code " + code
	powershell = "& ([scriptblock]::Create((irm " + src.Source + "/container/box.ps1))) -Registry " + src.Registry +
		" -Version " + src.Version + " -Core " + core + " -Code " + code
	return sh, powershell
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
	WriteJSONIfChanged(w, r, NodesResponse{Enabled: a.Enabled, Listen: a.Listen(), Version: a.Version, Nodes: views})
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
	code, exp, err := a.Nodes().NewJoinCodeFrom(nodes.JoinedFromDashboard)
	if err != nil {
		WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	core := a.CoreAddress()
	sh, ps := JoinCommands(code, core)
	bsh, bps := BoxCommands(code, core, a.Box)
	WriteJSON(w, http.StatusOK, JoinCodeResponse{Code: code, ExpiresAt: exp, Core: core, Command: sh, CommandPowerShell: ps, BoxCommand: bsh, BoxCommandPowerShell: bps})
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
	case errors.Is(err, nodes.ErrUpdate):
		WriteErr(w, http.StatusConflict, err.Error())
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

// NodeUpdateResponse is POST /api/nodes/{id}/update: the update runs in
// the background, and GET /api/nodes shows how it goes (the node's
// update).
type NodeUpdateResponse struct {
	ID string `json:"id"`
	To string `json:"to"`
}

// Update answers POST /api/nodes/{id}/update: update the node's fridolin
// to the core's version. force updates a busy node.
func (a *NodesAPI) Update(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Force bool `json:"force"`
	}
	if err := DecodeOptionalJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := r.PathValue("id")
	to, err := a.Nodes().StartUpdate(id, nodes.UpdateOptions{Force: req.Force})
	if err != nil {
		writeNodeErr(w, err)
		return
	}
	WriteJSON(w, http.StatusAccepted, NodeUpdateResponse{ID: id, To: to})
}

// UpdatePolicy answers PUT /api/nodes/{id}/update-policy: {"auto": bool}.
func (a *NodesAPI) UpdatePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Auto *bool `json:"auto"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Auto == nil {
		WriteErr(w, http.StatusBadRequest, "auto is missing: true or false")
		return
	}
	n, err := a.Nodes().SetUpdateAuto(r.PathValue("id"), *req.Auto)
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
		WriteErr(w, http.StatusBadRequest, "method must be one of exec, fs.list, fs.read, fs.write, fs.roots, fs.browse, host.info, notify")
		return
	}
	// A backup repository's files are restic's, written by the core's
	// backups only (vault targets), never by hand.
	if slices.Contains(nodeproto.BackupRepoMethods, req.Method) {
		WriteErr(w, http.StatusBadRequest, req.Method+" is the core's backups' own; the Vault page's targets use it")
		return
	}
	// An update goes through POST /api/nodes/{id}/update, which signs it.
	if slices.Contains(nodeproto.UpdateMethods, req.Method) {
		WriteErr(w, http.StatusBadRequest, req.Method+" is the core's updates' own; use POST /api/nodes/{id}/update")
		return
	}
	// A transfer's three steps go through POST
	// /api/nodes/core-secrets-transfer, which checks both sides first.
	if slices.Contains([]string{nodeproto.MethodCoreSecretsOffer, nodeproto.MethodCoreSecretsSend, nodeproto.MethodCoreSecretsReceive}, req.Method) {
		WriteErr(w, http.StatusBadRequest, req.Method+" is a host-secrets transfer's own; use POST /api/nodes/core-secrets-transfer (fridolin nodes core-secrets-transfer)")
		return
	}
	// A move's steps go through POST /api/nodes/move, which checks both
	// sides first and signs the plan.
	if slices.Contains(nodeproto.CoreMoveWrites, req.Method) {
		WriteErr(w, http.StatusBadRequest, req.Method+" is a core move's own; use POST /api/nodes/move (fridolin nodes move)")
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
