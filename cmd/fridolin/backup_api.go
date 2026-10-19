package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/backup"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/httpapi"
)

// BackupService is the backup job surface used by the HTTP adapter.
type BackupService interface {
	Status() map[string]any
	Connect() error
	Disconnect(context.Context) error
	StartRun([]string, bool, string, bool) error
	Cancel() error
	Versions(context.Context, string) ([]string, error)
	Restore(backup.RestoreRequest, func(string) (string, error)) (string, error)
}

type BackupAPI struct {
	Backup        func() BackupService
	UpdateConfig  func(func(*config.Config) error) error
	IncludeBridge func() bool
	UpdateBrain   func(string, *bool, []string) error
	RestoreTarget func(string) (string, error)
}

func (a *BackupAPI) Status(w http.ResponseWriter, r *http.Request) {
	httpapi.WriteJSON(w, http.StatusOK, a.Backup().Status())
}

func (a *BackupAPI) Connect(w http.ResponseWriter, r *http.Request) {
	if err := a.Backup().Connect(); err != nil {
		status := http.StatusInternalServerError
		switch apperr.KindOf(err) {
		case apperr.Precondition:
			status = http.StatusPreconditionFailed
		case apperr.Conflict:
			status = http.StatusConflict
		case apperr.Unavailable:
			status = http.StatusServiceUnavailable
		}
		httpapi.WriteErr(w, status, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "connecting"})
}

func (a *BackupAPI) Disconnect(w http.ResponseWriter, r *http.Request) {
	if err := a.Backup().Disconnect(r.Context()); err != nil {
		status := http.StatusInternalServerError
		if apperr.KindOf(err) == apperr.Conflict {
			status = http.StatusConflict
		}
		httpapi.WriteErr(w, status, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "disconnected"})
}

func (a *BackupAPI) Settings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled             *bool   `json:"enabled"`
		DailyAt             *string `json:"daily_at"`
		Root                *string `json:"root"`
		KeepVersionsDays    *int    `json:"keep_versions_days"`
		IncludeBridgeConfig *bool   `json:"include_bridge_config"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	err := a.UpdateConfig(func(c *config.Config) error {
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
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// apiBrainBackup changes one brain's backup settings, which live in its
// manifest: whether it is backed up and what is left out.
func (a *BackupAPI) BrainSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool    `json:"enabled"`
		Exclude []string `json:"exclude"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.UpdateBrain(r.PathValue("id"), req.Enabled, req.Exclude); err != nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (a *BackupAPI) Run(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Brains        []string `json:"brains"`
		IncludeBridge *bool    `json:"include_bridge"`
		// Force mirrors the named brains even when most of their backed-up
		// files are gone (see checkMassDeletion).
		Force bool `json:"force"`
	}
	if err := httpapi.DecodeOptionalJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Forcing is a decision about one brain's folder, made after looking at
	// it; without a list it would mirror every brain's deletions at once.
	if req.Force && len(req.Brains) == 0 {
		httpapi.WriteErr(w, http.StatusBadRequest, "force needs a brain: name the brains to mirror in \"brains\"")
		return
	}
	include := config.BoolOr(req.IncludeBridge, a.IncludeBridge())
	if err := a.Backup().StartRun(req.Brains, include, "manual", req.Force); err != nil {
		httpapi.WriteErr(w, http.StatusConflict, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (a *BackupAPI) Cancel(w http.ResponseWriter, r *http.Request) {
	if err := a.Backup().Cancel(); err != nil {
		httpapi.WriteErr(w, http.StatusConflict, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "cancelling"})
}

func (a *BackupAPI) Versions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	versions, err := a.Backup().Versions(r.Context(), id)
	if err != nil {
		status := http.StatusBadGateway
		switch apperr.KindOf(err) {
		case apperr.Invalid:
			status = http.StatusBadRequest
		case apperr.Precondition:
			status = http.StatusPreconditionFailed
		}
		httpapi.WriteErr(w, status, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"brain": id, "versions": versions})
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
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	target, err := a.Backup().Restore(backup.RestoreRequest{Brain: req.Brain, Version: req.Version, Target: req.Target}, a.RestoreTarget)
	if err != nil {
		status := http.StatusInternalServerError
		switch apperr.KindOf(err) {
		case apperr.Precondition:
			status = http.StatusPreconditionFailed
		case apperr.Invalid:
			status = http.StatusUnprocessableEntity
		case apperr.Conflict:
			status = http.StatusConflict
		case apperr.Unavailable:
			status = http.StatusServiceUnavailable
		}
		if errors.Is(err, backup.ErrRestoreRequest) {
			status = http.StatusBadRequest
		}
		httpapi.WriteErr(w, status, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "restoring", "target": target})
}
