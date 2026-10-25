package config

import (
	"time"

	"github.com/Icewater1337/fridolin/internal/trigger"
)

// Settings separates startup resources from live policy. Treat published values as immutable.
type Settings struct {
	Boot
	Live
}

// Live holds resolved global policy. BrainDirs are approval documents, whose
// optional manifest fields describe per-brain inheritance rather than defaults.
type Provider string

const (
	ProviderClaude Provider = "claude"
	ProviderCodex  Provider = "codex"
)

type QueueMode string

const (
	QueueFIFO      QueueMode = "queue"
	QueueInterrupt QueueMode = "interrupt"
)

type Live struct {
	Comment                    string
	Version                    int
	AllowedNumbers             []string
	AllowAnyDM                 bool
	AllowedGroups              []string
	GroupMembers               map[string][]string
	Triggers                   trigger.List
	ClaudePath                 string
	AgentProvider              Provider
	CodexPath                  string
	CodexModel                 string
	CodexEffort                string
	Timezone                   string
	MaxChunkSize               int
	MaxMediaMB                 int
	BrainModel                 string
	BrainEffort                string
	DisallowedTools            []string
	DangerouslySkipPermissions bool
	RunTimeout                 time.Duration
	MaxConcurrentRuns          int
	InheritClaudeUserConfig    bool
	ChildEnvPassthrough        []string
	BrainDirs                  []BrainRef
	Sandbox                    SandboxConfig
	OTPGuard                   bool
	OTPMarkers                 []string
	Viewer                     bool
	Queue                      QueueSettings
	Debounce                   DebounceSettings
	Ingest                     IngestConfig
	StartupChecks              StartupChecksConfig
	Backup                     BackupSettings
	Plaud                      PlaudSettings
	Dashboard                  DashboardSettings
	ScriptAPI                  ScriptAPISettings
	Core                       CoreConfig
	Vault                      VaultConfig
	Nodes                      NodesConfig
	Policy                     PolicyConfig
	Housekeeping               HousekeepingSettings
	Schedules                  SchedulesSettings
	Fride                      FrideConfig
}
type QueueSettings struct {
	Mode       QueueMode
	MaxPending int
	Notify     bool
}
type DebounceSettings struct {
	Quiet   time.Duration
	MaxWait time.Duration
}
type DashboardSettings struct {
	Disabled     bool
	RequireToken bool
}
type ScriptAPISettings struct {
	TokenlessSend bool
}
type BackupSettings struct {
	Enabled             bool
	Remote              string
	Root                string
	DailyAt             string
	RclonePath          string
	KeepVersionsDays    int
	IncludeBridgeConfig bool
	IncludeSecretFiles  bool
}
type PlaudSettings struct {
	// intervalDefaulted preserves the legacy effective JSON omission while
	// Interval always carries the actual polling duration.
	intervalDefaulted bool
	Enabled           bool
	Brain             string
	CLIPath           string
	Interval          time.Duration
	Polished          bool
	Message           string
	BrainAccess       bool
	PhoneDir          string
	PhoneUpload       bool
	Voice             bool
	VoiceBrain        string
	VoiceQuiet        bool
}

func settingsOf(c *File, paths Paths) *Settings {
	return &Settings{Boot: bootOf(c, paths), Live: liveOf(c)}
}
func liveOf(c *File) Live {
	return Live{
		Comment:                    c.Comment,
		Version:                    c.Version,
		AllowedNumbers:             c.AllowedNumbers,
		AllowAnyDM:                 *c.AllowAnyDM,
		AllowedGroups:              c.AllowedGroups,
		GroupMembers:               c.GroupMembers,
		Triggers:                   c.Triggers,
		ClaudePath:                 c.ClaudePath,
		AgentProvider:              Provider(c.AgentProvider),
		CodexPath:                  c.CodexPath,
		CodexModel:                 c.CodexModel,
		CodexEffort:                c.CodexEffort,
		Timezone:                   c.Timezone,
		MaxChunkSize:               c.MaxChunkSize,
		MaxMediaMB:                 c.MaxMediaMB,
		BrainModel:                 c.BrainModel,
		BrainEffort:                c.BrainEffort,
		DisallowedTools:            c.DisallowedTools,
		DangerouslySkipPermissions: c.DangerouslySkipPermissions,
		RunTimeout:                 time.Duration(*c.RunTimeoutMinutes) * time.Minute,
		MaxConcurrentRuns:          *c.MaxConcurrentRuns,
		InheritClaudeUserConfig:    c.InheritClaudeUserConfig,
		ChildEnvPassthrough:        c.ChildEnvPassthrough,
		BrainDirs:                  c.BrainDirs,
		Sandbox:                    c.Sandbox,
		OTPGuard:                   c.OTPGuard,
		OTPMarkers:                 c.OTPMarkers,
		Viewer:                     c.Viewer,
		Queue:                      queueSettingsOf(c.Queue),
		Debounce:                   debounceSettingsOf(c.Debounce),
		Ingest:                     c.Ingest,
		StartupChecks:              c.StartupChecks,
		Backup:                     backupSettingsOf(c.Backup),
		Plaud:                      plaudSettingsOf(c.Plaud),
		Dashboard:                  dashboardSettingsOf(c.Dashboard),
		ScriptAPI:                  scriptAPISettingsOf(c.ScriptAPI),
		Core:                       c.Core,
		Vault:                      c.Vault,
		Nodes:                      c.Nodes,
		Policy:                     c.Policy,
		Housekeeping:               housekeepingSettingsOf(c.Housekeeping),
		Schedules:                  schedulesSettingsOf(c.Schedules),
		Fride:                      c.Fride,
	}
}
func queueSettingsOf(c QueueConfig) QueueSettings {
	return QueueSettings{
		Mode:       QueueMode(c.Mode),
		MaxPending: c.MaxPending,
		Notify:     *c.Notify,
	}
}
func debounceSettingsOf(c DebounceConfig) DebounceSettings {
	return DebounceSettings{
		Quiet:   time.Duration(*c.QuietMS) * time.Millisecond,
		MaxWait: time.Duration(*c.MaxWaitMS) * time.Millisecond,
	}
}
func dashboardSettingsOf(c DashboardConfig) DashboardSettings {
	return DashboardSettings{
		Disabled:     c.Disabled,
		RequireToken: *c.RequireToken,
	}
}
func scriptAPISettingsOf(c ScriptAPIConfig) ScriptAPISettings {
	return ScriptAPISettings{
		TokenlessSend: *c.TokenlessSend,
	}
}
func backupSettingsOf(c BackupConfig) BackupSettings {
	return BackupSettings{
		Enabled:             c.Enabled,
		Remote:              c.Remote,
		Root:                c.Root,
		DailyAt:             c.DailyAt,
		RclonePath:          c.RclonePath,
		KeepVersionsDays:    *c.KeepVersionsDays,
		IncludeBridgeConfig: *c.IncludeBridgeConfig,
		IncludeSecretFiles:  c.IncludeSecretFiles,
	}
}
func plaudSettingsOf(c PlaudConfig) PlaudSettings {
	interval := time.Duration(c.IntervalSeconds) * time.Second
	if interval == 0 {
		interval = DefaultPlaudInterval
	}
	return PlaudSettings{
		intervalDefaulted: c.IntervalSeconds == 0,
		Enabled:           c.Enabled,
		Brain:             c.Brain,
		CLIPath:           c.CLIPath,
		Interval:          interval,
		Polished:          c.Polished,
		Message:           c.Message,
		BrainAccess:       c.BrainAccess,
		PhoneDir:          c.PhoneDir,
		PhoneUpload:       c.PhoneUpload,
		Voice:             c.Voice,
		VoiceBrain:        c.VoiceBrain,
		VoiceQuiet:        c.VoiceQuiet,
	}
}

// EffectiveFile projects runtime values back to the existing dashboard wire format.
func EffectiveFile(s *Settings) File {
	c := fileOf(s.Live)
	c.DataDir = s.Boot.DataDir
	c.DBPath = s.Boot.DBPath
	c.MediaDir = s.Boot.MediaDir
	c.HTTPListen = s.Boot.HTTPListen
	c.LogLevel = s.Boot.LogLevel
	return c
}
func fileOf(c Live) File {
	return File{
		Comment:                    c.Comment,
		Version:                    c.Version,
		AllowedNumbers:             c.AllowedNumbers,
		AllowAnyDM:                 new(c.AllowAnyDM),
		AllowedGroups:              c.AllowedGroups,
		GroupMembers:               c.GroupMembers,
		Triggers:                   c.Triggers,
		ClaudePath:                 c.ClaudePath,
		AgentProvider:              string(c.AgentProvider),
		CodexPath:                  c.CodexPath,
		CodexModel:                 c.CodexModel,
		CodexEffort:                c.CodexEffort,
		Timezone:                   c.Timezone,
		MaxChunkSize:               c.MaxChunkSize,
		MaxMediaMB:                 c.MaxMediaMB,
		BrainModel:                 c.BrainModel,
		BrainEffort:                c.BrainEffort,
		DisallowedTools:            c.DisallowedTools,
		DangerouslySkipPermissions: c.DangerouslySkipPermissions,
		RunTimeoutMinutes:          new(int(c.RunTimeout / time.Minute)),
		MaxConcurrentRuns:          new(c.MaxConcurrentRuns),
		InheritClaudeUserConfig:    c.InheritClaudeUserConfig,
		ChildEnvPassthrough:        c.ChildEnvPassthrough,
		BrainDirs:                  c.BrainDirs,
		Sandbox:                    c.Sandbox,
		OTPGuard:                   c.OTPGuard,
		OTPMarkers:                 c.OTPMarkers,
		Viewer:                     c.Viewer,
		Queue:                      queueConfigOf(c.Queue),
		Debounce:                   debounceConfigOf(c.Debounce),
		Ingest:                     c.Ingest,
		StartupChecks:              c.StartupChecks,
		Backup:                     backupConfigOf(c.Backup),
		Plaud:                      plaudConfigOf(c.Plaud),
		Dashboard:                  dashboardConfigOf(c.Dashboard),
		ScriptAPI:                  scriptAPIConfigOf(c.ScriptAPI),
		Core:                       c.Core,
		Vault:                      c.Vault,
		Nodes:                      c.Nodes,
		Policy:                     c.Policy,
		Housekeeping:               housekeepingConfigOf(c.Housekeeping),
		Schedules:                  schedulesConfigOf(c.Schedules),
		Fride:                      c.Fride,
	}
}
func queueConfigOf(c QueueSettings) QueueConfig {
	return QueueConfig{
		Mode:       string(c.Mode),
		MaxPending: c.MaxPending,
		Notify:     new(c.Notify),
	}
}
func debounceConfigOf(c DebounceSettings) DebounceConfig {
	return DebounceConfig{
		QuietMS:   new(int(c.Quiet / time.Millisecond)),
		MaxWaitMS: new(int(c.MaxWait / time.Millisecond)),
	}
}
func dashboardConfigOf(c DashboardSettings) DashboardConfig {
	return DashboardConfig{
		Disabled:     c.Disabled,
		RequireToken: new(c.RequireToken),
	}
}
func scriptAPIConfigOf(c ScriptAPISettings) ScriptAPIConfig {
	return ScriptAPIConfig{
		TokenlessSend: new(c.TokenlessSend),
	}
}
func backupConfigOf(c BackupSettings) BackupConfig {
	return BackupConfig{
		Enabled:             c.Enabled,
		Remote:              c.Remote,
		Root:                c.Root,
		DailyAt:             c.DailyAt,
		RclonePath:          c.RclonePath,
		KeepVersionsDays:    new(c.KeepVersionsDays),
		IncludeBridgeConfig: new(c.IncludeBridgeConfig),
		IncludeSecretFiles:  c.IncludeSecretFiles,
	}
}
func plaudConfigOf(c PlaudSettings) PlaudConfig {
	seconds := int(c.Interval / time.Second)
	if c.intervalDefaulted && c.Interval == DefaultPlaudInterval {
		seconds = 0
	}
	return PlaudConfig{
		Enabled:         c.Enabled,
		Brain:           c.Brain,
		CLIPath:         c.CLIPath,
		IntervalSeconds: seconds,
		Polished:        c.Polished,
		Message:         c.Message,
		BrainAccess:     c.BrainAccess,
		PhoneDir:        c.PhoneDir,
		PhoneUpload:     c.PhoneUpload,
		Voice:           c.Voice,
		VoiceBrain:      c.VoiceBrain,
		VoiceQuiet:      c.VoiceQuiet,
	}
}

// AnyoneCanDM reports an explicitly open allowlist.
func (s *Settings) AnyoneCanDM() bool { return len(s.AllowedNumbers) == 0 && s.AllowAnyDM }

// Location is the owner's time zone (timezone), else the process's own
// (time.Local, which TZ sets): the zone the bridge reads every time of day
// in. Use it, never time.Local, so that a native bridge and the core
// container agree.
func (l Live) Location() *time.Location {
	return trigger.Zone(l.Timezone)
}
