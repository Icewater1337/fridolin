package backup

import (
	"context"
	"errors"
	"log"
	"os"
	"slices"
	"time"

	"github.com/Icewater1337/fridolin/internal/apperr"
)

// StatusSnapshot is an owned view of the backup state. Optional pointers keep
// absent keys distinct from present empty values in the dashboard contract.
type StatusSnapshot struct {
	Brains              []BrainStatus  `json:"brains"`
	Bridge              *Result        `json:"bridge"`
	Client              *ClientStatus  `json:"client"`
	Connect             *connectJob    `json:"connect"`
	Connected           bool           `json:"connected"`
	DriveFolder         string         `json:"drive_folder,omitempty"`
	IncludeBridgeConfig bool           `json:"include_bridge_config"`
	Job                 *JobStatus     `json:"job"`
	LastRestore         *RestoreResult `json:"last_restore"`
	NextRun             *time.Time     `json:"next_run,omitempty"`
	Rclone              RcloneStatus   `json:"rclone"`
	RedirectURI         string         `json:"redirect_uri"`
	SecretExcludes      []string       `json:"secret_excludes"`
	Settings            Settings       `json:"settings"`
	// SharedClient says the link uses rclone's shared Google client, which
	// Google retires during 2026: the page offers the switch.
	SharedClient bool `json:"shared_client"`
}
type RcloneStatus struct {
	Error       *string `json:"error,omitempty"`
	Found       bool    `json:"found"`
	InstallHint string  `json:"install_hint"`
	Path        *string `json:"path,omitempty"`
	Version     *string `json:"version,omitempty"`
}
type BrainStatus struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Dir     string   `json:"dir"`
	Enabled bool     `json:"enabled"`
	Exclude []string `json:"exclude"`
	Last    *Result  `json:"last"`
}
type JobStatus struct {
	Brain   *string   `json:"brain,omitempty"`
	Current string    `json:"current"`
	Done    []string  `json:"done"`
	Kind    string    `json:"kind"`
	Queue   []string  `json:"queue"`
	Started time.Time `json:"started"`
	Target  *string   `json:"target,omitempty"`
}

// Status snapshots job state under the lock and returns it for encoding after unlock.
func (m *Manager) Status() StatusSnapshot {
	cfg := m.opts.Settings()
	rc := RcloneStatus{Found: false, InstallHint: m.opts.InstallHint()}
	if p, err := m.rclonePath(); err == nil {
		version := rcloneVersion(p, m.opts.Environ(context.Background()))
		rc.Found, rc.Path, rc.Version = true, &p, &version
	} else if !errors.Is(err, ErrRcloneMissing) {
		message := err.Error()
		rc.Error = &message
	}
	// Snapshot under the lock; slow HTTP clients must not hold up a job.
	m.mu.Lock()
	rows := []BrainStatus{}
	for _, br := range m.opts.Targets() {
		s := br.Settings()
		rows = append(rows, BrainStatus{ID: br.ID, Name: s.Name, Dir: br.Dir, Enabled: s.Enabled,
			Exclude: append([]string{}, s.Exclude...), Last: copyOf(m.state.Brains[br.ID])})
	}
	var job *JobStatus
	if j := m.job; j != nil {
		job = &JobStatus{Kind: j.kind, Started: j.started, Current: j.current,
			Queue: slices.Clone(j.queue), Done: slices.Clone(j.done)}
		if j.kind == jobRestore {
			brain, target := j.brain, j.target
			job.Brain, job.Target = &brain, &target
		}
	}
	connected := m.connected()
	var section map[string]string
	if data, err := os.ReadFile(m.rcloneConf()); err == nil {
		section = RcloneSection(string(data), cfg.Remote)
	}
	redirect := ""
	if m.opts.OAuth.RedirectURI != nil {
		redirect = m.opts.OAuth.RedirectURI()
	}
	resp := StatusSnapshot{
		Rclone: rc, Connected: connected, Connect: copyOf(m.connect), Settings: cfg,
		Client: m.clientStatus(), RedirectURI: redirect, SharedClient: connected && usesSharedClient(section),
		IncludeBridgeConfig: cfg.IncludeBridgeConfig, SecretExcludes: SecretExcludes,
		Brains: rows, Bridge: copyOf(m.state.Bridge), Job: job, LastRestore: copyOf(m.state.LastRestore),
	}
	if connected && section["type"] == "drive" && section["client_id"] != "" && section["root_folder_id"] != "" {
		resp.DriveFolder = DriveFolder
	}
	if next := m.nextRunLocked(cfg, connected); next != nil {
		value := *next
		resp.NextRun = &value
	}
	m.mu.Unlock()
	return resp
}

// Connect begins a Drive sign-in with the owner's own client and returns
// Google's consent page, for the dashboard to send the browser to. With
// reconnect it may replace a working link (one through rclone's shared
// client, say): that link stays and keeps backing up until the new one is
// in place, and stays for good if the sign-in is abandoned or fails. A
// sign-in still waiting for Google is replaced by the new one.
func (m *Manager) Connect(reconnect bool) (string, error) {
	if _, err := m.rclonePath(); err != nil {
		return "", apperr.New(apperr.Precondition, m.rcloneMissingHint(err))
	}
	if m.opts.OAuth.Start == nil {
		return "", apperr.New(apperr.Unavailable, "Google sign-in is not available in this bridge")
	}
	c, _ := m.driveClient()
	if c == nil {
		return "", apperr.New(apperr.Precondition, "add your Google OAuth client first: under Settings → Google sign-in, or for Drive alone on this page (docs/BACKUP.md, Use your own Google client)")
	}
	// Without reconnect a working link is never touched: replacing one is
	// a step the owner takes on purpose.
	if m.connected() && !reconnect {
		return "", apperr.New(apperr.Conflict, "Google Drive is already connected — disconnect it first to connect another account")
	}
	if err := m.connectAdmissible(); err != nil {
		return "", err
	}
	signedIn := make(chan driveSignIn, 1)
	authURL, err := m.opts.OAuth.Start(*c, []string{DriveScope}, func(tok *OAuthToken, err error) {
		select {
		case signedIn <- driveSignIn{tok, err}:
		default:
		}
	})
	if err != nil {
		return "", apperr.New(apperr.Precondition, err.Error())
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.connectAdmissibleLocked(); err != nil {
		return "", err
	}
	// Only waiting for the browser: the new sign-in takes its place.
	if old := m.connect; old != nil && old.State == "running" {
		old.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	job := &connectJob{State: "running", Step: stepSignIn, AuthURL: authURL, Reconnect: reconnect && m.connected(), cancel: cancel, done: make(chan struct{})}
	err = m.goLocked(func() {
		defer close(job.done)
		defer cancel()
		err := m.connectDrive(ctx, job, *c, signedIn)
		m.mu.Lock()
		defer m.mu.Unlock()
		if err != nil {
			job.State, job.Step, job.AuthURL, job.Error = "error", "", "", err.Error()
			if m.connect == job {
				log.Printf("backup: connecting Google Drive failed: %v", err)
			}
			return
		}
		job.State, job.Step, job.AuthURL = "done", "", ""
		log.Printf("backup: Google Drive connected with your own Google client (folder %s)", DriveFolder)
	})
	if err != nil {
		cancel()
		return "", apperr.Wrap(apperr.Unavailable, err)
	}
	m.connect = job
	return authURL, nil
}

// connectAdmissible says why a sign-in may not start now, or nil.
func (m *Manager) connectAdmissible() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connectAdmissibleLocked()
}

func (m *Manager) connectAdmissibleLocked() error {
	switch {
	case m.connect != nil && m.connect.State == "running" && m.connect.Step != stepSignIn:
		return apperr.New(apperr.Conflict, "already connecting — Google Drive is being set up, wait a moment")
	case m.job != nil:
		return apperr.New(apperr.Conflict, "a backup or restore is running — wait for it or cancel it first")
	case m.disconnecting:
		return apperr.Wrap(apperr.Conflict, errDisconnecting)
	}
	return nil
}

// Disconnect joins a pending sign-in before removing the Drive link.
func (m *Manager) Disconnect(ctx context.Context) error {
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
func (m *Manager) Cancel() error {
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
func (m *Manager) Versions(ctx context.Context, id string) ([]string, error) {
	if !NameOK(id) {
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
	SortVersions(versions)
	return versions, nil
}

// RestoreRequest identifies the snapshot and destination of a restore.
type RestoreRequest struct {
	Brain   string
	Version string
	Target  string
}

// ErrRestoreRequest distinguishes malformed remote path components from a
// rejected local destination. HTTP presents the former as 400 and the latter as 422.
var ErrRestoreRequest = errors.New("invalid brain or version")

// Restore validates the remote path components and starts a background restore
// after the supplied checker resolves and validates destination overlap.
func (m *Manager) Restore(req RestoreRequest, overlapCheck func(string) (string, error)) (string, error) {
	if req.Version == "" {
		req.Version = "current"
	}
	if !NameOK(req.Brain) || !VersionOK(req.Version) {
		return "", ErrRestoreRequest
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
