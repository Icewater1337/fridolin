package plaud

import (
	"context"
	"io"
	"time"

	mediafiles "github.com/Icewater1337/fridolin/internal/media"
)

// PlaudSettings is the integration's live configuration, including its dashboard shape.
// The command projection's explicit conversion checks that every field is represented.
type Settings struct {
	Enabled         bool   `json:"enabled"`
	Brain           string `json:"brain,omitempty"`
	CLIPath         string `json:"cli_path,omitempty"`
	IntervalSeconds int    `json:"interval_seconds,omitempty"`
	Polished        bool   `json:"polished,omitempty"`
	Message         string `json:"message,omitempty"`
	BrainAccess     bool   `json:"brain_access,omitempty"`
	PhoneDir        string `json:"phone_dir,omitempty"`
	PhoneUpload     bool   `json:"phone_upload,omitempty"`
}

// PlaudSnapshot keeps one poll's policy and paths together.
type Snapshot struct {
	Plaud             Settings
	DataDir, MediaDir string
	QueueMaxPending   int
	OwnerChat         string
	Interval          time.Duration
}

// PlaudOptions supplies startup paths and current settings without a config store.
type Options struct {
	PlaudDir, Home   string
	Settings         func() *Snapshot
	Target           func(id string) *Target
	HeldTarget       func() string
	Notify           func(context.Context, string, string) error
	FindCLI          func(string) (string, error)
	CLIVersion       func(string) string
	Environ          func(context.Context, string, string) []string
	DesktopContext   func(context.Context) context.Context
	Inbox            func(string) mediafiles.Inbox
	RunInterrupted   func(error) bool
	SiteURL          func() string
	AppSource        func() (string, error)
	AndroidToolchain func() (string, string, error)
	AppEnviron       func(context.Context, string, string) []string
	Runner           RunFunc
}

// PlaudTarget retains the selected runtime for a handoff. Its callbacks read
// that runtime's current reply settings and submit to that same runtime even
// when a reload replaces it with another brain bearing the same ID.
type Target struct {
	BrainID    string
	ReplyChat  func() string
	OtherOwner func(jid string) string
	Submit     func(jid, sender, text string, media []string, done func(error)) error
}

func (t *Target) ID() string { return t.BrainID }

// PlaudRunFunc executes one CLI call after login checks and serialization.
// out receives live output during browser sign-in; returned text is parsed by
// the poller. Nil uses the built-in process runner with Options.Environ.
type RunFunc func(ctx context.Context, bin, home string, out io.Writer, args ...string) (string, error)
