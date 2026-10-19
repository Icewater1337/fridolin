package main

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/registry"
)

func (b *Bridge) apiBackupStatus(w http.ResponseWriter, r *http.Request) {
	httpapi.WriteJSON(w, http.StatusOK, b.backup.Status())
}

func (b *Bridge) apiBackupConnect(w http.ResponseWriter, r *http.Request) {
	if err := b.backup.Connect(); err != nil {
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

func (b *Bridge) apiBackupDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := b.backup.Disconnect(r.Context()); err != nil {
		status := http.StatusInternalServerError
		if apperr.KindOf(err) == apperr.Conflict {
			status = http.StatusConflict
		}
		httpapi.WriteErr(w, status, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "disconnected"})
}

func (b *Bridge) apiBackupSettings(w http.ResponseWriter, r *http.Request) {
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
	_, err := b.cfgs.Update("", func(c *config.Config) error {
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
func (b *Bridge) apiBrainBackup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool    `json:"enabled"`
		Exclude []string `json:"exclude"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := b.registry.UpdateBackup(r.PathValue("id"), req.Enabled, req.Exclude); err != nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (b *Bridge) apiBackupRun(w http.ResponseWriter, r *http.Request) {
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
	include := config.BoolOr(req.IncludeBridge, b.cfgs.Get().Backup.IncludeBridge())
	if err := b.backup.StartRun(req.Brains, include, "manual", req.Force); err != nil {
		httpapi.WriteErr(w, http.StatusConflict, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (b *Bridge) apiBackupCancel(w http.ResponseWriter, r *http.Request) {
	if err := b.backup.Cancel(); err != nil {
		httpapi.WriteErr(w, http.StatusConflict, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "cancelling"})
}

func (b *Bridge) apiBackupVersions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	versions, err := b.backup.Versions(r.Context(), id)
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

// restoreTarget checks the folder a restore copies into, as for a new brain
// (CheckBrainPath): inside your home folder, neither the data dir nor in or
// around a brain, and not the credentials folder either; and new or empty.
// It returns the folder, resolved.
func (b *Bridge) restoreTarget(p string) (string, error) {
	cfg := b.cfgs.Get()
	home := b.cfgs.Boot().Home
	checkDir := brainDirCheck(b.cfgs)
	target, problems, _ := registry.CheckPath(resolveExisting(fsx.ExpandHome(strings.TrimSpace(p), home)), registry.PathContext{
		Home: home, Others: registryFor(b.cfgs).OtherDirs(cfg),
		CheckDir: func(dir, configured, home string) error { return checkDir(dir, configured, cfg, home) },
	})
	if creds := fsx.Canonical(cfg.Sandbox.CredentialsDir); len(problems) == 0 && creds != "" && (fsx.Within(target, creds) || fsx.Within(creds, target)) {
		problems = append(problems, "the credentials folder cannot be (or contain) the restore folder")
	}
	if len(problems) == 0 {
		if err := checkRestoreTarget(target, cfg, home); err != nil {
			problems = append(problems, "the restore folder "+err.Error()+" — pick a new folder")
		}
	}
	if len(problems) == 0 {
		if err := checkRestoreTarget(target, runtimeConfig(b.cfgs), home); err != nil {
			problems = append(problems, "the restore folder "+err.Error()+" — pick a new folder")
		}
	}
	if len(problems) > 0 {
		return "", errors.New(strings.Join(problems, "; "))
	}
	// The folder comes from the request, and CheckBrainPath only reports
	// on it. Build what is listed, made and copied into from your home
	// folder and a part of it that stays inside, so that it is there by
	// construction and not only by that report.
	home = fsx.Canonical(home)
	rel, err := filepath.Rel(home, target)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", errors.New("restore into a new folder inside your home folder")
	}
	target = filepath.Join(home, rel)
	entries, err := os.ReadDir(target)
	switch {
	case err == nil && len(entries) > 0:
		return "", errors.New("the restore folder must be new or empty")
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	return target, nil
}

// apiBackupRestore copies a backup into a new, empty folder. It never writes
// into an existing brain: you inspect the copy and add it as a brain (or
// swap folders) yourself. The restore runs as the job; how it went is saved
// as the state's LastRestore, which the Backup page shows.
func (b *Bridge) apiBackupRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Brain   string `json:"brain"`
		Version string `json:"version"` // "current" or a versions/<ts> name
		Target  string `json:"target"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	target, err := b.backup.Restore(BackupRestoreRequest{Brain: req.Brain, Version: req.Version, Target: req.Target}, b.restoreTarget)
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
		if errors.Is(err, ErrBackupRestoreRequest) {
			status = http.StatusBadRequest
		}
		httpapi.WriteErr(w, status, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "restoring", "target": target})
}
