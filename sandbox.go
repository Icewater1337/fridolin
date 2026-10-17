package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
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
// The profile is allow-by-default with an explicit denylist. That is a
// containment measure, not a jail: it stops a prompt-injected brain from
// reading the *other* brains' trees, your cross-session Claude state and your
// own Claude Code instructions, and the usual home-directory hotspots (keys,
// mail, browser data). It does not stop a determined attacker with code
// execution.
//
// Brains keep write access by default, so a brain can still maintain its own
// notes and files. Everything denied is denied for writes as well, and the
// folders above it cannot be renamed, nor created while missing: the
// sandbox matches paths, not files, so a brain that moved ~/.ssh (or the
// folder holding another brain) to a new name could read it there. Add
// anything else that must stay unreadable to sandbox.deny_paths in
// config.json.
type SandboxProfiler struct {
	mu sync.RWMutex

	enabled bool

	// workDirs maps brain id → its folder, by the real path the bridge runs
	// the brain in and by the path as configured where that differs (see
	// BrainSpec.Path), so that each profile allows its own folder and
	// denies the others' both ways.
	workDirs map[string][]string

	// dataDir is the bridge's runtime dir, denied wholesale except for media.
	dataDir  string
	mediaDir string

	// dbPath is the WhatsApp session and chat database, which db_path can
	// put outside the data dir.
	dbPath string

	// configFile is the config.json the bridge runs with; see SetConfigFile.
	configFile string

	// credsDir holds per-brain credential files; see SandboxConfig.CredentialsDir.
	credsDir string

	// gitConfig is "read" or "deny".
	gitConfig string

	// plaudShared: the Plaud login in ~/.plaud is meant for brains too.
	plaudShared bool

	// inheritUser: inherit_claude_user_config lets brains load your own
	// Claude Code instructions, the CLAUDE.md files above their folders.
	inheritUser bool

	// claudePath, rclonePath, plaudCLIPath and viewer name the programs the
	// bridge itself runs outside the sandbox; see outsidePrograms.
	claudePath, rclonePath, plaudCLIPath string
	viewer                               bool

	// extraDeny and extraAllow come from config.
	extraDeny  []string
	extraAllow []string

	// execPath is sandboxExecPath; tests point it at a missing file.
	execPath string

	// bridgeExe is the running bridge binary; tests point it elsewhere.
	bridgeExe string
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
	// These may be relative, and the bridge opens them relative to its
	// working directory; the comparisons below need them absolute.
	sp.dataDir = absPath(cfg.DataDir)
	sp.dbPath = absPath(cfg.DBPath)
	sp.mediaDir = absPath(cfg.MediaDir)
	if sp.mediaDir == "" && sp.dataDir != "" {
		sp.mediaDir = filepath.Join(sp.dataDir, "media")
	}
	sp.credsDir = cfg.Sandbox.CredentialsDir
	sp.gitConfig = cfg.Sandbox.GitConfig
	sp.plaudShared = cfg.Plaud.BrainAccess
	sp.inheritUser = cfg.InheritClaudeUserConfig
	sp.claudePath = cfg.ClaudePath
	sp.rclonePath = cfg.Backup.RclonePath
	sp.plaudCLIPath = cfg.Plaud.CLIPath
	sp.viewer = cfg.Viewer
	sp.extraDeny = append([]string(nil), cfg.Sandbox.DenyPaths...)
	sp.extraAllow = append([]string(nil), cfg.Sandbox.AllowPaths...)
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

// defaultDenySubpaths are home-relative directories no brain may read or
// write. Keychains are deliberately absent: the Claude CLI reads its
// credentials from the macOS keychain, and denying it breaks the CLI with
// "Not logged in".
var defaultDenySubpaths = []string{
	".ssh",
	".gnupg",
	".aws",
	".config/gh",
	".config/gcloud",
	".config/rclone",
	// The personal-data denylist that scripts/check-personal-data.sh reads,
	// under both names the project has had.
	".config/fridolin",
	".config/whatsapp-claude-bridge",
	// The bridge's own logs (setup.sh), which name every chat.
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
	"Library/Application Support/Google/Chrome",
	"Library/Application Support/Firefox",
	"Library/Safari",
	"Library/Containers",
	"Library/Group Containers",
}

// defaultDenyLiterals are individual home-relative files to deny, for
// reading and writing.
var defaultDenyLiterals = []string{
	".claude/history.jsonl",
	".git-credentials",
	".netrc",
	".zsh_history",
	".bash_history",
}

// defaultWriteDenySubpaths and defaultWriteDenyLiterals are home-relative
// paths brains may read but never write. Each one is loaded or run by
// something outside this brain's sandbox: your own Claude Code sessions
// and, with inherit_claude_user_config, every other brain's next run
// (settings and their hooks, CLAUDE.md, rules, agents, commands, skills,
// plugins, output styles), launchd at your next login, your own shells,
// the tmux server the activity viewer starts. A brain that could write one
// would carry a prompt injection out of its sandbox. Claude Code state the
// CLI writes on every run (~/.claude.json, projects, todos, shell
// snapshots) is not in this list.
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

// memoryFiles are the files Claude Code loads from its working directory and
// every folder above it, next to that folder's .claude/ (its CLAUDE.md, rules
// and settings), whichever brain or session runs there.
var memoryFiles = []string{"CLAUDE.md", "CLAUDE.local.md", ".mcp.json"}

// instructionFiles are the memory files Claude Code reads as instructions,
// whatever --setting-sources says; it skips .mcp.json under
// --strict-mcp-config.
var instructionFiles = []string{"CLAUDE.md", "CLAUDE.local.md"}

// Profile returns the sandbox profile source for the brain with this id. It
// fails when a path cannot be written into the profile: a profile that
// silently skipped a rule would protect less than the config says.
func (sp *SandboxProfiler) Profile(brainID string) (string, error) {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	home := os.Getenv("HOME")
	if !filepath.IsAbs(home) {
		// Every home-relative deny would silently become a relative path.
		return "", fmt.Errorf("HOME %q is not an absolute path", home)
	}
	// A brain's own folder wins over the denies above it (see below), so a
	// folder that holds HOME would open all of it, ~/CLAUDE.md included.
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

	var b profileBuilder
	b.creatable = b.spellings(filepath.Join(home, ".claude"), true)
	b.trustRoot(home)
	if sp.dataDir != "" {
		b.trustRoot(sp.dataDir)
	}
	if sp.credsDir != "" {
		b.trustRoot(sp.credsDir)
	}
	b.WriteString("(version 1)\n(allow default)\n\n")

	// ── home-directory hotspots ──────────────────────────────────────────
	b.WriteString(";; Cross-session state, credentials, and personal data stores\n")
	for _, sub := range defaultDenySubpaths {
		b.deny("file-read*", "subpath", filepath.Join(home, sub))
		b.deny("file-write*", "subpath", filepath.Join(home, sub))
	}
	for _, lit := range defaultDenyLiterals {
		b.deny("file-read*", "literal", filepath.Join(home, lit))
		b.deny("file-write*", "literal", filepath.Join(home, lit))
	}
	// Claude Code's record of every session, yours included: transcripts in
	// projects/, one file per running CLI in sessions/. The CLI writes its
	// own run's files there, so only reading is denied, and moving files
	// out: a rename is checked as file-write-unlink on its source, and a
	// transcript moved elsewhere could be read there. The CLI deletes its
	// sessions/<pid>.json on exit, so sessions/ keeps unlink and only the
	// folder itself is pinned.
	projects := filepath.Join(home, ".claude", "projects")
	sessions := filepath.Join(home, ".claude", "sessions")
	b.deny("file-read*", "subpath", projects)
	b.deny("file-write-unlink", "subpath", projects)
	b.deny("file-read*", "subpath", sessions)
	b.pin(sessions)
	// Nor may a brain plant a symlink in either. One that linked a project
	// folder the CLI had not created yet into its own folder got that
	// project's later transcripts, yours included: the CLI wrote them
	// through the link, to where the brain reads. The CLI creates real
	// folders and files there, which stay allowed.
	b.denySymlinksIn(projects)
	b.denySymlinksIn(sessions)
	if !sp.plaudShared {
		// The Plaud CLI's login, unless plaud.brain_access shares it.
		b.deny("file-read*", "subpath", filepath.Join(home, ".plaud"))
		b.deny("file-write*", "subpath", filepath.Join(home, ".plaud"))
	}
	b.WriteString("\n")

	// ── code that runs outside the sandbox ───────────────────────────────
	b.WriteString(";; Loaded or run outside this sandbox: readable, never writable\n")
	for _, sub := range defaultWriteDenySubpaths {
		b.deny("file-write*", "subpath", filepath.Join(home, sub))
	}
	for _, lit := range defaultWriteDenyLiterals {
		b.deny("file-write*", "literal", filepath.Join(home, lit))
	}
	// Above a brain's folder: what one brain wrote there would reach every
	// brain below it, and your own sessions in your home folder. ~/.claude
	// itself is the CLI's state; what it loads from there is denied above.
	homes := []string{home, resolvePath(home)}
	for _, dir := range memoryFolders(sp.workDirs, brainID, homes) {
		for _, f := range memoryFiles {
			b.deny("file-write*", "literal", filepath.Join(dir, f))
		}
		if !slices.Contains(homes, dir) {
			b.deny("file-write*", "subpath", filepath.Join(dir, ".claude"))
		}
	}
	// Auto-memory: Claude Code loads projects/<project>/memory/MEMORY.md into
	// every new session in that project, yours included. The CLI writes the
	// rest of projects/ on every run, and a project folder it has not
	// created yet cannot be listed, hence a pattern.
	b.denyBelow("file-write*", projects, `/[^/]+/memory(/.*)?$`)
	b.WriteString("\n")

	// ── the bridge and the programs it runs ──────────────────────────────
	// launchd runs the bridge outside any sandbox, and the bridge runs its
	// helpers unsandboxed too, so a brain that replaced one of them would
	// run code outside its sandbox the next time it starts.
	b.WriteString(";; The bridge, its checkout and the programs it runs: never writable\n")
	if sp.bridgeExe != "" {
		b.deny("file-write*", "literal", sp.bridgeExe)
		if repo := checkoutOf(sp.bridgeExe); repo != "" {
			b.deny("file-write*", "subpath", repo)
		}
	}
	// Only those under HOME for now: a later change makes writes
	// deny-by-default everywhere.
	inHome := func(p string) bool { return withinAny(p, homes) || withinAny(resolvePath(p), homes) }
	for _, p := range sp.outsidePrograms() {
		if !inHome(p) {
			continue
		}
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			b.deny("file-write*", "subpath", p)
		} else {
			b.deny("file-write*", "literal", p)
		}
	}
	// Nor may a brain plant a program the bridge would find next.
	files, dirs := sp.plantablePrograms(home)
	for _, p := range files {
		if inHome(p) {
			b.deny("file-write*", "literal", p)
		}
	}
	for _, p := range dirs {
		if inHome(p) {
			b.deny("file-write*", "subpath", p)
		}
	}
	b.WriteString("\n")

	// ── git configuration ────────────────────────────────────────────────
	// Reading git config lets a brain commit in its own folder (name, email,
	// signing settings). Writing it would let a brain change how *you* use
	// git, so that stays denied. Credentials stay denied either way.
	gitFiles := gitConfigChain(home)
	xdgGit := filepath.Join(home, ".config", "git")
	if sp.gitConfig == "deny" {
		b.WriteString(";; Git configuration (denied by sandbox.git_config)\n")
		for _, f := range gitFiles {
			b.deny("file-read*", "literal", f)
		}
		b.deny("file-read*", "subpath", xdgGit)
	} else {
		b.WriteString(";; Git configuration: readable, never writable\n")
	}
	// Never writable in either mode; denied files must not be renamable
	// either, or a brain could read them under their new name.
	for _, f := range gitFiles {
		b.deny("file-write*", "literal", f)
	}
	b.deny("file-write*", "subpath", xdgGit)
	b.WriteString("\n")

	// ── user-supplied extras ─────────────────────────────────────────────
	if len(sp.extraDeny) > 0 {
		b.WriteString(";; Extra denies from config\n")
		b.ownable = true
		for _, p := range sp.extraDeny {
			b.deny("file-read*", "subpath", p)
			b.deny("file-write*", "subpath", p)
		}
		b.ownable = false
		b.WriteString("\n")
	}
	if len(sp.extraAllow) > 0 {
		b.WriteString(";; Extra allows from config (override the denies above, not those below)\n")
		for _, p := range sp.extraAllow {
			b.allow("file-read*", "subpath", p)
		}
		b.WriteString("\n")
	}

	// ── your instructions above the brain's folder ───────────────────────
	// Claude Code reads CLAUDE.md, CLAUDE.local.md and .claude/ (its
	// CLAUDE.md, rules, skills, agents and commands) in every folder above
	// its working directory as part of the project, whatever
	// --setting-sources says, so a brain in HOME read ~/CLAUDE.md,
	// ~/.claude/CLAUDE.md and ~/.claude/rules: your instructions, not the
	// brain's. ~/.claude is also the CLI's state, so only those two are
	// denied there; they are your user memory too, so for brains outside
	// HOME as well. The claudeMdExcludes a brain runs with (see
	// excludeInstructionsAbove) skip the CLAUDE.md files and rules, but
	// neither the skills, agents and commands nor the brain's own tools.
	// Writes are denied too, so that nothing here can be renamed and read
	// under another name. After the allow_paths allows, which cannot open
	// them again; inherit_claude_user_config does.
	if !sp.inheritUser {
		b.WriteString(";; Your Claude Code instructions above this brain's folder\n")
		for _, dir := range foldersAbove(sp.workDirs[brainID]...) {
			for _, f := range instructionFiles {
				b.deny("file-read*", "literal", filepath.Join(dir, f))
				b.deny("file-write*", "literal", filepath.Join(dir, f))
			}
			if resolvePath(dir) != resolvePath(home) {
				b.deny("file-read*", "subpath", filepath.Join(dir, ".claude"))
				b.deny("file-write*", "subpath", filepath.Join(dir, ".claude"))
			}
		}
		userMemory, userRules := filepath.Join(home, ".claude", "CLAUDE.md"), filepath.Join(home, ".claude", "rules")
		b.deny("file-read*", "literal", userMemory)
		b.deny("file-write*", "literal", userMemory)
		b.deny("file-read*", "subpath", userRules)
		b.deny("file-write*", "subpath", userRules)
		b.WriteString("\n")
	}

	// ── other brains' working trees ──────────────────────────────────────
	// After the allow_paths allows, like the bridge's data below, so that
	// an entry covering the folder that holds the brains (~/assistant, say)
	// cannot open them again.
	var others []string
	for id, dirs := range sp.workDirs {
		if id != brainID {
			others = append(others, dirs...)
		}
	}
	sort.Strings(others)
	if len(others) > 0 {
		b.WriteString(";; Other brains' working trees\n")
		for _, dir := range others {
			b.deny("file-read*", "subpath", dir)
			b.deny("file-write*", "subpath", dir)
		}
		b.WriteString("\n")
	}

	// ── bridge runtime dir ───────────────────────────────────────────────
	// The DB holds the WhatsApp session keys (enough to take the account
	// over from another machine), the polls and their votes;
	// the dashboard token and backup credentials live in the data dir too.
	// Media is the shared inbound staging dir, so it is re-allowed below.
	// This comes after the allow_paths allows, which therefore cannot open
	// any of it again; only media and this brain's calendar files can.
	b.WriteString(";; Bridge runtime data (media staging re-allowed below)\n")
	// Both names the data dir has had, whichever is active: an install that
	// moved to ~/.fridolin can leave the old folder behind, with an old copy
	// of the session keys, the dashboard token and the Drive login.
	dataDirs := []string{filepath.Join(home, dataDirName), filepath.Join(home, legacyDataDirName)}
	if sp.dataDir != "" && !slices.Contains(dataDirs, filepath.Clean(sp.dataDir)) {
		dataDirs = append(dataDirs, sp.dataDir)
	}
	for _, dir := range dataDirs {
		b.deny("file-read*", "subpath", dir)
		b.deny("file-write*", "subpath", dir)
	}
	// db_path can put the DB anywhere. Its folder goes too, unless it is a
	// folder the brain needs (HOME or above, a temp dir), where denying it
	// would take the brain's home or scratch space away.
	var dbFiles []string
	if sp.dbPath != "" {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			dbFiles = append(dbFiles, sp.dbPath+suffix)
		}
		if dir := filepath.Dir(sp.dbPath); !withinAny(sp.dbPath, dataDirs) && !sharedDir(dir, home) {
			b.deny("file-read*", "subpath", dir)
			b.deny("file-write*", "subpath", dir)
		}
	}
	if sp.dataDir != "" {
		b.allow("file-read*", "subpath", sp.mediaDir)
		b.allow("file-write*", "subpath", sp.mediaDir)
		b.calendarAllows(sp.dataDir, brainID)
	}
	// After the media allow, so no media_dir setting can open the DB again.
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
		files := []string{sp.configFile, sp.configFile + ".bak", dotEnvPath(sp.configFile), ".env"}
		for _, f := range files {
			if abs, err := filepath.Abs(f); err == nil && !withinAny(abs, dataDirs) {
				b.deny("file-read*", "literal", abs)
				b.deny("file-write*", "literal", abs)
			}
		}
		// Every save writes the whole config to a temp file next to it
		// first (writeFileAtomic), which is readable until the rename.
		if dir := filepath.Dir(sp.configFile); !withinAny(sp.configFile, dataDirs) {
			tmp := "/" + regexp.QuoteMeta("."+filepath.Base(sp.configFile)+".") + `[^/]*\.tmp$`
			b.denyBelow("file-read*", dir, tmp)
			b.denyBelow("file-write*", dir, tmp)
		}
	}
	b.WriteString("\n")
	if sp.credsDir != "" {
		b.WriteString(";; Shared credentials dir: only this brain's own calendar files\n")
		b.deny("file-read*", "subpath", sp.credsDir)
		b.deny("file-write*", "subpath", sp.credsDir)
		b.calendarAllows(sp.credsDir, brainID)
		b.WriteString("\n")
	}

	// ── the folders above every denied path ──────────────────────────────
	b.WriteString(";; Folders above the denied paths: not renamable or removable\n")
	b.pinParents(home, resolvePath(home))
	b.WriteString("\n")

	// ── the brain's own folder ───────────────────────────────────────────
	// After the denies, so it wins over those on the folder and above it: a
	// brain always reaches its own files, even when its folder sits inside
	// a default-denied directory. It must not open what is denied inside
	// the folder, though (another brain, the data dir, a db_path folder,
	// the bridge's checkout), so every rule for a path in the folder is
	// written again after it, in order. A folder that is itself denied,
	// other than a deny_paths entry, is refused: opening it would give the
	// brain what it protects.
	if own := sp.workDirs[brainID]; len(own) > 0 {
		before := b.rules
		b.WriteString(";; This brain's own folder\n")
		// Both ways: allow follows no symlink, and the brain runs in the
		// real path, which a folder linked from a default-denied one, such
		// as ~/assistant → ~/Documents/assistant, would otherwise leave
		// denied.
		var owns, pins []string
		for _, dir := range own {
			owns = append(owns, b.allow("file-read*", "subpath", dir)...)
			b.allow("file-write*", "subpath", dir)
		}
		// The folder itself stays put, in every spelling. A brain that moved
		// it aside and left a symlink in its place pointed it at another
		// brain's folder, whose profile then failed (its folder had become
		// one this profile denies), or at HOME, which failed every profile;
		// and the bridge, outside the sandbox, followed the link to read and
		// write what it keeps there for this brain.
		for _, dir := range own {
			pins = append(pins, b.spellings(dir, true)...)
		}
		for _, s := range pins {
			b.emit(rulePin, "deny file-write*", "literal", s, s)
		}
		var again []profileRule
		for _, r := range before {
			switch {
			case r.kind == ruleDeny && slices.Contains(owns, r.path):
				return "", fmt.Errorf("brain %s: its folder %s is one the sandbox keeps from brains; give the brain a folder of its own", brainID, r.path)
			case r.kind == rulePin && slices.Contains(pins, r.path):
				// Pinned just above.
			case withinAny(r.path, owns) && (r.kind == rulePin || !slices.Contains(owns, r.path)):
				again = append(again, r)
			}
		}
		if len(again) > 0 {
			b.WriteString("\n;; Inside this brain's folder, the rules above apply again\n")
			for _, r := range again {
				b.add(r.kind, r.path, r.text)
			}
		}
	}

	if b.err != nil {
		return "", b.err
	}
	return b.String(), nil
}

// memoryFolders returns the folders above every brain's folder whose
// memory files and .claude/ Claude Code loads for the brains below: up to
// and including HOME (either spelling), or up to / for a folder outside
// HOME, and HOME itself, which your own sessions load. They are walked
// up from each spelling of a brain's folder, as configured and its real
// path, which differ below a symlink, and each spelling of a folder comes
// once. The brain's own folder is its own to write, so it is left out
// when it holds another one.
func memoryFolders(workDirs map[string][]string, brainID string, homes []string) []string {
	var own []string
	for _, dir := range workDirs[brainID] {
		own = append(own, resolvePath(dir))
	}
	seen := map[string]bool{}
	var out []string
	walk := func(dir string) {
		for {
			if !seen[dir] {
				seen[dir] = true
				if !slices.Contains(own, resolvePath(dir)) {
					out = append(out, dir)
				}
			}
			if slices.Contains(homes, dir) || filepath.Dir(dir) == dir {
				return
			}
			dir = filepath.Dir(dir)
		}
	}
	walk(homes[0])
	for _, dirs := range workDirs {
		for _, dir := range dirs {
			walk(filepath.Dir(filepath.Clean(dir)))
		}
	}
	sort.Strings(out)
	return out
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

// outsidePrograms returns the programs the bridge itself starts outside any
// sandbox: the Claude CLI (claude --version for the dashboard), rclone for
// backups, the Plaud CLI, tmux for the activity viewer, each as the bridge
// would find it now. A node script comes with its npm package folder, all
// of which node loads, and the node that runs it.
func (sp *SandboxProfiler) outsidePrograms() []string {
	var found []string
	if p, err := exec.LookPath(sp.claudePath); err == nil {
		found = append(found, p)
	}
	if p, err := findRclone(sp.rclonePath); err == nil {
		found = append(found, p)
	}
	if p, err := findPlaudCLI(sp.plaudCLIPath); err == nil {
		found = append(found, p)
	}
	if p := findTmux(); sp.viewer && p != "" {
		found = append(found, p)
	}
	var out []string
	for _, p := range found {
		out = append(out, p)
		if pkg := nodePackage(resolvePath(p)); pkg != "" {
			// #!/usr/bin/env node: the bridge runs the Plaud CLI with its own
			// folder first on PATH, everything else with its own PATH.
			out = append(out, pkg)
			for _, path := range []string{plaudPATH(p), os.Getenv("PATH")} {
				if node := lookPathIn("node", path); node != "" {
					out = append(out, node)
				}
			}
		}
	}
	return out
}

// programsRunByName are the programs the bridge starts by name, so from the
// first folder on its PATH that has one: the Claude CLI, rclone and the
// Plaud CLI unless configured with a path, the node the Plaud CLI's
// #!/usr/bin/env node finds, tmux and the sh and tail it runs for the
// viewer, touch, osascript (the viewer, the folder picker) and open.
var programsRunByName = []string{"claude", "rclone", "plaud", "node", "tmux", "sh", "tail", "touch", "osascript", "open"}

// plantablePrograms returns where a brain could plant a program for the
// bridge to run outside the sandbox later, ahead of the one it finds now:
// every name it runs by name in every folder of its PATH and the Plaud
// CLI's, whether that file exists yet or not, and rclone's fallbacks off
// PATH. Without plaud.cli_path it also returns nvm's node versions, since
// the bridge takes the Plaud CLI of whichever version name sorts last.
func (sp *SandboxProfiler) plantablePrograms(home string) (files, dirs []string) {
	names := slices.Clone(programsRunByName)
	for _, p := range []string{sp.claudePath, sp.rclonePath} {
		if p != "" && !strings.Contains(p, "/") && !slices.Contains(names, p) {
			names = append(names, p)
		}
	}
	path := filepath.SplitList(os.Getenv("PATH"))
	if p, err := findPlaudCLI(sp.plaudCLIPath); err == nil {
		path = append(path, filepath.SplitList(plaudPATH(p))...)
	}
	seen := map[string]bool{}
	for _, dir := range path {
		// exec.LookPath skips relative entries.
		if dir = filepath.Clean(dir); !filepath.IsAbs(dir) || seen[dir] {
			continue
		}
		seen[dir] = true
		for _, name := range names {
			files = append(files, filepath.Join(dir, name))
		}
	}
	files = append(files, rcloneFallbacks()...)
	if sp.plaudCLIPath == "" {
		dirs = append(dirs, filepath.Join(home, nvmNodeVersions))
	}
	return files, dirs
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

// profileBuilder writes SBPL. Every path goes through deny or allow, so each
// one is quoted and canonicalized the same way; the first path that cannot be
// written is kept in err and fails the whole profile.
type profileBuilder struct {
	strings.Builder
	err error
	// roots are folders a brain cannot replace, with their real paths.
	roots []pathAlias
	// denied holds every spelling of every path denied so far, so that
	// pinParents can protect the folders above them.
	denied []string
	// rules holds every rule written so far, for the brain's own folder;
	// see Profile.
	rules []profileRule
	// ownable marks the denies written while it is set as ones a brain's
	// folder may itself be.
	ownable bool
	// creatable holds every spelling of ~/.claude, the folder a pin lets
	// a brain create (with the folders in it) while missing; see pin.
	creatable []string
	// denies holds the text of every deny written since the last allow;
	// see add.
	denies map[string]bool
}

type pathAlias struct{ configured, real string }

// profileRule is one rule as written, with the path it covers (for a
// pattern, the folder it matches below).
type profileRule struct {
	text, path string
	kind       ruleKind
}

type ruleKind int

const (
	// ruleDeny denies a path no brain's folder may be.
	ruleDeny ruleKind = iota
	// ruleOwnable denies a sandbox.deny_paths entry, which a brain's folder
	// may be: that keeps the folder from every other brain.
	ruleOwnable
	// rulePin keeps a folder from being renamed or removed.
	rulePin
	ruleAllow
)

// denyKind is the kind of the denies written now.
func (b *profileBuilder) denyKind() ruleKind {
	if b.ownable {
		return ruleOwnable
	}
	return ruleDeny
}

// deny emits "(deny op (filter "path"))", e.g. deny("file-read*", "subpath", dir).
//
// The sandbox matches real paths, so a rule for a path that runs through a
// symlink never fires: under a symlinked HOME (or /var instead of
// /private/var) a brain read ~/.fridolin straight through its real path.
// deny therefore emits the rule for every spelling (see spellings): a deny
// that covers more only takes access away.
func (b *profileBuilder) deny(op, filter, path string) {
	// Reading a symlink is part of following it, so a read rule on the link
	// itself would also block what is allowed behind it, such as media under
	// a symlinked data dir. Writes act on the link, so they need its entry.
	for _, s := range b.spellings(path, strings.HasPrefix(op, "file-write")) {
		b.emit(b.denyKind(), "deny "+op, filter, s, s)
		b.denied = append(b.denied, s)
	}
}

// denyBelow emits "(deny op (regex "^<dir>pattern"))" for every spelling of
// dir, for paths below it that cannot be listed in advance. The folders
// above dir are pinned like those above any denied path.
func (b *profileBuilder) denyBelow(op, dir, pattern string) {
	for _, s := range b.spellings(dir, strings.HasPrefix(op, "file-write")) {
		b.emit(b.denyKind(), "deny "+op, "regex", s, "^"+regexp.QuoteMeta(s)+pattern)
		b.denied = append(b.denied, s)
	}
}

// denySymlinksIn refuses a new symlink at dir and anywhere below it, for
// every spelling of dir; see emitNoSymlink.
func (b *profileBuilder) denySymlinksIn(dir string) {
	for _, s := range b.spellings(dir, true) {
		b.emitNoSymlink(b.denyKind(), "regex", s, "^"+regexp.QuoteMeta(s)+"(/.*)?$")
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
// it free to create and change files inside. A brain that renamed a folder
// above a denied path took the path out from under its rule (the sandbox
// matches paths) and read it under the new name; one that replaced such a
// folder with a symlink redirected what other programs write there.
//
// The folder is pinned for every write to the folder itself: rename,
// delete, chmod, swapping it for a symlink, and creating it while it does
// not exist yet. A brain that created a missing one as a symlink into its
// own folder redirected what lies below it, such as a missing ~/.config,
// where your git looks for its config, or a PATH folder, where the bridge
// looks for the programs it runs. Refusing only symlinks would not do: a
// brain can as well move a folder it prepared into place, with symlinks
// or files of its own inside, and the sandbox checks where a rename lands
// the same way as a new folder. The exception is ~/.claude and the
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
		b.emit(rulePin, "deny file-write-unlink", "literal", dir, dir)
		b.emitNoSymlink(rulePin, "literal", dir, dir)
	} else {
		b.emit(rulePin, "deny file-write*", "literal", dir, dir)
	}
}

// pinParents pins every folder above a denied path, up to and including
// HOME (either spelling), or up to / for a path outside HOME. Each folder
// is pinned in every spelling, so a symlink on the way (a brains folder
// linked from another volume) cannot be renamed either.
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
// Unlike deny it follows no other symlink: a brain can replace its own
// folder, the media dir or its calendar token with a symlink, and an allow
// that followed it would open whatever the symlink points to. It returns
// the spellings it allowed.
func (b *profileBuilder) allow(op, filter, path string) []string {
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
	for _, s := range out {
		b.emit(ruleAllow, "allow "+op, filter, s, s)
	}
	return out
}

// trustRoot lets allow rules under dir resolve through dir's real path. Only
// for folders a brain cannot swap for a symlink: HOME, and the data and
// credentials dirs, which the profile denies writes to.
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

// emit writes "(rule (filter "value"))", a rule of this kind for path.
func (b *profileBuilder) emit(kind ruleKind, rule, filter, path, value string) {
	if q, ok := b.quote(value); ok {
		b.add(kind, path, fmt.Sprintf("(%s (%s %s))\n", rule, filter, q))
	}
}

// emitNoSymlink writes a rule of this kind for path that refuses a new
// symlink wherever filter matches. The sandbox checks the type of what is
// created, and checks a symlink moved there the same way, so files and
// folders may still be created where no symlink can.
func (b *profileBuilder) emitNoSymlink(kind ruleKind, filter, path, value string) {
	if q, ok := b.quote(value); ok {
		b.add(kind, path, fmt.Sprintf("(deny file-write-create (require-all (%s %s) (vnode-type SYMLINK)))\n", filter, q))
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

// add writes the rule text, a rule of this kind for path. A deny written
// already since the last allow changes nothing, so it is written once:
// the same paths come from several places (a program the bridge runs is
// also one it runs by name), and the profile goes inline on every run.
// After an allow it is written again, since the last matching rule wins.
// Every rule is kept in rules either way.
func (b *profileBuilder) add(kind ruleKind, path, text string) {
	b.rules = append(b.rules, profileRule{text, path, kind})
	switch {
	case kind == ruleAllow:
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

// maxSymlinks bounds resolvePath on a symlink loop.
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

// calendarAllows re-allows a brain's own calendar credential pair in dir:
// read both, and write the token back on refresh.
func (b *profileBuilder) calendarAllows(dir, brainID string) {
	creds := filepath.Join(dir, "google_calendar_credentials_"+brainID+".json")
	token := filepath.Join(dir, "google_calendar_token_"+brainID+".json")
	b.allow("file-read*", "literal", creds)
	b.allow("file-read*", "literal", token)
	b.allow("file-write*", "literal", token)
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

// Command returns the program and arguments that run claudePath with args
// under the brain's profile. The profile goes inline (-p): a profile file in
// the shared TMPDIR could be swapped by another brain between being written
// and being read, and a brain that made TMPDIR unwritable broke the sandbox.
func (sp *SandboxProfiler) Command(brainID, claudePath string, args []string) (string, []string, error) {
	if err := sp.Available(); err != nil {
		return "", nil, err
	}
	profile, err := sp.Profile(brainID)
	if err != nil {
		return "", nil, err
	}
	return sp.execPath, append([]string{"-p", profile, claudePath}, args...), nil
}
