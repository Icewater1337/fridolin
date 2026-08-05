package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// BrainConfig describes one isolated Claude "brain" — a working directory plus
// the chats it answers for. Brains never see each other's files: each spawn is
// wrapped in a sandbox profile that denies reads into the other brains' trees.
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
}

// Config holds all configuration for the bridge.
type Config struct {
	// AllowedNumbers lists phone numbers (country code, no "+") allowed to DM
	// the bridge. Empty means "allow anyone who can message this account" —
	// only sensible if the account is not publicly reachable.
	AllowedNumbers []string `json:"allowed_numbers"`

	// AllowedGroups lists group JIDs (…@g.us) the bridge will respond in.
	// Messages from any other group are ignored and logged.
	AllowedGroups []string `json:"allowed_groups"`

	// Triggers are the keywords that make the bridge answer in a group.
	// Matched case-insensitively anywhere in the message and stripped before
	// the text reaches Claude. An @-mention of the bridge account always
	// counts as a trigger, whatever this list contains.
	Triggers []string `json:"triggers"`

	// ClaudePath is the path to the Claude Code CLI binary ("claude" uses PATH).
	ClaudePath string `json:"claude_path"`

	// DataDir holds runtime state: SQLite DB, downloaded media, QR code.
	DataDir string `json:"data_dir"`

	// DBPath and MediaDir default to <data_dir>/bridge.db and <data_dir>/media.
	DBPath   string `json:"db_path"`
	MediaDir string `json:"media_dir"`

	// MaxChunkSize is the max characters in one outgoing WhatsApp message
	// (longer replies are split and numbered). Default 3900.
	MaxChunkSize int `json:"max_chunk_size"`

	// Model is the Claude model for classic (non-brain) mode, e.g. "sonnet".
	// Empty uses the CLI default.
	Model string `json:"model"`

	// BrainModel is the model used by brains (falls back to Model when empty).
	BrainModel string `json:"brain_model"`

	// DangerouslySkipPermissions passes --dangerously-skip-permissions to the
	// Claude CLI so it can use tools without an interactive prompt. Required
	// for unattended operation; see the security notes in the README.
	DangerouslySkipPermissions bool `json:"dangerously_skip_permissions"`

	// HTTPListen is the address for the local outbound-send API
	// (e.g. "127.0.0.1:8765"). Empty disables it.
	HTTPListen string `json:"http_listen"`

	// LogLevel: "debug", "info", "warn", "error".
	LogLevel string `json:"log_level"`

	// BrainMode enables the multi-brain runner. When false the bridge runs in
	// classic mode: one Claude session per chat, resumed with --resume.
	BrainMode bool `json:"brain_mode"`

	// Brains defines the brains used when BrainMode is true.
	Brains []BrainConfig `json:"brains"`

	// WorkDir is the working directory for classic mode (default: home dir).
	WorkDir string `json:"work_dir"`

	// Sandbox controls the macOS sandbox-exec wrapper around brain spawns.
	Sandbox SandboxConfig `json:"sandbox"`

	// OTPGuard drops bare numeric codes (e.g. "483920") when the bridge
	// recently asked for one, so a two-factor code typed into the chat is not
	// handed to the model. See docs/CONFIGURATION.md.
	OTPGuard bool `json:"otp_guard"`

	// Viewer opens a tmux + Terminal window per brain tailing its activity log.
	// macOS only, off by default.
	Viewer bool `json:"viewer"`
}

// SandboxConfig tunes the macOS sandbox profile applied to brain spawns.
type SandboxConfig struct {
	// Enabled wraps each brain spawn in sandbox-exec. macOS only; ignored
	// elsewhere. Strongly recommended when running with skip-permissions.
	Enabled bool `json:"enabled"`

	// DenyPaths are extra absolute paths (or ~-relative paths) that brains
	// must not read — other projects, client work, anything private.
	DenyPaths []string `json:"deny_paths"`

	// AllowPaths are paths to re-allow after the built-in denies, for the rare
	// case where a brain legitimately needs one of the default-denied dirs.
	AllowPaths []string `json:"allow_paths"`
}

// LoadConfig reads configuration with this priority (highest → lowest):
//  1. Environment variables (including those loaded from .env)
//  2. config.json values
//  3. Built-in defaults
//
// The .env file is looked for next to the config file, then in the working directory.
func LoadConfig(cfgPath string) (*Config, error) {
	envPaths := []string{
		filepath.Join(filepath.Dir(cfgPath), ".env"),
		".env",
	}
	for _, p := range envPaths {
		if err := godotenv.Overload(p); err == nil {
			break // loaded the first one found
		}
	}

	cfg := &Config{}
	if data, err := os.ReadFile(cfgPath); err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parsing config file %q: %w", cfgPath, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading config file %q: %w", cfgPath, err)
	}

	applyEnvOverrides(cfg)

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting home dir: %w", err)
	}

	if cfg.ClaudePath == "" {
		cfg.ClaudePath = "claude"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Join(home, ".whatsapp-claude-bridge")
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
	if cfg.WorkDir == "" {
		cfg.WorkDir = home
	}
	cfg.WorkDir = expandHome(cfg.WorkDir, home)
	if cfg.MaxChunkSize <= 0 {
		cfg.MaxChunkSize = 3900
	}
	if len(cfg.Triggers) == 0 {
		cfg.Triggers = []string{"@claude"}
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}

	for i := range cfg.Brains {
		b := &cfg.Brains[i]
		b.WorkDir = expandHome(b.WorkDir, home)
		if b.PromptFile == "" {
			b.PromptFile = filepath.Join(b.WorkDir, "brain-prompt.md")
		}
		b.PromptFile = expandHome(b.PromptFile, home)
	}
	for i := range cfg.Sandbox.DenyPaths {
		cfg.Sandbox.DenyPaths[i] = expandHome(cfg.Sandbox.DenyPaths[i], home)
	}
	for i := range cfg.Sandbox.AllowPaths {
		cfg.Sandbox.AllowPaths[i] = expandHome(cfg.Sandbox.AllowPaths[i], home)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o700); err != nil {
		return nil, fmt.Errorf("creating db directory: %w", err)
	}
	if err := os.MkdirAll(cfg.MediaDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating media directory: %w", err)
	}

	return cfg, nil
}

// validate rejects configurations that would misroute messages or start a
// brain with no working directory.
func (cfg *Config) validate() error {
	if cfg.BrainMode && len(cfg.Brains) == 0 {
		return fmt.Errorf("brain_mode is enabled but no brains are configured")
	}

	seenNames := make(map[string]bool, len(cfg.Brains))
	seenGroups := make(map[string]string, len(cfg.Brains))
	dmBrain := ""

	for _, b := range cfg.Brains {
		if b.Name == "" {
			return fmt.Errorf("every brain needs a name")
		}
		if strings.ContainsAny(b.Name, `/\ `) {
			return fmt.Errorf("brain name %q must not contain slashes or spaces", b.Name)
		}
		if seenNames[b.Name] {
			return fmt.Errorf("duplicate brain name %q", b.Name)
		}
		seenNames[b.Name] = true

		if b.WorkDir == "" {
			return fmt.Errorf("brain %q needs a work_dir", b.Name)
		}
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

// applyEnvOverrides overwrites cfg fields with any matching environment variables.
// Comma-separated list vars replace the whole slice. Brains are configured in
// config.json only — they are too structured for environment variables.
func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("ALLOWED_NUMBERS"); v != "" {
		cfg.AllowedNumbers = splitTrimmed(v)
	}
	if v := os.Getenv("ALLOWED_GROUPS"); v != "" {
		cfg.AllowedGroups = splitTrimmed(v)
	}
	if v := os.Getenv("TRIGGERS"); v != "" {
		cfg.Triggers = splitTrimmed(v)
	}
	if v := os.Getenv("CLAUDE_PATH"); v != "" {
		cfg.ClaudePath = v
	}
	if v := os.Getenv("DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("WORK_DIR"); v != "" {
		cfg.WorkDir = v
	}
	if v := os.Getenv("DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("MEDIA_DIR"); v != "" {
		cfg.MediaDir = v
	}
	if v := os.Getenv("MAX_CHUNK_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxChunkSize = n
		}
	}
	if v := os.Getenv("MODEL"); v != "" {
		cfg.Model = v
	}
	if v := os.Getenv("BRAIN_MODEL"); v != "" {
		cfg.BrainModel = v
	}
	if v := os.Getenv("DANGEROUSLY_SKIP_PERMISSIONS"); v != "" {
		cfg.DangerouslySkipPermissions = isTruthy(v)
	}
	if v := os.Getenv("HTTP_LISTEN"); v != "" {
		cfg.HTTPListen = v
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv("BRAIN_MODE"); v != "" {
		cfg.BrainMode = isTruthy(v)
	}
	if v := os.Getenv("SANDBOX_ENABLED"); v != "" {
		cfg.Sandbox.Enabled = isTruthy(v)
	}
	if v := os.Getenv("OTP_GUARD"); v != "" {
		cfg.OTPGuard = isTruthy(v)
	}
	if v := os.Getenv("VIEWER"); v != "" {
		cfg.Viewer = isTruthy(v)
	}
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

// splitTrimmed splits a comma-separated string and trims whitespace from each item.
func splitTrimmed(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
