package config

import (
	"errors"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/trigger"
	"path/filepath"
)

// Defaults for optional settings.
const (
	DefaultHTTPListen    = "127.0.0.1:8765"
	defaultQueueMax      = 10
	DefaultQuietMS       = 2000
	defaultMaxWaitMS     = 10000
	DefaultBackupRemote  = "brain-gdrive"
	DefaultBackupRoot    = "fridolin-backup"
	defaultBackupDailyAt = "03:30"
	defaultKeepVersions  = 30
	defaultMaxMediaMB    = 100
	maxMaxMediaMB        = 2048
	// Agentic runs can take a while; a day is far more than any should.
	defaultRunTimeoutMinutes = 30
	maxRunTimeoutMinutes     = 24 * 60
	defaultConcurrentRuns    = 2
	maxConcurrentRuns        = 64
)

// Resolve derives the running configuration from the on-disk layer:
// defaults, ~ expansion and platform validation from explicit inputs. It reads
// no environment or filesystem state and writes nothing, see EnsureDirs.
func Resolve(file *File, paths Paths, goos string) (*File, error) {
	cfg, err := file.Clone()
	if err != nil {
		return nil, err
	}

	home := paths.Home
	if home == "" {
		return nil, errors.New("getting home dir: $HOME is not set")
	}

	if cfg.ClaudePath == "" {
		cfg.ClaudePath = "claude"
	}
	cfg.ClaudePath = fsx.ExpandHome(cfg.ClaudePath, home)
	if cfg.AgentProvider == "" {
		cfg.AgentProvider = "claude"
	}
	if cfg.CodexPath == "" {
		cfg.CodexPath = "codex"
	}
	cfg.CodexPath = fsx.ExpandHome(cfg.CodexPath, home)
	if cfg.CodexModel == "" {
		cfg.CodexModel = configDefaultCodexModel
	}
	if cfg.CodexEffort == "" {
		cfg.CodexEffort = "high"
	}
	cfg.DataDir, _ = ResolveDataDir(cfg.DataDir, paths)
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(cfg.DataDir, "bridge.db")
	}
	if cfg.MediaDir == "" {
		cfg.MediaDir = filepath.Join(cfg.DataDir, "media")
	}
	cfg.DBPath = fsx.ExpandHome(cfg.DBPath, home)
	cfg.MediaDir = fsx.ExpandHome(cfg.MediaDir, home)
	if cfg.MaxChunkSize == 0 {
		cfg.MaxChunkSize = configDefaultChunkSize
	}
	if cfg.MaxMediaMB == 0 {
		cfg.MaxMediaMB = defaultMaxMediaMB
	}
	if len(cfg.Triggers) == 0 {
		cfg.Triggers = trigger.FromStrings([]string{"@claude"})
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	if cfg.HTTPListen == "" {
		cfg.HTTPListen = DefaultHTTPListen
	}
	if cfg.Queue.Mode == "" {
		cfg.Queue.Mode = QueueModeQueue
	}
	if cfg.Queue.MaxPending <= 0 {
		cfg.Queue.MaxPending = defaultQueueMax
	}
	// Fill optional settings so the Resolve config shows real values.
	t := true
	if cfg.Queue.Notify == nil {
		cfg.Queue.Notify = &t
	}
	if cfg.Debounce.QuietMS == nil {
		q := DefaultQuietMS
		cfg.Debounce.QuietMS = &q
	}
	if cfg.Debounce.MaxWaitMS == nil {
		mw := defaultMaxWaitMS
		cfg.Debounce.MaxWaitMS = &mw
	}
	if cfg.AllowAnyDM == nil {
		// Unset fails closed: an empty list allows nobody.
		f := false
		cfg.AllowAnyDM = &f
	}
	if cfg.Dashboard.RequireToken == nil {
		cfg.Dashboard.RequireToken = &t
	}
	if cfg.RunTimeoutMinutes == nil {
		rt := defaultRunTimeoutMinutes
		cfg.RunTimeoutMinutes = &rt
	}
	if cfg.MaxConcurrentRuns == nil {
		n := defaultConcurrentRuns
		cfg.MaxConcurrentRuns = &n
	}
	if cfg.ScriptAPI.TokenlessSend == nil {
		cfg.ScriptAPI.TokenlessSend = &t
	}
	if cfg.Backup.IncludeBridgeConfig == nil {
		inc := defaultIncludeBridgeConfig
		cfg.Backup.IncludeBridgeConfig = &inc
	}
	if cfg.Sandbox.GitConfig == "" {
		cfg.Sandbox.GitConfig = "read"
	}
	cfg.Sandbox.CredentialsDir = fsx.ExpandHome(cfg.Sandbox.CredentialsDir, home)
	if cfg.Backup.Remote == "" {
		cfg.Backup.Remote = DefaultBackupRemote
	}
	if cfg.Backup.Root == "" {
		cfg.Backup.Root = DefaultBackupRoot
	}
	if cfg.Backup.DailyAt == "" {
		cfg.Backup.DailyAt = defaultBackupDailyAt
	}
	if cfg.Backup.KeepVersionsDays == nil {
		n := defaultKeepVersions
		cfg.Backup.KeepVersionsDays = &n
	}
	cfg.Backup.RclonePath = fsx.ExpandHome(cfg.Backup.RclonePath, home)
	cfg.Plaud.PhoneDir = fsx.ExpandHome(cfg.Plaud.PhoneDir, home)

	// Compared with senders as digits, however they were typed into the
	// file. The dashboard writes them so; a hand edit may not have.
	cfg.AllowedNumbers = chat.NumbersAsNumbers(cfg.AllowedNumbers)
	cfg.GroupMembers = GroupMembersAsNumbers(cfg.GroupMembers)
	for i := range cfg.BrainDirs {
		cfg.BrainDirs[i].Path = fsx.ExpandHome(cfg.BrainDirs[i].Path, home)
	}
	for i := range cfg.Sandbox.DenyPaths {
		cfg.Sandbox.DenyPaths[i] = fsx.ExpandHome(cfg.Sandbox.DenyPaths[i], home)
	}
	for i := range cfg.Sandbox.AllowPaths {
		cfg.Sandbox.AllowPaths[i] = fsx.ExpandHome(cfg.Sandbox.AllowPaths[i], home)
	}
	for i := range cfg.StartupChecks.RequiredFiles {
		cfg.StartupChecks.RequiredFiles[i] = fsx.ExpandHome(cfg.StartupChecks.RequiredFiles[i], home)
	}

	if err := cfg.Validate(goos); err != nil {
		return nil, err
	}
	return cfg, nil
}

// These configuration defaults also belong to the direct provider/chunker
// contracts. Composition tests keep the independent boundaries consistent.
const (
	configDefaultCodexModel = "gpt-6.1-sol"
	configDefaultChunkSize  = 3900
	configMinChunkSize      = 100
)

// defaultIncludeBridgeConfig is include_bridge_config's default.
const defaultIncludeBridgeConfig = true
