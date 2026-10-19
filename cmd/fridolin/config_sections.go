package main

import (
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

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

var (
	dailyAtRe    = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
	remoteNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
)

// defaultIncludeBridgeConfig is include_bridge_config's default.
const defaultIncludeBridgeConfig = true

// includeBridge and keepVersions read the two settings that are pointers so
// that an unset one can be told from false or 0. effective fills both in,
// with the same defaults, so the default of each lives in one place.
func (bc BackupConfig) IncludeBridge() bool {
	return boolOr(bc.IncludeBridgeConfig, defaultIncludeBridgeConfig)
}

func (bc BackupConfig) KeepVersions() int { return intOr(bc.KeepVersionsDays, defaultKeepVersions) }

func (bc BackupConfig) Validate() error {
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

// PlaudConfig controls the Plaud integration.
type PlaudConfig struct {
	// Enabled turns on polling for new recordings.
	Enabled bool `json:"enabled"`
	// Brain receives the recordings (default: the brain that handles DMs,
	// else the default brain).
	Brain string `json:"brain,omitempty"`
	// CLIPath overrides the plaud binary (default: found in the PATH
	// folders helperPath keeps, else in the newest nvm node version, else
	// in /opt/homebrew or /usr/local).
	CLIPath string `json:"cli_path,omitempty"`
	// IntervalSeconds between polls (default 60, minimum 30).
	IntervalSeconds int `json:"interval_seconds,omitempty"`
	// Polished uses Plaud's AI-cleaned transcript instead of the raw one.
	Polished bool `json:"polished,omitempty"`
	// Message is the instruction sent to the brain with each recording,
	// after a fixed line that says what arrived. {name}, {when} and
	// {duration} are filled in. Empty uses defaultPlaudMessage.
	Message string `json:"message,omitempty"`
	// BrainAccess keeps the login in ~/.plaud/ so brains can use the CLI.
	BrainAccess bool `json:"brain_access,omitempty"`
	// PhoneDir, when set, is where recordings come from instead of Plaud's
	// cloud: a folder that the Plaud Sync phone app's Documents/PlaudSync
	// is synced to (plaud-android/). Its transcripts, one
	// YYYY-MM-DD_HH-MM-SS.txt per recording, are handed over as the
	// cloud's are; Plaud's CLI and its login are not used.
	PhoneDir string `json:"phone_dir,omitempty"`
	// PhoneUpload takes the Plaud Sync app's transcripts through the site
	// instead: the app, built on the dashboard, pairs with Fridolin and
	// signs each upload (plaud_app.go). They go into PhoneDir, or without
	// one into <data_dir>/plaud/inbox, and are read from there.
	PhoneUpload bool `json:"phone_upload,omitempty"`
}

// interval is how often Plaud is checked.
func (pc PlaudConfig) Interval() time.Duration {
	if pc.IntervalSeconds > 0 {
		return time.Duration(pc.IntervalSeconds) * time.Second
	}
	return time.Minute
}

func (pc PlaudConfig) Validate() error {
	if pc.Brain != "" && !manifest.ValidID(pc.Brain) {
		return fmt.Errorf("plaud.brain %q is not a brain id", pc.Brain)
	}
	if pc.IntervalSeconds != 0 && pc.IntervalSeconds < 30 {
		return fmt.Errorf("plaud.interval_seconds must be at least 30")
	}
	if pc.PhoneDir != "" && !filepath.IsAbs(pc.PhoneDir) {
		return fmt.Errorf("plaud.phone_dir %q must be an absolute path or start with ~/", pc.PhoneDir)
	}
	return nil
}

// BrainRef is one brain folder in config.json's brain_dirs. Approved is the
// manifest you accepted in the dashboard. The bridge runs the approved
// version: a brain can edit its own brain.json, and must not be able to grant
// itself your DMs or another brain's groups that way. Differences show up in
// the dashboard as "changed on disk" until you approve them.
type BrainRef struct {
	Path     string             `json:"path"`
	Approved *manifest.Manifest `json:"approved"`
}

// Dir is the brain's folder with ~ expanded, for code that works on the
// file layer, where Path is kept as typed. The effective configuration has
// Path expanded already.
func (r BrainRef) Dir() string { return fsx.ExpandHome(r.Path, homeDir()) }

// isLoopbackAddr reports whether addr binds to a loopback interface.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
