package httpapi

import (
	"context"
	"net/http"

	"github.com/Icewater1337/fridolin/internal/apperr"
)

// VaultService is the vault as the dashboard and fridolin vault use it.
type VaultService interface {
	// Status is the vault's state, as GET /api/vault/status answers it.
	Status(ctx context.Context) any
	Sync() error
	Evict() error
	Rebuild() error
	Snapshot(kind string) error
	Check(readDataSubset string) error
	RestoreDrill() error
	AllowPrune() error
	// MigrateEncryption copies what has one layer of encryption to the
	// stores with two; dryRun only counts, deleteOld retires the old ones.
	MigrateEncryption(dryRun, deleteOld bool) error
	// Forget starts the job that drops what the vault holds below prefix,
	// a folder it no longer keeps.
	Forget(prefix string) error
	// Get brings path back from the cold tier and returns where it is.
	Get(ctx context.Context, path string, allowOverBudget bool) (string, error)
	// BackupSecretFolders starts a backup of the secret folders.
	BackupSecretFolders() error
	// RestoreSecretFolders restores a version of the secret folders, the
	// newest at or before at, into to (a new folder), or with force over
	// the live folders, and says what it did.
	RestoreSecretFolders(ctx context.Context, to, at string, force bool) (any, error)
}

// VaultAPI serves /api/vault/*. Its jobs run in the background: a POST
// that starts one answers 202, or 409 while another runs, and the status
// says how it went.
type VaultAPI struct {
	Vault func() VaultService
	// Targets serves the node targets, /api/vault/targets.
	Targets *VaultTargetsAPI
}

// VaultGetResponse is POST /api/vault/get's answer.
type VaultGetResponse struct {
	Path string `json:"path"`
}

func (a *VaultAPI) Status(w http.ResponseWriter, r *http.Request) {
	WriteJSONIfChanged(w, r, a.Vault().Status(r.Context()))
}

func (a *VaultAPI) job(w http.ResponseWriter, err error) {
	if err != nil {
		WriteErr(w, statusFor(apperr.KindOf(err)), err.Error())
		return
	}
	WriteJSON(w, http.StatusAccepted, OperationResponse{Status: "started"})
}

func (a *VaultAPI) Sync(w http.ResponseWriter, r *http.Request)  { a.job(w, a.Vault().Sync()) }
func (a *VaultAPI) Evict(w http.ResponseWriter, r *http.Request) { a.job(w, a.Vault().Evict()) }
func (a *VaultAPI) Rebuild(w http.ResponseWriter, r *http.Request) {
	a.job(w, a.Vault().Rebuild())
}
func (a *VaultAPI) RestoreDrill(w http.ResponseWriter, r *http.Request) {
	a.job(w, a.Vault().RestoreDrill())
}

func (a *VaultAPI) Snapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind string `json:"kind"` // "nightly" (default) or "hourly"
	}
	if err := DecodeOptionalJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Kind != "" && req.Kind != "nightly" && req.Kind != "hourly" {
		WriteErr(w, http.StatusBadRequest, `kind is "nightly" or "hourly"`)
		return
	}
	a.job(w, a.Vault().Snapshot(req.Kind))
}

func (a *VaultAPI) Check(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ReadDataSubset string `json:"read_data_subset"` // such as "5%"; "" checks the structure only
	}
	if err := DecodeOptionalJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.job(w, a.Vault().Check(req.ReadDataSubset))
}

func (a *VaultAPI) AllowPrune(w http.ResponseWriter, r *http.Request) {
	if err := DecodeOptionalJSON(r, &struct{}{}); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.Vault().AllowPrune(); err != nil {
		WriteErr(w, statusFor(apperr.KindOf(err)), err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, OperationResponse{Status: "allowed"})
}

func (a *VaultAPI) Forget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prefix string `json:"prefix"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Prefix == "" {
		WriteErr(w, http.StatusBadRequest, "prefix is empty: name the folder the vault forgets, such as brains-folders/Personal")
		return
	}
	a.job(w, a.Vault().Forget(req.Prefix))
}

func (a *VaultAPI) Get(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path            string `json:"path"`
		AllowOverBudget bool   `json:"allow_over_budget"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Path == "" {
		WriteErr(w, http.StatusBadRequest, "path is empty: name the vault path to bring back")
		return
	}
	p, err := a.Vault().Get(r.Context(), req.Path, req.AllowOverBudget)
	if err != nil {
		WriteErr(w, statusFor(apperr.KindOf(err)), err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, VaultGetResponse{Path: p})
}

// MigrateEncryption answers POST /api/vault/migrate-encryption.
func (a *VaultAPI) MigrateEncryption(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DryRun    bool `json:"dry_run"`
		DeleteOld bool `json:"delete_old"`
	}
	if err := DecodeOptionalJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.DryRun && req.DeleteOld {
		WriteErr(w, http.StatusBadRequest, "a dry run deletes nothing: dry_run and delete_old together")
		return
	}
	a.job(w, a.Vault().MigrateEncryption(req.DryRun, req.DeleteOld))
}

// BackupSecretFolders answers POST /api/vault/secret-folders/backup.
func (a *VaultAPI) BackupSecretFolders(w http.ResponseWriter, r *http.Request) {
	if err := DecodeOptionalJSON(r, &struct{}{}); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.job(w, a.Vault().BackupSecretFolders())
}

// RestoreSecretFolders answers POST /api/vault/secret-folders/restore,
// once the restore is done.
func (a *VaultAPI) RestoreSecretFolders(w http.ResponseWriter, r *http.Request) {
	var req struct {
		To    string `json:"to"`
		At    string `json:"at"`
		Force bool   `json:"force"`
	}
	if err := DecodeOptionalJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := a.Vault().RestoreSecretFolders(r.Context(), req.To, req.At, req.Force)
	if err != nil {
		WriteErr(w, statusFor(apperr.KindOf(err)), err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, res)
}
