package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
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

	var b profileBuilder
	b.trustRoot(home)
	if sp.dataDir != "" {
		b.trustRoot(sp.dataDir)
	}
	if sp.credsDir != "" {
		b.trustRoot(sp.credsDir)
	}
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
			b.deny("file-read*", "subpath", dir)
			b.deny("file-write*", "subpath", dir)
		}
		b.WriteString("\n")
	}

	// ── home-directory hotspots ──────────────────────────────────────────
	b.WriteString(";; Cross-session state, credentials, and personal data stores\n")
	for _, sub := range defaultDenySubpaths {
		b.deny("file-read*", "subpath", filepath.Join(home, sub))
	}
	for _, lit := range defaultDenyLiterals {
		b.deny("file-read*", "literal", filepath.Join(home, lit))
	}
	if !sp.plaudShared {
		// The Plaud CLI's login, unless plaud.brain_access shares it.
		b.deny("file-read*", "subpath", filepath.Join(home, ".plaud"))
		b.deny("file-write*", "subpath", filepath.Join(home, ".plaud"))
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
			b.deny("file-read*", "literal", f)
		}
	} else {
		b.WriteString(";; Git configuration: readable, never writable\n")
		for _, f := range gitFiles {
			b.deny("file-write*", "literal", f)
		}
		b.deny("file-write*", "subpath", filepath.Join(home, ".config", "git"))
	}
	b.WriteString("\n")

	// ── bridge runtime dir ───────────────────────────────────────────────
	// The DB holds every chat's session IDs and poll votes; the dashboard
	// token and backup credentials live here too. Media is the shared inbound
	// staging dir, so it is re-allowed below.
	if sp.dataDir != "" {
		b.WriteString(";; Bridge runtime data (media staging re-allowed below)\n")
		b.deny("file-read*", "subpath", sp.dataDir)
		b.deny("file-write*", "subpath", sp.dataDir)
		b.allow("file-read*", "subpath", sp.mediaDir)
		b.allow("file-write*", "subpath", sp.mediaDir)
		b.calendarAllows(sp.dataDir, brainID)
		b.WriteString("\n")
	}
	if sp.credsDir != "" {
		b.WriteString(";; Shared credentials dir: only this brain's own calendar files\n")
		b.deny("file-read*", "subpath", sp.credsDir)
		b.deny("file-write*", "subpath", sp.credsDir)
		b.calendarAllows(sp.credsDir, brainID)
		b.WriteString("\n")
	}

	// ── user-supplied extras ─────────────────────────────────────────────
	if len(sp.extraDeny) > 0 {
		b.WriteString(";; Extra denies from config\n")
		for _, p := range sp.extraDeny {
			b.deny("file-read*", "subpath", p)
			b.deny("file-write*", "subpath", p)
		}
		b.WriteString("\n")
	}
	if len(sp.extraAllow) > 0 {
		b.WriteString(";; Extra allows from config (override the denies above)\n")
		for _, p := range sp.extraAllow {
			b.allow("file-read*", "subpath", p)
		}
		b.WriteString("\n")
	}

	// ── the brain's own folder ───────────────────────────────────────────
	// Last, so it wins over any deny above: a brain always reaches its own
	// files, even when its folder sits inside a default-denied directory.
	if own := sp.workDirs[brainID]; own != "" {
		b.WriteString(";; This brain's own folder\n")
		b.allow("file-read*", "subpath", own)
		b.allow("file-write*", "subpath", own)
	}

	if b.err != nil {
		return "", b.err
	}
	return b.String(), nil
}

// profileBuilder writes SBPL. Every path goes through deny or allow, so each
// one is quoted and canonicalized the same way; the first path that cannot be
// written is kept in err and fails the whole profile.
type profileBuilder struct {
	strings.Builder
	err error
	// roots are folders a brain cannot replace, with their real paths.
	roots []pathAlias
}

type pathAlias struct{ configured, real string }

// deny emits "(deny op (filter "path"))", e.g. deny("file-read*", "subpath", dir).
//
// The sandbox matches real paths, so a rule for a path that runs through a
// symlink never fires: under a symlinked HOME (or /var instead of
// /private/var) a brain read ~/.fridolin straight through its real path.
// deny therefore emits the rule for the real path as well, following every
// symlink: a deny that covers more only takes access away. The configured
// spelling stays too, for the day the symlink is replaced by a real folder.
func (b *profileBuilder) deny(op, filter, path string) {
	p, ok := b.absolute(path)
	if !ok {
		return
	}
	configured := filepath.Clean(p)
	b.emit("deny "+op, filter, configured)
	if real := resolvePath(p); real != configured {
		b.emit("deny "+op, filter, real)
	}
}

// allow emits "(allow op (filter "path"))" for path, and again with its
// longest trusted root (see trustRoot) replaced by that root's real path.
// Unlike deny it follows no other symlink: a brain can replace its own
// folder, the media dir or its calendar token with a symlink, and an allow
// that followed it would open whatever the symlink points to.
func (b *profileBuilder) allow(op, filter, path string) {
	p, ok := b.absolute(path)
	if !ok {
		return
	}
	configured := filepath.Clean(p)
	b.emit("allow "+op, filter, configured)
	best := pathAlias{}
	for _, r := range b.roots {
		if (configured == r.configured || strings.HasPrefix(configured, r.configured+string(filepath.Separator))) &&
			len(r.configured) > len(best.configured) {
			best = r
		}
	}
	if best.configured != "" {
		b.emit("allow "+op, filter, best.real+strings.TrimPrefix(configured, best.configured))
	}
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

func (b *profileBuilder) emit(rule, filter, path string) {
	q, err := sbplString(path)
	if err != nil {
		b.fail(err)
		return
	}
	fmt.Fprintf(b, "(%s (%s %s))\n", rule, filter, q)
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
