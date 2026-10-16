package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Backups go to Google Drive through rclone (https://rclone.org), driven by
// the bridge with its own rclone config file in <data_dir>/rclone/ — your
// personal rclone setup is never read or changed. The Drive link uses the
// drive.file scope, so rclone can only see the files it created.
//
// Layout in Drive, under backup.root:
//
//	<brain-id>/current/…              the latest copy of the brain folder
//	<brain-id>/versions/<timestamp>/… files changed or deleted by that run
//	_bridge/current/…                 config.json and the approved manifests
//
// Each run is an incremental `rclone sync`, so only changed files upload.
// Older versions are pruned after keep_versions_days.

// BackupConfig controls Google Drive backups.
type BackupConfig struct {
	// Enabled turns on the daily scheduled backup.
	Enabled bool `json:"enabled"`
	// Remote is the rclone remote name inside the bridge's own rclone config.
	Remote string `json:"remote,omitempty"`
	// Root is the folder in Google Drive that holds every backup.
	Root string `json:"root,omitempty"`
	// DailyAt is the local time of the scheduled backup, "HH:MM".
	DailyAt string `json:"daily_at,omitempty"`
	// RclonePath overrides the rclone binary (default: found on PATH).
	RclonePath string `json:"rclone_path,omitempty"`
	// KeepVersionsDays prunes old versions (default 30; 0 keeps them forever).
	KeepVersionsDays *int `json:"keep_versions_days,omitempty"`
	// IncludeBridgeConfig also backs up config.json and the brain list
	// (default true). Never included: the WhatsApp session, .env, tokens.
	IncludeBridgeConfig *bool `json:"include_bridge_config,omitempty"`
}

var (
	dailyAtRe    = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
	remoteNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
)

func (bc BackupConfig) validate() error {
	if bc.DailyAt != "" && !dailyAtRe.MatchString(bc.DailyAt) {
		return fmt.Errorf("backup.daily_at %q must be HH:MM (24h)", bc.DailyAt)
	}
	if bc.Remote != "" && !remoteNameRe.MatchString(bc.Remote) {
		return fmt.Errorf("backup.remote %q may only contain letters, digits, - and _", bc.Remote)
	}
	if strings.ContainsAny(bc.Root, ":\r\n\\") || strings.HasPrefix(bc.Root, "/") || strings.Contains(bc.Root, "..") {
		return fmt.Errorf("backup.root %q must be a plain folder name such as my-assistant-backup", bc.Root)
	}
	if bc.KeepVersionsDays != nil && *bc.KeepVersionsDays < 0 {
		return fmt.Errorf("backup.keep_versions_days cannot be negative")
	}
	return nil
}

// BackupResult is the outcome of backing up one brain (or the bridge config).
type BackupResult struct {
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
	Bytes    int64     `json:"bytes"`
	Files    int64     `json:"files"`
	Seconds  float64   `json:"seconds"`
	Version  string    `json:"version"`
	Trigger  string    `json:"trigger"` // manual or scheduled
}

type backupState struct {
	LastScheduledDay string                   `json:"last_scheduled_day"`
	Brains           map[string]*BackupResult `json:"brains"`
	Bridge           *BackupResult            `json:"bridge"`
}

type backupJob struct {
	started time.Time
	current string
	queue   []string
	done    []string
	cancel  context.CancelFunc
}

type connectJob struct {
	State   string `json:"state"` // running, done, error
	AuthURL string `json:"auth_url,omitempty"`
	Error   string `json:"error,omitempty"`
	cancel  context.CancelFunc
}

// BackupManager runs and schedules backups.
type BackupManager struct {
	cfgs *ConfigStore
	bm   *BrainManager

	mu      sync.Mutex
	state   backupState
	job     *backupJob
	connect *connectJob
	stop    context.CancelFunc
	runner  func(ctx context.Context, args []string, stderr io.Writer) ([]byte, error)
}

// NewBackupManager loads the saved backup state.
func NewBackupManager(cfgs *ConfigStore, bm *BrainManager) *BackupManager {
	m := &BackupManager{cfgs: cfgs, bm: bm, state: backupState{Brains: map[string]*BackupResult{}}}
	m.runner = m.runRclone
	if data, err := os.ReadFile(m.statePath()); err == nil {
		_ = json.Unmarshal(data, &m.state)
		if m.state.Brains == nil {
			m.state.Brains = map[string]*BackupResult{}
		}
	}
	return m
}

func (m *BackupManager) dir() string       { return filepath.Join(m.cfgs.Get().DataDir, "backup") }
func (m *BackupManager) statePath() string { return filepath.Join(m.dir(), "state.json") }
func (m *BackupManager) rcloneConf() string {
	return filepath.Join(m.cfgs.Get().DataDir, "rclone", "rclone.conf")
}

func (m *BackupManager) saveStateLocked() {
	data, err := json.MarshalIndent(m.state, "", "  ")
	if err == nil {
		_ = writeFileAtomic(m.statePath(), data, 0o600, false)
	}
}

// rclonePath finds the rclone binary.
func (m *BackupManager) rclonePath() (string, error) {
	p := m.cfgs.Get().Backup.RclonePath
	if p == "" {
		p = "rclone"
	}
	found, err := exec.LookPath(p)
	if err != nil {
		for _, c := range []string{"/opt/homebrew/bin/rclone", "/usr/local/bin/rclone", filepath.Join(os.Getenv("HOME"), ".local/bin/rclone")} {
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
		}
		return "", errors.New("rclone is not installed")
	}
	return found, nil
}

// rcloneEnv is the environment for rclone: the bridge's own config file, and
// none of your RCLONE_* settings.
func (m *BackupManager) rcloneEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "RCLONE_") {
			env = append(env, kv)
		}
	}
	return append(env, "RCLONE_CONFIG="+m.rcloneConf())
}

// runRclone runs rclone with the isolated config. Stdout is returned to the
// caller and never logged (config commands print tokens there).
func (m *BackupManager) runRclone(ctx context.Context, args []string, stderr io.Writer) ([]byte, error) {
	bin, err := m.rclonePath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(m.rcloneConf()), 0o700); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, append([]string{"--config", m.rcloneConf()}, args...)...)
	cmd.Env = m.rcloneEnv()
	cmd.Stdin = nil
	var out bytes.Buffer
	cmd.Stdout = &out
	if stderr != nil {
		cmd.Stderr = stderr
	} else {
		cmd.Stderr = io.Discard
	}
	err = cmd.Run()
	return out.Bytes(), err
}

// connected reports whether the isolated rclone config has a usable remote:
// a Drive remote with a token, or any other kind of remote someone set up by
// hand (rclone supports dozens of storage providers).
func (m *BackupManager) connected() bool {
	data, err := os.ReadFile(m.rcloneConf())
	if err != nil {
		return false
	}
	section := rcloneSection(string(data), m.cfgs.Get().Backup.Remote)
	if section == nil {
		return false
	}
	if section["type"] == "drive" {
		return section["token"] != ""
	}
	return section["type"] != ""
}

// rcloneSection returns the key/values of one [name] section of an rclone
// config file, or nil when it is absent.
func rcloneSection(conf, name string) map[string]string {
	var cur map[string]string
	for _, line := range strings.Split(conf, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if cur != nil {
				return cur
			}
			if line[1:len(line)-1] == name {
				cur = map[string]string{}
			}
			continue
		}
		if cur != nil {
			if k, v, ok := strings.Cut(line, "="); ok {
				cur[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	}
	return cur
}

// ── summary and status ───────────────────────────────────────────────────

type backupSummary struct {
	Connected bool       `json:"connected"`
	Enabled   bool       `json:"enabled"`
	Running   bool       `json:"running"`
	LastOK    *time.Time `json:"last_ok,omitempty"`
	LastError string     `json:"last_error,omitempty"`
	NextRun   *time.Time `json:"next_run,omitempty"`
}

// Summary is the short version shown on the overview.
func (m *BackupManager) Summary() backupSummary {
	cfg := m.cfgs.Get()
	m.mu.Lock()
	defer m.mu.Unlock()
	s := backupSummary{Connected: m.connected(), Enabled: cfg.Backup.Enabled, Running: m.job != nil}
	for _, r := range m.allResultsLocked() {
		if r.OK && (s.LastOK == nil || r.Finished.After(*s.LastOK)) {
			t := r.Finished
			s.LastOK = &t
		}
		if !r.OK && r.Error != "" {
			s.LastError = r.Error
		}
	}
	if cfg.Backup.Enabled {
		n := nextRun(cfg.Backup.DailyAt, time.Now())
		s.NextRun = &n
	}
	return s
}

func (m *BackupManager) allResultsLocked() []*BackupResult {
	var out []*BackupResult
	for _, r := range m.state.Brains {
		if r != nil {
			out = append(out, r)
		}
	}
	if m.state.Bridge != nil {
		out = append(out, m.state.Bridge)
	}
	return out
}

func nextRun(dailyAt string, now time.Time) time.Time {
	t, err := time.ParseInLocation("15:04", dailyAt, now.Location())
	if err != nil {
		t, _ = time.ParseInLocation("15:04", defaultBackupDailyAt, now.Location())
	}
	next := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func (b *Bridge) apiBackupStatus(w http.ResponseWriter, r *http.Request) {
	m := b.backup
	cfg := b.cfgs.Get()
	rc := map[string]any{"found": false, "install_hint": rcloneInstallHint()}
	if p, err := m.rclonePath(); err == nil {
		rc["found"], rc["path"], rc["version"] = true, p, rcloneVersion(p)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
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
			Exclude: nz(s.manifest.Backup.Exclude), Last: m.state.Brains[br.ID()]})
	}
	var job map[string]any
	if m.job != nil {
		job = map[string]any{"started": m.job.started, "current": m.job.current, "queue": m.job.queue, "done": m.job.done}
	}
	resp := map[string]any{
		"rclone":                rc,
		"connected":             m.connected(),
		"connect":               m.connect,
		"settings":              cfg.Backup,
		"include_bridge_config": boolOr(cfg.Backup.IncludeBridgeConfig, true),
		"brains":                rows,
		"bridge":                m.state.Bridge,
		"job":                   job,
	}
	if cfg.Backup.Enabled {
		resp["next_run"] = nextRun(cfg.Backup.DailyAt, time.Now())
	}
	writeJSON(w, http.StatusOK, resp)
}

var (
	rcloneVersionMu    sync.Mutex
	rcloneVersionCache = map[string]string{}
)

// rcloneVersion runs `rclone version` once per binary.
func rcloneVersion(p string) string {
	rcloneVersionMu.Lock()
	defer rcloneVersionMu.Unlock()
	if v, ok := rcloneVersionCache[p]; ok {
		return v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, p, "version").Output()
	v := ""
	if err == nil {
		v = strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	}
	rcloneVersionCache[p] = v
	return v
}

func rcloneInstallHint() string {
	switch {
	case fileExists("/opt/homebrew/bin/brew") || fileExists("/usr/local/bin/brew"):
		return "brew install rclone"
	case fileExists("/usr/bin/apt-get"):
		return "sudo apt-get install rclone   (or: sudo -v ; curl https://rclone.org/install.sh | sudo bash)"
	default:
		return "See https://rclone.org/install/"
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// ── connecting Google Drive ──────────────────────────────────────────────

var authURLRe = regexp.MustCompile(`http://127\.0\.0\.1:\d+/auth\?state=[A-Za-z0-9_-]+`)

// rcloneQuestion is one step of rclone's non-interactive config flow.
type rcloneQuestion struct {
	State  string `json:"State"`
	Option *struct {
		Name       string `json:"Name"`
		DefaultStr string `json:"DefaultStr"`
	} `json:"Option"`
	Error string `json:"Error"`
}

// answerFor decides how to answer each question rclone asks while creating
// the Drive remote: authorise in the browser on this machine, no shared drive.
func answerFor(q rcloneQuestion) string {
	if q.Option == nil {
		return ""
	}
	switch q.Option.Name {
	case "config_is_local":
		return "true"
	case "config_change_team_drive", "config_team_drive", "team_drive":
		return "false"
	}
	return q.Option.DefaultStr
}

func (b *Bridge) apiBackupConnect(w http.ResponseWriter, r *http.Request) {
	m := b.backup
	if _, err := m.rclonePath(); err != nil {
		writeErr(w, http.StatusPreconditionFailed, "rclone is not installed — "+rcloneInstallHint())
		return
	}
	m.mu.Lock()
	if m.connect != nil && m.connect.State == "running" {
		m.mu.Unlock()
		writeErr(w, http.StatusConflict, "already connecting — finish in the browser tab rclone opened")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	job := &connectJob{State: "running", cancel: cancel}
	m.connect = job
	m.mu.Unlock()

	go func() {
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
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "connecting"})
}

// connectDrive walks rclone's config questions. Answering config_is_local
// starts rclone's local OAuth listener and opens the browser; that step
// blocks until you approve access (or the 10 minutes run out).
func (m *BackupManager) connectDrive(ctx context.Context, job *connectJob) error {
	remote := m.cfgs.Get().Backup.Remote
	_, _ = m.runner(ctx, []string{"config", "delete", remote}, nil)

	stderr := &authURLWatcher{onURL: func(u string) {
		m.mu.Lock()
		job.AuthURL = u
		m.mu.Unlock()
	}}
	out, err := m.runner(ctx, []string{"config", "create", remote, "drive", "scope=drive.file", "--non-interactive"}, stderr)
	for step := 0; step < 10; step++ {
		if err != nil {
			return fmt.Errorf("rclone: %v %s", err, stderr.tail())
		}
		var q rcloneQuestion
		if jerr := json.Unmarshal(out, &q); jerr != nil {
			return fmt.Errorf("unexpected rclone output")
		}
		if q.Error != "" {
			return errors.New(q.Error)
		}
		if q.State == "" {
			break
		}
		out, err = m.runner(ctx, []string{"config", "update", remote, "--continue", "--state", q.State,
			"--result", answerFor(q), "--non-interactive"}, stderr)
	}
	if !m.connected() {
		return errors.New("Google Drive was not authorised")
	}
	// Prove the link works and create the backup folder.
	if _, err := m.runner(ctx, []string{"mkdir", remote + ":" + m.cfgs.Get().Backup.Root}, nil); err != nil {
		return fmt.Errorf("connected, but creating the backup folder failed: %v", err)
	}
	return nil
}

// authURLWatcher scans rclone's stderr for the browser link.
type authURLWatcher struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	onURL func(string)
	seen  bool
}

func (a *authURLWatcher) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.buf.Write(p)
	if !a.seen {
		if u := authURLRe.FindString(a.buf.String()); u != "" {
			a.seen = true
			a.onURL(u)
		}
	}
	return len(p), nil
}

func (a *authURLWatcher) tail() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := strings.TrimSpace(a.buf.String())
	if len(s) > 400 {
		s = s[len(s)-400:]
	}
	return s
}

func (b *Bridge) apiBackupDisconnect(w http.ResponseWriter, r *http.Request) {
	m := b.backup
	m.mu.Lock()
	if m.connect != nil && m.connect.cancel != nil {
		m.connect.cancel()
	}
	m.connect = nil
	m.mu.Unlock()
	_, _ = m.runner(r.Context(), []string{"config", "delete", b.cfgs.Get().Backup.Remote}, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "disconnected"})
}

func (b *Bridge) apiBackupSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled             *bool    `json:"enabled"`
		DailyAt             *string  `json:"daily_at"`
		Root                *string  `json:"root"`
		KeepVersionsDays    *int     `json:"keep_versions_days"`
		IncludeBridgeConfig *bool    `json:"include_bridge_config"`
		Brain               string   `json:"brain"`
		BrainEnabled        *bool    `json:"brain_enabled"`
		BrainExclude        []string `json:"brain_exclude"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_, err := b.cfgs.Update("", func(c *Config) error {
		if req.Enabled != nil {
			if *req.Enabled && !c.Backup.Enabled {
				b.backup.skipTodayIfPastDue(c.Backup.DailyAt, req.DailyAt)
			}
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
		if req.Brain == "" {
			return nil
		}
		return updateBrainBackup(c, req.Brain, req.BrainEnabled, req.BrainExclude)
	})
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// updateBrainBackup changes one brain's backup settings in its manifest.
func updateBrainBackup(c *Config, id string, enabled *bool, exclude []string) error {
	return updateManifestIn(c, id, func(m *BrainManifest) error {
		if enabled != nil {
			m.Backup.Enabled = *enabled
		}
		if exclude != nil {
			var clean []string
			for _, x := range exclude {
				if x = strings.TrimSpace(x); x != "" {
					clean = append(clean, x)
				}
			}
			m.Backup.Exclude = clean
		}
		return m.Validate()
	})
}

// ── running backups ──────────────────────────────────────────────────────

func (b *Bridge) apiBackupRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Brains        []string `json:"brains"`
		IncludeBridge *bool    `json:"include_bridge"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	include := boolOr(req.IncludeBridge, boolOr(b.cfgs.Get().Backup.IncludeBridgeConfig, true))
	if err := b.backup.StartRun(req.Brains, include, "manual"); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (b *Bridge) apiBackupCancel(w http.ResponseWriter, r *http.Request) {
	b.backup.mu.Lock()
	defer b.backup.mu.Unlock()
	if b.backup.job == nil {
		writeErr(w, http.StatusConflict, "no backup is running")
		return
	}
	b.backup.job.cancel()
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelling"})
}

// StartRun backs up the given brains (all with backup enabled when empty).
func (m *BackupManager) StartRun(ids []string, includeBridge bool, trigger string) error {
	if !m.connected() {
		return errors.New("Google Drive is not connected")
	}
	var targets []*Brain
	for _, br := range m.bm.Brains() {
		s := br.settings.Load()
		if len(ids) == 0 && !s.manifest.Backup.Enabled {
			continue
		}
		if len(ids) > 0 && !contains(ids, br.ID()) {
			continue
		}
		targets = append(targets, br)
	}
	m.mu.Lock()
	if m.job != nil {
		m.mu.Unlock()
		return errors.New("a backup is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &backupJob{started: time.Now(), cancel: cancel}
	for _, br := range targets {
		job.queue = append(job.queue, br.ID())
	}
	if includeBridge {
		job.queue = append(job.queue, "_bridge")
	}
	m.job = job
	m.mu.Unlock()

	go func() {
		defer cancel()
		for _, br := range targets {
			m.runOne(ctx, job, br.ID(), func() *BackupResult { return m.backupBrain(ctx, br, trigger) })
		}
		if includeBridge {
			m.runOne(ctx, job, "_bridge", func() *BackupResult { return m.backupBridge(ctx, trigger) })
		}
		if ctx.Err() == nil {
			m.prune(ctx, targets)
		}
		m.mu.Lock()
		m.job = nil
		m.mu.Unlock()
	}()
	return nil
}

func (m *BackupManager) runOne(ctx context.Context, job *backupJob, id string, fn func() *BackupResult) {
	m.mu.Lock()
	job.current = id
	if len(job.queue) > 0 {
		job.queue = job.queue[1:]
	}
	m.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	res := fn()
	m.mu.Lock()
	if id == "_bridge" {
		m.state.Bridge = res
	} else {
		m.state.Brains[id] = res
	}
	job.done = append(job.done, id)
	job.current = ""
	m.saveStateLocked()
	m.mu.Unlock()
	if res.OK {
		log.Printf("backup %s: ok, %d files, %.1f MB in %.0fs", id, res.Files, float64(res.Bytes)/1e6, res.Seconds)
	} else {
		log.Printf("backup %s: FAILED: %s", id, res.Error)
	}
}

// filterLines turns exclude globs into an rclone filter file.
func filterLines(excludes []string) string {
	var sb strings.Builder
	for _, x := range excludes {
		x = strings.TrimSpace(x)
		if x == "" || strings.ContainsAny(x, "\r\n") || strings.HasPrefix(x, "+") {
			continue
		}
		x = strings.TrimPrefix(x, "- ")
		if strings.HasSuffix(x, "/") {
			x += "**"
		}
		sb.WriteString("- " + x + "\n")
	}
	return sb.String()
}

func (m *BackupManager) backupBrain(ctx context.Context, br *Brain, trigger string) *BackupResult {
	s := br.settings.Load()
	filter := filepath.Join(m.dir(), "filters-"+br.ID()+".txt")
	if err := writeFileAtomic(filter, []byte(filterLines(s.manifest.Backup.Exclude)), 0o600, false); err != nil {
		return &BackupResult{Started: time.Now(), Finished: time.Now(), Error: err.Error(), Trigger: trigger}
	}
	return m.syncDir(ctx, br.Dir(), br.ID(), filter, trigger)
}

// backupBridge stages the bridge's own configuration and syncs it.
func (m *BackupManager) backupBridge(ctx context.Context, trigger string) *BackupResult {
	stage := filepath.Join(m.dir(), "_staging")
	_ = os.RemoveAll(stage)
	if err := os.MkdirAll(filepath.Join(stage, "brains"), 0o700); err != nil {
		return &BackupResult{Started: time.Now(), Finished: time.Now(), Error: err.Error(), Trigger: trigger}
	}
	file := m.cfgs.FileCopy()
	data, _ := json.MarshalIndent(file, "", "  ")
	_ = os.WriteFile(filepath.Join(stage, "config.json"), data, 0o600)
	for _, ref := range file.BrainDirs {
		if ref.Approved != nil {
			md, _ := json.MarshalIndent(ref.Approved, "", "  ")
			_ = os.WriteFile(filepath.Join(stage, "brains", ref.Approved.ID+".brain.json"), md, 0o600)
		}
	}
	readme := "Bridge configuration backup.\n\nTo restore on a new machine: put config.json at ~/.whatsapp-claude-bridge/config.json,\n" +
		"restore each brain folder from <brain-id>/current, and pair WhatsApp again from the dashboard.\n" +
		"The WhatsApp session, .env files and tokens are deliberately not backed up.\n"
	_ = os.WriteFile(filepath.Join(stage, "README.txt"), []byte(readme), 0o600)
	return m.syncDir(ctx, stage, "_bridge", "", trigger)
}

// syncDir runs one incremental sync with versioning.
func (m *BackupManager) syncDir(ctx context.Context, src, id, filterFile, trigger string) *BackupResult {
	cfg := m.cfgs.Get()
	res := &BackupResult{Started: time.Now(), Trigger: trigger, Version: time.Now().Format("20060102-150405")}
	base := cfg.Backup.Remote + ":" + cfg.Backup.Root + "/" + id
	args := []string{"sync", src + "/", base + "/current",
		"--backup-dir", base + "/versions/" + res.Version,
		"--links", "--use-json-log", "--stats", "15s", "--stats-log-level", "NOTICE", "-v",
		"--transfers", "4", "--checkers", "8", "--drive-chunk-size", "32M",
	}
	if filterFile != "" {
		args = append(args, "--filter-from", filterFile)
	}
	stderr := &jsonStats{}
	_, err := m.runner(ctx, args, stderr)
	res.Finished = time.Now()
	res.Seconds = res.Finished.Sub(res.Started).Seconds()
	res.Bytes, res.Files = stderr.bytes, stderr.files
	switch {
	case ctx.Err() != nil:
		res.Error = "cancelled"
	case err != nil:
		res.Error = strings.TrimSpace(err.Error() + " " + stderr.lastError())
	default:
		res.OK = true
	}
	return res
}

// jsonStats reads rclone's JSON log for transfer totals and errors.
type jsonStats struct {
	mu    sync.Mutex
	rest  string
	bytes int64
	files int64
	errs  []string
}

func (j *jsonStats) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	data := j.rest + string(p)
	lines := strings.Split(data, "\n")
	j.rest = lines[len(lines)-1]
	for _, l := range lines[:len(lines)-1] {
		var e struct {
			Level string `json:"level"`
			Msg   string `json:"msg"`
			Stats *struct {
				Bytes     int64 `json:"bytes"`
				Transfers int64 `json:"transfers"`
			} `json:"stats"`
		}
		if json.Unmarshal([]byte(l), &e) != nil {
			continue
		}
		if e.Stats != nil {
			j.bytes, j.files = e.Stats.Bytes, e.Stats.Transfers
		}
		if e.Level == "error" || e.Level == "critical" {
			j.errs = append(j.errs, e.Msg)
			if len(j.errs) > 5 {
				j.errs = j.errs[1:]
			}
		}
	}
	return len(p), nil
}

func (j *jsonStats) lastError() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.errs) == 0 {
		return ""
	}
	return truncate(j.errs[len(j.errs)-1], 300)
}

// prune deletes versions older than keep_versions_days.
func (m *BackupManager) prune(ctx context.Context, targets []*Brain) {
	cfg := m.cfgs.Get()
	days := intOr(cfg.Backup.KeepVersionsDays, defaultKeepVersions)
	if days <= 0 {
		return
	}
	ids := []string{"_bridge"}
	for _, br := range targets {
		ids = append(ids, br.ID())
	}
	for _, id := range ids {
		versions := cfg.Backup.Remote + ":" + cfg.Backup.Root + "/" + id + "/versions"
		_, _ = m.runner(ctx, []string{"delete", versions, "--min-age", fmt.Sprintf("%dd", days)}, nil)
		_, _ = m.runner(ctx, []string{"rmdirs", versions, "--leave-root"}, nil)
	}
}

// ── versions and restore ─────────────────────────────────────────────────

func (b *Bridge) apiBackupVersions(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("brain")
	if id == "" || strings.ContainsAny(id, "/:") {
		writeErr(w, http.StatusBadRequest, "brain required")
		return
	}
	cfg := b.cfgs.Get()
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	var stderr strings.Builder
	out, err := b.backup.runner(ctx, []string{"lsjson", "--dirs-only", cfg.Backup.Remote + ":" + cfg.Backup.Root + "/" + id + "/versions"}, &lockedWriter{w: &stderr})
	versions := []string{}
	if err != nil && !strings.Contains(strings.ToLower(stderr.String()), "not found") {
		writeErr(w, http.StatusBadGateway, "listing versions failed: "+truncate(strings.TrimSpace(stderr.String()+" "+err.Error()), 300))
		return
	}
	if err == nil {
		var entries []struct {
			Name string `json:"Name"`
		}
		_ = json.Unmarshal(out, &entries)
		for _, e := range entries {
			versions = append(versions, e.Name)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(versions)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"brain": id, "versions": versions})
}

// apiBackupRestore copies a backup into a new, empty folder. It never writes
// into an existing brain: you inspect the copy and add it as a brain (or
// swap folders) yourself.
func (b *Bridge) apiBackupRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Brain   string `json:"brain"`
		Version string `json:"version"` // "current" or a versions/<ts> name
		Target  string `json:"target"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Brain == "" || strings.ContainsAny(req.Brain+req.Version, "/:\\") || strings.Contains(req.Version, "..") {
		writeErr(w, http.StatusBadRequest, "invalid brain or version")
		return
	}
	cfg := b.cfgs.Get()
	home, _ := os.UserHomeDir()
	target := canonicalDir(expandHome(strings.TrimSpace(req.Target), home))
	if !filepath.IsAbs(target) || !isWithin(target, canonicalDir(home)) || target == canonicalDir(home) {
		writeErr(w, http.StatusUnprocessableEntity, "restore into a new folder inside your home folder")
		return
	}
	for _, d := range b.otherBrainDirs("") {
		d = canonicalDir(d)
		if isWithin(target, d) || isWithin(d, target) {
			writeErr(w, http.StatusUnprocessableEntity, "the restore folder overlaps the brain at "+d+" — pick a new folder")
			return
		}
	}
	if entries, err := os.ReadDir(target); err == nil && len(entries) > 0 {
		writeErr(w, http.StatusUnprocessableEntity, "the restore folder must be new or empty")
		return
	}
	src := cfg.Backup.Remote + ":" + cfg.Backup.Root + "/" + req.Brain + "/current"
	if req.Version != "" && req.Version != "current" {
		src = cfg.Backup.Remote + ":" + cfg.Backup.Root + "/" + req.Brain + "/versions/" + req.Version
	}
	m := b.backup
	m.mu.Lock()
	if m.job != nil {
		m.mu.Unlock()
		writeErr(w, http.StatusConflict, "a backup or restore is already running")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &backupJob{started: time.Now(), cancel: cancel, current: "restore:" + req.Brain}
	m.job = job
	m.mu.Unlock()
	go func() {
		defer cancel()
		_ = os.MkdirAll(target, 0o700)
		stderr := &jsonStats{}
		_, err := m.runner(ctx, []string{"copy", src, target, "--links", "--use-json-log", "-v"}, stderr)
		if err != nil {
			log.Printf("restore %s into %s FAILED: %v %s", req.Brain, target, err, stderr.lastError())
		} else {
			log.Printf("restore %s (%s) into %s: done", req.Brain, req.Version, target)
		}
		m.mu.Lock()
		m.job = nil
		m.mu.Unlock()
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "restoring", "target": target})
}

// ── scheduler ────────────────────────────────────────────────────────────

// Start runs the daily schedule. A run missed while the machine slept runs
// as soon as it is awake again (once per day).
func (m *BackupManager) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.stop = cancel
	m.mu.Unlock()
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				m.tick(now)
			}
		}
	}()
}

func (m *BackupManager) tick(now time.Time) {
	cfg := m.cfgs.Get()
	if !cfg.Backup.Enabled || !m.connected() {
		return
	}
	due, err := time.ParseInLocation("15:04", cfg.Backup.DailyAt, now.Location())
	if err != nil {
		return
	}
	today := now.Format("2006-01-02")
	dueToday := time.Date(now.Year(), now.Month(), now.Day(), due.Hour(), due.Minute(), 0, 0, now.Location())
	m.mu.Lock()
	last := m.state.LastScheduledDay
	busy := m.job != nil
	m.mu.Unlock()
	if now.Before(dueToday) || last == today || busy {
		return
	}
	if err := m.StartRun(nil, boolOr(cfg.Backup.IncludeBridgeConfig, true), "scheduled"); err != nil {
		log.Printf("scheduled backup: %v", err)
		return
	}
	m.mu.Lock()
	m.state.LastScheduledDay = today
	m.saveStateLocked()
	m.mu.Unlock()
}

// skipTodayIfPastDue makes a schedule switched on after today's time start
// tomorrow, instead of firing a backup the moment it is enabled.
func (m *BackupManager) skipTodayIfPastDue(current string, next *string) {
	at := current
	if next != nil {
		at = *next
	}
	if at == "" {
		at = defaultBackupDailyAt
	}
	now := time.Now()
	due, err := time.ParseInLocation("15:04", at, now.Location())
	if err != nil {
		return
	}
	dueToday := time.Date(now.Year(), now.Month(), now.Day(), due.Hour(), due.Minute(), 0, 0, now.Location())
	if now.After(dueToday) {
		m.mu.Lock()
		m.state.LastScheduledDay = now.Format("2006-01-02")
		m.saveStateLocked()
		m.mu.Unlock()
	}
}

// lockedWriter makes a writer safe for concurrent writes.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// Stop ends the scheduler and cancels a running backup.
func (m *BackupManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stop != nil {
		m.stop()
	}
	if m.job != nil {
		m.job.cancel()
	}
	if m.connect != nil && m.connect.cancel != nil {
		m.connect.cancel()
	}
}
