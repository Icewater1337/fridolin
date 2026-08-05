package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
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
	// workDirs maps brain name → work dir, so each profile can deny the others.
	workDirs map[string]string

	// dataDir is the bridge's runtime dir, denied wholesale except for media.
	dataDir string

	// extraDeny and extraAllow come from config.
	extraDeny  []string
	extraAllow []string
}

// NewSandboxProfiler builds a profiler for the configured brains.
func NewSandboxProfiler(workDirs map[string]string, cfg *Config) *SandboxProfiler {
	return &SandboxProfiler{
		workDirs:   workDirs,
		dataDir:    cfg.DataDir,
		extraDeny:  cfg.Sandbox.DenyPaths,
		extraAllow: cfg.Sandbox.AllowPaths,
	}
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
	".gitconfig",
	".git-credentials",
	".netrc",
	".zsh_history",
	".bash_history",
}

// Profile returns the sandbox profile source for the named brain.
func (sp *SandboxProfiler) Profile(brainName string) string {
	home := os.Getenv("HOME")

	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n\n")

	// ── other brains' working trees ──────────────────────────────────────
	others := make([]string, 0, len(sp.workDirs))
	for name, dir := range sp.workDirs {
		if name != brainName && dir != "" {
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
	b.WriteString("\n")

	// ── bridge runtime dir ───────────────────────────────────────────────
	// The DB holds every chat's session IDs and poll votes; brains have no
	// business reading it. Media is the shared inbound staging dir, so it is
	// re-allowed below.
	if sp.dataDir != "" {
		b.WriteString(";; Bridge runtime data (media staging re-allowed below)\n")
		fmt.Fprintf(&b, "(deny file-read* (subpath %q))\n", sp.dataDir)
		fmt.Fprintf(&b, "(deny file-write* (subpath %q))\n", sp.dataDir)
		mediaDir := filepath.Join(sp.dataDir, "media")
		fmt.Fprintf(&b, "(allow file-read* (subpath %q))\n", mediaDir)
		fmt.Fprintf(&b, "(allow file-write* (subpath %q))\n", mediaDir)

		// Calendar credentials are per brain: <data_dir>/google_calendar_*_<brain>.json.
		// A brain may read its own pair and write its own token back on refresh.
		creds := filepath.Join(sp.dataDir, "google_calendar_credentials_"+brainName+".json")
		token := filepath.Join(sp.dataDir, "google_calendar_token_"+brainName+".json")
		fmt.Fprintf(&b, "(allow file-read* (literal %q))\n", creds)
		fmt.Fprintf(&b, "(allow file-read* (literal %q))\n", token)
		fmt.Fprintf(&b, "(allow file-write* (literal %q))\n", token)
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

	return b.String()
}

// Write renders the profile for a brain into a private temp file and returns
// its path. The caller unlinks the file once the spawned process exits.
func (sp *SandboxProfiler) Write(brainName string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("sandbox-exec is macOS-only, got %s", runtime.GOOS)
	}
	if _, err := os.Stat(sandboxExecPath); err != nil {
		return "", fmt.Errorf("%s not available: %w", sandboxExecPath, err)
	}

	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}

	name := fmt.Sprintf("wcb-brain-%s-%s.sb", brainName, hex.EncodeToString(buf))
	path := filepath.Join(os.TempDir(), name)
	if err := os.WriteFile(path, []byte(sp.Profile(brainName)), 0o600); err != nil {
		return "", fmt.Errorf("write profile: %w", err)
	}
	return path, nil
}
