package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// BrainConfig describes one brain inline in config.json — the original way to
// configure brains. New brains live in their own folder with a brain.json
// manifest (see manifest.go) and are listed in brain_dirs; inline brains keep
// working and `fridolin migrate` converts them.
type BrainConfig struct {
	// Name identifies the brain in logs, session keys, and sandbox profiles.
	// Use a filesystem-safe slug, e.g. "personal", "work", "family".
	Name string `json:"name"`

	// WorkDir is the directory Claude runs from for this brain. It holds the
	// brain's prompt file, conversation history, memories, and any files the
	// brain creates.
	WorkDir string `json:"work_dir"`

	// Groups lists WhatsApp group JIDs (…@g.us) routed to this brain.
	Groups []string `json:"groups"`

	// HandlesDMs routes all direct messages to this brain. Exactly one brain
	// should set this; if none does, DMs go to the default brain.
	HandlesDMs bool `json:"handles_dms"`

	// Default marks the fallback brain for allowed chats that no other brain
	// claims. If no brain is marked default, the first brain is used.
	Default bool `json:"default"`

	// PromptFile overrides the system-prompt path (default: <work_dir>/brain-prompt.md).
	PromptFile string `json:"prompt_file"`

	// Model overrides the global brain model for this brain only.
	Model string `json:"model"`

	// Effort overrides the global brain effort for this brain only.
	Effort string `json:"effort,omitempty"`
}

// Config holds all configuration for the bridge.
type Config struct {
	Comment string `json:"_comment,omitempty"`

	// AllowedNumbers lists phone numbers (country code, no "+") allowed to DM
	// the bridge.
	AllowedNumbers []string `json:"allowed_numbers"`

	// AllowAnyDM decides what an empty allowed_numbers means. Unset or false:
	// nobody may DM until you add a number. Only an explicit true lets anyone
	// in, because allowed senders run Claude with all its permissions and a
	// hand-written or partial config must not open the bridge by omission.
	AllowAnyDM *bool `json:"allow_any_dm,omitempty"`

	// AllowedGroups lists group JIDs (…@g.us) the bridge will respond in.
	// Messages from any other group are ignored and logged.
	AllowedGroups []string `json:"allowed_groups"`

	// Triggers are the keywords that make the bridge answer in a group. Each
	// is a string (matched anywhere, case-insensitively) or
	// {"text": "@c", "match": "start"|"word"}. An @-mention of the bridge
	// account always counts as a trigger.
	Triggers TriggerList `json:"triggers"`

	// ClaudePath is the path to the Claude Code CLI binary ("claude" uses PATH).
	ClaudePath string `json:"claude_path"`

	// DataDir holds runtime state: SQLite DB, downloaded media, dashboard
	// token, backup state.
	DataDir string `json:"data_dir"`

	// DBPath and MediaDir default to <data_dir>/bridge.db and <data_dir>/media.
	DBPath   string `json:"db_path,omitempty"`
	MediaDir string `json:"media_dir,omitempty"`

	// MaxChunkSize is the most characters (runes) in one outgoing WhatsApp
	// message, its "[1/3] " numbering included; longer replies are split
	// and numbered. 0 means the default, 3900; anything else must be at
	// least minChunkSize.
	MaxChunkSize int `json:"max_chunk_size,omitempty"`

	// MaxMediaMB is the largest attachment the bridge downloads, in MB.
	// Larger ones are skipped and the sender is told. Default 100, at most
	// 2048 (WhatsApp's own limit is 2 GB).
	MaxMediaMB int `json:"max_media_mb,omitempty"`

	// Model is the model brains use when brain_model is empty, e.g.
	// "sonnet". Left from classic mode, which had no brain_model.
	Model string `json:"model,omitempty"`

	// BrainModel is the model used by brains (falls back to Model when empty).
	BrainModel string `json:"brain_model"`

	// BrainEffort is passed to the CLI as --effort (low, medium, high, xhigh,
	// max). Empty uses the CLI default. Brains can override it.
	BrainEffort string `json:"brain_effort,omitempty"`

	// DisallowedTools are passed as --disallowed-tools, e.g. ["WebFetch"].
	// Brains can override the list.
	DisallowedTools []string `json:"disallowed_tools,omitempty"`

	// DangerouslySkipPermissions passes --dangerously-skip-permissions to the
	// Claude CLI so it can use tools without an interactive prompt. Required
	// for unattended operation; see the security notes in the README.
	DangerouslySkipPermissions bool `json:"dangerously_skip_permissions"`

	// InheritClaudeUserConfig lets brains load your own Claude Code setup,
	// as they did before this setting existed: your user settings (with
	// their hooks, plugins and permissions), skills, agents and commands,
	// the MCP servers in ~/.claude.json, and the CLAUDE.md files and
	// .claude folders above the brain's folder, ~/CLAUDE.md and
	// ~/.claude/CLAUDE.md included. Off by default: a brain loads only its
	// own folder's, since anyone who may message it can steer it. Off, the
	// CLI is told to skip the CLAUDE.md files and rules above the folder,
	// and only the sandbox keeps out the skills, agents and commands there.
	InheritClaudeUserConfig bool `json:"inherit_claude_user_config,omitempty"`

	// HTTPListen is the address of the local HTTP server: the dashboard plus
	// the /send and /ingest APIs. Must be a loopback address. Default
	// 127.0.0.1:8765; "off" disables it.
	HTTPListen string `json:"http_listen"`

	// LogLevel: "debug", "info", "warn", "error".
	LogLevel string `json:"log_level,omitempty"`

	// BrainMode is the switch classic mode had: brains always run now, and
	// a config that sets it to false is refused. The key is still written,
	// as true, so that an older bridge you roll back to runs brains too:
	// without it, that one falls back to classic mode.
	BrainMode retiredBrainMode `json:"brain_mode"`

	// Brains are brains configured inline (the original format).
	Brains []BrainConfig `json:"brains,omitempty"`

	// BrainDirs are brain folders added through the dashboard. Each holds a
	// brain.json manifest; Approved is the version you accepted (a brain
	// cannot widen its own routing by editing its manifest).
	BrainDirs []BrainRef `json:"brain_dirs,omitempty"`

	// Sandbox controls the macOS sandbox-exec wrapper around brain spawns.
	Sandbox SandboxConfig `json:"sandbox"`

	// OTPGuard drops bare numeric codes (e.g. "483920") when the bridge
	// recently asked for one, so a two-factor code typed into the chat is not
	// handed to the model.
	OTPGuard bool `json:"otp_guard"`

	// OTPMarkers are the phrases that mark an outbound message as a request
	// for a code. Empty uses the built-in list.
	OTPMarkers []string `json:"otp_markers,omitempty"`

	// Viewer opens a tmux + Terminal window per brain tailing its activity log.
	// macOS only, off by default.
	Viewer bool `json:"viewer"`

	Queue         QueueConfig         `json:"queue"`
	Debounce      DebounceConfig      `json:"debounce"`
	Ingest        IngestConfig        `json:"ingest"`
	StartupChecks StartupChecksConfig `json:"startup_checks"`
	Backup        BackupConfig        `json:"backup"`
	Plaud         PlaudConfig         `json:"plaud"`
	Dashboard     DashboardConfig     `json:"dashboard"`
}

// retiredBrainMode is the brain_mode key. A missing key, null and true all
// mean brains.
type retiredBrainMode struct{}

// MarshalJSON always writes true, whatever the file had.
func (retiredBrainMode) MarshalJSON() ([]byte, error) { return []byte("true"), nil }

// UnmarshalJSON refuses false: whoever wrote it asked for classic mode, and
// quietly running brains instead would leave their chats with no brain set
// up. Dashboard saves are parsed the same way, so none can turn it off.
func (*retiredBrainMode) UnmarshalJSON(data []byte) error {
	var on *bool
	if err := json.Unmarshal(data, &on); err != nil {
		return fmt.Errorf("brain_mode: %w", err)
	}
	if on != nil && !*on {
		return classicModeRemoved(`"brain_mode": false`, `Set "brain_mode" to true in config.json (an older version you go back to needs it)`)
	}
	return nil
}

// classicModeRemoved is the error for a setting that asks for classic mode:
// what names it, fix says what to change.
func classicModeRemoved(what, fix string) error {
	return fmt.Errorf("%s asks for classic mode, which was removed: every chat now goes to a brain. %s, start the bridge and add a brain in the dashboard under Brains → Add brain (see docs/BRAINS.md)", what, fix)
}

// SandboxConfig tunes the macOS sandbox profile applied to brain spawns.
type SandboxConfig struct {
	// Enabled wraps each brain spawn in sandbox-exec. macOS only: elsewhere
	// the config is rejected. Strongly recommended when running with
	// skip-permissions. A run whose sandbox cannot be set up is refused,
	// never run without it.
	Enabled bool `json:"enabled"`

	// DenyPaths are extra absolute paths (or ~-relative paths) that brains
	// must not read — other projects, client work, anything private.
	// Relative paths are rejected.
	DenyPaths []string `json:"deny_paths"`

	// AllowPaths are paths to re-allow after the built-in denies, for the rare
	// case where a brain legitimately needs one of the default-denied dirs.
	// Absolute or ~-relative, like DenyPaths.
	AllowPaths []string `json:"allow_paths"`

	// GitConfig is "read" (default: brains may read ~/.gitconfig and the files
	// it includes, so they can commit in their own folder) or "deny".
	GitConfig string `json:"git_config,omitempty"`

	// CredentialsDir is a directory of per-brain credential files. Brains are
	// denied the whole directory except their own calendar credentials
	// (google_calendar_{credentials,token}_<brain>.json). Absolute or
	// ~-relative.
	CredentialsDir string `json:"credentials_dir,omitempty"`
}

// QueueConfig decides what happens when a message arrives while the brain is
// still busy with the previous one.
type QueueConfig struct {
	// Mode "queue" (default) runs messages one after another; "interrupt"
	// stops the current run and starts on the newest message.
	Mode string `json:"mode,omitempty"`
	// MaxPending caps the queue per brain (default 10).
	MaxPending int `json:"max_pending,omitempty"`
	// Notify sends "⏳ queued" when a tagged message has to wait (default true).
	Notify *bool `json:"notify,omitempty"`
}

// DebounceConfig merges messages that arrive in quick succession from the
// same person in the same chat (several photos, a file plus its question)
// into one brain run.
type DebounceConfig struct {
	// QuietMS waits this long after the last message (default 2000; 0 = off).
	QuietMS *int `json:"quiet_ms,omitempty"`
	// MaxWaitMS never holds a batch longer than this (default 10000).
	MaxWaitMS *int `json:"max_wait_ms,omitempty"`
}

// IngestConfig controls POST /ingest, which hands files that arrived outside
// WhatsApp (a web upload, a recorder sync) to a brain as if they were sent in
// its chat.
type IngestConfig struct {
	Enabled bool `json:"enabled"`
}

// StartupChecksConfig lists files that must exist for the assistant to work —
// credentials, typically. Missing ones are logged loudly, shown on the
// dashboard and optionally sent to you over WhatsApp.
type StartupChecksConfig struct {
	RequiredFiles  []string `json:"required_files,omitempty"`
	NotifyWhatsApp bool     `json:"notify_whatsapp,omitempty"`
	// NotifyJID receives the alert (default: the first allowed number).
	NotifyJID string `json:"notify_jid,omitempty"`
	// Hint is appended to the alert, e.g. how to restore the files.
	Hint string `json:"hint,omitempty"`
}

// DashboardConfig controls the web dashboard served on http_listen.
type DashboardConfig struct {
	Disabled bool `json:"disabled,omitempty"`
	// RequireToken (default true) requires the token from
	// <data_dir>/dashboard.token for every API call. Brains run on this
	// machine and can reach localhost; the token is what keeps a manipulated
	// brain from reconfiguring the bridge.
	RequireToken *bool `json:"require_token,omitempty"`
}

// Defaults for optional settings.
const (
	defaultHTTPListen    = "127.0.0.1:8765"
	defaultQueueMax      = 10
	defaultQuietMS       = 2000
	defaultMaxWaitMS     = 10000
	defaultBackupRemote  = "brain-gdrive"
	defaultBackupRoot    = "fridolin-backup"
	defaultBackupDailyAt = "03:30"
	defaultKeepVersions  = 30
	defaultMaxMediaMB    = 100
	maxMaxMediaMB        = 2048
)

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func intOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// QueueNotify reports whether to tell the sender their message is queued.
func (c *Config) QueueNotify() bool { return boolOr(c.Queue.Notify, true) }

// DebounceQuietMS and DebounceMaxWaitMS return the debounce timings.
func (c *Config) DebounceQuietMS() int   { return intOr(c.Debounce.QuietMS, defaultQuietMS) }
func (c *Config) DebounceMaxWaitMS() int { return intOr(c.Debounce.MaxWaitMS, defaultMaxWaitMS) }

// DashboardRequiresToken reports whether API calls need the dashboard token.
func (c *Config) DashboardRequiresToken() bool { return boolOr(c.Dashboard.RequireToken, true) }

// MaxMediaBytes is the largest attachment to download, in bytes.
func (c *Config) MaxMediaBytes() int64 {
	mb := c.MaxMediaMB
	if mb <= 0 {
		mb = defaultMaxMediaMB
	}
	return int64(mb) << 20
}

// HTTPEnabled reports whether the local HTTP server should run.
func (c *Config) HTTPEnabled() bool { return c.HTTPListen != "" && c.HTTPListen != "off" }

// AllowsAnyDM reports whether an empty allowed_numbers list means everyone.
// It takes an explicit allow_any_dm: true.
func (c *Config) AllowsAnyDM() bool { return boolOr(c.AllowAnyDM, false) }

// AnyoneCanDM reports whether every sender passes the DM allowlist.
func (c *Config) AnyoneCanDM() bool { return len(c.AllowedNumbers) == 0 && c.AllowsAnyDM() }

// LoadConfig reads config.json and fills in the built-in defaults. The file
// is the only source of settings: no environment variable or .env overrides
// any of them.
func LoadConfig(cfgPath string) (*Config, error) {
	cs, err := OpenConfigStore(cfgPath)
	if err != nil {
		return nil, err
	}
	return cs.Get(), nil
}

// dataDirName and legacyDataDirName are the home-relative names of the data
// dir, now and before the project was renamed.
const (
	dataDirName       = ".fridolin"
	legacyDataDirName = ".whatsapp-claude-bridge"
)

// dotEnvPath is the .env next to the config, which older versions read
// settings from. The bridge reads it no more, but it may still hold settings
// and secrets, so the sandbox profile denies it to brains.
func dotEnvPath(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), ".env")
}

// defaultDataDir is ~/.fridolin, or the folder installs used before the
// project was renamed (~/.whatsapp-claude-bridge) when only that one exists.
func defaultDataDir(home string) string {
	current := filepath.Join(home, dataDirName)
	legacy := filepath.Join(home, legacyDataDirName)
	if _, err := os.Stat(current); os.IsNotExist(err) {
		if st, err := os.Stat(legacy); err == nil && st.IsDir() {
			return legacy
		}
	}
	return current
}

// effective derives the running configuration from the on-disk layer:
// defaults, ~ expansion, validation.
func effective(file *Config) (*Config, error) {
	cfg, err := file.clone()
	if err != nil {
		return nil, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting home dir: %w", err)
	}

	if cfg.ClaudePath == "" {
		cfg.ClaudePath = "claude"
	}
	cfg.ClaudePath = expandHome(cfg.ClaudePath, home)
	if cfg.DataDir == "" {
		cfg.DataDir = defaultDataDir(home)
	}
	cfg.DataDir = expandHome(cfg.DataDir, home)
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(cfg.DataDir, "bridge.db")
	}
	if cfg.MediaDir == "" {
		cfg.MediaDir = filepath.Join(cfg.DataDir, "media")
	}
	cfg.DBPath = expandHome(cfg.DBPath, home)
	cfg.MediaDir = expandHome(cfg.MediaDir, home)
	if cfg.MaxChunkSize == 0 {
		cfg.MaxChunkSize = defaultChunkSize
	}
	if cfg.MaxMediaMB == 0 {
		cfg.MaxMediaMB = defaultMaxMediaMB
	}
	if len(cfg.Triggers) == 0 {
		cfg.Triggers = TriggersFromStrings([]string{"@claude"})
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	if cfg.HTTPListen == "" {
		cfg.HTTPListen = defaultHTTPListen
	}
	if cfg.Queue.Mode == "" {
		cfg.Queue.Mode = "queue"
	}
	if cfg.Queue.MaxPending <= 0 {
		cfg.Queue.MaxPending = defaultQueueMax
	}
	// Fill optional settings so the effective config shows real values.
	t := true
	if cfg.Queue.Notify == nil {
		cfg.Queue.Notify = &t
	}
	if cfg.Debounce.QuietMS == nil {
		q := defaultQuietMS
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
	if cfg.Backup.IncludeBridgeConfig == nil {
		cfg.Backup.IncludeBridgeConfig = &t
	}
	if cfg.Sandbox.GitConfig == "" {
		cfg.Sandbox.GitConfig = "read"
	}
	cfg.Sandbox.CredentialsDir = expandHome(cfg.Sandbox.CredentialsDir, home)
	if cfg.Backup.Remote == "" {
		cfg.Backup.Remote = defaultBackupRemote
	}
	if cfg.Backup.Root == "" {
		cfg.Backup.Root = defaultBackupRoot
	}
	if cfg.Backup.DailyAt == "" {
		cfg.Backup.DailyAt = defaultBackupDailyAt
	}
	if cfg.Backup.KeepVersionsDays == nil {
		n := defaultKeepVersions
		cfg.Backup.KeepVersionsDays = &n
	}
	cfg.Backup.RclonePath = expandHome(cfg.Backup.RclonePath, home)

	for i := range cfg.Brains {
		b := &cfg.Brains[i]
		b.WorkDir = expandHome(b.WorkDir, home)
		if b.PromptFile == "" {
			b.PromptFile = filepath.Join(b.WorkDir, "brain-prompt.md")
		}
		b.PromptFile = expandHome(b.PromptFile, home)
	}
	for i := range cfg.BrainDirs {
		cfg.BrainDirs[i].Path = expandHome(cfg.BrainDirs[i].Path, home)
	}
	for i := range cfg.Sandbox.DenyPaths {
		cfg.Sandbox.DenyPaths[i] = expandHome(cfg.Sandbox.DenyPaths[i], home)
	}
	for i := range cfg.Sandbox.AllowPaths {
		cfg.Sandbox.AllowPaths[i] = expandHome(cfg.Sandbox.AllowPaths[i], home)
	}
	for i := range cfg.StartupChecks.RequiredFiles {
		cfg.StartupChecks.RequiredFiles[i] = expandHome(cfg.StartupChecks.RequiredFiles[i], home)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating data directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o700); err != nil {
		return nil, fmt.Errorf("creating db directory: %w", err)
	}
	if err := os.MkdirAll(cfg.MediaDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating media directory: %w", err)
	}

	return cfg, nil
}

// protectDataDir takes group and other access away from the data dir and
// warns about folders it leaves open. MkdirAll gives a new data dir 0700 but
// leaves the mode of an existing one alone, so a legacy folder or one made
// by hand could stay open to every local account. It runs once at startup,
// not in effective(): that also runs on every dashboard save, which would
// change a folder typed into Settings before any restart and repeat the
// warnings on every write.
//
// Three kinds of folder only get a warning: the home folder or one above it
// (data_dir "~"), where other users and services may need access; a data
// dir that belongs to another user, whose mode is theirs to set even when
// the bridge runs as root; and the folder of a db_path outside the data
// dir, which may be shared on purpose. bridge.db itself is 0600 either way.
// A chmod that fails is logged, not fatal, so an upgrade cannot keep a
// working bridge from starting.
func protectDataDir(cfg *Config) {
	// Compare absolute, resolved paths: a relative data_dir such as "."
	// depends on the folder the bridge was started from, and isWithin cannot
	// relate it to the home folder, so it would get past the home check.
	dataDir := absPath(cfg.DataDir)
	dbDir := absPath(filepath.Dir(cfg.DBPath))
	home, homeErr := os.UserHomeDir()
	homeOrAbove := func(dir string) bool {
		return homeErr == nil && isWithin(canonicalDir(home), canonicalDir(dir))
	}

	mode, open := openToOthers(dataDir)
	switch {
	case homeOrAbove(dataDir):
		if open {
			log.Printf("warning: data_dir %s is your home folder or one above it, so its mode %04o stays as it is; a folder of its own, such as ~/.fridolin, keeps the bridge's files private", dataDir, uint32(mode))
		}
	case ownedByOther(dataDir):
		if open {
			log.Printf("warning: data_dir %s belongs to another user, so its mode %04o stays as it is; a folder of your own, such as ~/.fridolin, keeps the bridge's files private", dataDir, uint32(mode))
		}
	default:
		if err := restrictToOwner(dataDir); err != nil {
			log.Printf("warning: could not make %s private: %v", dataDir, err)
		}
	}

	if !isWithin(canonicalDir(dbDir), canonicalDir(dataDir)) {
		if mode, open := openToOthers(dbDir); open {
			// Not for the home folder: making it 0700 is what the data dir
			// rule above avoids.
			advice := "; consider chmod 700"
			if homeOrAbove(dbDir) {
				advice = ""
			}
			log.Printf("warning: %s, the folder of db_path, is open to other users (mode %04o)%s", dbDir, uint32(mode), advice)
		}
	}
}

// ownedByOther reports whether path exists and belongs to a user other than
// the one the bridge runs as.
func ownedByOther(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	return ok && int(sys.Uid) != os.Geteuid()
}

// openToOthers returns the permission bits of dir and whether they give
// group or other users any access.
func openToOthers(dir string) (os.FileMode, bool) {
	st, err := os.Stat(dir)
	if err != nil {
		return 0, false
	}
	return st.Mode().Perm(), st.Mode().Perm()&0o077 != 0
}

// clone deep-copies a Config through JSON.
func (c *Config) clone() (*Config, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	out := &Config{}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, err
	}
	return out, nil
}

var validEfforts = map[string]bool{"": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// validate rejects configurations that would misroute messages or start a
// brain with no working directory. Zero brains is fine: a fresh install adds
// its first brain from the dashboard.
func (c *Config) validate() error {
	if !validEfforts[c.BrainEffort] {
		return fmt.Errorf("brain_effort %q must be one of low, medium, high, xhigh, max", c.BrainEffort)
	}
	switch c.Queue.Mode {
	case "queue", "interrupt":
	default:
		return fmt.Errorf("queue.mode must be \"queue\" or \"interrupt\", not %q", c.Queue.Mode)
	}
	switch c.Sandbox.GitConfig {
	case "read", "deny":
	default:
		return fmt.Errorf("sandbox.git_config must be \"read\" or \"deny\", not %q", c.Sandbox.GitConfig)
	}
	if err := c.Sandbox.validate(runtime.GOOS); err != nil {
		return err
	}
	if c.MaxChunkSize < minChunkSize {
		return fmt.Errorf("max_chunk_size %d is too small: use at least %d, or 0 for the default of %d", c.MaxChunkSize, minChunkSize, defaultChunkSize)
	}
	if c.MaxMediaMB < 1 || c.MaxMediaMB > maxMaxMediaMB {
		return fmt.Errorf("max_media_mb must be between 1 and %d, not %d", maxMaxMediaMB, c.MaxMediaMB)
	}
	if c.HTTPEnabled() && !isLoopbackAddr(c.HTTPListen) {
		return fmt.Errorf("http_listen %q must be a loopback address such as 127.0.0.1:8765 — the API and dashboard are for this machine only", c.HTTPListen)
	}
	if err := c.Backup.validate(); err != nil {
		return err
	}
	if err := c.Plaud.validate(); err != nil {
		return err
	}

	for _, b := range c.Brains {
		if b.Name == "" {
			return fmt.Errorf("every brain needs a name")
		}
		if strings.ContainsAny(b.Name, `/\ `) {
			return fmt.Errorf("brain name %q must not contain slashes or spaces", b.Name)
		}
		if b.WorkDir == "" {
			return fmt.Errorf("brain %q needs a work_dir", b.Name)
		}
		if !validEfforts[b.Effort] {
			return fmt.Errorf("brain %q: effort %q is not valid", b.Name, b.Effort)
		}
	}
	// Names, groups and the DM brain must be unique across inline brains.
	// Manifest brains join this check when the brain set is resolved.
	seenNames := make(map[string]bool, len(c.Brains))
	seenGroups := make(map[string]string, len(c.Brains))
	dmBrain := ""
	for _, b := range c.Brains {
		if seenNames[b.Name] {
			return fmt.Errorf("duplicate brain name %q", b.Name)
		}
		seenNames[b.Name] = true
		for _, g := range b.Groups {
			if owner, ok := seenGroups[g]; ok {
				return fmt.Errorf("group %s is claimed by both brain %q and brain %q", g, owner, b.Name)
			}
			seenGroups[g] = b.Name
		}
		if b.HandlesDMs {
			if dmBrain != "" {
				return fmt.Errorf("brains %q and %q both set handles_dms", dmBrain, b.Name)
			}
			dmBrain = b.Name
		}
	}
	return nil
}

// validate rejects sandbox settings that goos cannot honour. Brain runs
// refuse to start without their sandbox, so an enabled sandbox on a system
// without sandbox-exec is a configuration error, not something to ignore.
// It runs after ~ expansion.
func (sc SandboxConfig) validate(goos string) error {
	if sc.Enabled && goos != "darwin" {
		return fmt.Errorf("sandbox.enabled needs macOS (sandbox-exec) and this is %s: set it to false and run the bridge as a dedicated user instead", goos)
	}
	// A relative path is relative to whatever folder a brain runs in, so as
	// a rule it would protect (or open) nothing anyone meant.
	check := func(field, p string) error {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("%s: %q must be an absolute path or start with ~/", field, p)
		}
		if _, err := sbplString(p); err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		return nil
	}
	for _, p := range sc.DenyPaths {
		if err := check("sandbox.deny_paths", p); err != nil {
			return err
		}
	}
	for _, p := range sc.AllowPaths {
		if err := check("sandbox.allow_paths", p); err != nil {
			return err
		}
	}
	if sc.CredentialsDir != "" {
		return check("sandbox.credentials_dir", sc.CredentialsDir)
	}
	return nil
}

// expandHome rewrites a leading "~" to the user's home directory.
func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

// isTruthy reports whether v reads as true: true, 1, yes or on.
func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}
