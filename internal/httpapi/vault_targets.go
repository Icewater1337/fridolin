package httpapi

import (
	"context"
	"net/http"

	"github.com/Icewater1337/fridolin/internal/config"
)

// VaultTargetsService is the vault's node targets as the Vault page and
// fridolin vault use them (docs/VAULT.md, "Node targets").
type VaultTargetsService interface {
	// Add adds a target and approves its folder on the node; it answers
	// the target as the status shows it.
	Add(ctx context.Context, t config.VaultTarget) (any, error)
	Change(name string, t config.VaultTarget) error
	Remove(name string) error
	Snapshot(name, kind string) error
	Check(name, readDataSubset string) error
	RestoreDrill(name string) error
	AllowPrune(name string) error
}

// VaultTargetsAPI serves /api/vault/targets.
type VaultTargetsAPI struct {
	Targets func() VaultTargetsService
}

// VaultTargetResponse is POST /api/vault/targets's answer.
type VaultTargetResponse struct {
	Target any `json:"target"`
}

// Add answers POST /api/vault/targets.
func (a *VaultTargetsAPI) Add(w http.ResponseWriter, r *http.Request) {
	var req config.VaultTarget
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	v, err := a.Targets().Add(r.Context(), req)
	if err != nil {
		writeKindErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, VaultTargetResponse{Target: v})
}

// Change answers PUT /api/vault/targets/{name}.
func (a *VaultTargetsAPI) Change(w http.ResponseWriter, r *http.Request) {
	var req config.VaultTarget
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.Targets().Change(r.PathValue("name"), req); err != nil {
		writeKindErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, OperationResponse{Status: "saved"})
}

// Remove answers DELETE /api/vault/targets/{name}.
func (a *VaultTargetsAPI) Remove(w http.ResponseWriter, r *http.Request) {
	if err := a.Targets().Remove(r.PathValue("name")); err != nil {
		writeKindErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, OperationResponse{Status: "removed"})
}

func (a *VaultTargetsAPI) job(w http.ResponseWriter, err error) {
	if err != nil {
		writeKindErr(w, err)
		return
	}
	WriteJSON(w, http.StatusAccepted, OperationResponse{Status: "started"})
}

// Snapshot answers POST /api/vault/targets/{name}/snapshot.
func (a *VaultTargetsAPI) Snapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind string `json:"kind"`
	}
	if err := DecodeOptionalJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.job(w, a.Targets().Snapshot(r.PathValue("name"), req.Kind))
}

// Check answers POST /api/vault/targets/{name}/check.
func (a *VaultTargetsAPI) Check(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ReadDataSubset string `json:"read_data_subset"`
	}
	if err := DecodeOptionalJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.job(w, a.Targets().Check(r.PathValue("name"), req.ReadDataSubset))
}

// RestoreDrill answers POST /api/vault/targets/{name}/restore-drill.
func (a *VaultTargetsAPI) RestoreDrill(w http.ResponseWriter, r *http.Request) {
	if err := DecodeOptionalJSON(r, &struct{}{}); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.job(w, a.Targets().RestoreDrill(r.PathValue("name")))
}

// AllowPrune answers POST /api/vault/targets/{name}/prune/allow.
func (a *VaultTargetsAPI) AllowPrune(w http.ResponseWriter, r *http.Request) {
	if err := DecodeOptionalJSON(r, &struct{}{}); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.Targets().AllowPrune(r.PathValue("name")); err != nil {
		writeKindErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, OperationResponse{Status: "allowed"})
}
