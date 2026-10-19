package main

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/registry"
)

func (b *Bridge) apiBackupStatus(w http.ResponseWriter, r *http.Request) {
	m := b.backup
	cfg := b.cfgs.Get()
	rc := map[string]any{"found": false, "install_hint": rcloneInstallHint()}
	if p, err := m.rclonePath(); err == nil {
		rc["found"], rc["path"], rc["version"] = true, p, rcloneVersion(p, m.rcloneEnv(anyChild))
	} else if !errors.Is(err, errRcloneMissing) {
		rc["error"] = err.Error() // backup.rclone_path names one that does not work
	}
	// The response is built under the lock and written after it: a client
	// that reads slowly must not hold up the backup job.
	m.mu.Lock()
	type brainRow struct {
		ID      string        `json:"id"`
		Name    string        `json:"name"`
		Dir     string        `json:"dir"`
		Enabled bool          `json:"enabled"`
		Exclude []string      `json:"exclude"`
		Last    *BackupResult `json:"last"`
	}
	rows := []brainRow{}
	for _, br := range b.brainMgr.Brains() {
		s := br.settings.Load()
		rows = append(rows, brainRow{ID: br.ID(), Name: s.name, Dir: br.Dir(), Enabled: s.manifest.Backup.Enabled,
			Exclude: nonNil(s.manifest.Backup.Exclude), Last: copyOf(m.state.Brains[br.ID()])})
	}
	var job map[string]any
	if j := m.job; j != nil {
		job = map[string]any{"kind": j.kind, "started": j.started, "current": j.current,
			"queue": slices.Clone(j.queue), "done": slices.Clone(j.done)}
		if j.kind == jobRestore {
			job["brain"], job["target"] = j.brain, j.target
		}
	}
	connected := m.connected()
	resp := map[string]any{
		"rclone":                rc,
		"connected":             connected,
		"connect":               copyOf(m.connect),
		"settings":              cfg.Backup,
		"include_bridge_config": cfg.Backup.IncludeBridge(),
		"secret_excludes":       backupSecretExcludes,
		"brains":                rows,
		"bridge":                copyOf(m.state.Bridge),
		"job":                   job,
		"last_restore":          copyOf(m.state.LastRestore),
	}
	if next := m.nextRunLocked(cfg, connected); next != nil {
		resp["next_run"] = *next
	}
	m.mu.Unlock()
	httpapi.WriteJSON(w, http.StatusOK, resp)
}

func (b *Bridge) apiBackupConnect(w http.ResponseWriter, r *http.Request) {
	m := b.backup
	if _, err := m.rclonePath(); err != nil {
		httpapi.WriteErr(w, http.StatusPreconditionFailed, rcloneMissingHint(err))
		return
	}
	// Connecting starts by deleting the remote, so it would throw away a
	// working link, for good if the new sign-in is then abandoned, and a
	// backup running on it would fail half-way. Disconnect comes first.
	if m.connected() {
		httpapi.WriteErr(w, http.StatusConflict, "Google Drive is already connected — disconnect it first to connect another account")
		return
	}
	m.mu.Lock()
	if m.connect != nil && m.connect.State == "running" {
		m.mu.Unlock()
		httpapi.WriteErr(w, http.StatusConflict, "already connecting — finish in the browser tab rclone opened")
		return
	}
	if m.job != nil {
		m.mu.Unlock()
		httpapi.WriteErr(w, http.StatusConflict, "a backup or restore is running — wait for it or cancel it first")
		return
	}
	if m.disconnecting {
		m.mu.Unlock()
		httpapi.WriteErr(w, http.StatusConflict, errDisconnecting.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	job := &connectJob{State: "running", cancel: cancel, done: make(chan struct{})}
	err := m.goLocked(func() {
		defer close(job.done)
		defer cancel()
		err := m.connectDrive(ctx, job)
		m.mu.Lock()
		defer m.mu.Unlock()
		if err != nil {
			job.State, job.Error = "error", err.Error()
			log.Printf("backup: connecting Google Drive failed: %v", err)
			return
		}
		job.State, job.AuthURL = "done", ""
		log.Printf("backup: Google Drive connected")
	})
	if err != nil {
		m.mu.Unlock()
		cancel()
		httpapi.WriteErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	m.connect = job
	m.mu.Unlock()
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "connecting"})
}

func (b *Bridge) apiBackupDisconnect(w http.ResponseWriter, r *http.Request) {
	m := b.backup
	m.mu.Lock()
	switch {
	case m.job != nil:
		m.mu.Unlock()
		httpapi.WriteErr(w, http.StatusConflict, "a backup or restore is running — wait for it or cancel it first")
		return
	case m.disconnecting:
		m.mu.Unlock()
		httpapi.WriteErr(w, http.StatusConflict, errDisconnecting.Error())
		return
	}
	m.disconnecting = true
	job := m.connect
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.disconnecting = false
		m.mu.Unlock()
	}()
	// A sign-in still running writes its token into the config when it
	// ends. It has to be gone before the delete, or a token written after it
	// would leave Drive connected behind the dashboard's back. It stays
	// m.connect until it has ended, so that one that does not stop still
	// shows on the status page and keeps another from starting next to it.
	if job != nil && job.cancel != nil {
		job.cancel()
		select {
		case <-job.done:
		case <-time.After(backupStopWait):
			httpapi.WriteErr(w, http.StatusInternalServerError, "the Google Drive sign-in did not stop — try again in a moment")
			return
		}
	}
	m.mu.Lock()
	if m.connect == job {
		m.connect = nil
	}
	m.mu.Unlock()
	if _, err := m.runner(r.Context(), []string{"config", "delete", b.cfgs.Get().Backup.Remote}, nil); err != nil {
		httpapi.WriteErr(w, http.StatusInternalServerError, "removing the Google Drive link failed: "+err.Error())
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
	b.backup.mu.Lock()
	job := b.backup.job
	if job != nil {
		job.cancel()
	}
	b.backup.mu.Unlock()
	if job == nil {
		httpapi.WriteErr(w, http.StatusConflict, "no backup is running")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "cancelling"})
}

func (b *Bridge) apiBackupVersions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !backupNameOK(id) {
		httpapi.WriteErr(w, http.StatusBadRequest, "invalid brain")
		return
	}
	if !b.backup.connected() {
		httpapi.WriteErr(w, http.StatusPreconditionFailed, "Google Drive is not connected")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	versions, err := b.backup.listVersions(ctx, backupPath(b.cfgs.Get().Backup, id, "versions"))
	if err != nil {
		httpapi.WriteErr(w, http.StatusBadGateway, "listing versions failed: "+err.Error())
		return
	}
	sortVersions(versions)
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
	if req.Version == "" {
		req.Version = "current"
	}
	if !backupNameOK(req.Brain) || !restoreVersionRe.MatchString(req.Version) {
		httpapi.WriteErr(w, http.StatusBadRequest, "invalid brain or version")
		return
	}
	m := b.backup
	if !m.connected() {
		httpapi.WriteErr(w, http.StatusPreconditionFailed, "Google Drive is not connected")
		return
	}
	target, err := b.restoreTarget(req.Target)
	if err != nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	src := backupPath(b.cfgs.Get().Backup, req.Brain, "current")
	if req.Version != "current" {
		src = backupPath(b.cfgs.Get().Backup, req.Brain, "versions", req.Version)
	}
	m.mu.Lock()
	if m.job != nil {
		m.mu.Unlock()
		httpapi.WriteErr(w, http.StatusConflict, "a backup or restore is already running")
		return
	}
	if m.disconnecting {
		m.mu.Unlock()
		httpapi.WriteErr(w, http.StatusConflict, errDisconnecting.Error())
		return
	}
	// Made here, so that a folder that cannot be made is the request's
	// error rather than a restore that fails out of sight.
	if err := os.MkdirAll(target, 0o700); err != nil {
		m.mu.Unlock()
		httpapi.WriteErr(w, http.StatusInternalServerError, "creating the restore folder failed: "+err.Error())
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &backupJob{kind: jobRestore, started: time.Now(), cancel: cancel, brain: req.Brain, target: target}
	err = m.goLocked(func() {
		defer cancel()
		res := m.restore(ctx, src, req.Brain, req.Version, target)
		m.mu.Lock()
		m.state.LastRestore = res
		m.saveStateLocked()
		m.job = nil
		m.mu.Unlock()
	})
	if err != nil {
		m.mu.Unlock()
		cancel()
		httpapi.WriteErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	m.job = job
	m.mu.Unlock()
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "restoring", "target": target})
}
