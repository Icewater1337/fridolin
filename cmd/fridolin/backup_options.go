package main

import (
	"context"
	"errors"
	"io"
	"time"
)

// BackupSettings is an owned projection of one live config snapshot. The two
// resolved scalar fields intentionally have no omitempty: the status response
// previously included their non-nil pointers even for false and zero.
type BackupSettings struct {
	Enabled             bool     `json:"enabled"`
	Remote              string   `json:"remote,omitempty"`
	Root                string   `json:"root,omitempty"`
	DailyAt             string   `json:"daily_at,omitempty"`
	RclonePath          string   `json:"rclone_path,omitempty"`
	KeepVersionsDays    int      `json:"keep_versions_days"`
	IncludeBridgeConfig bool     `json:"include_bridge_config"`
	IncludeSecretFiles  bool     `json:"include_secret_files,omitempty"`
	ConfiguredIDs       []string `json:"-"`
}

// BackupTarget keeps the selected runtime identity for the life of a job. Its
// settings are read at the original operation boundaries, including after WaitIdle.
type BackupTarget struct {
	ID, Dir  string
	Settings func() BackupTargetSettings
	WaitIdle func(context.Context, time.Duration) bool
	Notify   func(context.Context, string) error
}
type BackupTargetSettings struct {
	Name    string
	Enabled bool
	Exclude []string
}

// BackupBridgeFiles preserves configured manifest order, including a per-file
// serialization error, so staging still attempts every remaining write.
type BackupBridgeFiles struct {
	Config    []byte
	Manifests []BackupManifestFile
}
type BackupManifestFile struct {
	ID   string
	Data []byte
	Err  error
}

type BackupRunFunc func(context.Context, []string, io.Writer) ([]byte, error)

type BackupOptions struct {
	BackupDir, RcloneConf string
	Settings              func() BackupSettings
	Targets               func() []BackupTarget
	BridgeFiles           func() (BackupBridgeFiles, error)
	FindRclone            func(string) (string, error)
	Environ               func(context.Context) []string
	DesktopContext        func(context.Context) context.Context
	InstallHint           func() string
	RefusalText           func(string, string) string
	Notify                func(context.Context, BackupTarget, string) error
	Runner                BackupRunFunc
}

// SettingsChanged runs only after a successful configuration publication.
func (m *BackupManager) SettingsChanged(prev, next BackupSettings) {
	if next.Enabled && (!prev.Enabled || prev.DailyAt != next.DailyAt) {
		m.mu.Lock()
		m.state.ArmedAt = m.now()
		m.saveStateLocked()
		m.mu.Unlock()
	}
}

// errRcloneMissing is findRclone's error when no rclone_path is set and no
// rclone is found.
var errRcloneMissing = errors.New("rclone is not installed")
