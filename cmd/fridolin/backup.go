package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"fridolin/internal/fsx"
	"fridolin/internal/textx"
	"golang.org/x/text/unicode/norm"
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
// Version folders are pruned keep_versions_days after the run that made them.
// A brain folder that lost many of its files, or most of one of its
// folders, is not mirrored without force (see checkMassDeletion), and a
// scheduled run that refuses tells the owner on WhatsApp (see
// noticeRefusal).

// bridgeBackupID stands for the bridge's own settings wherever backups
// list brains by id: in a job, in the backup state, and in Drive, where it
// is the folder name above, so it must not change. The dashboard holds it
// as BRIDGE_BACKUP_ID (web/assets/lib/brain.js) and shows it as "Bridge
// settings". It cannot clash with a brain: brain ids start with a letter
// or digit.
const bridgeBackupID = "_bridge"

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
	// RclonePath overrides the rclone binary (default: found in the PATH
	// folders helperPath keeps, else in rcloneFallbacks).
	RclonePath string `json:"rclone_path,omitempty"`
	// KeepVersionsDays is how long a version folder is kept after the run
	// that made it (default 30; 0 keeps them forever).
	KeepVersionsDays *int `json:"keep_versions_days,omitempty"`
	// IncludeBridgeConfig also backs up config.json and the brain list
	// (default true). Never included: the WhatsApp session, .env, tokens.
	IncludeBridgeConfig *bool `json:"include_bridge_config,omitempty"`
	// IncludeSecretFiles also uploads the files of backupSecretExcludes
	// from brain folders, which are otherwise left out whatever a brain's
	// excludes say. Off by default; config.json only.
	IncludeSecretFiles bool `json:"include_secret_files,omitempty"`
}

// backupSecretExcludes are the files of a brain folder a backup leaves out
// whatever the brain's exclude list says, unless include_secret_files is on:
// a brain may keep API keys in a .env, private keys, and a calendar token
// or CLI login copied into its folder, and a copy in Drive is one more
// place they can leak from. Every '+' line of an exclude list is dropped
// (see filterLines), so none can include them again. "*.key" is not among
// them: it is also Keynote's extension. docs/BACKUP.md and the dashboard's
// Backup page list them; TestBackupSecretExcludesAreDocumented keeps the
// docs in step.
var backupSecretExcludes = []string{
	".env", ".env.*",
	"*.pem", "*.p12",
	"id_rsa*", "id_ecdsa*", "id_ed25519*",
	"google_calendar_*.json",
	".credentials.json",
}

var (
	dailyAtRe    = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
	remoteNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
)

// defaultIncludeBridgeConfig is include_bridge_config's default.
const defaultIncludeBridgeConfig = true

// includeBridge and keepVersions read the two settings that are pointers so
// that an unset one can be told from false or 0. effective fills both in,
// with the same defaults, so the default of each lives in one place.
func (bc BackupConfig) includeBridge() bool {
	return boolOr(bc.IncludeBridgeConfig, defaultIncludeBridgeConfig)
}

func (bc BackupConfig) keepVersions() int { return intOr(bc.KeepVersionsDays, defaultKeepVersions) }

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
	// Refused is set when the run would have mirrored a mass deletion and
	// was stopped; a manual run with force mirrors it anyway.
	Refused bool `json:"refused,omitempty"`
}

type backupState struct {
	LastScheduledDay string `json:"last_scheduled_day"`
	// ArmedAt is when the daily backup was last switched on or moved to
	// another time. That day's run is due only if its time comes after
	// this, so neither starts a backup at once (see nextDue).
	ArmedAt time.Time                `json:"armed_at,omitzero"`
	Brains  map[string]*BackupResult `json:"brains"`
	Bridge  *BackupResult            `json:"bridge"`
	// LastRestore is how the last restore went, for the Backup page.
	LastRestore *RestoreResult `json:"last_restore,omitempty"`
	// RefusalNotices holds when the owner was told on WhatsApp that a
	// brain's scheduled backup refused a mass deletion; an entry stays until
	// a backup of that brain succeeds, so each refusal streak is told once.
	RefusalNotices map[string]time.Time `json:"refusal_notices,omitempty"`
}

// RestoreResult is the outcome of a restore.
type RestoreResult struct {
	Brain    string    `json:"brain"`
	Version  string    `json:"version"`
	Target   string    `json:"target"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
	Bytes    int64     `json:"bytes"`
	Files    int64     `json:"files"`
}

// The kinds of backupJob.
const (
	jobBackup  = "backup"
	jobRestore = "restore"
)

// backupJob is the backup or restore that runs; there is one at a time.
type backupJob struct {
	kind    string // jobBackup or jobRestore
	started time.Time
	// A backup's progress: the id being backed up, those still to come
	// and those done.
	current string
	queue   []string
	done    []string
	// A restore's brain and folder.
	brain, target string
	cancel        context.CancelFunc
}

// BackupManager runs and schedules backups.
type BackupManager struct {
	cfgs *ConfigStore
	bm   *BrainManager

	mu      sync.Mutex
	state   backupState
	job     *backupJob
	connect *connectJob
	// disconnecting is set while Disconnect removes the Drive link, so that
	// no backup, restore or sign-in starts on a remote about to go.
	disconnecting bool
	stop          context.CancelFunc
	// warnedDay is the day warnNotConnected last logged.
	warnedDay string
	// stopped is set by Stop: nothing new starts after it, so that wg is
	// not added to while Stop waits on it.
	stopped bool
	// wg counts the goroutines of the scheduler, a backup or restore job
	// and a Drive sign-in, for Stop to wait on.
	wg     sync.WaitGroup
	runner func(ctx context.Context, args []string, stderr io.Writer) ([]byte, error)
	// now is the clock of the schedule, a variable so tests can set it.
	now func() time.Time
	// notify sends a WhatsApp message about a brain to its owner. The bridge
	// sets it; nil sends nothing.
	notify func(ctx context.Context, br *Brain, text string) error
	// idleWait is how long a brain's backup waits for the brain to finish
	// what it is running; see backupBrain.
	idleWait time.Duration
}

// backupIdleWait is how long a brain's backup waits for the brain to go
// idle by default.
const backupIdleWait = 2 * time.Minute

// syncRetries and syncRetrySleep are how often, and after how long, rclone
// tries a sync again that ended with errors, such as a file that changed
// while it was copied ("source file is being updated").
const (
	syncRetries    = 3
	syncRetrySleep = 10 * time.Second
)

// refusalNoticeTimeout bounds the WhatsApp send of a refusal notice. It runs
// inside the backup job, so a send that hangs would keep the job running and
// Back up now disabled.
var refusalNoticeTimeout = 30 * time.Second

// NewBackupManager loads the saved backup state. A state file that cannot
// be read is kept as state.json.corrupt and the bridge starts with none: the
// last results and the day of the last scheduled run are gone then, and the
// log says so rather than leaving it to the next scheduled run to show.
func NewBackupManager(cfgs *ConfigStore, bm *BrainManager) *BackupManager {
	m := &BackupManager{cfgs: cfgs, bm: bm, now: time.Now, idleWait: backupIdleWait}
	m.runner = m.runRclone
	data, err := os.ReadFile(m.statePath())
	switch {
	case err == nil:
		if jerr := json.Unmarshal(data, &m.state); jerr != nil {
			m.state = backupState{}
			kept := m.statePath() + ".corrupt"
			if werr := fsx.WriteFileAtomic(kept, data, 0o600, false); werr != nil {
				kept = "not kept: " + werr.Error()
			}
			log.Printf("backup: %s is unreadable (%v); starting without the last results and schedule (the old file: %s)", m.statePath(), jerr, kept)
		}
	case !errors.Is(err, fs.ErrNotExist):
		log.Printf("backup: reading %s: %v; starting without the last results and schedule", m.statePath(), err)
	}
	if m.state.Brains == nil {
		m.state.Brains = map[string]*BackupResult{}
	}
	// After the change is saved, never while it is being made: a change
	// that validation or the write then refuses must not move the schedule.
	cfgs.Subscribe(func(prev, next *Config) {
		if next.Backup.Enabled && (!prev.Backup.Enabled || prev.Backup.DailyAt != next.Backup.DailyAt) {
			m.mu.Lock()
			m.state.ArmedAt = m.now()
			m.saveStateLocked()
			m.mu.Unlock()
		}
	})
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
		err = fsx.WriteFileAtomic(m.statePath(), data, 0o600, false)
	}
	if err != nil {
		log.Printf("backup: saving %s: %v", m.statePath(), err)
	}
}

// goLocked runs fn in a goroutine that Stop waits for, unless Stop already
// ran. Hold m.mu.
func (m *BackupManager) goLocked(fn func()) error {
	if m.stopped {
		return errBackupStopped
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		fn()
	}()
	return nil
}

// errBackupStopped refuses work asked for while the bridge stops.
var errBackupStopped = errors.New("the bridge is stopping")

// rclonePath finds the rclone binary.
func (m *BackupManager) rclonePath() (string, error) {
	return findRclone(m.cfgs.Get().Backup.RclonePath)
}

// findRclone finds the rclone binary, given backup.rclone_path. The sandbox
// profile uses it too, to keep brains from replacing the binary (see
// bridgeProgramsOf).
//
// A configured path must work as it is: falling back would quietly run
// another rclone, maybe another version, than the one you named, and hide
// the typo. A name without a folder, and rclone itself when none is
// configured, is looked up only in the PATH folders helperPath keeps:
// rclone runs outside the sandbox. Only without a configured one does it
// look further, where launchd's PATH lacks Homebrew's folder and rclone's
// installer's.
func findRclone(p string) (string, error) {
	if p != "" {
		if !strings.Contains(p, "/") {
			if found := lookPathIn(p, helperPath()); found != "" {
				return found, nil
			}
			return "", fmt.Errorf("backup.rclone_path %s: %w", p, exec.ErrNotFound)
		}
		found, err := exec.LookPath(p)
		if err != nil {
			return "", fmt.Errorf("backup.rclone_path %s: %w", p, err)
		}
		return found, nil
	}
	if found := lookPathIn("rclone", helperPath()); found != "" {
		return found, nil
	}
	for _, c := range rcloneFallbacks() {
		// For an absolute path LookPath only checks that it is an
		// executable file: one that merely exists would fail to run.
		if found, err := exec.LookPath(c); err == nil {
			return found, nil
		}
	}
	return "", errRcloneMissing
}

// errRcloneMissing is findRclone's error when no rclone_path is set and no
// rclone is found.
var errRcloneMissing = errors.New("rclone is not installed")

// rcloneFallbacks are where findRclone looks when rclone is not in the
// folders of PATH it looks in first, a variable so tests can point it at
// their own files.
var rcloneFallbacks = func() []string {
	return []string{"/opt/homebrew/bin/rclone", "/usr/local/bin/rclone", filepath.Join(homeDir(), ".local/bin/rclone")}
}

// rcloneMissingHint says what to do about findRclone's error: install
// rclone, or fix backup.rclone_path, whose error names the path.
func rcloneMissingHint(err error) string {
	if errors.Is(err, errRcloneMissing) {
		return err.Error() + " — " + rcloneInstallHint()
	}
	return err.Error()
}

// rcloneEnv is the environment for rclone run as scope (see helperScope):
// what childEnv gives it, and the bridge's own config file. None of your
// RCLONE_* settings are on childEnvAllowlist, and child_env_passthrough is
// for brains, so rclone gets neither.
func (m *BackupManager) rcloneEnv(scope childScope) []string {
	return childEnv(scope, nil, "RCLONE_CONFIG="+m.rcloneConf())
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
	// A cancel interrupts rclone as Ctrl-C would, so that it ends its
	// transfers and removes the partial files they leave (a restore's in
	// the target folder above all). It is killed only if it is still
	// there rcloneStopGrace later.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = rcloneStopGrace
	cmd.Env = m.rcloneEnv(helperScope(ctx))
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

// rcloneStopGrace is how long a cancelled rclone has to exit after its
// interrupt; backupStopWait is how long Stop waits for the cancelled work,
// a little more, so that a run's result is saved after its rclone is gone.
var (
	rcloneStopGrace = 5 * time.Second
	backupStopWait  = rcloneStopGrace + 3*time.Second
)

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
	Restoring bool       `json:"restoring,omitempty"` // what runs is a restore, not a backup
	LastOK    *time.Time `json:"last_ok,omitempty"`
	LastError string     `json:"last_error,omitempty"`
	// LastErrorBrain is whose backup LastError is: a brain's id, or
	// bridgeBackupID for the bridge's settings.
	LastErrorBrain string     `json:"last_error_brain,omitempty"`
	NextRun        *time.Time `json:"next_run,omitempty"`
}

// Summary is the short version shown on the overview. It looks at the
// brains that run and the bridge's settings, as the Backup page does, and
// its error is the latest failure among them, whatever order the results
// are stored in: with several, a refresh must not show another each time.
func (m *BackupManager) Summary() backupSummary {
	cfg := m.cfgs.Get()
	brains := m.bm.Brains()
	m.mu.Lock()
	defer m.mu.Unlock()
	s := backupSummary{Connected: m.connected(), Enabled: cfg.Backup.Enabled, Running: m.job != nil}
	s.Restoring = s.Running && m.job.kind == jobRestore
	results := map[string]*BackupResult{bridgeBackupID: m.state.Bridge}
	for _, br := range brains {
		results[br.ID()] = m.state.Brains[br.ID()]
	}
	var failed *BackupResult
	for id, r := range results {
		switch {
		case r == nil:
		case r.OK:
			if s.LastOK == nil || r.Finished.After(*s.LastOK) {
				t := r.Finished
				s.LastOK = &t
			}
		case r.Error != "":
			if failed == nil || r.Finished.After(failed.Finished) || r.Finished.Equal(failed.Finished) && id < s.LastErrorBrain {
				failed, s.LastErrorBrain = r, id
			}
		}
	}
	if failed != nil {
		s.LastError = failed.Error
	}
	s.NextRun = m.nextRunLocked(cfg, s.Connected)
	return s
}

// forgetRemovedLocked drops the results of brains that are no longer
// configured, so that state.json does not keep them for good and a new
// brain that reuses an id does not start with another's last result. A
// brain whose folder is missing for now is still configured, and keeps
// its result. Hold m.mu.
func (m *BackupManager) forgetRemovedLocked(cfg *Config) {
	configured := map[string]bool{}
	for _, ref := range cfg.BrainDirs {
		if ref.Approved != nil {
			configured[ref.Approved.ID] = true
		}
	}
	for id := range m.state.Brains {
		if !configured[id] {
			delete(m.state.Brains, id)
		}
	}
}

// dayLayout names a day in backupState.LastScheduledDay.
const dayLayout = "2006-01-02"

// dueOn is daily_at on day's date, in day's time zone. On a day whose
// clocks skip that time it is the moment the clocks jump to, as time.Date
// has it. daily_at is valid HH:MM: effective refuses anything else.
func dueOn(day time.Time, dailyAt string) time.Time {
	t, _ := time.Parse("15:04", dailyAt)
	y, m, d := day.Date()
	return time.Date(y, m, d, t.Hour(), t.Minute(), 0, 0, day.Location())
}

// nextDue is the daily schedule, for the scheduler and the status page
// alike: the next scheduled run is today's at daily_at, unless it already
// ran today (lastRunDay) or the schedule was switched on or moved after
// today's time (armedAt). Then it is tomorrow's. runNow says that today's
// is due: the time has come, perhaps while the machine slept.
func nextDue(now time.Time, dailyAt, lastRunDay string, armedAt time.Time) (due time.Time, runNow bool) {
	due = dueOn(now, dailyAt)
	if lastRunDay == now.Format(dayLayout) || !due.After(armedAt) {
		y, m, d := now.Date()
		return dueOn(time.Date(y, m, d+1, 12, 0, 0, 0, now.Location()), dailyAt), false
	}
	return due, !now.Before(due)
}

// nextRunLocked is when the next scheduled backup runs, for the status:
// none while the schedule is off or Drive is not connected, since it would
// never come, and now for a run that is due and only waits for the next
// tick. Hold m.mu.
func (m *BackupManager) nextRunLocked(cfg *Config, connected bool) *time.Time {
	if !cfg.Backup.Enabled || !connected {
		return nil
	}
	now := m.now()
	due, runNow := nextDue(now, cfg.Backup.DailyAt, m.state.LastScheduledDay, m.state.ArmedAt)
	if runNow {
		due = now
	}
	return &due
}

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
		"include_bridge_config": cfg.Backup.includeBridge(),
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
	writeJSON(w, http.StatusOK, resp)
}

// rcloneVersionKey names one build of rclone: an upgrade in place (brew
// upgrade) keeps the path but changes the file.
type rcloneVersionKey struct {
	path    string
	modTime time.Time
	size    int64
}

// rcloneVersions caches rcloneVersion's answers by rcloneVersionKey.
var rcloneVersions sync.Map

// rcloneVersion runs `rclone version` once per build of rclone at p, with
// env. No lock is held while it runs: the status page asks on every poll,
// and a slow first run must not hold up the others.
func rcloneVersion(p string, env []string) string {
	fi, err := os.Stat(p)
	if err != nil {
		return ""
	}
	key := rcloneVersionKey{path: p, modTime: fi.ModTime(), size: fi.Size()}
	if v, ok := rcloneVersions.Load(key); ok {
		return v.(string)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p, "version")
	cmd.Env = env
	out, err := cmd.Output()
	v := ""
	if err == nil {
		v = strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	}
	rcloneVersions.Store(key, v)
	return v
}

func rcloneInstallHint() string {
	switch {
	case fsx.Exists("/opt/homebrew/bin/brew") || fsx.Exists("/usr/local/bin/brew"):
		return "brew install rclone"
	case fsx.Exists("/usr/bin/apt-get"):
		return "sudo apt-get install rclone   (or: sudo -v ; curl https://rclone.org/install.sh | sudo bash)"
	default:
		return "See https://rclone.org/install/"
	}
}

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
		writeErr(w, http.StatusPreconditionFailed, rcloneMissingHint(err))
		return
	}
	// Connecting starts by deleting the remote, so it would throw away a
	// working link, for good if the new sign-in is then abandoned, and a
	// backup running on it would fail half-way. Disconnect comes first.
	if m.connected() {
		writeErr(w, http.StatusConflict, "Google Drive is already connected — disconnect it first to connect another account")
		return
	}
	m.mu.Lock()
	if m.connect != nil && m.connect.State == "running" {
		m.mu.Unlock()
		writeErr(w, http.StatusConflict, "already connecting — finish in the browser tab rclone opened")
		return
	}
	if m.job != nil {
		m.mu.Unlock()
		writeErr(w, http.StatusConflict, "a backup or restore is running — wait for it or cancel it first")
		return
	}
	if m.disconnecting {
		m.mu.Unlock()
		writeErr(w, http.StatusConflict, errDisconnecting.Error())
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
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	m.connect = job
	m.mu.Unlock()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "connecting"})
}

// connectTimeout is how long a Drive sign-in waits for you to approve it.
const connectTimeout = 10 * time.Minute

// connectSteps bounds rclone's config questions: a Drive remote takes a
// handful, so more means rclone asks something answerFor does not know.
const connectSteps = 10

// connectDrive walks rclone's config questions. Answering config_is_local
// starts rclone's local OAuth listener and opens the browser, which is why
// its runs get your desktop session's variables; that step blocks until you
// approve access (or connectTimeout runs out).
func (m *BackupManager) connectDrive(ctx context.Context, job *connectJob) error {
	ctx = withDesktop(ctx)
	remote := m.cfgs.Get().Backup.Remote
	// What is left of a remote that is not connected, one whose sign-in was
	// abandoned say, would make config create fail. apiBackupConnect keeps
	// a connected one from getting here.
	if _, err := m.runner(ctx, []string{"config", "delete", remote}, nil); err != nil {
		return fmt.Errorf("removing the unfinished Drive link: %w", err)
	}

	stderr := &authURLWatcher{onURL: func(u string) {
		m.mu.Lock()
		job.AuthURL = u
		m.mu.Unlock()
	}}
	args := []string{"config", "create", remote, "drive", "scope=drive.file", "--non-interactive"}
	for step := 1; ; step++ {
		out, err := m.runner(ctx, args, stderr)
		if err != nil {
			switch {
			case errors.Is(ctx.Err(), context.DeadlineExceeded):
				return fmt.Errorf("access was not approved within %v — try again", connectTimeout)
			case ctx.Err() != nil:
				return errors.New("connecting was cancelled")
			}
			return fmt.Errorf("rclone: %w %s", err, stderr.tail())
		}
		var q rcloneQuestion
		if err := json.Unmarshal(out, &q); err != nil {
			return fmt.Errorf("unexpected rclone output: %w %s", err, stderr.tail())
		}
		if q.Error != "" {
			return errors.New(q.Error)
		}
		if q.State == "" {
			break
		}
		if step >= connectSteps {
			return fmt.Errorf("rclone asked more than %d questions; set the remote up by hand (docs/BACKUP.md, Other storage)", connectSteps)
		}
		args = []string{"config", "update", remote, "--continue", "--state", q.State, "--result", answerFor(q), "--non-interactive"}
	}
	if !m.connected() {
		return errors.New("access to Google Drive was not authorised")
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
	return textx.Tail(a.buf.String(), 400)
}

// errDisconnecting refuses what would start on the Drive link while
// Disconnect removes it.
var errDisconnecting = errors.New("the Google Drive link is being removed — wait for Disconnect to finish")

func (b *Bridge) apiBackupDisconnect(w http.ResponseWriter, r *http.Request) {
	m := b.backup
	m.mu.Lock()
	switch {
	case m.job != nil:
		m.mu.Unlock()
		writeErr(w, http.StatusConflict, "a backup or restore is running — wait for it or cancel it first")
		return
	case m.disconnecting:
		m.mu.Unlock()
		writeErr(w, http.StatusConflict, errDisconnecting.Error())
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
			writeErr(w, http.StatusInternalServerError, "the Google Drive sign-in did not stop — try again in a moment")
			return
		}
	}
	m.mu.Lock()
	if m.connect == job {
		m.connect = nil
	}
	m.mu.Unlock()
	if _, err := m.runner(r.Context(), []string{"config", "delete", b.cfgs.Get().Backup.Remote}, nil); err != nil {
		writeErr(w, http.StatusInternalServerError, "removing the Google Drive link failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "disconnected"})
}

func (b *Bridge) apiBackupSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled             *bool   `json:"enabled"`
		DailyAt             *string `json:"daily_at"`
		Root                *string `json:"root"`
		KeepVersionsDays    *int    `json:"keep_versions_days"`
		IncludeBridgeConfig *bool   `json:"include_bridge_config"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_, err := b.cfgs.Update("", func(c *Config) error {
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
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// apiBrainBackup changes one brain's backup settings, which live in its
// manifest: whether it is backed up and what is left out.
func (b *Bridge) apiBrainBackup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool    `json:"enabled"`
		Exclude []string `json:"exclude"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := b.cfgs.UpdateTx("", func(c *Config, tx *Tx) error {
		return updateBrainBackup(c, tx, r.PathValue("id"), req.Enabled, req.Exclude)
	}); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// updateBrainBackup changes one brain's backup settings in its manifest.
func updateBrainBackup(c *Config, tx *Tx, id string, enabled *bool, exclude []string) error {
	return updateManifestIn(c, tx, id, func(m *BrainManifest) error {
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
		// Force mirrors the named brains even when most of their backed-up
		// files are gone (see checkMassDeletion).
		Force bool `json:"force"`
	}
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Forcing is a decision about one brain's folder, made after looking at
	// it; without a list it would mirror every brain's deletions at once.
	if req.Force && len(req.Brains) == 0 {
		writeErr(w, http.StatusBadRequest, "force needs a brain: name the brains to mirror in \"brains\"")
		return
	}
	include := boolOr(req.IncludeBridge, b.cfgs.Get().Backup.includeBridge())
	if err := b.backup.StartRun(req.Brains, include, "manual", req.Force); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (b *Bridge) apiBackupCancel(w http.ResponseWriter, r *http.Request) {
	b.backup.mu.Lock()
	job := b.backup.job
	if job != nil {
		job.cancel()
	}
	b.backup.mu.Unlock()
	if job == nil {
		writeErr(w, http.StatusConflict, "no backup is running")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelling"})
}

// copyOf returns a copy of *p, or nil: what the status shows of state a
// job goes on changing once the lock is released.
func copyOf[T any](p *T) *T {
	if p == nil {
		return nil
	}
	c := *p
	return &c
}

// StartRun backs up the given brains (all with backup enabled when empty).
// force skips the mass-deletion check, for a manual run that mirrors a
// deletion the owner meant.
func (m *BackupManager) StartRun(ids []string, includeBridge bool, trigger string, force bool) error {
	if !m.connected() {
		return errors.New("not connected to Google Drive")
	}
	var targets []*Brain
	for _, br := range m.bm.Brains() {
		s := br.settings.Load()
		if len(ids) == 0 && !s.manifest.Backup.Enabled {
			continue
		}
		if len(ids) > 0 && !slices.Contains(ids, br.ID()) {
			continue
		}
		targets = append(targets, br)
	}
	m.mu.Lock()
	if m.job != nil {
		m.mu.Unlock()
		return errors.New("a backup is already running")
	}
	if m.disconnecting {
		m.mu.Unlock()
		return errDisconnecting
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &backupJob{kind: jobBackup, started: time.Now(), cancel: cancel}
	for _, br := range targets {
		job.queue = append(job.queue, br.ID())
	}
	if includeBridge {
		job.queue = append(job.queue, bridgeBackupID)
	}
	err := m.goLocked(func() {
		defer cancel()
		for _, br := range targets {
			if res := m.runOne(ctx, job, br.ID(), func() *BackupResult { return m.backupBrain(ctx, br, trigger, force) }); res != nil {
				m.noticeRefusal(ctx, br, res)
			}
		}
		if includeBridge {
			m.runOne(ctx, job, bridgeBackupID, func() *BackupResult { return m.backupBridge(ctx, trigger) })
		}
		if ctx.Err() == nil {
			m.prune(ctx, targets)
		}
		m.mu.Lock()
		m.job = nil
		m.mu.Unlock()
	})
	if err != nil {
		m.mu.Unlock()
		cancel()
		return err
	}
	m.job = job
	m.mu.Unlock()
	return nil
}

// runOne runs and records one backup. It returns its result, or nil when
// the job was cancelled before it started.
func (m *BackupManager) runOne(ctx context.Context, job *backupJob, id string, fn func() *BackupResult) *BackupResult {
	m.mu.Lock()
	job.current = id
	if len(job.queue) > 0 {
		job.queue = job.queue[1:]
	}
	m.mu.Unlock()
	if ctx.Err() != nil {
		return nil
	}
	res := fn()
	m.mu.Lock()
	if id == bridgeBackupID {
		m.state.Bridge = res
	} else {
		m.state.Brains[id] = res
	}
	job.done = append(job.done, id)
	job.current = ""
	m.forgetRemovedLocked(m.cfgs.Get())
	m.saveStateLocked()
	m.mu.Unlock()
	if res.OK {
		log.Printf("backup %s: ok, %d files, %.1f MB in %.0fs", id, res.Files, float64(res.Bytes)/1e6, res.Seconds)
	} else {
		log.Printf("backup %s: FAILED: %s", id, res.Error)
	}
	return res
}

// noticeRefusal tells the owner on WhatsApp when a scheduled backup refused
// to mirror a mass deletion. Otherwise the refusal shows only on the
// dashboard and repeats every night unseen, while the copies of the lost
// files in versions/ get closer to expiring. It is told once per streak:
// later refusals stay quiet until a backup of the brain succeeds. A notice
// that could not be sent is tried again with the next scheduled refusal.
func (m *BackupManager) noticeRefusal(ctx context.Context, br *Brain, res *BackupResult) {
	id := br.ID()
	live := map[string]bool{}
	for _, b := range m.bm.Brains() {
		live[b.ID()] = true
	}
	m.mu.Lock()
	changed := false
	// A removed brain's entry would stay forever, and keep a new brain that
	// reuses its id from being told about its first refusal.
	for k := range m.state.RefusalNotices {
		if !live[k] {
			delete(m.state.RefusalNotices, k)
			changed = true
		}
	}
	_, told := m.state.RefusalNotices[id]
	if res.OK && told {
		delete(m.state.RefusalNotices, id)
		changed = true
	}
	if changed {
		m.saveStateLocked()
	}
	send := res.Refused && res.Trigger == "scheduled" && !told && m.notify != nil
	m.mu.Unlock()
	if !send {
		return
	}
	name := br.settings.Load().name
	if name == "" {
		name = id
	}
	sctx, cancel := context.WithTimeout(ctx, refusalNoticeTimeout)
	err := m.notify(sctx, br, backupRefusedAlert(name, res.Error))
	cancel()
	if err != nil {
		log.Printf("backup %s: could not send the refusal notice: %v", id, err)
		return
	}
	m.mu.Lock()
	if m.state.RefusalNotices == nil {
		m.state.RefusalNotices = map[string]time.Time{}
	}
	m.state.RefusalNotices[id] = time.Now()
	m.saveStateLocked()
	m.mu.Unlock()
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

// brainFilter is the rclone filter file for a brain folder: the secret
// files first, unless includeSecrets, then the brain's own excludes. rclone
// stops at the first rule that matches.
func brainFilter(excludes []string, includeSecrets bool) string {
	if includeSecrets {
		return filterLines(excludes)
	}
	return filterLines(backupSecretExcludes) + filterLines(excludes)
}

func (m *BackupManager) backupBrain(ctx context.Context, br *Brain, trigger string, force bool) *BackupResult {
	started := time.Now()
	fail := func(msg string) *BackupResult {
		now := time.Now()
		return &BackupResult{Started: started, Finished: now, Seconds: now.Sub(started).Seconds(), Error: msg, Trigger: trigger}
	}
	// A run changes the brain's folder as it goes: the bridge appends to its
	// history, Claude edits memories and maybe a git repository. A sync of
	// a folder mid-run copies some files before the change and some after,
	// a .git index newer than its objects, say. So wait for the brain to
	// finish, but not for ever: a brain busy for hours must still be backed
	// up, and an append that comes in during the sync is what rclone's
	// retries are for (see syncDir).
	if !br.waitIdle(ctx, m.idleWait) && ctx.Err() == nil {
		log.Printf("backup %s: the brain is still running after %v, so its folder is copied while it changes", br.ID(), m.idleWait)
	}
	s := br.settings.Load()
	// One config snapshot for the filter, the check and the sync, so a
	// backup.root changed mid-run cannot count one folder and sync into
	// another.
	cfg := m.cfgs.Get()
	filter := filepath.Join(m.dir(), "filters-"+br.ID()+".txt")
	if err := fsx.WriteFileAtomic(filter, []byte(brainFilter(s.manifest.Backup.Exclude, cfg.Backup.IncludeSecretFiles)), 0o600, false); err != nil {
		return fail(err.Error())
	}
	base := backupPath(cfg.Backup, br.ID())
	if force {
		return m.syncDir(ctx, br.Dir(), base, filter, trigger, -1)
	}
	check, err := m.checkMassDeletion(ctx, br.Dir(), base, filter)
	refuse := func(what string) *BackupResult {
		res := fail(fmt.Sprintf("%s: %s, so the backup in Drive was left as it was. "+
			"If the deletion was intended, mirror it with a forced backup of this brain.", massDeletionError, what))
		res.Refused = true
		return res
	}
	switch {
	case ctx.Err() != nil:
		return fail("cancelled")
	case errors.Is(err, errListingBrain):
		// A folder rclone could only partly read cannot be counted, but
		// skipping its sync would leave its readable new and changed files
		// out of the backup as well. Sync it with no deletion allowed:
		// rclone uploads what it can read and fails on the rest.
		res := m.syncDir(ctx, br.Dir(), base, filter, trigger, 0)
		if !res.OK && !res.Refused && ctx.Err() == nil {
			// rclone's own error only says it deleted nothing because of
			// read errors; the listing's names what it could not read.
			res.Error = "parts of the brain folder could not be read, so the backup synced the rest and deleted nothing: " + err.Error()
		}
		return res
	case err != nil:
		return fail("checking the backup before syncing failed: " + err.Error())
	case check.gone > maxDeletions(check.backedUp):
		return refuse(fmt.Sprintf("%d of %d backed-up files are gone from the brain folder", check.gone, check.backedUp))
	case check.folder != "":
		return refuse(fmt.Sprintf("%d of the %d backed-up files in %s/ are gone from the brain folder", check.folderGone, check.folderFiles, check.folder))
	}
	// The check and the sync are moments apart; --max-delete keeps a brain
	// that deletes files in between from slipping past the limit.
	return m.syncDir(ctx, br.Dir(), base, filter, trigger, maxDeletions(check.backedUp))
}

// massDeletionError starts the error of a run that refused to mirror a
// mass deletion.
const massDeletionError = "refusing to mirror a mass deletion"

// What a brain's backup mirrors without being forced (see
// checkMassDeletion). It refuses when more than half of the backed-up files
// are gone, however few there are (an empty or missing brain folder
// included); when more than a fifth are, once that is at least ten; when
// 250 or more are; and when a folder at the top of the brain folder with at
// least five backed-up files lost more than half of them. A wipe need not
// empty the folder: a brain told to delete its memories/ leaves the rest,
// which in a large folder is most of it. maxDeletions applies the counts,
// which the sync also gets as --max-delete.
const (
	massDeletionShare      = 5   // more than 1/5 of the files gone…
	massDeletionMinFiles   = 10  // …once at least this many are, is refused
	massDeletionMaxFiles   = 250 // this many gone is refused however many there are
	massDeletionFolderSize = 5   // a top folder this large may lose half, no more
)

// maxDeletions is the most of backedUp files that a brain's backup lets go
// without being forced (see massDeletionShare).
func maxDeletions(backedUp int) int {
	return min(backedUp/2, max(massDeletionMinFiles-1, backedUp/massDeletionShare), massDeletionMaxFiles-1)
}

// deletionCheck is what checkMassDeletion found.
type deletionCheck struct {
	// gone of the backedUp files in current/ are no longer in the brain
	// folder: the sync would delete them.
	gone, backedUp int
	// folder is the folder at the top of the brain folder that lost the
	// most files of those with at least massDeletionFolderSize backed-up
	// files that lost more than half: folderGone of its folderFiles. ""
	// when none did.
	folder                  string
	folderGone, folderFiles int
}

// checkMassDeletion compares a brain folder with its backup under base
// (remote:root/<brain-id>) before a sync.
// A folder that suddenly lacks many of its files, or most of one of its
// folders, was more likely wiped (a brain told to rm -rf, an unmounted
// disk, a botched move) than tidied, and a plain sync would mirror that
// into current/ and report OK; the copies in versions/ would then expire
// unnoticed. It counts the backed-up files the sync would delete, in all
// and per top folder; the caller refuses above the limits of
// maxDeletions and massDeletionFolderSize. Both sides are listed with the
// sync's own filters and symlink handling, so excluded files, which the
// sync never deletes, do not count.
func (m *BackupManager) checkMassDeletion(ctx context.Context, src, base, filterFile string) (deletionCheck, error) {
	// Only a brain folder that is not there at all counts as empty (all of
	// it gone). One that rclone could only partly read (a subfolder it may
	// not open, a folder removed while it was listed) is never reported as
	// wiped: that fails with errListingBrain, and the caller syncs it
	// without deleting anything.
	var local []string
	var localErr error
	if _, serr := os.Stat(src); !errors.Is(serr, fs.ErrNotExist) {
		if local, localErr = m.listFiles(ctx, src+"/", filterFile); localErr != nil {
			localErr = fmt.Errorf("%w: %w", errListingBrain, localErr)
		}
	}
	// No current/ yet is a first backup: nothing to delete. A backup that
	// cannot be listed fails the check either way; a sync would most likely
	// fail on it too.
	remote, err := m.listFiles(ctx, base+"/current", filterFile)
	if err != nil && !errors.Is(err, errDirNotFound) {
		return deletionCheck{}, fmt.Errorf("listing the backup: %w", err)
	}
	if localErr != nil {
		return deletionCheck{backedUp: len(remote)}, localErr
	}
	// rclone sync matches names whatever their Unicode normalization (an
	// "ü" stored decomposed on one side and composed on the other is the
	// same file), but lsjson returns the bytes as stored; compare the way
	// the sync does.
	have := make(map[string]bool, len(local))
	for _, p := range local {
		have[norm.NFC.String(p)] = true
	}
	c := deletionCheck{backedUp: len(remote)}
	files, gone := map[string]int{}, map[string]int{}
	for _, p := range remote {
		p = norm.NFC.String(p)
		top, _, inFolder := strings.Cut(p, "/")
		if inFolder {
			files[top]++
		}
		if !have[p] {
			c.gone++
			if inFolder {
				gone[top]++
			}
		}
	}
	for _, top := range slices.Sorted(maps.Keys(gone)) {
		if files[top] >= massDeletionFolderSize && gone[top]*2 > files[top] && gone[top] > c.folderGone {
			c.folder, c.folderGone, c.folderFiles = top, gone[top], files[top]
		}
	}
	return c, nil
}

// errDirNotFound is listFiles' error for a folder that does not exist.
var errDirNotFound = errors.New("directory not found")

// errListingBrain marks checkMassDeletion's error for a brain folder that is
// there but that rclone could not list in full.
var errListingBrain = errors.New("listing the brain folder")

// listFiles lists the paths of the files under an rclone path.
func (m *BackupManager) listFiles(ctx context.Context, path, filterFile string) ([]string, error) {
	args := []string{"lsjson", "-R", "--files-only", "--no-modtime", "--no-mimetype", "--links", path}
	if filterFile != "" {
		args = append(args, "--filter-from", filterFile)
	}
	var stderr strings.Builder
	out, err := m.runner(ctx, args, &stderr)
	if err != nil {
		if strings.Contains(strings.ToLower(stderr.String()), "directory not found") {
			return nil, errDirNotFound
		}
		msg := textx.Truncate(strings.TrimSpace(stderr.String()), 300)
		if msg == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", err, msg)
	}
	var entries []struct {
		Path string `json:"Path"`
	}
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("reading rclone's file list: %w", err)
	}
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		paths = append(paths, e.Path)
	}
	return paths, nil
}

// backupBridge stages the bridge's own configuration and syncs it. A
// staging folder that could not be written in full is not synced: the
// sync would move the good copies in Drive to versions/ and report OK
// without them.
func (m *BackupManager) backupBridge(ctx context.Context, trigger string) *BackupResult {
	started := time.Now()
	stage := filepath.Join(m.dir(), "_staging")
	if err := m.stageBridge(stage); err != nil {
		now := time.Now()
		return &BackupResult{Started: started, Finished: now, Seconds: now.Sub(started).Seconds(), Trigger: trigger,
			Error: "staging the bridge's settings failed, so nothing was synced: " + err.Error()}
	}
	// No deletion limit: the bridge writes this folder itself, and removing
	// a brain drops its manifest, a deletion the backup must mirror.
	cfg := m.cfgs.Get()
	return m.syncDir(ctx, stage, backupPath(cfg.Backup, bridgeBackupID), "", trigger, -1)
}

// writeStaged writes one file into the staging folder, a variable so tests
// can make a write fail.
var writeStaged = os.WriteFile

// stageBridge writes config.json, the approved manifests and a README
// into a fresh staging folder, and returns every error on the way.
func (m *BackupManager) stageBridge(stage string) error {
	// What the last run staged must go, or a removed brain's manifest
	// would stay in the backup.
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(stage, "brains"), 0o700); err != nil {
		return err
	}
	file := m.cfgs.FileCopy()
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	errs := []error{writeStaged(filepath.Join(stage, "config.json"), data, 0o600)}
	for _, ref := range file.BrainDirs {
		if ref.Approved == nil {
			continue
		}
		md, err := json.MarshalIndent(ref.Approved, "", "  ")
		if err == nil {
			err = writeStaged(filepath.Join(stage, "brains", ref.Approved.ID+".brain.json"), md, 0o600)
		}
		errs = append(errs, err)
	}
	readme := "Bridge configuration backup.\n\nTo restore on a new machine: put config.json at the default config location\n" +
		"(~/.fridolin/config.json), restore each brain folder from <brain-id>/current, and pair WhatsApp\n" +
		"again from the dashboard.\n" +
		"The WhatsApp session, .env files and tokens are deliberately not backed up.\n"
	errs = append(errs, writeStaged(filepath.Join(stage, "README.txt"), []byte(readme), 0o600))
	return errors.Join(errs...)
}

// versionLayout names a version folder after the time of its run, in UTC
// (versionName). Local time comes twice in the hour the clocks go back, and
// would sort a later run's folder before an earlier one's or give two runs
// one name. legacyVersionLayout is the local time the folders made before
// are named in. Prune and the restore list read the run time back out of
// either.
const (
	versionLayout       = "20060102-150405Z"
	legacyVersionLayout = "20060102-150405"
)

// versionName is the version folder name of a run that started at t.
func versionName(t time.Time) string { return t.UTC().Format(versionLayout) }

// syncDir runs one incremental sync with versioning into base
// (remote:root/<id>). With maxDelete >= 0, rclone stops after that many
// deletions and the run is refused as a mass deletion.
func (m *BackupManager) syncDir(ctx context.Context, src, base, filterFile, trigger string, maxDelete int) *BackupResult {
	res := &BackupResult{Started: time.Now(), Trigger: trigger}
	res.Version = versionName(res.Started)
	args := []string{"sync", src + "/", base + "/current",
		"--backup-dir", base + "/versions/" + res.Version,
		"--links", "--use-json-log", "--stats", "15s", "--stats-log-level", "NOTICE", "-v",
		"--transfers", "4", "--checkers", "8", "--drive-chunk-size", "32M",
		"--retries", strconv.Itoa(syncRetries), "--retries-sleep", syncRetrySleep.String(),
	}
	if filterFile != "" {
		args = append(args, "--filter-from", filterFile)
	}
	if maxDelete >= 0 {
		args = append(args, "--max-delete", strconv.Itoa(maxDelete))
	}
	stderr := &jsonStats{}
	_, err := m.runner(ctx, args, stderr)
	res.Finished = time.Now()
	res.Seconds = res.Finished.Sub(res.Started).Seconds()
	res.Bytes, res.Files = stderr.bytes, stderr.files
	if n := stderr.failedAttempts(); n > 0 {
		log.Printf("backup to %s: rclone tried %d more time(s), most likely because files changed while it copied them", base, n)
	}
	switch {
	case ctx.Err() != nil:
		res.Error = "cancelled"
	case err != nil && stderr.hitMaxDelete():
		// rclone deletes up to the limit before it stops, so unlike the
		// check before the sync this one has already changed Drive a little.
		// A limit of 0 (a brain folder that could not be read in full) moved
		// nothing, and "up to 0 files" would only puzzle the owner.
		moved := fmt.Sprintf("It stopped at its limit of %d deletion(s), so up to that many files were already moved from current/ to versions/%s.", maxDelete, res.Version)
		if maxDelete == 0 {
			moved = "It allowed no deletion and stopped at the first one, so nothing was removed from current/."
		}
		res.Error = fmt.Sprintf("%s: files disappeared from the brain folder during the backup. %s "+
			"If the deletion was intended, mirror it with a forced backup of this brain.", massDeletionError, moved)
		res.Refused = true
	case err != nil:
		res.Error = strings.TrimSpace(err.Error() + " " + stderr.lastError())
	default:
		res.OK = true
	}
	return res
}

// jsonStats reads rclone's JSON log for transfer totals and errors.
type jsonStats struct {
	mu        sync.Mutex
	rest      string
	bytes     int64
	files     int64
	errs      []string
	maxDelete bool
	attempts  int // failed attempts rclone tried again or gave up after
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
			// "Attempt 1/3 failed with 1 errors and: …", once per try that
			// ended with errors.
			if strings.HasPrefix(e.Msg, "Attempt ") && strings.Contains(e.Msg, " failed with ") {
				j.attempts++
			}
			j.errs = append(j.errs, e.Msg)
			if len(j.errs) > 5 {
				j.errs = j.errs[1:]
			}
			if strings.Contains(e.Msg, "--max-delete threshold reached") {
				j.maxDelete = true
			}
		}
	}
	return len(p), nil
}

// failedAttempts is how many of rclone's tries ended with errors.
func (j *jsonStats) failedAttempts() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.attempts
}

// hitMaxDelete reports whether rclone stopped at the --max-delete limit.
func (j *jsonStats) hitMaxDelete() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.maxDelete
}

func (j *jsonStats) lastError() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.errs) == 0 {
		return ""
	}
	return textx.Truncate(j.errs[len(j.errs)-1], 300)
}

// prune deletes the version folders whose run is older than
// keep_versions_days. It goes by the run time in the folder name, never by
// file times: a file moved into versions/ keeps its own modification time,
// so filtering on that (rclone delete --min-age) would throw away the only
// copy of a long-unchanged file in the very run that replaced or deleted it.
func (m *BackupManager) prune(ctx context.Context, targets []*Brain) {
	cfg := m.cfgs.Get()
	days := cfg.Backup.keepVersions()
	if days <= 0 {
		return
	}
	ids := []string{bridgeBackupID}
	for _, br := range targets {
		ids = append(ids, br.ID())
	}
	now := time.Now()
	for _, id := range ids {
		// One config snapshot for the listing and the purges, so a backup.root
		// changed mid-run cannot make them point at different folders.
		versions := backupPath(cfg.Backup, id, "versions")
		names, err := m.listVersions(ctx, versions)
		if ctx.Err() != nil {
			return // cancelled: stop rather than log a failure for every id
		}
		if err != nil {
			log.Printf("backup %s: listing versions to prune failed: %v", id, err)
			continue
		}
		for _, v := range expiredVersions(names, now, days) {
			_, err := m.runner(ctx, []string{"purge", versions + "/" + v}, nil)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				log.Printf("backup %s: pruning version %s failed: %v", id, v, err)
			}
		}
	}
}

// parseVersion reads the run time out of a version folder name, of
// either layout.
func parseVersion(name string) (time.Time, bool) {
	if t, err := time.Parse(versionLayout, name); err == nil {
		return t, true
	}
	t, err := time.ParseInLocation(legacyVersionLayout, name, time.Local)
	return t, err == nil
}

// expiredVersions picks the version folders whose run is more than days
// before now. A name that is not a version stamp is never picked, so prune
// only ever deletes folders the bridge made.
func expiredVersions(names []string, now time.Time, days int) []string {
	cutoff := now.AddDate(0, 0, -days)
	var out []string
	for _, name := range names {
		if t, ok := parseVersion(name); ok && t.Before(cutoff) {
			out = append(out, name)
		}
	}
	return out
}

// sortVersions orders version folder names newest run first; names that
// are not version stamps go last.
func sortVersions(names []string) {
	sort.SliceStable(names, func(i, j int) bool {
		ti, iok := parseVersion(names[i])
		tj, jok := parseVersion(names[j])
		if iok != jok {
			return iok
		}
		if !iok {
			return names[i] > names[j]
		}
		return ti.After(tj)
	})
}

// listVersions returns the folder names in one backup's versions/ folder
// (an rclone path). A missing versions folder (no run has replaced or
// deleted a file yet) is an empty list, not an error.
func (m *BackupManager) listVersions(ctx context.Context, versions string) ([]string, error) {
	var stderr strings.Builder
	out, err := m.runner(ctx, []string{"lsjson", "--dirs-only", versions}, &stderr)
	if err != nil {
		if strings.Contains(strings.ToLower(stderr.String()), "not found") {
			return []string{}, nil
		}
		msg := textx.Truncate(strings.TrimSpace(stderr.String()), 300)
		if msg == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", err, msg)
	}
	var entries []struct {
		Name string `json:"Name"`
	}
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("reading rclone's list of versions: %w", err)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name)
	}
	return names, nil
}

// ── versions and restore ─────────────────────────────────────────────────

// backupPath is the rclone path of the backup named id under backup.root,
// with parts below it: backupPath(bc, "notes", "versions") is
// "brain-gdrive:fridolin-backup/notes/versions". Every path the backups
// use is built here, from a name that backupNameOK accepts.
func backupPath(bc BackupConfig, id string, parts ...string) string {
	return bc.Remote + ":" + strings.Join(append([]string{bc.Root, id}, parts...), "/")
}

// backupNameOK reports whether id can name a backup in Drive: one path
// segment, not "." or "..", with no separator, colon or control character
// that would make the remote path name another folder. That covers the
// bridge's own (bridgeBackupID) and every brain id, and the names of brains
// backed up before ids were held to brainIDRe, which may still be restored.
func backupNameOK(id string) bool {
	if id == "" || id == "." || id == ".." || len(id) > 100 {
		return false
	}
	return !strings.ContainsFunc(id, func(r rune) bool {
		return r == '/' || r == '\\' || r == ':' || unicode.IsControl(r)
	})
}

// restoreVersionRe matches what a restore may copy: the latest copy, or a
// version folder named as the runs name them, in either layout (see
// versionLayout).
var restoreVersionRe = regexp.MustCompile(`^(current|\d{8}-\d{6}Z?)$`)

func (b *Bridge) apiBackupVersions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !backupNameOK(id) {
		writeErr(w, http.StatusBadRequest, "invalid brain")
		return
	}
	if !b.backup.connected() {
		writeErr(w, http.StatusPreconditionFailed, "Google Drive is not connected")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	versions, err := b.backup.listVersions(ctx, backupPath(b.cfgs.Get().Backup, id, "versions"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "listing versions failed: "+err.Error())
		return
	}
	sortVersions(versions)
	writeJSON(w, http.StatusOK, map[string]any{"brain": id, "versions": versions})
}

// resolveExisting resolves the symlinks of absolute path p as far as it
// exists, and puts back the part that does not. CheckBrainPath resolves
// only a path that exists, so a new folder under a symlink would be checked
// where the link is, and then made, and restored into, where it points: in
// a brain, say.
func resolveExisting(p string) string {
	if !filepath.IsAbs(p) {
		return p
	}
	p = filepath.Clean(p)
	rest := ""
	for d := p; ; d = filepath.Dir(d) {
		if r, err := filepath.EvalSymlinks(d); err == nil {
			return filepath.Join(r, rest)
		}
		if filepath.Dir(d) == d {
			return p
		}
		rest = filepath.Join(filepath.Base(d), rest)
	}
}

// checkRestoreTarget reports why target, a folder by its real path, lies
// in what the sandbox keeps from brains ("is inside …", like
// checkBrainDir), or nil. The bridge writes the copy outside any sandbox,
// so where a brain folder may still live, inside ~/.ssh or a deny_paths
// entry, no restore needs to write. The folders of your own files are the
// exception, ~/Documents and the rest of userFileFolders: brains live
// there, and so may a restored one, as CheckBrainPath lets a new one.
func checkRestoreTarget(target string, cfg *Config, home string) error {
	var denied []protectedPath
	for _, p := range sandboxPolicyOf(cfg).protected(home, ownLayout()) {
		if !slices.ContainsFunc(userFileFolders, func(sub string) bool { return p.path == filepath.Join(home, sub) }) {
			p.inside = true
		}
		denied = append(denied, p)
	}
	for _, p := range cfg.Sandbox.DenyPaths {
		denied = append(denied, protectedPath{fsx.ExpandHome(p, home), true, "the deny_paths entry " + p})
	}
	if what, ok := protectedIn(target, denied); ok {
		return fmt.Errorf("%s, which the sandbox keeps from brains", what)
	}
	return nil
}

// restoreTarget checks the folder a restore copies into, as for a new brain
// (CheckBrainPath): inside your home folder, neither the data dir nor in or
// around a brain, and not the credentials folder either; and new or empty.
// It returns the folder, resolved.
func (b *Bridge) restoreTarget(p string) (string, error) {
	cfg := b.cfgs.Get()
	target, problems, _ := CheckBrainPath(resolveExisting(fsx.ExpandHome(strings.TrimSpace(p), homeDir())), cfg, brainDirs(cfg))
	if creds := fsx.Canonical(cfg.Sandbox.CredentialsDir); len(problems) == 0 && creds != "" && (fsx.Within(target, creds) || fsx.Within(creds, target)) {
		problems = append(problems, "the credentials folder cannot be (or contain) the restore folder")
	}
	if len(problems) == 0 {
		if err := checkRestoreTarget(target, cfg, homeDir()); err != nil {
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
	home := fsx.Canonical(homeDir())
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
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Version == "" {
		req.Version = "current"
	}
	if !backupNameOK(req.Brain) || !restoreVersionRe.MatchString(req.Version) {
		writeErr(w, http.StatusBadRequest, "invalid brain or version")
		return
	}
	m := b.backup
	if !m.connected() {
		writeErr(w, http.StatusPreconditionFailed, "Google Drive is not connected")
		return
	}
	target, err := b.restoreTarget(req.Target)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	src := backupPath(b.cfgs.Get().Backup, req.Brain, "current")
	if req.Version != "current" {
		src = backupPath(b.cfgs.Get().Backup, req.Brain, "versions", req.Version)
	}
	m.mu.Lock()
	if m.job != nil {
		m.mu.Unlock()
		writeErr(w, http.StatusConflict, "a backup or restore is already running")
		return
	}
	if m.disconnecting {
		m.mu.Unlock()
		writeErr(w, http.StatusConflict, errDisconnecting.Error())
		return
	}
	// Made here, so that a folder that cannot be made is the request's
	// error rather than a restore that fails out of sight.
	if err := os.MkdirAll(target, 0o700); err != nil {
		m.mu.Unlock()
		writeErr(w, http.StatusInternalServerError, "creating the restore folder failed: "+err.Error())
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
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	m.job = job
	m.mu.Unlock()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "restoring", "target": target})
}

// restore copies src, a backup of brain, into target, and says how it went.
func (m *BackupManager) restore(ctx context.Context, src, brain, version, target string) *RestoreResult {
	res := &RestoreResult{Brain: brain, Version: version, Target: target, Started: time.Now()}
	stderr := &jsonStats{}
	_, err := m.runner(ctx, []string{"copy", src, target, "--links", "--use-json-log", "-v",
		"--stats", "15s", "--stats-log-level", "NOTICE"}, stderr)
	res.Finished = time.Now()
	res.Bytes, res.Files = stderr.bytes, stderr.files
	switch {
	case ctx.Err() != nil:
		res.Error = "cancelled"
	case err != nil:
		res.Error = strings.TrimSpace(err.Error() + " " + stderr.lastError())
	default:
		res.OK = true
	}
	if res.OK {
		log.Printf("restore %s (%s) into %s: ok, %d files", brain, version, target, res.Files)
	} else {
		log.Printf("restore %s (%s) into %s FAILED: %s", brain, version, target, res.Error)
	}
	return res
}

// ── scheduler ────────────────────────────────────────────────────────────

// Start runs the daily schedule. A run missed while the machine slept runs
// as soon as it is awake again (once per day).
func (m *BackupManager) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stop = cancel
	_ = m.goLocked(func() {
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
	})
}

func (m *BackupManager) tick(now time.Time) {
	cfg := m.cfgs.Get()
	if !cfg.Backup.Enabled {
		return
	}
	if !m.connected() {
		m.warnNotConnected(now)
		return
	}
	m.mu.Lock()
	_, runNow := nextDue(now, cfg.Backup.DailyAt, m.state.LastScheduledDay, m.state.ArmedAt)
	busy := m.job != nil || m.disconnecting
	m.mu.Unlock()
	if !runNow || busy {
		return
	}
	if err := m.StartRun(nil, cfg.Backup.includeBridge(), "scheduled", false); err != nil {
		log.Printf("scheduled backup: %v", err)
		return
	}
	m.mu.Lock()
	m.state.LastScheduledDay = now.Format(dayLayout)
	m.saveStateLocked()
	m.mu.Unlock()
}

// warnNotConnected logs, once a day, that the daily backup is on but has no
// Drive link to run on: after an abandoned reconnect, say. The overview says
// so too; without either, the backups would just stop.
func (m *BackupManager) warnNotConnected(now time.Time) {
	day := now.Format(dayLayout)
	m.mu.Lock()
	warned := m.warnedDay == day
	m.warnedDay = day
	m.mu.Unlock()
	if !warned {
		log.Printf("backup: daily backups are on, but Google Drive is not connected, so none run — connect it on the Backup page")
	}
}

// Stop ends the scheduler, cancels a running backup, restore or Drive
// sign-in, and waits up to backupStopWait for them: for rclone to exit and
// a cancelled run's result to be saved. Otherwise both could still be going
// on after the bridge closed everything else, or be cut off half-way when
// the process exits. Nothing starts after Stop.
func (m *BackupManager) Stop() {
	m.mu.Lock()
	m.stopped = true
	if m.stop != nil {
		m.stop()
	}
	if m.job != nil {
		m.job.cancel()
	}
	if m.connect != nil && m.connect.cancel != nil {
		m.connect.cancel()
	}
	m.mu.Unlock()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(backupStopWait):
		log.Printf("backup: rclone has not stopped after %v; stopping without it", backupStopWait)
	}
}
