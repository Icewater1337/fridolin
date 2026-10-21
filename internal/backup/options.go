package backup

import (
	"context"
	"errors"
	"io"
	"time"
)

// Settings is an owned projection of one live config snapshot. The two
// resolved scalar fields intentionally have no omitempty: the status response
// previously included their non-nil pointers even for false and zero.
type Settings struct {
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

// Target keeps the selected runtime identity for the life of a job. Its
// settings are read at the original operation boundaries, including after WaitIdle.
type Target struct {
	ID, Dir  string
	Settings func() TargetSettings
	WaitIdle func(context.Context, time.Duration) bool
	Notify   func(context.Context, string) error
}
type TargetSettings struct {
	Name    string
	Enabled bool
	Exclude []string
}

// BridgeFiles preserves configured manifest order, including a per-file
// serialization error, so staging still attempts every remaining write.
type BridgeFiles struct {
	Config    []byte
	Manifests []ManifestFile
}
type ManifestFile struct {
	ID   string
	Data []byte
	Err  error
}

type RunFunc func(context.Context, []string, io.Writer) ([]byte, error)

type Options struct {
	BackupDir, RcloneConf string
	Settings              func() Settings
	Targets               func() []Target
	BridgeFiles           func() (BridgeFiles, error)
	FindRclone            func(string) (string, error)
	Environ               func(context.Context) []string
	InstallHint           func() string
	RefusalText           func(string, string) string
	Notify                func(context.Context, Target, string) error
	Runner                RunFunc
	// DriveClientPath is where an OAuth client pasted for Drive alone is
	// kept; without one the Drive link uses OAuth's SignInClient.
	DriveClientPath string
	// OAuth is Google sign-in as the bridge runs it (internal/google).
	OAuth OAuth
	// Superseded reports whether the vault's restic repository on Drive
	// has taken this backup's place: it has completed a copy once. Then
	// the daily scheduled run no longer runs; Back up now and restores
	// still work. compat: until then the Drive sync keeps running, so a
	// bridge that turns the vault on is never without a backup on Drive.
	Superseded func() bool
}

// SettingsChanged runs only after a successful configuration publication.
func (m *Manager) SettingsChanged(prev, next Settings) {
	if next.Enabled && (!prev.Enabled || prev.DailyAt != next.DailyAt) {
		m.mu.Lock()
		m.state.ArmedAt = m.now()
		m.saveStateLocked()
		m.mu.Unlock()
	}
}

// ErrRcloneMissing is findRclone's error when no rclone_path is set and no
// rclone is found.
var ErrRcloneMissing = errors.New("rclone is not installed")
