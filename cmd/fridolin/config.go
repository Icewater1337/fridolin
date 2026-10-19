package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/trigger"
)

// Config holds all configuration for the bridge.
type Config struct {
	Comment string `json:"_comment,omitempty"`

	// Version is the version of config.json the file was written in, see
	// currentConfigVersion. A file without it is version 1. Every save
	// writes currentConfigVersion.
	Version int `json:"version,omitempty"`

	// AllowedNumbers lists phone numbers allowed to DM the bridge: digits
	// with the country code. The running config has each in that form,
	// however it was written in the file (see allowedNumbersAsNumbers).
	AllowedNumbers []string `json:"allowed_numbers"`

	// AllowAnyDM decides what an empty allowed_numbers means. Unset or false:
	// nobody may DM until you add a number. Only an explicit true lets anyone
	// in, because allowed senders run Claude with all its permissions and a
	// hand-written or partial config must not open the bridge by omission.
	AllowAnyDM *bool `json:"allow_any_dm,omitempty"`

	// AllowedGroups lists group JIDs (…@g.us) the bridge will respond in.
	// Messages from any other group are ignored and logged.
	AllowedGroups []string `json:"allowed_groups"`

	// GroupMembers says who in an allowed group reaches its brain: the
	// group's JID and the phone numbers of its members whose messages are
	// read (digits with the country code, like allowed_numbers), or ["*"]
	// for every member. A group without an entry admits whom
	// allowed_numbers admits. Every key must be in allowed_groups. The
	// bridge's own account always gets through (see groupSenderAllowed).
	GroupMembers map[string][]string `json:"group_members,omitempty"`

	// Triggers are the keywords that make the bridge answer in a group. Each
	// is a string (matched anywhere, case-insensitively) or
	// {"text": "@c", "match": "start"|"word"}. An @-mention of the bridge
	// account always counts as a trigger.
	Triggers trigger.List `json:"triggers"`

	// ClaudePath is the path to the Claude Code CLI binary ("claude" uses PATH).
	ClaudePath string `json:"claude_path"`

	// AgentProvider selects the CLI for new runs; omission keeps Claude.
	AgentProvider string `json:"agent_provider,omitempty"`
	// Codex settings are separate so changing mode preserves Claude models,
	// including model/effort overrides in brain.json.
	CodexPath   string `json:"codex_path,omitempty"`
	CodexModel  string `json:"codex_model,omitempty"`
	CodexEffort string `json:"codex_effort,omitempty"`

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

	// BrainModel is the model brains use, e.g. "sonnet"; empty uses the CLI
	// default. Brains can override it. A config that still has the retired
	// "model" key and no brain_model gets that value, see retiredKeys.
	BrainModel string `json:"brain_model"`

	// BrainEffort is passed to the CLI as --effort (low, medium, high, xhigh,
	// max). Empty uses the CLI default. Brains can override it.
	BrainEffort string `json:"brain_effort,omitempty"`

	// DisallowedTools are passed as --disallowed-tools, e.g. ["WebFetch"].
	// Brains can override the list.
	DisallowedTools []string `json:"disallowed_tools,omitempty"`

	// DangerouslySkipPermissions passes --dangerously-skip-permissions to the
	// Claude CLI so it can use tools without an interactive prompt. Without
	// it a run, in -p mode with nobody to approve, is refused every tool
	// that needs approval. Codex instead uses its read-only sandbox without
	// this flag; see the security notes in the README.
	DangerouslySkipPermissions bool `json:"dangerously_skip_permissions"`

	// RunTimeoutMinutes stops a brain's run that takes longer, so that one
	// stuck run (a hung tool, a network stall) cannot hold up its brain's
	// queue until someone sends !stop. Default 30; 0 lets a run take as long
	// as it takes.
	RunTimeoutMinutes *int `json:"run_timeout_minutes,omitempty"`

	// MaxConcurrentRuns is how many Claude runs may go at once, all brains
	// together; others wait for one to end, requests ahead of untagged
	// chatter. Each run is a full Claude Code agent, so this bounds the
	// load and the use of the account's rate limits. Default 2; 0 for no
	// limit.
	MaxConcurrentRuns *int `json:"max_concurrent_runs,omitempty"`

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

	// ChildEnvPassthrough names variables of the bridge's environment that
	// brains, and the activity viewer's tmux server they can read, get
	// besides childEnvAllowlist, such as GROQ_API_KEY for a transcription
	// tool. Anyone who may message a brain can read them.
	ChildEnvPassthrough []string `json:"child_env_passthrough,omitempty"`

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

	// BrainDirs are brain folders added through the dashboard. Each holds a
	// brain.json manifest; Approved is the version you accepted (a brain
	// cannot widen its own routing by editing its manifest).
	BrainDirs []BrainRef `json:"brain_dirs,omitempty"`

	// Sandbox controls the macOS sandbox-exec wrapper around brain spawns.
	Sandbox SandboxConfig `json:"sandbox"`

	// OTPGuard drops a message that is only a one-time code (e.g. "483920")
	// in a chat that was asked for one in the last minutes (see otp.go), so
	// a two-factor code typed into the chat is not handed to the model.
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
	ScriptAPI     ScriptAPIConfig     `json:"script_api"`
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

// migrateBuild is a commit of main that still has `fridolin migrate`, which
// moves inline brains into their folders. It is the one their removal was
// built on, so it reads the config.json this version reads and its
// write-back drops no setting.
const migrateBuild = "2592376"

// currentConfigVersion is the version of config.json this bridge reads
// and writes, in its "version" key. A file without the key is version 1,
// as every config.json was before the key existed. The version goes up
// when a key is renamed or removed or comes to mean something else, so
// that a file written before needs a migration to keep its meaning; a key
// that is only added needs none, as an older file simply lacks it.
const currentConfigVersion = 2

// configMigrations take config.json from one version to the next: the one
// at index i reads version i+1 and makes it version i+2. Each works on the
// file's top-level keys before they are decoded and returns a line for the
// log about each key it changed. None writes the file: the next save
// writes what it made, as currentConfigVersion.
//
// compat: a version 1 config.json, from before the key existed, is read
// with its retired keys folded in (foldRetiredKeys). Unlike the other
// compat: shims, migrations stay for good, so that a config.json of any
// earlier version loads.
var configMigrations = []func(obj map[string]json.RawMessage) ([]string, error){
	// Version 2 is the file after classic mode and inline brains were
	// removed: the keys only they read are retired.
	foldRetiredKeys,
}

// errConfigTooNew means a config.json, or a dashboard save, is of a version
// newer than this bridge reads.
var errConfigTooNew = errors.New("a newer Fridolin wrote it, and this one could drop the settings it does not know")

// migrateConfig brings obj, the top-level keys of a config.json, to
// currentConfigVersion and returns the version it was written in and the
// lines the migrations it applied logged. A version newer than this
// bridge reads is refused: the file may have settings this one does not
// know, which a save would drop, or keys that mean something else now.
func migrateConfig(obj map[string]json.RawMessage) (from int, notes []string, err error) {
	from = 1
	if raw, ok := obj["version"]; ok {
		if err := json.Unmarshal(raw, &from); err != nil || from < 1 {
			return 0, nil, fmt.Errorf("version %s: must be a whole number from 1 to %d", raw, currentConfigVersion)
		}
	}
	if from > currentConfigVersion {
		return from, nil, fmt.Errorf("version %d is newer than the version %d this build of Fridolin reads: %w", from, currentConfigVersion, errConfigTooNew)
	}
	for v := from; v < currentConfigVersion; v++ {
		n, err := configMigrations[v-1](obj)
		if err != nil {
			return from, nil, err
		}
		notes = append(notes, n...)
	}
	obj["version"] = json.RawMessage(strconv.Itoa(currentConfigVersion))
	return from, notes, nil
}

// migrateConfigJSON returns data, a config.json or a whole configuration
// the dashboard saves, migrated to currentConfigVersion (see
// migrateConfig), with the lines to log. Anything but a JSON object comes
// back as it is, for the decoder to refuse; null is an empty file.
func migrateConfigJSON(data []byte) (migrated []byte, notes []string, err error) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return data, nil, nil
	}
	if obj == nil {
		obj = map[string]json.RawMessage{}
	}
	if _, notes, err = migrateConfig(obj); err != nil {
		return nil, nil, err
	}
	if migrated, err = json.Marshal(obj); err != nil {
		return nil, nil, err
	}
	return migrated, notes, nil
}

// retiredKeys are the top-level keys version 1 had that version 2 has no
// setting for. Migrating a version 1 file folds each into what it still
// means and takes it out, logging what became of it, so the next save
// leaves it out. In a version 2 file they are unknown keys. brain_mode is
// not one of them: saves keep writing it, see retiredBrainMode.
var retiredKeys = []struct {
	name string
	// fold carries over what the value still means into the other keys of
	// obj and says what became of it, or refuses a value this version
	// cannot honour.
	fold func(obj map[string]json.RawMessage, value json.RawMessage) (string, error)
}{
	{"model", foldModel},
	{"work_dir", func(map[string]json.RawMessage, json.RawMessage) (string, error) {
		return "it named the folder classic mode ran in, and brains run in their own", nil
	}},
	{"brains", foldInlineBrains},
}

// foldModel takes over model, the classic-mode model that brains fell back
// to while brain_model was empty, so those brains keep the model they ran
// with.
func foldModel(obj map[string]json.RawMessage, value json.RawMessage) (string, error) {
	var model, brainModel string
	if err := json.Unmarshal(value, &model); err != nil {
		return "", fmt.Errorf("model: %w", err)
	}
	if raw, ok := obj["brain_model"]; ok {
		if err := json.Unmarshal(raw, &brainModel); err != nil {
			return "", fmt.Errorf("brain_model: %w", err)
		}
	}
	switch {
	case model == "":
		return "it was empty", nil
	case brainModel == "":
		obj["brain_model"], _ = json.Marshal(model)
		return fmt.Sprintf("brain_model is now %q, the model brains fell back to", model), nil
	}
	return fmt.Sprintf("brains use brain_model %q, so %q went unused", brainModel, model), nil
}

// foldInlineBrains checks brains, where brains were configured before they
// had folders of their own. Inline brains were removed together with
// `fridolin migrate`. A list with brains in it is refused: running without
// them would leave their chats unanswered, or answered by whichever brain
// is the default. An empty list or null loads.
func foldInlineBrains(_ map[string]json.RawMessage, value json.RawMessage) (string, error) {
	var brains []json.RawMessage
	if err := json.Unmarshal(value, &brains); err != nil {
		return "", fmt.Errorf("brains: %w", err)
	}
	if len(brains) > 0 {
		return "", fmt.Errorf(`config.json lists brains inline under "brains", which this version no longer runs. Move them into their folders first: run "fridolin migrate" with a build from before this change (commit %s), then start this version again (see docs/BRAINS.md#configs-with-inline-brains)`, migrateBuild)
	}
	return "it listed no brains, and brains live in their folders (brain_dirs)", nil
}

// foldRetiredKeys is the migration from version 1 to 2: it applies
// retiredKeys to obj, takes them out of it, and returns a line for each
// retired key obj had, in table order.
func foldRetiredKeys(obj map[string]json.RawMessage) ([]string, error) {
	var notes []string
	for _, rk := range retiredKeys {
		value, ok := obj[rk.name]
		if !ok {
			continue
		}
		what, err := rk.fold(obj, value)
		if err != nil {
			return nil, err
		}
		delete(obj, rk.name)
		notes = append(notes, fmt.Sprintf("%q is retired and is dropped the next time the dashboard saves: %s", rk.name, what))
	}
	return notes, nil
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

	// CredentialsDir is a directory of per-brain credential files: the
	// calendar tokens (google_calendar_token_<brain>.json), which the
	// bridge uses for the brains. Brains are denied the whole directory.
	// Absolute or ~-relative.
	CredentialsDir string `json:"credentials_dir,omitempty"`
}

// QueueConfig decides what happens when a message arrives while the brain is
// still busy with the previous one.
type QueueConfig struct {
	// Mode is queueModeQueue (default) or queueModeInterrupt.
	Mode string `json:"mode,omitempty"`
	// MaxPending caps the queue per brain (default 10).
	MaxPending int `json:"max_pending,omitempty"`
	// Notify sends "⏳ queued" when a tagged message has to wait (default true).
	Notify *bool `json:"notify,omitempty"`
}

// The queue modes. The Settings page offers the same two values.
const (
	// queueModeQueue runs a brain's messages one after another.
	queueModeQueue = "queue"
	// queueModeInterrupt lets a newer request replace its sender's own in
	// the same chat, waiting or running.
	queueModeInterrupt = "interrupt"
)

// validQueueModes are the values queue.mode takes once effective() has
// filled in the default.
var validQueueModes = map[string]bool{queueModeQueue: true, queueModeInterrupt: true}

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
// dashboard and optionally sent to you over WhatsApp (see health.go).
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

// ScriptAPIConfig controls who may use /send and /ingest (docs/API.md).
type ScriptAPIConfig struct {
	// TokenlessSend (default true) lets /send work without a token, as it
	// did before tokens, but only to the allowed chats. While it is on, any
	// program on this computer, a brain among them, can message those
	// chats. New configs turn it off; scripts then send the token in
	// <data_dir>/scripts.token.
	TokenlessSend *bool `json:"tokenless_send,omitempty"`
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
	// Agentic runs can take a while; a day is far more than any should.
	defaultRunTimeoutMinutes = 30
	maxRunTimeoutMinutes     = 24 * 60
	defaultConcurrentRuns    = 2
	maxConcurrentRuns        = 64
)

// runTimeoutUnit is what one of run_timeout_minutes is, a variable so that
// tests need not wait a minute.
var runTimeoutUnit = time.Minute

// RunTimeout is how long a brain's run may take, 0 for no limit.
func (c *Config) RunTimeout() time.Duration {
	return time.Duration(intOr(c.RunTimeoutMinutes, defaultRunTimeoutMinutes)) * runTimeoutUnit
}

// RunLimit is how many Claude runs may go at once, 0 for no limit.
func (c *Config) RunLimit() int { return intOr(c.MaxConcurrentRuns, defaultConcurrentRuns) }

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

// TokenlessSend reports whether /send works without a token.
func (c *Config) TokenlessSend() bool { return boolOr(c.ScriptAPI.TokenlessSend, true) }

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

// warnOddChats logs entries of allowed_numbers and allowed_groups that can
// never match a chat: an entry that is no phone number, and a group the
// bridge compares as it is written, with a typo or spaces. A number is
// read however it was written (phoneNumber), so only one that is none is
// named. They are logged rather than refused, so a config that ran
// before still starts.
func warnOddChats(path string, c *Config) {
	var odd []string
	for _, n := range c.AllowedNumbers {
		if _, err := phoneNumber(n); err != nil {
			odd = append(odd, fmt.Sprintf("allowed_numbers %q (digits with the country code)", n))
		}
	}
	for _, g := range c.AllowedGroups {
		switch want, err := allowListGroup(g); {
		case err != nil:
			odd = append(odd, fmt.Sprintf("allowed_groups %q (digits@g.us)", g))
		case want != g:
			odd = append(odd, fmt.Sprintf("allowed_groups %q (write it as %s)", g, want))
		}
	}
	if len(odd) > 0 {
		log.Printf("config %s: these entries match no chat: %s", path, strings.Join(odd, "; "))
	}
}

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

// LoadExistingConfig is LoadConfig for a command about a bridge that is
// set up: a config file that is not there is an error, not the defaults,
// so that a mistyped --config is reported rather than checked, or used,
// as an empty config.
func LoadExistingConfig(cfgPath string) (*Config, error) {
	if _, err := os.Stat(cfgPath); errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no config file at %s (run fridolin init)", cfgPath)
	}
	return LoadConfig(cfgPath)
}

// dataDirName and legacyDataDirName are the home-relative names of the data
// dir, now and before the project was renamed. compat: installs from before
// the rename keep the old one, see defaultDataDir.
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

// Where the data dir came from, as the log and `fridolin paths` name it.
const (
	dataDirFromConfig  = "config"  // data_dir in the config file
	dataDirFromDefault = "default" // ~/.fridolin
	dataDirFromLegacy  = "legacy"  // the folder from before the rename
)

// resolveDataDir is the data dir a config file's data_dir value stands
// for, and where it came from: the value itself, or for an empty one
// defaultDataDir's choice.
func resolveDataDir(value, home string) (dir, source string) {
	dir = value
	if dir == "" {
		dir = defaultDataDir(home)
	}
	dir = fsx.ExpandHome(dir, home)
	return dir, dataDirSource(value, dir, home)
}

// dataDirSource says where dir, the data dir resolveDataDir made of the
// config file's data_dir value, came from. It looks at no folder: once
// the bridge has made the data dir, asking defaultDataDir again could
// answer otherwise.
func dataDirSource(value, dir, home string) string {
	switch {
	case value != "":
		return dataDirFromConfig
	case dir == filepath.Join(home, legacyDataDirName):
		return dataDirFromLegacy
	}
	return dataDirFromDefault
}

// effective derives the running configuration from the on-disk layer:
// defaults, ~ expansion, validation. It writes nothing, see EnsureDirs.
func effective(file *Config) (*Config, error) {
	cfg, err := file.clone()
	if err != nil {
		return nil, err
	}

	home := homeDir()
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
		cfg.CodexModel = defaultCodexModel
	}
	if cfg.CodexEffort == "" {
		cfg.CodexEffort = "high"
	}
	cfg.DataDir, _ = resolveDataDir(cfg.DataDir, home)
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(cfg.DataDir, "bridge.db")
	}
	if cfg.MediaDir == "" {
		cfg.MediaDir = filepath.Join(cfg.DataDir, "media")
	}
	cfg.DBPath = fsx.ExpandHome(cfg.DBPath, home)
	cfg.MediaDir = fsx.ExpandHome(cfg.MediaDir, home)
	if cfg.MaxChunkSize == 0 {
		cfg.MaxChunkSize = defaultChunkSize
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
		cfg.HTTPListen = defaultHTTPListen
	}
	if cfg.Queue.Mode == "" {
		cfg.Queue.Mode = queueModeQueue
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
	cfg.Backup.RclonePath = fsx.ExpandHome(cfg.Backup.RclonePath, home)
	cfg.Plaud.PhoneDir = fsx.ExpandHome(cfg.Plaud.PhoneDir, home)

	// Compared with senders as digits, however they were typed into the
	// file. The dashboard writes them so; a hand edit may not have.
	cfg.AllowedNumbers = allowedNumbersAsNumbers(cfg.AllowedNumbers)
	cfg.GroupMembers = groupMembersAsNumbers(cfg.GroupMembers)
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

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// EnsureDirs creates the data dir and the folders of the database and the
// media. effective does not: it runs for check and open too, which must
// not make a ~/.fridolin for a config file that is not there, and for
// every dashboard save, which would make a folder typed into Settings
// before the restart that uses it. The bridge calls EnsureDirs once, as it
// starts.
func (c *Config) EnsureDirs() error {
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(c.DBPath), 0o700); err != nil {
		return fmt.Errorf("creating db directory: %w", err)
	}
	if err := os.MkdirAll(c.MediaDir, 0o700); err != nil {
		return fmt.Errorf("creating media directory: %w", err)
	}
	return nil
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
	// depends on the folder the bridge was started from, and fsx.Within
	// cannot relate it to the home folder, so it would get past the home
	// check.
	dataDir := absPath(cfg.DataDir)
	dbDir := absPath(filepath.Dir(cfg.DBPath))
	home := homeDir()
	homeOrAbove := func(dir string) bool {
		return home != "" && fsx.Within(fsx.Canonical(home), fsx.Canonical(dir))
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
		if err := fsx.RestrictToOwner(dataDir); err != nil {
			log.Printf("warning: could not make %s private: %v", dataDir, err)
		}
	}

	if !fsx.Within(fsx.Canonical(dbDir), fsx.Canonical(dataDir)) {
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

// validGitConfigs are the values sandbox.git_config takes once
// effective() has filled in the default, "read".
var validGitConfigs = map[string]bool{"read": true, "deny": true}

// validate rejects settings the bridge cannot run with. Brains are checked
// when they are resolved (ResolveBrains), which leaves one that clashes with
// another out instead of stopping them all. Zero brains is fine: a fresh
// install adds its first brain from the dashboard.
func (c *Config) validate() error {
	if c.AgentProvider != "claude" && c.AgentProvider != "codex" {
		return fmt.Errorf("agent_provider must be claude or codex, not %q", c.AgentProvider)
	}
	if !manifest.ValidEffort(c.CodexEffort) {
		return fmt.Errorf("codex_effort %q must be one of low, medium, high, xhigh, max", c.CodexEffort)
	}
	if !manifest.ValidEffort(c.BrainEffort) {
		return fmt.Errorf("brain_effort %q must be one of low, medium, high, xhigh, max", c.BrainEffort)
	}
	if !validQueueModes[c.Queue.Mode] {
		return fmt.Errorf("queue.mode must be %q or %q, not %q", queueModeQueue, queueModeInterrupt, c.Queue.Mode)
	}
	if !validGitConfigs[c.Sandbox.GitConfig] {
		return fmt.Errorf("sandbox.git_config must be \"read\" or \"deny\", not %q", c.Sandbox.GitConfig)
	}
	if err := c.Sandbox.validate(runtime.GOOS); err != nil {
		return err
	}
	for _, name := range c.ChildEnvPassthrough {
		if !validEnvName(name) {
			return fmt.Errorf("child_env_passthrough: %q is not a variable name: use letters, digits and _, and do not start with a digit", name)
		}
	}
	if c.MaxChunkSize < minChunkSize {
		return fmt.Errorf("max_chunk_size %d is too small: use at least %d, or 0 for the default of %d", c.MaxChunkSize, minChunkSize, defaultChunkSize)
	}
	if n := intOr(c.RunTimeoutMinutes, defaultRunTimeoutMinutes); n < 0 || n > maxRunTimeoutMinutes {
		return fmt.Errorf("run_timeout_minutes must be between 0 (no limit) and %d, not %d", maxRunTimeoutMinutes, n)
	}
	if n := c.RunLimit(); n < 0 || n > maxConcurrentRuns {
		return fmt.Errorf("max_concurrent_runs must be between 0 (no limit) and %d, not %d", maxConcurrentRuns, n)
	}
	if c.MaxMediaMB < 1 || c.MaxMediaMB > maxMaxMediaMB {
		return fmt.Errorf("max_media_mb must be between 1 and %d, not %d", maxMaxMediaMB, c.MaxMediaMB)
	}
	if c.HTTPEnabled() && !isLoopbackAddr(c.HTTPListen) {
		return fmt.Errorf("http_listen %q must be a loopback address such as 127.0.0.1:8765 — the API and dashboard are for this machine only", c.HTTPListen)
	}
	if err := validateGroupMembers(c.GroupMembers, c.AllowedGroups); err != nil {
		return err
	}
	if err := c.Backup.validate(); err != nil {
		return err
	}
	return c.Plaud.validate()
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

// homeDir is the user's home folder, $HOME on Unix, or "" when there is
// none. Every part of the bridge looks it up here, so that ~ and the
// defaults under it mean the same folder everywhere.
func homeDir() string {
	home, _ := os.UserHomeDir()
	return home
}

// isTruthy reports whether v reads as true: true, 1, yes or on.
func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}
