package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/backup"
	"github.com/Icewater1337/fridolin/internal/config"
)

// BackupService is the backup job surface used by the HTTP adapter.
type BackupService interface {
	Status() backup.StatusSnapshot
	Connect(reconnect bool) (string, error)
	SetDriveClient([]byte) (*backup.OAuthClient, error)
	DeleteDriveClient() error
	Disconnect(context.Context) error
	StartRun([]string, bool, string, bool) error
	Cancel() error
	Versions(context.Context, string) ([]string, error)
	Restore(backup.RestoreRequest, func(string) (string, error)) (string, error)
}

type BackupAPI struct {
	Backup        func() BackupService
	UpdateConfig  func(func(*config.File) error) error
	IncludeBridge func() bool
	UpdateBrain   func(string, *bool, []string) error
	// UpdateTargets sets brain.json's backup.targets and backup.node_only
	// (nil leaves each as it is).
	UpdateTargets func(id string, targets map[string]bool, nodeOnly *[]string) error
	RestoreTarget func(string) (string, error)
	// RedirectURI is the bridge's Google sign-in redirect, for the note
	// on a pasted Web application client.
	RedirectURI func() string
}

func (a *BackupAPI) Status(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, a.Backup().Status())
}

// Connect starts a Drive sign-in with the owner's own Google client. The
// body may be left out; {"reconnect": true} replaces a working link (one
// through rclone's shared client), which keeps working until the new one
// is in place.
func (a *BackupAPI) Connect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reconnect bool `json:"reconnect"`
	}
	if err := DecodeOptionalJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	authURL, err := a.Backup().Connect(req.Reconnect)
	if err != nil {
		status := statusFor(apperr.KindOf(err))
		WriteErr(w, status, err.Error())
		return
	}
	WriteJSON(w, http.StatusAccepted, BackupConnectResponse{Status: "connecting", AuthURL: authURL})
}

// SetDriveClient keeps a Google OAuth client for the Drive link alone,
// pasted as the JSON Google Cloud Console offers.
func (a *BackupAPI) SetDriveClient(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JSON string `json:"json"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := a.Backup().SetDriveClient([]byte(strings.TrimSpace(req.JSON)))
	if err != nil {
		WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	note := ""
	if c.Type == "web" && a.RedirectURI != nil {
		note = "This is a Web application client: add " + a.RedirectURI() + " to its Authorized redirect URIs in Google Cloud Console, or create a Desktop app client instead."
	}
	WriteJSON(w, http.StatusOK, GoogleClientSavedResponse{Status: "saved", Type: c.Type, Note: note})
}

// DeleteDriveClient forgets the Drive client; sign-ins use the one under
// Settings → Google sign-in again.
func (a *BackupAPI) DeleteDriveClient(w http.ResponseWriter, r *http.Request) {
	if err := a.Backup().DeleteDriveClient(); err != nil {
		WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, OperationResponse{Status: "removed"})
}

func (a *BackupAPI) Disconnect(w http.ResponseWriter, r *http.Request) {
	if err := a.Backup().Disconnect(r.Context()); err != nil {
		status := statusFor(apperr.KindOf(err))
		WriteErr(w, status, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, OperationResponse{Status: "disconnected"})
}

func (a *BackupAPI) Settings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled             *bool   `json:"enabled"`
		DailyAt             *string `json:"daily_at"`
		Root                *string `json:"root"`
		KeepVersionsDays    *int    `json:"keep_versions_days"`
		IncludeBridgeConfig *bool   `json:"include_bridge_config"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	err := a.UpdateConfig(func(c *config.File) error {
		if req.Enabled != nil {
			c.Backup.Enabled = *req.Enabled
		}
		if req.DailyAt != nil {
			c.Backup.DailyAt = *req.DailyAt
		}
		if req.Root != nil {
			c.Backup.Root = *req.Root
		}
		if req.KeepVersionsDays != nil {
			c.Backup.KeepVersionsDays = req.KeepVersionsDays
		}
		if req.IncludeBridgeConfig != nil {
			c.Backup.IncludeBridgeConfig = req.IncludeBridgeConfig
		}
		return nil
	})
	if err != nil {
		WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, OperationResponse{Status: "saved"})
}

// apiBrainBackup changes one brain's backup settings, which live in its
// manifest: whether it is backed up and what is left out.
func (a *BackupAPI) BrainSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool    `json:"enabled"`
		Exclude []string `json:"exclude"`
		// Targets and NodeOnly are backup.targets and backup.node_only.
		Targets  map[string]bool `json:"targets"`
		NodeOnly *[]string       `json:"node_only"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Enabled != nil || req.Exclude != nil || req.Targets == nil && req.NodeOnly == nil {
		if err := a.UpdateBrain(r.PathValue("id"), req.Enabled, req.Exclude); err != nil {
			WriteErr(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	}
	if (req.Targets != nil || req.NodeOnly != nil) && a.UpdateTargets != nil {
		if err := a.UpdateTargets(r.PathValue("id"), req.Targets, req.NodeOnly); err != nil {
			WriteErr(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	}
	WriteJSON(w, http.StatusOK, OperationResponse{Status: "saved"})
}

func (a *BackupAPI) Run(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Brains        []string `json:"brains"`
		IncludeBridge *bool    `json:"include_bridge"`
		// Force mirrors the named brains even when most of their backed-up
		// files are gone (see checkMassDeletion).
		Force bool `json:"force"`
	}
	if err := DecodeOptionalJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Forcing is a decision about one brain's folder, made after looking at
	// it; without a list it would mirror every brain's deletions at once.
	if req.Force && len(req.Brains) == 0 {
		WriteErr(w, http.StatusBadRequest, "force needs a brain: name the brains to mirror in \"brains\"")
		return
	}
	include := a.IncludeBridge()
	if req.IncludeBridge != nil {
		include = *req.IncludeBridge
	}
	if err := a.Backup().StartRun(req.Brains, include, "manual", req.Force); err != nil {
		WriteErr(w, http.StatusConflict, err.Error())
		return
	}
	WriteJSON(w, http.StatusAccepted, OperationResponse{Status: "started"})
}

func (a *BackupAPI) Cancel(w http.ResponseWriter, r *http.Request) {
	if err := a.Backup().Cancel(); err != nil {
		WriteErr(w, http.StatusConflict, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, OperationResponse{Status: "cancelling"})
}

func (a *BackupAPI) Versions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	versions, err := a.Backup().Versions(r.Context(), id)
	if err != nil {
		status := http.StatusBadGateway
		// Every other kind is the remote's failure: 502.
		//exhaustive:ignore
		switch apperr.KindOf(err) {
		case apperr.Invalid:
			status = http.StatusBadRequest
		case apperr.Precondition:
			status = http.StatusPreconditionFailed
		}
		WriteErr(w, status, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, BackupVersionsResponse{Brain: id, Versions: versions})
}

// apiBackupRestore copies a backup into a new, empty folder. It never writes
// into an existing brain: you inspect the copy and add it as a brain (or
// swap folders) yourself. The restore runs as the job; how it went is saved
// as the state's LastRestore, which the Backup page shows.
func (a *BackupAPI) Restore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Brain   string `json:"brain"`
		Version string `json:"version"` // "current" or a versions/<ts> name
		Target  string `json:"target"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	target, err := a.Backup().Restore(backup.RestoreRequest{Brain: req.Brain, Version: req.Version, Target: req.Target}, a.RestoreTarget)
	if err != nil {
		status := statusFor(apperr.KindOf(err))
		if errors.Is(err, backup.ErrRestoreRequest) {
			status = http.StatusBadRequest
		}
		WriteErr(w, status, err.Error())
		return
	}
	WriteJSON(w, http.StatusAccepted, RestoreStartedResponse{Status: "restoring", Target: target})
}
