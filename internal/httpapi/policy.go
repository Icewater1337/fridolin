package httpapi

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Icewater1337/fridolin/internal/apperr"
)

// ThreadRun is one run of a thread (GET /api/threads).
type ThreadRun struct {
	Run        string `json:"run"`
	Brain      string `json:"brain"`
	Chat       string `json:"chat"`
	Started    string `json:"started"`
	Skill      string `json:"skill"`
	Risk       string `json:"risk"`
	Detail     string `json:"detail"`
	Outcome    string `json:"outcome"`
	DurationMS int64  `json:"duration_ms"`
}

// ThreadView is a thread: the chat its runs serve, newest run first.
type ThreadView struct {
	Thread string      `json:"thread"`
	Last   string      `json:"last"`
	Count  int         `json:"count"`
	Brains []string    `json:"brains"`
	Runs   []ThreadRun `json:"runs"`
}

// DecisionRequest is the body of POST /api/approvals/{id}.
type DecisionRequest struct {
	Decision string `json:"decision"` // approve or deny
}

// LockdownRequest is the body of POST /api/policy/lockdown.
type LockdownRequest struct {
	On *bool `json:"on"`
}

// SkillApproveRequest is the body of POST /api/brains/{id}/skills/approve:
// the skill file and the hash of the policy reviewed.
type SkillApproveRequest struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

// SkillPolicyRequest is the body of PUT /api/brains/{id}/skills/policy.
type SkillPolicyRequest struct {
	Path     string   `json:"path"`
	Risk     string   `json:"risk"`
	Vault    []string `json:"vault"`
	Approve  []string `json:"approve"`
	Triggers []string `json:"triggers"`
}

// DevVoteRequest is the body of POST /api/dev/vote: a vote in a poll the
// dev chat shows, as WhatsApp would bring it.
type DevVoteRequest struct {
	Poll   string   `json:"poll"`
	Voter  string   `json:"voter"`
	Option []string `json:"options"`
}

// PolicyAPI serves the policy's views: threads, approvals, the audit log,
// the lockdown and the skills' policies.
type PolicyAPI struct {
	Status         func(context.Context) (any, error)
	Threads        func(ctx context.Context, limit int) (any, error)
	Audit          func(*http.Request) (any, error)
	Approvals      func(context.Context) (any, error)
	Decide         func(ctx context.Context, id string, approve bool) (any, error)
	Lockdown       func(ctx context.Context, on bool) (any, error)
	Skills         func(ctx context.Context, brain string) (any, error)
	ApproveSkill   func(ctx context.Context, brain string, req SkillApproveRequest) (any, error)
	SetSkillPolicy func(ctx context.Context, brain string, req SkillPolicyRequest) (any, error)
	// Fride lists the FrIDE threads with their news (GET /api/fride).
	Fride func(context.Context) (any, error)
}

// get answers a GET with what fn returns, with an ETag.
func (a *PolicyAPI) get(fn func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := fn(r)
		if err != nil {
			WriteErr(w, statusFor(apperr.KindOf(err)), err.Error())
			return
		}
		WriteJSONIfChanged(w, r, out)
	}
}

func writeResult(w http.ResponseWriter, out any, err error) {
	if err != nil {
		WriteErr(w, statusFor(apperr.KindOf(err)), err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// DecideHandler is POST /api/approvals/{id}: the owner decides in the
// dashboard, as a vote in the poll would.
func (a *PolicyAPI) DecideHandler(w http.ResponseWriter, r *http.Request) {
	var req DecisionRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Decision != "approve" && req.Decision != "deny" {
		WriteErr(w, http.StatusUnprocessableEntity, `decision must be "approve" or "deny"`)
		return
	}
	out, err := a.Decide(r.Context(), r.PathValue("id"), req.Decision == "approve")
	writeResult(w, out, err)
}

// LockdownHandler is POST /api/policy/lockdown.
func (a *PolicyAPI) LockdownHandler(w http.ResponseWriter, r *http.Request) {
	var req LockdownRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.On == nil {
		WriteErr(w, http.StatusUnprocessableEntity, `"on" is required: true locks down, false unlocks`)
		return
	}
	out, err := a.Lockdown(r.Context(), *req.On)
	writeResult(w, out, err)
}

// ApproveSkillHandler is POST /api/brains/{id}/skills/approve.
func (a *PolicyAPI) ApproveSkillHandler(w http.ResponseWriter, r *http.Request) {
	var req SkillApproveRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := a.ApproveSkill(r.Context(), r.PathValue("id"), req)
	writeResult(w, out, err)
}

// SkillPolicyHandler is PUT /api/brains/{id}/skills/policy.
func (a *PolicyAPI) SkillPolicyHandler(w http.ResponseWriter, r *http.Request) {
	var req SkillPolicyRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := a.SetSkillPolicy(r.Context(), r.PathValue("id"), req)
	writeResult(w, out, err)
}

// queryInt is the query parameter name as a number, 0 when not one.
func queryInt(r *http.Request, name string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(name))
	return n
}
