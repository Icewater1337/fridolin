package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodes"
)

// The core's move between two core hosts (#187; docs/CORE.md, "The core
// moves itself"): POST /api/nodes/move-check is the pre-flight, read
// only; POST /api/nodes/move runs the move in the background; GET
// /api/nodes/moves/{id} follows it, also from the core that runs after
// it, which the record reaches in the final export.

// MoveRequest is the body of both POSTs. Source and Target name each
// node by its id or its name. The timings are for a test or a slow
// machine; the defaults are 15 minutes and 10.
type MoveRequest struct {
	Source             string `json:"source"`
	Target             string `json:"target"`
	DeadlineSeconds    int    `json:"deadline_seconds,omitempty"`
	ReadyWithinSeconds int    `json:"ready_within_seconds,omitempty"`
}

// MoveResponse is POST /api/nodes/move's answer: the record of the move
// that runs, or the report and the error that kept it from running.
type MoveResponse struct {
	nodes.MoveRecord
	Error string `json:"error,omitempty"`
}

func decodeMove(w http.ResponseWriter, r *http.Request) (MoveRequest, bool) {
	var req MoveRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return req, false
	}
	if req.Source == "" || req.Target == "" {
		WriteErr(w, http.StatusBadRequest, "source and target are two nodes, by id or name")
		return req, false
	}
	if req.DeadlineSeconds < 0 || req.ReadyWithinSeconds < 0 || req.DeadlineSeconds > 3600 || req.ReadyWithinSeconds > 3600 {
		WriteErr(w, http.StatusBadRequest, "deadline_seconds and ready_within_seconds are at most 3600")
		return req, false
	}
	return req, true
}

// moveEnds are the ids of the nodes a move's request names.
func (a *NodesAPI) moveEnds(w http.ResponseWriter, req MoveRequest) (src, dst string, ok bool) {
	if src, ok = a.nodeID(w, req.Source); !ok {
		return "", "", false
	}
	if dst, ok = a.nodeID(w, req.Target); !ok {
		return "", "", false
	}
	return src, dst, true
}

// MoveCheck answers POST /api/nodes/move-check.
func (a *NodesAPI) MoveCheck(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeMove(w, r)
	if !ok {
		return
	}
	src, dst, ok := a.moveEnds(w, req)
	if !ok {
		return
	}
	rep, err := a.Nodes().MoveCheck(r.Context(), src, dst)
	if err != nil {
		writeNodeErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, rep)
}

// StartMove answers POST /api/nodes/move: 202 with the record of the
// move that now runs, 409 with the report when it cannot.
func (a *NodesAPI) StartMove(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeMove(w, r)
	if !ok {
		return
	}
	src, dst, ok := a.moveEnds(w, req)
	if !ok {
		return
	}
	rec, err := a.Nodes().StartMove(r.Context(), src, dst, nodes.MoveOptions{
		HandoffDeadline: time.Duration(req.DeadlineSeconds) * time.Second, ReadyWithin: time.Duration(req.ReadyWithinSeconds) * time.Second})
	switch {
	case errors.Is(err, nodes.ErrUnknownNode), errors.Is(err, nodes.ErrInvalidInput):
		writeNodeErr(w, err)
	case err != nil:
		WriteJSON(w, http.StatusConflict, MoveResponse{MoveRecord: rec, Error: err.Error()})
	default:
		WriteJSON(w, http.StatusAccepted, MoveResponse{MoveRecord: rec})
	}
}

// Move answers GET /api/nodes/moves/{id}.
func (a *NodesAPI) Move(w http.ResponseWriter, r *http.Request) {
	rec, err := a.Nodes().Move(r.PathValue("id"))
	if err != nil {
		writeNodeErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, rec)
}
