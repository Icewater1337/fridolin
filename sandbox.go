package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// sandboxExecPath is the macOS sandbox wrapper used for brain spawns.
const sandboxExecPath = "/usr/bin/sandbox-exec"

// SandboxProfiler generates a macOS sandbox-exec profile per brain.
//
// Writing is denied by default: a brain may write only its own folder, its
// own media inbox, and its own state the bridge keeps for it (a Claude
// Code config dir, TMPDIR and caches; see brainState). Everything else
// your user may write stays closed, so a prompt-injected brain cannot plant
// code that runs outside its sandbox: in ~/.claude, which your own sessions
// load, in a LaunchAgent, a shell startup file, a program on PATH or the
// bridge's own install. Reading is allowed by default, with an explicit
// denylist: the other brains' folders, the bridge's own files, your Claude
// Code setup and instructions, the keychains, and the usual home-directory
// hotspots (keys, mail, browser data). That is a containment measure, not a
// jail: "What the sandbox does not cover" in docs/CONFIGURATION.md lists
// what it leaves open. Add anything else that must stay unreadable to
// sandbox.deny_paths in config.json.
type SandboxProfiler struct {
	mu sync.RWMutex

	enabled bool

	// workDirs maps brain id → its folder, by the real path the bridge runs
	// the brain in and by the path as configured where that differs (see
	// BrainSpec.Path), so that each profile allows its own folder and
	// denies the others' both ways.
	workDirs map[string][]string

	sandboxPolicy

	// configFile is the config.json the bridge runs with; see SetConfigFile.
	configFile string

	// execPath is sandboxExecPath; tests point it at a missing file.
	execPath string

	// bridgeExe is the running bridge binary; tests point it elsewhere.
	bridgeExe string
}

// sandboxPolicy is what the profiles take from the config, with its paths
// made absolute.
type sandboxPolicy struct {
	// dataDir is the bridge's runtime dir, denied wholesale except for this
	// brain's media inbox and state.
	dataDir  string
	mediaDir string

	// dbPath is the WhatsApp session and chat database, which db_path can
	// put outside the data dir.
	dbPath string

	// credsDir holds per-brain credential files; see SandboxConfig.CredentialsDir.
	credsDir string

	// gitConfig is "read" or "deny".
	gitConfig string

	// plaudShared: the Plaud login in ~/.plaud is meant for brains too.
	plaudShared bool

	// inheritUser: inherit_claude_user_config lets brains load your own
	// Claude Code setup and share its state, rather than keep their own.
	inheritUser bool

	// claudeDir is the bridge's own CLAUDE_CONFIG_DIR, where your Claude
	// Code keeps its setup and state instead of ~/.claude, or "".
	claudeDir string

	// inbox and state name what the bridge keeps for brain id outside its
	// folder, the only places there the brain may write: its media inbox
	// (see mediaInbox) and its own state (see brainStateOf). Each is nil
	// without a media or data folder. The bridge creates them before a run
	// (see prepareBrainState); the profile only names them.
	inbox func(id string) string
	state func(id string) brainState

	// extraDeny and extraAllow come from config.
	extraDeny  []string
	extraAllow []string
}

// sandboxPolicyOf takes the profiles' settings from cfg.
func sandboxPolicyOf(cfg *Config) sandboxPolicy {
	// These may be relative, and the bridge opens them relative to its
	// working directory; the comparisons need them absolute.
	p := sandboxPolicy{
		dataDir:     absPath(cfg.DataDir),
		dbPath:      absPath(cfg.DBPath),
		mediaDir:    absPath(cfg.MediaDir),
		credsDir:    cfg.Sandbox.CredentialsDir,
		gitConfig:   cfg.Sandbox.GitConfig,
		plaudShared: cfg.Plaud.BrainAccess,
		inheritUser: cfg.InheritClaudeUserConfig,
		extraDeny:   append([]string(nil), cfg.Sandbox.DenyPaths...),
		extraAllow:  append([]string(nil), cfg.Sandbox.AllowPaths...),
	}
	if p.mediaDir == "" && p.dataDir != "" {
		p.mediaDir = filepath.Join(p.dataDir, "media")
	}
	if media := p.mediaDir; media != "" {
		p.inbox = func(id string) string { return mediaInbox(media, id) }
	}
	if data := p.dataDir; data != "" {
		p.state = func(id string) brainState { return brainStateOf(data, id) }
	}
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); filepath.IsAbs(dir) {
		p.claudeDir = filepath.Clean(dir)
	}
	return p
}

// NewSandboxProfiler builds a profiler for the configured brains.
func NewSandboxProfiler(workDirs map[string][]string, cfg *Config) *SandboxProfiler {
	sp := &SandboxProfiler{execPath: sandboxExecPath}
	if exe, err := os.Executable(); err == nil {
		sp.bridgeExe = exe
	}
	sp.Update(workDirs, cfg)
	return sp
}

// Update replaces the brain set and settings. Profiles are built per run,
// so the next run of every brain picks the change up.
func (sp *SandboxProfiler) Update(workDirs map[string][]string, cfg *Config) {
	wd := make(map[string][]string, len(workDirs))
	for k, v := range workDirs {
		// Made absolute, "" would be the bridge's working directory.
		wd[k] = slices.DeleteFunc(slices.Clone(v), func(d string) bool { return d == "" })
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.enabled = cfg.Sandbox.Enabled
	sp.workDirs = wd
	sp.sandboxPolicy = sandboxPolicyOf(cfg)
}

// SetConfigFile names the config.json the bridge runs with. It lives in the
// data dir by default, but --config or FRIDOLIN_CONFIG can put it anywhere,
// so the profile denies it, its .bak, the temp file each save writes, and
// the .env files older versions read: the one next to it and the one in the
// working directory.
func (sp *SandboxProfiler) SetConfigFile(path string) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.configFile = absPath(path)
}

// Enabled reports whether brain spawns should be sandboxed.
func (sp *SandboxProfiler) Enabled() bool {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	return sp.enabled
}

// sitePaths returns what the site's sandbox keeps from module code as well
// (site_sandbox.go), beyond the data dir and the usual secrets: deny, the
// DB, the config file and its companions, the credentials dir and the
// sandbox.deny_paths entries; noWrite, the bridge binary and its install
// folder or checkout.
func (sp *SandboxProfiler) sitePaths() (deny, noWrite []string) {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	if sp.dataDir != "" {
		deny = append(deny, sp.dataDir)
	}
	if sp.dbPath != "" {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			deny = append(deny, sp.dbPath+suffix)
		}
	}
	if sp.configFile != "" {
		deny = append(deny, sp.configFile, sp.configFile+".bak", dotEnvPath(sp.configFile))
	}
	if sp.credsDir != "" {
		deny = append(deny, absPath(sp.credsDir))
	}
	for _, p := range sp.extraDeny {
		deny = append(deny, absPath(p))
	}
	if sp.bridgeExe != "" {
		noWrite = append(noWrite, sp.bridgeExe)
		l := layoutOf(sp.bridgeExe)
		for _, dir := range []string{l.Dir, l.Source} {
			if dir != "" {
				noWrite = append(noWrite, dir)
			}
		}
	}
	return deny, noWrite
}

// defaultDenySubpaths are home-relative directories no brain may read.
var defaultDenySubpaths = []string{
	".ssh",
	".gnupg",
	".aws",
	".config/gh",
	".config/gcloud",
	".config/rclone",
	// The personal-data denylist that scripts/check-personal-data.sh reads,
	// under both names the project has had (compat: the old one may still
	// be on disk, as may the old log folder below).
	".config/fridolin",
	".config/whatsapp-claude-bridge",
	// The bridge's own logs (the launchd agent's, see service.go), which
	// name every chat.
	"Library/Logs/fridolin",
	"Library/Logs/whatsapp-claude-bridge",
	"Documents",
	"Downloads",
	"Desktop",
	"Movies",
	"Music",
	"Pictures",
	"Library/Mail",
	"Library/Messages",
	"Library/Calendars",
	"Library/Cookies",
	"Library/Application Support/AddressBook",
	"Library/Application Support/Notes",
	// Browser profiles: cookies, saved logins, history and the pages'
	// storage, where a dashboard session or an older dashboard's token
	// lives. Google as a whole, for Chrome's Beta and Canary beside it;
	// the browsers in browserProfilePrefixes by the start of the name.
	"Library/Application Support/Google",
	"Library/Application Support/Firefox",
	"Library/Application Support/BraveSoftware",
	"Library/Application Support/Arc",
	"Library/Application Support/Chromium",
	"Library/Safari",
	"Library/Containers",
	"Library/Group Containers",
}

// browserProfilePrefixes are browsers that keep each release channel in a
// folder of its own beside the stable one in ~/Library/Application Support
// ("Microsoft Edge Beta", "Vivaldi Snapshot", "com.operasoftware.OperaGX"),
// so every folder whose name starts with one of them is denied.
var browserProfilePrefixes = []string{
	"Microsoft Edge",
	"Vivaldi",
	"com.operasoftware.Opera",
}

// browserProfilePattern matches, below Application Support, every folder
// named with one of browserProfilePrefixes and everything in it.
func browserProfilePattern() string {
	quoted := make([]string, len(browserProfilePrefixes))
	for i, p := range browserProfilePrefixes {
		quoted[i] = regexp.QuoteMeta(p)
	}
	return "/(" + strings.Join(quoted, "|") + ")"
}

// inBrowserProfile reports whether p is in a folder browserProfilePattern
// matches in home's Application Support.
func inBrowserProfile(home, p string) bool {
	appSupport := filepath.Join(home, "Library", "Application Support")
	return regexp.MustCompile("^" + regexp.QuoteMeta(appSupport) + browserProfilePattern()).MatchString(p)
}

// defaultWriteDenySubpaths and defaultWriteDenyLiterals are home-relative
// paths that are loaded or run by something outside any sandbox: your own
// Claude Code sessions (settings and their hooks, CLAUDE.md, rules, agents,
// commands, skills, plugins, output styles), launchd at your next login,
// your own shells, the tmux server the activity viewer starts. A brain's
// profile denies every write outside its own places and so needs no list;
// the live site's sandbox (site_sandbox.go) keeps the paths a module
// declares off these.
var defaultWriteDenySubpaths = []string{
	".claude/hooks",
	".claude/rules",
	".claude/agents",
	".claude/commands",
	".claude/skills",
	".claude/plugins",
	".claude/output-styles",
	"Library/LaunchAgents",
	".config/fish",
	".config/tmux",
}

var defaultWriteDenyLiterals = []string{
	".claude/settings.json",
	".claude/settings.local.json",
	".claude/CLAUDE.md",
	".zshenv",
	".zprofile",
	".zshrc",
	".zlogin",
	".zlogout",
	".bash_profile",
	".bash_login",
	".bashrc",
	".profile",
	".tmux.conf",
}

// defaultDenyLiterals are individual home-relative files no brain may read.
var defaultDenyLiterals = []string{
	".claude/history.jsonl",
	".git-credentials",
	".netrc",
	".zsh_history",
	".bash_history",
}

// claudeConfigSubpaths and claudeConfigLiterals are what Claude Code loads
// from its config dir as configuration: settings and their hooks, CLAUDE.md,
// rules, agents, commands, skills, plugins, output styles. With
// inherit_claude_user_config, brains share your config dir, ~/.claude, and
// write the CLI's state there, but never these: your own sessions and every
// other brain's next run would load them.
var claudeConfigSubpaths = []string{"hooks", "rules", "agents", "commands", "skills", "plugins", "output-styles"}

var claudeConfigLiterals = []string{"settings.json", "settings.local.json", "CLAUDE.md"}

// instructionFiles are the memory files Claude Code reads as instructions,
// whatever --setting-sources says, from every folder above its working
// directory.
var instructionFiles = []string{"CLAUDE.md", "CLAUDE.local.md"}

// writableDevices are the devices any program writes to: discarded output,
// the controlling terminal, its own file descriptors. They lead nowhere a
// brain could keep anything.
var writableDevices = []struct{ filter, path string }{
	{"literal", "/dev/null"},
	{"literal", "/dev/zero"},
	{"literal", "/dev/tty"},
	{"literal", "/dev/dtracehelper"},
	{"subpath", "/dev/fd"},
}

// Profile returns the sandbox profile source for the brain with this id. It
// fails when a path cannot be written into the profile: a profile that
// silently skipped a rule would protect less than the config says.
//
// The sandbox applies the last rule that matches, so the order of the
// sections is the precedence model, from weakest to strongest:
//
//  1. Writing is denied everywhere, but for a few devices.
//  2. Soft denies: reading private home folders, your Claude Code setup,
//     deny_paths entries. The allows below may open them.
//  3. Allows: the brain's own folder (read and write), allow_paths (read).
//  4. Hard denies: other brains' folders, the bridge's own files, the
//     keychains and your instructions above the brain's folder. Nothing a
//     brain or an allow_paths entry is given opens these again...
//  5. ...but what the bridge gives this brain inside them: its media inbox,
//     its state dir and its runs' brain API tokens (to read); and with
//     plaud.brain_access the Plaud login, which the Plaud CLI refreshes.
//  6. What stays closed inside the brain's own places: deny_paths entries
//     inside its folder, the database, and the folders themselves, which the
//     brain may fill but not move, remove or swap for a symlink.
func (sp *SandboxProfiler) Profile(brainID string) (string, error) {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	home := homeDir()
	if !filepath.IsAbs(home) {
		// Every home-relative deny would silently become a relative path.
		return "", fmt.Errorf("HOME %q is not an absolute path", home)
	}
	// Every profile denies every other brain's folder, so a folder that
	// holds HOME would close all of it to the others, and open everything
	// the sandbox protects there to itself.
	ids := make([]string, 0, len(sp.workDirs))
	for id := range sp.workDirs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		for _, dir := range sp.workDirs[id] {
			if isWithin(home, dir) || isWithin(resolvePath(home), resolvePath(dir)) {
				return "", fmt.Errorf("brain %s: its folder %s is your home folder or holds it, which the sandbox cannot protect; give the brain a folder of its own", id, dir)
			}
		}
	}
	own := sp.workDirs[brainID]
	protected := sp.protected(home, sp.bridgeExe)
	for id, dirs := range sp.workDirs {
		if id != brainID {
			for _, dir := range dirs {
				protected = append(protected, protectedPath{dir, true, "brain " + id + "'s folder"})
			}
		}
	}
	for _, dir := range own {
		if p, ok := protectedIn(dir, protected); ok {
			return "", fmt.Errorf("brain %s: its folder %s %s, which the sandbox keeps from brains; give the brain a folder of its own", brainID, dir, p)
		}
	}

	var b profileBuilder
	b.creatable = b.spellings(sp.userClaudeDir(home), true)
	b.trustRoot(home)
	for _, dir := range []string{sp.dataDir, sp.credsDir, sp.mediaDir} {
		if dir != "" {
			b.trustRoot(dir)
		}
	}
	b.WriteString("(version 1)\n(allow default)\n\n")

	// ── 1. writing ───────────────────────────────────────────────────────
	b.WriteString(";; 1. Writing is denied everywhere; this brain's own places are allowed below\n")
	b.WriteString("(deny file-write* (subpath \"/\"))\n")
	for _, d := range writableDevices {
		b.allow("file-write*", d.filter, d.path)
	}
	b.WriteString("\n")

	// ── 2. soft denies ───────────────────────────────────────────────────
	b.WriteString(";; 2. Private places: not readable, unless allowed below\n")
	for _, sub := range defaultDenySubpaths {
		b.deny("file-read*", "subpath", filepath.Join(home, sub))
	}
	b.denyBelow("file-read*", filepath.Join(home, "Library", "Application Support"), browserProfilePattern())
	for _, lit := range defaultDenyLiterals {
		b.deny("file-read*", "literal", filepath.Join(home, lit))
	}
	if !sp.inheritUser {
		// Your Claude Code setup and state: settings and their hooks,
		// plugins, skills, every session's transcript, and ~/.claude.json
		// with its MCP servers and the backups the CLI keeps of it. A
		// brain has a config dir of its own (see brainState).
		for _, dir := range sp.userClaudeDirs(home) {
			b.deny("file-read*", "subpath", dir)
		}
		b.deny("file-read*", "literal", filepath.Join(home, ".claude.json"))
		b.denyBelow("file-read*", home, `/\.claude\.json\.[^/]*$`)
	} else {
		// Brains share your config dir; they still read no other session's
		// records: transcripts in projects/, one file per running CLI in
		// sessions/.
		dir := sp.userClaudeDir(home)
		b.deny("file-read*", "subpath", filepath.Join(dir, "projects"))
		b.deny("file-read*", "subpath", filepath.Join(dir, "sessions"))
	}
	if !sp.plaudShared {
		// The Plaud CLI's login, unless plaud.brain_access shares it.
		b.deny("file-read*", "subpath", filepath.Join(home, ".plaud"))
	}
	// Reading git config lets a brain commit in its own folder (name, email,
	// signing settings); git_config "deny" hides it.
	gitFiles := gitConfigChain(home)
	xdgGit := filepath.Join(home, ".config", "git")
	if sp.gitConfig == "deny" {
		for _, f := range gitFiles {
			b.deny("file-read*", "literal", f)
		}
		b.deny("file-read*", "subpath", xdgGit)
	}
	// A deny_paths entry inside this brain's folder comes in section 6,
	// where it wins over the folder; one that holds the folder leaves the
	// folder itself open to this brain and closes it to every other.
	var innerDeny []string
	for _, p := range sp.extraDeny {
		if withinAny(absPath(p), own) || withinAny(resolvePath(absPath(p)), own) {
			innerDeny = append(innerDeny, p)
			continue
		}
		b.deny("file-read*", "subpath", p)
	}
	b.WriteString("\n")

	// ── 3. allows ────────────────────────────────────────────────────────
	b.WriteString(";; 3. This brain's own folder, and allow_paths\n")
	// Both ways: allow follows no symlink, and the brain runs in the real
	// path, which a folder linked from a default-denied one, such as
	// ~/assistant → ~/Documents/assistant, would otherwise leave denied.
	var owns []string
	for _, dir := range own {
		owns = append(owns, b.allow("file-read*", "subpath", dir)...)
		b.allow("file-write*", "subpath", dir)
	}
	// Reading only: an allow_paths entry re-opens what the defaults closed
	// for reading, never for writing.
	for _, p := range sp.extraAllow {
		b.allow("file-read*", "subpath", p)
	}
	b.WriteString("\n")

	// ── 4. hard denies ───────────────────────────────────────────────────
	b.WriteString(";; 4. Kept from every brain, whatever is allowed above\n")
	var others []string
	for id, dirs := range sp.workDirs {
		if id != brainID {
			others = append(others, dirs...)
		}
	}
	sort.Strings(others)
	for _, dir := range others {
		b.deny("file-read*", "subpath", dir)
		b.deny("file-write*", "subpath", dir)
	}
	// The DB holds the WhatsApp session keys (enough to take the account
	// over from another machine), the polls and their votes; the dashboard
	// token and backup credentials live in the data dir too. Both names the
	// data dir has had, whichever is active: an install that moved to
	// ~/.fridolin can leave the old folder behind, with an old copy of the
	// session keys, the dashboard token and the Drive login.
	dataDirs := sp.dataDirs(home)
	for _, dir := range dataDirs {
		b.deny("file-read*", "subpath", dir)
		b.deny("file-write*", "subpath", dir)
	}
	// The media folder, wherever media_dir puts it: attachments wait there
	// for their brain, which moves them into its own inbox (see media.go).
	if sp.mediaDir != "" {
		b.deny("file-read*", "subpath", sp.mediaDir)
		b.deny("file-write*", "subpath", sp.mediaDir)
	}
	// db_path can put the DB anywhere. Its folder goes too, unless it is a
	// folder the brain needs (HOME or above, a temp dir), where denying it
	// would take the brain's home or scratch space away.
	dbFiles := sp.dbFiles()
	if dir := sp.dbFolder(home); dir != "" {
		b.deny("file-read*", "subpath", dir)
		b.deny("file-write*", "subpath", dir)
	}
	for _, f := range dbFiles {
		b.deny("file-read*", "literal", f)
		b.deny("file-write*", "literal", f)
	}
	// The config file (saved with a .bak copy), where the data dir deny
	// does not cover it already, and the .env files older versions read
	// settings from: the one next to the config, and the one in the
	// working directory when there was none there. The bridge reads
	// neither any more, but either may still hold settings and secrets.
	if sp.configFile != "" {
		for _, f := range []string{sp.configFile, sp.configFile + ".bak", dotEnvPath(sp.configFile), ".env"} {
			if abs, err := filepath.Abs(f); err == nil && !withinAny(abs, dataDirs) {
				b.deny("file-read*", "literal", abs)
				b.deny("file-write*", "literal", abs)
			}
		}
		// Every save writes the whole config to a temp file next to it
		// first (writeFileAtomic), which is readable until the rename.
		if !withinAny(sp.configFile, dataDirs) {
			tmp := "/" + regexp.QuoteMeta("."+filepath.Base(sp.configFile)+".") + `[^/]*\.tmp$`
			b.denyBelow("file-read*", filepath.Dir(sp.configFile), tmp)
			b.denyBelow("file-write*", filepath.Dir(sp.configFile), tmp)
		}
	}
	if sp.credsDir != "" {
		b.deny("file-read*", "subpath", sp.credsDir)
		b.deny("file-write*", "subpath", sp.credsDir)
	}
	// Claude Code reads CLAUDE.md, CLAUDE.local.md and .claude/ (its
	// CLAUDE.md, rules, skills, agents and commands) in every folder above
	// its working directory as part of the project, whatever
	// --setting-sources says, so a brain in HOME read ~/CLAUDE.md: your
	// instructions, not the brain's. The claudeMdExcludes a brain runs with
	// (see excludeInstructionsAbove) skip the CLAUDE.md files and rules,
	// but neither the skills, agents and commands nor the brain's own tools.
	// inherit_claude_user_config opens them again.
	if !sp.inheritUser {
		// The user memory and rules in your config dir, denied with the
		// rest of it above, again here so that no allow_paths entry for
		// ~/.claude opens them.
		for _, dir := range sp.userClaudeDirs(home) {
			b.deny("file-read*", "literal", filepath.Join(dir, "CLAUDE.md"))
			b.deny("file-read*", "subpath", filepath.Join(dir, "rules"))
		}
		for _, dir := range foldersAbove(own...) {
			for _, f := range instructionFiles {
				b.deny("file-read*", "literal", filepath.Join(dir, f))
			}
			if resolvePath(dir) != resolvePath(home) {
				b.deny("file-read*", "subpath", filepath.Join(dir, ".claude"))
			}
		}
	}
	// What runs or loads outside the sandbox and lies where a brain may
	// write: git's configuration (core.fsmonitor and aliases run in your
	// own git), the bridge's binary, its install and its checkout. Writing
	// is denied everywhere anyway; this keeps them closed when one of them
	// sits in a brain's folder.
	for _, f := range gitFiles {
		b.deny("file-write*", "literal", f)
	}
	b.deny("file-write*", "subpath", xdgGit)
	if sp.bridgeExe != "" {
		b.deny("file-write*", "literal", sp.bridgeExe)
	}
	for _, p := range sp.bridgeDirs() {
		b.deny("file-write*", "subpath", p)
	}
	b.WriteString("\n")

	// ── 5. what the bridge gives this brain ──────────────────────────────
	b.WriteString(";; 5. This brain's media inbox, state and brain API tokens, and a shared Plaud login\n")
	var pins []string
	places := slices.Clone(own) // the brain's own places, for the folders above them
	if sp.inbox != nil {
		inbox := sp.inbox(brainID)
		b.allow("file-read*", "subpath", inbox)
		pins = append(pins, b.allow("file-write*", "subpath", inbox)...)
		places = append(places, inbox)
	}
	var state brainState
	if sp.state != nil {
		state = sp.state(brainID)
		b.allow("file-read*", "subpath", state.dir)
		pins = append(pins, b.allow("file-write*", "subpath", state.dir)...)
		for _, f := range state.folders() {
			pins = append(pins, b.spellings(f, true)...)
		}
		places = append(places, state.dir)
	}
	// The brain API tokens of this brain's runs (see brainRunEnv), to read
	// and no more; every other brain's stay denied with the data dir. No
	// brain reads its calendar token: the bridge makes its calendar calls
	// (google_calendar.go), so the files stay with the bridge.
	if sp.dataDir != "" {
		b.allow("file-read*", "subpath", runTokenDir(sp.dataDir, brainID))
	}
	// plaud.brain_access shares your Plaud login with brains, and the Plaud
	// CLI rewrites it in place when it refreshes a login about to expire,
	// after it sets the modes of ~/.plaud and of the file. A refresh it could
	// not save failed the brain's command as not logged in, and when Plaud
	// had rotated the refresh token, left the revoked one behind, which
	// signed Plaud out at the bridge's next check. The file's contents and
	// mode and the folder's mode, and nothing else there: not removing,
	// renaming or creating the file. A brain that could have deleted the
	// login and put a symlink in its place (to ~/.zshrc, say) had the
	// bridge's own Plaud CLI, which runs outside the sandbox, write your
	// next sign-in through it, and chmod what it points to.
	if sp.plaudShared {
		plaud := filepath.Join(home, ".plaud")
		tokens := filepath.Join(plaud, "tokens.json")
		b.allow("file-write-data", "literal", tokens)
		b.allow("file-write-mode", "literal", tokens)
		b.allow("file-write-mode", "literal", plaud)
	}
	// The folders above those places, and above the brain's folder, may be
	// looked at, though not listed: programs stat every folder on the way
	// to a path (mkdir -p, realpath), and failed in their own state below
	// the denied data dir, or in a brain folder below ~/Documents.
	for _, dir := range foldersAbove(places...) {
		b.allow("file-read-metadata", "literal", dir)
	}
	if sp.inheritUser {
		// The CLI's state in your config dir, which brains share with you.
		b.allow("file-write*", "subpath", sp.userClaudeDir(home))
		if sp.claudeDir == "" {
			b.allow("file-write*", "literal", filepath.Join(home, ".claude.json"))
			b.allow("file-write*", "regex", "^"+regexp.QuoteMeta(home)+`/\.claude\.json\.[^/]*$`)
		}
	}
	b.WriteString("\n")

	// ── 6. kept inside this brain's own places ───────────────────────────
	b.WriteString(";; 6. Inside this brain's own places: what stays closed, and the folders themselves\n")
	for _, p := range innerDeny {
		b.deny("file-read*", "subpath", p)
		b.deny("file-write*", "subpath", p)
	}
	// An allow_paths entry inside one opens it for reading again, as it
	// does with a deny_paths entry anywhere else.
	for _, p := range sp.extraAllow {
		if withinAny(absPath(p), innerDeny) {
			b.allow("file-read*", "subpath", p)
		}
	}
	// The folders between the brain's folder and such an entry stay put:
	// a brain that renamed one would take the entry out from under its
	// rule (the sandbox matches paths) and read it under the new name.
	for _, p := range innerDeny {
		for _, s := range b.spellings(p, true) {
			for dir := filepath.Dir(s); withinAny(dir, owns) && !slices.Contains(owns, dir); dir = filepath.Dir(dir) {
				pins = append(pins, b.spellings(dir, true)...)
			}
		}
	}
	// No allow above can open the database again.
	for _, f := range dbFiles {
		b.deny("file-read*", "literal", f)
		b.deny("file-write*", "literal", f)
	}
	// The brain's folder, inbox and state folders themselves, in every
	// spelling. A brain that moved its folder aside and left a symlink in
	// its place pointed it at another brain's folder, whose profile then
	// failed (its folder had become one this profile denies), or at HOME,
	// which failed every profile; and the bridge, outside the sandbox,
	// followed the link to read and write what it keeps there for this
	// brain. The same goes for the inbox the bridge moves files into and
	// the state folders it creates.
	for _, dir := range own {
		pins = append(pins, b.spellings(dir, true)...)
	}
	slices.Sort(pins)
	for _, s := range slices.Compact(pins) {
		b.emit("deny file-write*", "literal", s)
	}
	if sp.inheritUser {
		sp.sharedClaudeDirRules(&b, home)
	}
	b.WriteString("\n")

	if b.err != nil {
		return "", b.err
	}
	return b.String(), nil
}

// sharedClaudeDirRules writes the rules for a config dir brains share with
// you (inherit_claude_user_config), after the allow that lets them write the
// CLI's state there.
func (sp *SandboxProfiler) sharedClaudeDirRules(b *profileBuilder, home string) {
	dir := sp.userClaudeDir(home)
	for _, sub := range claudeConfigSubpaths {
		b.deny("file-write*", "subpath", filepath.Join(dir, sub))
	}
	for _, lit := range claudeConfigLiterals {
		b.deny("file-write*", "literal", filepath.Join(dir, lit))
	}
	// Claude Code's record of every session, yours included: transcripts in
	// projects/, one file per running CLI in sessions/. The CLI writes its
	// own run's files there, and reading is denied above; so is moving files
	// out, since a rename is checked as file-write-unlink on its source and
	// a transcript moved elsewhere could be read there. The CLI deletes its
	// sessions/<pid>.json on exit, so sessions/ keeps unlink and only the
	// folder itself is pinned.
	projects := filepath.Join(dir, "projects")
	sessions := filepath.Join(dir, "sessions")
	b.deny("file-write-unlink", "subpath", projects)
	// Nor may a brain plant a symlink in either. One that linked a project
	// folder the CLI had not created yet into its own folder got that
	// project's later transcripts, yours included: the CLI wrote them
	// through the link, to where the brain reads. The CLI creates real
	// folders and files there, which stay allowed.
	b.denySymlinksIn(projects)
	b.denySymlinksIn(sessions)
	// Auto-memory: Claude Code loads projects/<project>/memory/MEMORY.md into
	// every new session in that project, yours included. A project folder
	// the CLI has not created yet cannot be listed, hence a pattern.
	b.denyBelow("file-write*", projects, `/[^/]+/memory(/.*)?$`)
	// The config dir and its two record folders stay put; the CLI creates
	// them on its first run (see pin).
	b.pin(dir)
	b.pin(projects)
	b.pin(sessions)
	if sp.claudeDir == "" {
		b.pin(home)
	}
}

// userClaudeDir is the config dir of your own Claude Code: the bridge's
// CLAUDE_CONFIG_DIR, or ~/.claude.
func (sp *SandboxProfiler) userClaudeDir(home string) string {
	if sp.claudeDir != "" {
		return sp.claudeDir
	}
	return filepath.Join(home, ".claude")
}

// userClaudeDirs is ~/.claude, and the bridge's CLAUDE_CONFIG_DIR when it
// names another folder: both can hold your setup.
func (sp *SandboxProfiler) userClaudeDirs(home string) []string {
	dirs := []string{filepath.Join(home, ".claude")}
	if sp.claudeDir != "" && sp.claudeDir != dirs[0] {
		dirs = append(dirs, sp.claudeDir)
	}
	return dirs
}

// dataDirs is the data dir and both names it has had in HOME.
func (p sandboxPolicy) dataDirs(home string) []string {
	dirs := []string{filepath.Join(home, dataDirName), filepath.Join(home, legacyDataDirName)}
	if p.dataDir != "" && !slices.Contains(dirs, filepath.Clean(p.dataDir)) {
		dirs = append(dirs, p.dataDir)
	}
	return dirs
}

// dbFiles is the database and the files SQLite keeps next to it.
func (p sandboxPolicy) dbFiles() []string {
	if p.dbPath == "" {
		return nil
	}
	var out []string
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		out = append(out, p.dbPath+suffix)
	}
	return out
}

// dbFolder is the folder of a database outside the data dir, which the
// profile denies with it, or "": it is left open when it is HOME or above,
// or a temp dir, which a brain needs.
func (p sandboxPolicy) dbFolder(home string) string {
	if p.dbPath == "" || withinAny(p.dbPath, p.dataDirs(home)) {
		return ""
	}
	if dir := filepath.Dir(p.dbPath); !sharedDir(dir, home) {
		return dir
	}
	return ""
}

// bridgeDirs are the folder the bridge was installed in and the checkout it
// was built from: launchd runs the bridge outside any sandbox, it runs its
// tools from there, and setup.sh builds what is in the checkout.
func (sp *SandboxProfiler) bridgeDirs() []string {
	if sp.bridgeExe == "" {
		return nil
	}
	l := layoutOf(sp.bridgeExe)
	var out []string
	for _, d := range []string{l.Dir, l.Source} {
		if d != "" && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// protectedPath is a path the sandbox keeps from brains, which a brain's
// folder must therefore neither be nor hold: the folder's allow would open
// it. A brain folder may not lie inside one marked inside either: what the
// bridge keeps for itself, and the folders whose files run or load outside
// the sandbox.
type protectedPath struct {
	path   string
	inside bool
	what   string // what it is, for the error
}

// protected lists the protected paths for brain folders in HOME home, with
// the bridge binary exe ("" for none).
func (p sandboxPolicy) protected(home, exe string) []protectedPath {
	var out []protectedPath
	add := func(path string, inside bool, what string) {
		if path != "" {
			out = append(out, protectedPath{filepath.Clean(absPath(path)), inside, what})
		}
	}
	for _, sub := range defaultDenySubpaths {
		add(filepath.Join(home, sub), false, "~/"+sub)
	}
	for _, lit := range defaultDenyLiterals {
		add(filepath.Join(home, lit), false, "~/"+lit)
	}
	add(filepath.Join(home, ".claude.json"), false, "~/.claude.json")
	add(filepath.Join(home, ".gitconfig"), false, "~/.gitconfig")
	add(filepath.Join(home, ".config", "git"), false, "~/.config/git")
	if !p.plaudShared {
		add(filepath.Join(home, ".plaud"), false, "~/.plaud")
	}
	add(filepath.Join(home, ".claude"), true, "your Claude Code folder ~/.claude")
	add(p.claudeDir, true, "your Claude Code folder (CLAUDE_CONFIG_DIR)")
	add(filepath.Join(home, "Library", "LaunchAgents"), true, "~/Library/LaunchAgents")
	for _, dir := range p.dataDirs(home) {
		add(dir, true, "the bridge's data folder")
	}
	add(p.mediaDir, true, "the media folder")
	add(p.credsDir, true, "the credentials folder")
	add(p.dbPath, true, "the database")
	add(p.dbFolder(home), true, "the database's folder")
	if exe != "" {
		l := layoutOf(exe)
		add(l.Dir, true, "the folder the bridge is installed in")
		add(l.Source, true, "the bridge's source checkout")
	}
	return out
}

// protectedIn reports the first protected path that dir is, holds or (for
// one marked inside) lies in, by either spelling, described for an error.
func protectedIn(dir string, protected []protectedPath) (string, bool) {
	spell := func(p string) []string {
		return slices.Compact([]string{filepath.Clean(absPath(p)), resolvePath(absPath(p))})
	}
	for _, pp := range protected {
		for _, d := range spell(dir) {
			for _, p := range spell(pp.path) {
				switch {
				case d == p:
					return "is " + pp.what, true
				case isWithin(p, d):
					return "holds " + pp.what, true
				case pp.inside && isWithin(d, p):
					return "is inside " + pp.what, true
				}
			}
		}
	}
	return "", false
}

// foldersAbove returns every folder above each of dirs up to /, along its
// path as given and along its real path, each folder once: the CLI walks
// up from the real path of its working directory, which differs below a
// symlink (a brains folder linked from another volume).
func foldersAbove(dirs ...string) []string {
	seen := map[string]bool{}
	var out []string
	var starts []string
	for _, dir := range dirs {
		starts = append(starts, absPath(dir), resolvePath(absPath(dir)))
	}
	for _, start := range starts {
		for d := filepath.Dir(filepath.Clean(start)); ; d = filepath.Dir(d) {
			if real := resolvePath(d); !seen[real] {
				seen[real] = true
				out = append(out, d)
			}
			if d == filepath.Dir(d) {
				break
			}
		}
	}
	return out
}

// nodePackage returns the npm package folder that holds the file p
// (…/node_modules/<name> or …/node_modules/@scope/<name>), or "".
func nodePackage(p string) string {
	const nm = "/node_modules/"
	i := strings.LastIndex(p, nm)
	if i < 0 {
		return ""
	}
	rest := strings.Split(p[i+len(nm):], "/")
	n := 1
	if strings.HasPrefix(rest[0], "@") {
		n = 2
	}
	if len(rest) <= n {
		return ""
	}
	return p[:i+len(nm)] + strings.Join(rest[:n], "/")
}

// lookPathIn is exec.LookPath for name in the list of folders path.
func lookPathIn(name, path string) string {
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// absPath is p made absolute against the working directory, or p itself
// when it is empty or that fails.
func absPath(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// sharedDir reports whether dir is HOME or a temp dir, or a folder above
// one of them: a folder that holds much more than the bridge's files.
func sharedDir(dir, home string) bool {
	real := resolvePath(dir)
	for _, d := range []string{home, os.TempDir(), "/tmp"} {
		if isWithin(resolvePath(d), real) {
			return true
		}
	}
	return false
}

// withinAny reports whether p is one of dirs or inside one.
func withinAny(p string, dirs []string) bool {
	return slices.ContainsFunc(dirs, func(dir string) bool { return isWithin(p, dir) })
}

// profileBuilder writes SBPL. Every path goes through deny or allow, so each
// one is quoted and canonicalized the same way; the first path that cannot be
// written is kept in err and fails the whole profile.
type profileBuilder struct {
	strings.Builder
	err error
	// roots are folders a brain cannot replace, with their real paths.
	roots []pathAlias
	// creatable holds every spelling of your Claude Code config dir, the
	// folder a pin lets a brain create (with the folders in it) while
	// missing; see pin.
	creatable []string
	// denies holds the text of every deny written since the last allow;
	// see add.
	denies map[string]bool
	// denied holds every spelling of every path denied or pinned so far,
	// for pinParents.
	denied []string
}

type pathAlias struct{ configured, real string }

// deny emits "(deny op (filter "path"))", e.g. deny("file-read*", "subpath", dir).
//
// The sandbox matches real paths, so a rule for a path that runs through a
// symlink never fires: under a symlinked HOME (or /var instead of
// /private/var) a brain read ~/.fridolin straight through its real path.
// deny therefore emits the rule for every spelling (see spellings): a deny
// that covers more only takes access away.
func (b *profileBuilder) deny(op, filter, path string) {
	// Reading a symlink is part of following it, so a read rule on the link
	// itself would also block what is allowed behind it, such as an inbox
	// under a symlinked data dir. Writes act on the link, so they need its
	// entry.
	for _, s := range b.spellings(path, strings.HasPrefix(op, "file-write")) {
		b.emit("deny "+op, filter, s)
		b.denied = append(b.denied, s)
	}
}

// denyBelow emits "(deny op (regex "^<dir>pattern"))" for every spelling of
// dir, for paths below it that cannot be listed in advance.
func (b *profileBuilder) denyBelow(op, dir, pattern string) {
	for _, s := range b.spellings(dir, strings.HasPrefix(op, "file-write")) {
		b.emit("deny "+op, "regex", "^"+regexp.QuoteMeta(s)+pattern)
		b.denied = append(b.denied, s)
	}
}

// denySymlinksIn refuses a new symlink at dir and anywhere below it, for
// every spelling of dir; see emitNoSymlink.
func (b *profileBuilder) denySymlinksIn(dir string) {
	for _, s := range b.spellings(dir, true) {
		b.emitNoSymlink("regex", "^"+regexp.QuoteMeta(s)+"(/.*)?$")
		b.denied = append(b.denied, s)
	}
}

// spellings returns path as configured and its real path, following every
// symlink, and with entry also the path of its entry: its folder resolved,
// its own name kept. The configured spelling stays for the day a symlink is
// replaced by a real folder. The entry matters when path itself is a
// symlink, such as a ~/.zshrc linked from a dotfiles repo: renaming or
// deleting it acts on the link, not on its target, so a write rule for the
// target alone would let a brain swap the link for a file of its own.
func (b *profileBuilder) spellings(path string, entry bool) []string {
	p, ok := b.absolute(path)
	if !ok {
		return nil
	}
	configured := filepath.Clean(p)
	out := []string{configured}
	if dir := filepath.Dir(configured); entry && dir != configured {
		out = append(out, filepath.Join(resolvePath(dir), filepath.Base(configured)))
	}
	out = append(out, resolvePath(p))
	return slices.Compact(out)
}

// pin stops a brain from renaming or removing the folder dir while leaving
// it free to create and change files inside, where it may write. A brain
// that renamed a folder above a denied path took the path out from under
// its rule (the sandbox matches paths) and read it under the new name; one
// that replaced such a folder with a symlink redirected what other programs
// write there.
//
// The folder is pinned for every write to the folder itself: rename,
// delete, chmod, swapping it for a symlink, and creating it while it does
// not exist yet. The exception is your Claude Code config dir and the
// folders in it, which the Claude CLI creates on its first run: a missing
// one may still be created as a folder, though not as a symlink, and not
// renamed or removed afterwards; the next run's profile pins it fully.
func (b *profileBuilder) pin(dir string) {
	for _, s := range b.spellings(dir, true) {
		b.pinSpelling(s)
		b.denied = append(b.denied, s)
	}
}

func (b *profileBuilder) pinSpelling(dir string) {
	if _, err := os.Lstat(dir); err != nil && withinAny(dir, b.creatable) {
		// file-write-unlink is what the sandbox checks on the source of a
		// rename, and on a delete.
		b.emit("deny file-write-unlink", "literal", dir)
		b.emitNoSymlink("literal", dir)
	} else {
		b.emit("deny file-write*", "literal", dir)
	}
}

// pinParents pins every folder above a denied path, up to and including
// HOME (either spelling), or up to / for a path outside HOME, in every
// spelling. A brain's profile has no need of it: it denies every write
// outside the brain's own places, folders above them included. The live
// site's sandbox does, since what a module declares it may write can hold
// such a folder (see site_sandbox.go).
func (b *profileBuilder) pinParents(homes ...string) {
	seen := map[string]bool{}
	var dirs []string
	for _, p := range b.denied {
		for dir := filepath.Dir(p); !seen[dir]; dir = filepath.Dir(dir) {
			seen[dir] = true
			dirs = append(dirs, dir)
			if dir == string(filepath.Separator) || slices.Contains(homes, dir) {
				break
			}
		}
	}
	pinned := map[string]bool{}
	var spelled []string
	for _, dir := range dirs {
		for _, s := range b.spellings(dir, true) {
			if !pinned[s] {
				pinned[s] = true
				spelled = append(spelled, s)
			}
		}
	}
	sort.Strings(spelled)
	for _, dir := range spelled {
		b.pinSpelling(dir)
	}
}

// allow emits "(allow op (filter "path"))" for path, and again with its
// longest trusted root (see trustRoot) replaced by that root's real path.
// Unlike deny it follows no other symlink: a brain can replace what is in
// its own folder or inbox with a symlink, and an allow that followed it
// would open whatever the symlink points to. It returns the spellings it
// allowed. A regex filter is written as given.
func (b *profileBuilder) allow(op, filter, path string) []string {
	if filter == "regex" {
		b.emit("allow "+op, filter, path)
		return nil
	}
	out := b.allowSpellings(path)
	for _, s := range out {
		b.emit("allow "+op, filter, s)
	}
	return out
}

// allowSpellings returns the spellings allow writes a rule for: path as
// configured, and with its longest trusted root replaced by that root's real
// path.
func (b *profileBuilder) allowSpellings(path string) []string {
	p, ok := b.absolute(path)
	if !ok {
		return nil
	}
	configured := filepath.Clean(p)
	out := []string{configured}
	best := pathAlias{}
	for _, r := range b.roots {
		if (configured == r.configured || strings.HasPrefix(configured, r.configured+string(filepath.Separator))) &&
			len(r.configured) > len(best.configured) {
			best = r
		}
	}
	if best.configured != "" {
		out = append(out, best.real+strings.TrimPrefix(configured, best.configured))
	}
	return out
}

// trustRoot lets allow rules under dir resolve through dir's real path. Only
// for folders a brain cannot swap for a symlink: HOME, and the data,
// credentials and media dirs, which the profile denies writes to.
func (b *profileBuilder) trustRoot(dir string) {
	p, ok := b.absolute(dir)
	if !ok {
		return
	}
	if configured, real := filepath.Clean(p), resolvePath(p); real != configured {
		b.roots = append(b.roots, pathAlias{configured, real})
	}
}

// absolute returns path made absolute, or fails the profile.
func (b *profileBuilder) absolute(path string) (string, bool) {
	if path == "" {
		// Made absolute, "" would be the bridge's working directory.
		b.fail(errors.New("empty path in the sandbox profile"))
		return "", false
	}
	if filepath.IsAbs(path) {
		return path, true
	}
	// A relative rule compiles and matches nothing. The bridge itself
	// resolves such a path against its working directory, so do the same.
	wd, err := os.Getwd()
	if err != nil {
		b.fail(fmt.Errorf("resolving sandbox path %q: %w", path, err))
		return "", false
	}
	return wd + string(filepath.Separator) + path, true
}

// emit writes "(rule (filter "value"))".
func (b *profileBuilder) emit(rule, filter, value string) {
	if q, ok := b.quote(value); ok {
		b.add(strings.HasPrefix(rule, "allow"), fmt.Sprintf("(%s (%s %s))\n", rule, filter, q))
	}
}

// emitNoSymlink writes a rule that refuses a new symlink wherever filter
// matches. The sandbox checks the type of what is created, and checks a
// symlink moved there the same way, so files and folders may still be
// created where no symlink can.
func (b *profileBuilder) emitNoSymlink(filter, value string) {
	if q, ok := b.quote(value); ok {
		b.add(false, fmt.Sprintf("(deny file-write-create (require-all (%s %s) (vnode-type SYMLINK)))\n", filter, q))
	}
}

// quote returns value as an SBPL string, or fails the profile.
func (b *profileBuilder) quote(value string) (string, bool) {
	q, err := sbplString(value)
	if err != nil {
		b.fail(err)
		return "", false
	}
	return q, true
}

// add writes the rule text. A deny written already since the last allow
// changes nothing, so it is written once: the same paths come from several
// places (a git config file is also a protected one), and the profile goes
// inline on every run. After an allow it is written again, since the last
// matching rule wins.
func (b *profileBuilder) add(allow bool, text string) {
	switch {
	case allow:
		clear(b.denies)
	case b.denies[text]:
		return
	case b.denies == nil:
		b.denies = map[string]bool{text: true}
	default:
		b.denies[text] = true
	}
	b.WriteString(text)
}

func (b *profileBuilder) fail(err error) {
	if b.err == nil {
		b.err = err
	}
}

const maxSymlinks = 64

// resolvePath returns the real path of the absolute path p. Every symlink in
// the part that exists is resolved, including a dangling one (it resolves to
// its target), and the part that does not exist yet is kept as written, so
// a rule still matches a folder created later. ".." is applied to the
// resolved path, as the kernel does. On a symlink loop p comes back as is.
func resolvePath(p string) string {
	resolved := string(filepath.Separator)
	rest := strings.Split(p, string(filepath.Separator))
	links := 0
	for len(rest) > 0 {
		name := rest[0]
		rest = rest[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}
		next := filepath.Join(resolved, name)
		fi, err := os.Lstat(next)
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			// A real entry, or one that does not exist (yet): nothing to resolve.
			resolved = next
			continue
		}
		target, err := os.Readlink(next)
		if links++; err != nil || links > maxSymlinks {
			return filepath.Clean(p)
		}
		if !filepath.IsAbs(target) {
			target = resolved + string(filepath.Separator) + target
		}
		// Walk the target from the root: it may hold symlinks and ".." too.
		rest = append(strings.Split(target, string(filepath.Separator)), rest...)
		resolved = string(filepath.Separator)
	}
	return resolved
}

// sbplString quotes s as an SBPL string literal. Go's %q is not one: it
// writes a no-break space as \u00a0 and the joiner inside emoji as \u200d,
// which the profile reader does not decode, so the rule named a path that
// does not exist and never matched. Only backslash and double quote need
// escaping; everything else goes in as UTF-8. A NUL cannot be passed in an
// argument at all, and line breaks or invalid UTF-8 are refused rather than
// guessed at.
func sbplString(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("sandbox path %q is not valid UTF-8", s)
	}
	if strings.ContainsAny(s, "\x00\n\r") {
		return "", fmt.Errorf("sandbox path %q contains a NUL or a line break", s)
	}
	return `"` + sbplEscaper.Replace(s) + `"`, nil
}

var sbplEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// gitConfigChain returns ~/.gitconfig plus every file it includes
// ([include] / [includeIf] path = …), one level deep.
func gitConfigChain(home string) []string {
	main := filepath.Join(home, ".gitconfig")
	out := []string{main}
	f, err := os.Open(main)
	if err != nil {
		return out
	}
	defer f.Close()
	inInclude := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			sec := strings.ToLower(line)
			inInclude = strings.HasPrefix(sec, "[include]") || strings.HasPrefix(sec, "[includeif")
			continue
		}
		if !inInclude {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(strings.ToLower(k)) != "path" {
			continue
		}
		p := strings.Trim(strings.TrimSpace(v), `"`)
		p = expandHome(p, home)
		if !filepath.IsAbs(p) {
			p = filepath.Join(home, p)
		}
		out = append(out, p)
	}
	return out
}

// Available reports why brain runs cannot be sandboxed on this machine, or
// nil when they can.
func (sp *SandboxProfiler) Available() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("sandbox-exec is macOS-only, got %s", runtime.GOOS)
	}
	if _, err := os.Stat(sp.execPath); err != nil {
		return fmt.Errorf("%s not available: %w", sp.execPath, err)
	}
	return nil
}

// Command returns the program and arguments that run prog with args as
// brain brainID under its profile. The profile goes inline (-p): a profile
// file in a shared TMPDIR could be swapped by another brain between being
// written and being read, and a brain that made TMPDIR unwritable broke
// the sandbox. The caller creates the brain's own state first, which the
// profile pins, and points the run at it (see prepareBrainState).
func (sp *SandboxProfiler) Command(brainID, prog string, args []string) (string, []string, error) {
	if err := sp.Available(); err != nil {
		return "", nil, err
	}
	profile, err := sp.Profile(brainID)
	if err != nil {
		return "", nil, err
	}
	return sp.execPath, append([]string{"-p", profile, prog}, args...), nil
}
