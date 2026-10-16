package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// sandboxExecPath is the macOS sandbox wrapper used for brain spawns.
const sandboxExecPath = "/usr/bin/sandbox-exec"

// SandboxProfiler generates a macOS sandbox-exec profile per brain.
//
// The profile is allow-by-default with an explicit denylist. That is a
// containment measure, not a jail: it stops a prompt-injected brain from
// reading the *other* brains' trees, your cross-session Claude state, and the
// usual home-directory hotspots (keys, mail, browser data). It does not stop a
// determined attacker with code execution.
//
// Brains keep write access by default, so a brain can still maintain its own
// notes and files. Add anything else that must stay unreadable to
// sandbox.deny_paths in config.json.
type SandboxProfiler struct {
	mu sync.RWMutex

	enabled bool

	// workDirs maps brain id → work dir, so each profile can deny the others.
	workDirs map[string]string

	// dataDir is the bridge's runtime dir, denied wholesale except for media.
	dataDir  string
	mediaDir string

	// credsDir holds per-brain credential files; see SandboxConfig.CredentialsDir.
	credsDir string

	// gitConfig is "read" or "deny".
	gitConfig string

	// plaudShared: the Plaud login in ~/.plaud is meant for brains too.
	plaudShared bool

	// extraDeny and extraAllow come from config.
	extraDeny  []string
	extraAllow []string

	// execPath is sandboxExecPath; tests point it at a missing file.
	execPath string
}

// NewSandboxProfiler builds a profiler for the configured brains.
func NewSandboxProfiler(workDirs map[string]string, cfg *Config) *SandboxProfiler {
	sp := &SandboxProfiler{execPath: sandboxExecPath}
	sp.Update(workDirs, cfg)
	return sp
}

// Update replaces the brain set and settings. Profiles are built per run,
// so the next run of every brain picks the change up.
func (sp *SandboxProfiler) Update(workDirs map[string]string, cfg *Config) {
	wd := make(map[string]string, len(workDirs))
	for k, v := range workDirs {
		wd[k] = v
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.enabled = cfg.Sandbox.Enabled
	sp.workDirs = wd
	sp.dataDir = cfg.DataDir
	sp.mediaDir = cfg.MediaDir
	if sp.mediaDir == "" && cfg.DataDir != "" {
		sp.mediaDir = filepath.Join(cfg.DataDir, "media")
	}
	sp.credsDir = cfg.Sandbox.CredentialsDir
	sp.gitConfig = cfg.Sandbox.GitConfig
	sp.plaudShared = cfg.Plaud.BrainAccess
	sp.extraDeny = append([]string(nil), cfg.Sandbox.DenyPaths...)
	sp.extraAllow = append([]string(nil), cfg.Sandbox.AllowPaths...)
}

// Enabled reports whether brain spawns should be sandboxed.
func (sp *SandboxProfiler) Enabled() bool {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	return sp.enabled
}

// defaultDenySubpaths are home-relative directories no brain should read.
// Keychains are deliberately absent: the Claude CLI reads its credentials from
// the macOS keychain, and denying it breaks the CLI with "Not logged in".
var defaultDenySubpaths = []string{
	".claude/projects", // other Claude Code sessions' transcripts
	".claude/sessions",
	".ssh",
	".gnupg",
	".aws",
	".config/gh",
	".config/gcloud",
	".config/rclone",
	".config/fridolin",
	".config/whatsapp-claude-bridge", // the name before the rename
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

// defaultDenyLiterals are individual home-relative files to deny.
var defaultDenyLiterals = []string{
	".claude/history.jsonl",
	".git-credentials",
	".netrc",
	".zsh_history",
	".bash_history",
}

// Profile returns the sandbox profile source for the brain with this id.
func (sp *SandboxProfiler) Profile(brainID string) string {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	home := os.Getenv("HOME")

	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n\n")

	// ── other brains' working trees ──────────────────────────────────────
	others := make([]string, 0, len(sp.workDirs))
	for id, dir := range sp.workDirs {
		if id != brainID && dir != "" {
			others = append(others, dir)
		}
	}
	sort.Strings(others)
	if len(others) > 0 {
		b.WriteString(";; Other brains' working trees\n")
		for _, dir := range others {
			fmt.Fprintf(&b, "(deny file-read* (subpath %q))\n", dir)
			fmt.Fprintf(&b, "(deny file-write* (subpath %q))\n", dir)
		}
		b.WriteString("\n")
	}

	// ── home-directory hotspots ──────────────────────────────────────────
	b.WriteString(";; Cross-session state, credentials, and personal data stores\n")
	for _, sub := range defaultDenySubpaths {
		fmt.Fprintf(&b, "(deny file-read* (subpath %q))\n", filepath.Join(home, sub))
	}
	for _, lit := range defaultDenyLiterals {
		fmt.Fprintf(&b, "(deny file-read* (literal %q))\n", filepath.Join(home, lit))
	}
	if !sp.plaudShared {
		// The Plaud CLI's login, unless plaud.brain_access shares it.
		fmt.Fprintf(&b, "(deny file-read* (subpath %q))\n", filepath.Join(home, ".plaud"))
		fmt.Fprintf(&b, "(deny file-write* (subpath %q))\n", filepath.Join(home, ".plaud"))
	}
	b.WriteString("\n")

	// ── git configuration ────────────────────────────────────────────────
	// Reading git config lets a brain commit in its own folder (name, email,
	// signing settings). Writing it would let a brain change how *you* use
	// git, so that stays denied. Credentials stay denied either way.
	gitFiles := gitConfigChain(home)
	if sp.gitConfig == "deny" {
		b.WriteString(";; Git configuration (denied by sandbox.git_config)\n")
		for _, f := range gitFiles {
			fmt.Fprintf(&b, "(deny file-read* (literal %q))\n", f)
		}
	} else {
		b.WriteString(";; Git configuration: readable, never writable\n")
		for _, f := range gitFiles {
			fmt.Fprintf(&b, "(deny file-write* (literal %q))\n", f)
		}
		fmt.Fprintf(&b, "(deny file-write* (subpath %q))\n", filepath.Join(home, ".config", "git"))
	}
	b.WriteString("\n")

	// ── bridge runtime dir ───────────────────────────────────────────────
	// The DB holds every chat's session IDs and poll votes; the dashboard
	// token and backup credentials live here too. Media is the shared inbound
	// staging dir, so it is re-allowed below.
	if sp.dataDir != "" {
		b.WriteString(";; Bridge runtime data (media staging re-allowed below)\n")
		fmt.Fprintf(&b, "(deny file-read* (subpath %q))\n", sp.dataDir)
		fmt.Fprintf(&b, "(deny file-write* (subpath %q))\n", sp.dataDir)
		fmt.Fprintf(&b, "(allow file-read* (subpath %q))\n", sp.mediaDir)
		fmt.Fprintf(&b, "(allow file-write* (subpath %q))\n", sp.mediaDir)
		writeCalendarAllows(&b, sp.dataDir, brainID)
		b.WriteString("\n")
	}
	if sp.credsDir != "" {
		b.WriteString(";; Shared credentials dir: only this brain's own calendar files\n")
		fmt.Fprintf(&b, "(deny file-read* (subpath %q))\n", sp.credsDir)
		fmt.Fprintf(&b, "(deny file-write* (subpath %q))\n", sp.credsDir)
		writeCalendarAllows(&b, sp.credsDir, brainID)
		b.WriteString("\n")
	}

	// ── user-supplied extras ─────────────────────────────────────────────
	if len(sp.extraDeny) > 0 {
		b.WriteString(";; Extra denies from config\n")
		for _, p := range sp.extraDeny {
			fmt.Fprintf(&b, "(deny file-read* (subpath %q))\n", p)
			fmt.Fprintf(&b, "(deny file-write* (subpath %q))\n", p)
		}
		b.WriteString("\n")
	}
	if len(sp.extraAllow) > 0 {
		b.WriteString(";; Extra allows from config (override the denies above)\n")
		for _, p := range sp.extraAllow {
			fmt.Fprintf(&b, "(allow file-read* (subpath %q))\n", p)
		}
		b.WriteString("\n")
	}

	// ── the brain's own folder ───────────────────────────────────────────
	// Last, so it wins over any deny above: a brain always reaches its own
	// files, even when its folder sits inside a default-denied directory.
	if own := sp.workDirs[brainID]; own != "" {
		b.WriteString(";; This brain's own folder\n")
		fmt.Fprintf(&b, "(allow file-read* (subpath %q))\n", own)
		fmt.Fprintf(&b, "(allow file-write* (subpath %q))\n", own)
	}

	return b.String()
}

// writeCalendarAllows re-allows a brain's own calendar credential pair in dir:
// read both, and write the token back on refresh.
func writeCalendarAllows(b *strings.Builder, dir, brainID string) {
	creds := filepath.Join(dir, "google_calendar_credentials_"+brainID+".json")
	token := filepath.Join(dir, "google_calendar_token_"+brainID+".json")
	fmt.Fprintf(b, "(allow file-read* (literal %q))\n", creds)
	fmt.Fprintf(b, "(allow file-read* (literal %q))\n", token)
	fmt.Fprintf(b, "(allow file-write* (literal %q))\n", token)
}

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
	return sp.execPath, append([]string{"-p", sp.Profile(brainID), claudePath}, args...), nil
}
