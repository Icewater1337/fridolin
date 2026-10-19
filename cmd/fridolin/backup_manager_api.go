package main

import (
	"context"
	"errors"
	"log"
	"os"
	"slices"
	"time"

	"github.com/Icewater1337/fridolin/internal/backup"

	"github.com/Icewater1337/fridolin/internal/apperr"
)

// Status snapshots job state under the lock and returns it for encoding after unlock.
func (m *BackupManager) Status() map[string]any {
	cfg := m.opts.Settings()
	rc := map[string]any{"found": false, "install_hint": m.opts.InstallHint()}
	if p, err := m.rclonePath(); err == nil {
		rc["found"], rc["path"], rc["version"] = true, p, rcloneVersion(p, m.opts.Environ(context.Background()))
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
	for _, br := range m.opts.Targets() {
		s := br.Settings()
		rows = append(rows, brainRow{ID: br.ID, Name: s.Name, Dir: br.Dir, Enabled: s.Enabled,
			Exclude: append([]string{}, s.Exclude...), Last: copyOf(m.state.Brains[br.ID])})
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
		"settings":              cfg,
		"include_bridge_config": cfg.IncludeBridgeConfig,
		"secret_excludes":       backup.SecretExcludes,
		"brains":                rows,
		"bridge":                copyOf(m.state.Bridge),
		"job":                   job,
		"last_restore":          copyOf(m.state.LastRestore),
	}
	if next := m.nextRunLocked(cfg, connected); next != nil {
		resp["next_run"] = *next
	}
	m.mu.Unlock()
	return resp
}

// Connect begins a Drive sign-in while preserving the current job admission order.
func (m *BackupManager) Connect() error {
	if _, err := m.rclonePath(); err != nil {
		return apperr.New(apperr.Precondition, m.rcloneMissingHint(err))
	}
	// Connecting starts by deleting the remote, so it would throw away a
	// working link, for good if the new sign-in is then abandoned, and a
	// backup running on it would fail half-way. Disconnect comes first.
	if m.connected() {
		return apperr.New(apperr.Conflict, "Google Drive is already connected — disconnect it first to connect another account")
	}
	m.mu.Lock()
	if m.connect != nil && m.connect.State == "running" {
		m.mu.Unlock()
		return apperr.New(apperr.Conflict, "already connecting — finish in the browser tab rclone opened")
	}
	if m.job != nil {
		m.mu.Unlock()
		return apperr.New(apperr.Conflict, "a backup or restore is running — wait for it or cancel it first")
	}
	if m.disconnecting {
		m.mu.Unlock()
		return apperr.Wrap(apperr.Conflict, errDisconnecting)
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
		return apperr.Wrap(apperr.Unavailable, err)
	}
	m.connect = job
	m.mu.Unlock()
	return nil
}

// Disconnect joins a pending sign-in before removing the Drive link.
func (m *BackupManager) Disconnect(ctx context.Context) error {
	m.mu.Lock()
	switch {
	case m.job != nil:
		m.mu.Unlock()
		return apperr.New(apperr.Conflict, "a backup or restore is running — wait for it or cancel it first")
	case m.disconnecting:
		m.mu.Unlock()
		return apperr.Wrap(apperr.Conflict, errDisconnecting)
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
			return apperr.New(apperr.Unknown, "the Google Drive sign-in did not stop — try again in a moment")
		}
	}
	m.mu.Lock()
	if m.connect == job {
		m.connect = nil
	}
	m.mu.Unlock()
	if _, err := m.runner(ctx, []string{"config", "delete", m.opts.Settings().Remote}, nil); err != nil {
		return apperr.New(apperr.Unknown, "removing the Google Drive link failed: "+err.Error())
	}
	return nil
}

// Cancel requests cancellation of the current backup or restore.
func (m *BackupManager) Cancel() error {
	m.mu.Lock()
	job := m.job
	if job != nil {
		job.cancel()
	}
	m.mu.Unlock()
	if job == nil {
		return apperr.New(apperr.Conflict, "no backup is running")
	}
	return nil
}

// Versions lists a brain's saved versions in the dashboard's existing order.
func (m *BackupManager) Versions(ctx context.Context, id string) ([]string, error) {
	if !backup.NameOK(id) {
		return nil, apperr.New(apperr.Invalid, "invalid brain")
	}
	if !m.connected() {
		return nil, apperr.New(apperr.Precondition, "Google Drive is not connected")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	versions, err := m.listVersions(ctx, backupPath(m.opts.Settings(), id, "versions"))
	if err != nil {
		return nil, apperr.New(apperr.Unknown, "listing versions failed: "+err.Error())
	}
	backup.SortVersions(versions)
	return versions, nil
}

// BackupRestoreRequest identifies the snapshot and destination of a restore.
type BackupRestoreRequest struct {
	Brain   string
	Version string
	Target  string
}

// errBackupRestoreRequest distinguishes malformed remote path components from a
// rejected local destination. HTTP presents the former as 400 and the latter as 422.
var errBackupRestoreRequest = errors.New("invalid brain or version")

// Restore validates the remote path components and starts a background restore
// after the supplied checker resolves and validates destination overlap.
func (m *BackupManager) Restore(req BackupRestoreRequest, overlapCheck func(string) (string, error)) (string, error) {
	if req.Version == "" {
		req.Version = "current"
	}
	if !backup.NameOK(req.Brain) || !backup.VersionOK(req.Version) {
		return "", errBackupRestoreRequest
	}
	if !m.connected() {
		return "", apperr.New(apperr.Precondition, "Google Drive is not connected")
	}
	target, err := overlapCheck(req.Target)
	if err != nil {
		return "", apperr.Wrap(apperr.Invalid, err)
	}
	src := backupPath(m.opts.Settings(), req.Brain, "current")
	if req.Version != "current" {
		src = backupPath(m.opts.Settings(), req.Brain, "versions", req.Version)
	}
	m.mu.Lock()
	if m.job != nil {
		m.mu.Unlock()
		return "", apperr.New(apperr.Conflict, "a backup or restore is already running")
	}
	if m.disconnecting {
		m.mu.Unlock()
		return "", apperr.Wrap(apperr.Conflict, errDisconnecting)
	}
	// Made here, so that a folder that cannot be made is the request's
	// error rather than a restore that fails out of sight.
	if err := os.MkdirAll(target, 0o700); err != nil {
		m.mu.Unlock()
		return "", apperr.New(apperr.Unknown, "creating the restore folder failed: "+err.Error())
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
		return "", apperr.Wrap(apperr.Unavailable, err)
	}
	m.job = job
	m.mu.Unlock()
	return target, nil
}
