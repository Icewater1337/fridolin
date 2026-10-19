package app

import (
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/Icewater1337/fridolin/internal/platform"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/sandbox"
)

// SandboxProfiler generates a macOS sandbox-exec profile per brain.
//
// Writing is denied by default: a brain may write only its own folder, its
// own media inbox, and its own state the bridge keeps for it (a Claude
// Code config dir, TMPDIR and caches; see brainState). Everything else
// your user may write stays closed, so a prompt-injected brain cannot plant
// code that runs outside its sandbox: in ~/.claude, which your own sessions
// load, in a LaunchAgent, a shell startup file, your apps' preferences, a
// program on PATH or the bridge's own install. Reading is allowed by
// default, with an explicit denylist: the other brains' folders, the
// bridge's own files, your Claude Code setup and instructions, the
// keychains, and the usual home-directory hotspots (keys, mail, browser
// data). That is a containment measure, not a jail: "What the sandbox does
// not cover" in docs/CONFIGURATION.md lists what it leaves open. Add
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

	sandboxConfig

	// configFile is the config.json the bridge runs with; see SetConfigFile.
	configFile string
	home       string // captured at startup; retained across runtime policy updates

	// execPath is sandboxExecPath; tests point it at a missing file.
	execPath string

	// bridgeExe is the running bridge binary; tests point it elsewhere.
	bridgeExe string
}

// sandboxPolicyOf takes the profiles' settings from cfg.
func sandboxPolicyOf(cfg *config.File) sandboxConfig {
	// These may be relative, and the bridge opens them relative to its
	// working directory; the comparisons need them absolute.
	p := sandboxConfig{Settings: sandbox.Settings{
		DataDir:     AbsPath(cfg.DataDir),
		DBPath:      AbsPath(cfg.DBPath),
		MediaDir:    AbsPath(cfg.MediaDir),
		CredsDir:    cfg.Sandbox.CredentialsDir,
		GitConfig:   cfg.Sandbox.GitConfig,
		PlaudShared: cfg.Plaud.BrainAccess,
		InheritUser: cfg.InheritClaudeUserConfig,
		ExtraDeny:   append([]string(nil), cfg.Sandbox.DenyPaths...),
		ExtraAllow:  append([]string(nil), cfg.Sandbox.AllowPaths...),
	}, claudePath: cfg.ClaudePath, codexPath: cfg.CodexPath, rclonePath: cfg.Backup.RclonePath, plaudCLIPath: cfg.Plaud.CLIPath, viewer: cfg.Viewer}
	if p.MediaDir == "" && p.DataDir != "" {
		p.MediaDir = filepath.Join(p.DataDir, "media")
	}
	if media := p.MediaDir; media != "" {
		p.Inbox = func(id string) string { return mediaInboxAt(media).Path(id) }
	}
	if data := p.DataDir; data != "" {
		p.State = func(id string) sandbox.StatePaths {
			s := brainStateOf(data, id)
			return sandbox.StatePaths{Dir: s.dir, Folders: s.folders()}
		}
		p.Tokens = func(id string) string { return runTokenDir(data, id) }
	}
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); filepath.IsAbs(dir) {
		p.ClaudeDir = filepath.Clean(dir)
	}
	for _, o := range PathOwners(cfg) {
		p.brainDirs = append(p.brainDirs, o.Dirs...)
	}
	return p
}

// NewSandboxProfiler builds a profiler for the configured brains.
func NewSandboxProfiler(workDirs map[string][]string, cfg *config.File) *SandboxProfiler {
	sp := &SandboxProfiler{execPath: sandbox.ExecPath, home: HomeDir()}
	if exe, err := os.Executable(); err == nil {
		sp.bridgeExe = exe
	}
	sp.Update(workDirs, cfg)
	return sp
}

// Update replaces the brain set and settings. Profiles are built per run,
// so the next run of every brain picks the change up.
func (sp *SandboxProfiler) Update(workDirs map[string][]string, cfg *config.File) {
	wd := make(map[string][]string, len(workDirs))
	for k, v := range workDirs {
		// Made absolute, "" would be the bridge's working directory.
		wd[k] = slices.DeleteFunc(slices.Clone(v), func(d string) bool { return d == "" })
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.enabled = cfg.Sandbox.Enabled
	sp.workDirs = wd
	sp.sandboxConfig = sandboxPolicyOf(cfg)
	for _, dirs := range wd {
		sp.brainDirs = append(sp.brainDirs, dirs...)
	}
}

// SetConfigFile names the config.json the bridge runs with. It lives in the
// data dir by default, but --config or FRIDOLIN_CONFIG can put it anywhere,
// so the profile denies it, its .bak, the temp file each save writes, and
// the .env files older versions read: the one next to it and the one in the
// working directory.
func (sp *SandboxProfiler) SetConfigFile(path string) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.configFile = AbsPath(path)
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
	if sp.DataDir != "" {
		deny = append(deny, sp.DataDir)
	}
	if sp.DBPath != "" {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			deny = append(deny, sp.DBPath+suffix)
		}
	}
	if sp.configFile != "" {
		deny = append(deny, sp.configFile, sp.configFile+".bak", config.DotEnvPath(sp.configFile))
	}
	if sp.CredsDir != "" {
		deny = append(deny, AbsPath(sp.CredsDir))
	}
	for _, p := range sp.ExtraDeny {
		deny = append(deny, AbsPath(p))
	}
	if sp.bridgeExe != "" {
		noWrite = append(noWrite, sp.bridgeExe)
		l := platform.LayoutOf(sp.bridgeExe)
		for _, dir := range []string{l.Dir, l.Source} {
			if dir != "" {
				noWrite = append(noWrite, dir)
			}
		}
	}
	return deny, noWrite
}

// bridgeProgramsOf finds the bridge's programs as it would find them now.
func (p sandboxConfig) bridgeProgramsOf(home string) sandbox.Programs {
	var found []string
	if c, err := exec.LookPath(cmp.Or(p.claudePath, "claude")); err == nil {
		found = append(found, c)
	}
	if c, err := exec.LookPath(cmp.Or(p.codexPath, "codex")); err == nil {
		found = append(found, c)
	}
	if r, err := findRclone(p.rclonePath); err == nil {
		found = append(found, r)
	}
	plaud, plaudErr := findPlaudCLI(p.plaudCLIPath)
	if plaudErr == nil {
		found = append(found, plaud)
	}
	if p.viewer {
		if t := findTmux(); t != "" {
			found = append(found, t)
		}
	}
	var out sandbox.Programs
	var path []string
	path = append(path, filepath.SplitList(os.Getenv("PATH"))...)
	if plaudErr == nil {
		path = append(path, filepath.SplitList(plaudPATH(plaud))...)
	}
	// A folder of the bridge's PATH inside a brain folder is one it runs
	// nothing from: setBridgePATH takes it out, and only that brain's runs
	// get it. Before it has (the brain is being added, say), it still
	// counts as out. The folders the bridge falls back on, and those of
	// the programs it found, count wherever they are.
	fromPATH := map[string]bool{}
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		fromPATH[filepath.Clean(d)] = true
	}
	path = slices.DeleteFunc(path, func(d string) bool {
		d = filepath.Clean(d)
		return fromPATH[d] && filepath.IsAbs(d) && (withinAny(d, p.brainDirs) || withinAny(fsx.Resolve(d), p.brainDirs))
	})
	path = append(path, toolFallbackDirs()...)
	if n, err := findNode(); err == nil {
		// The site's processes run with node's folder first on PATH.
		found = append(found, n)
		path = append(path, filepath.Dir(n))
	}
	for _, f := range found {
		abs := AbsPath(f)
		out.Files = append(out.Files, abs, fsx.Resolve(abs))
		if pkg := nodePackage(fsx.Resolve(abs)); pkg != "" {
			out.Dirs = append(out.Dirs, pkg)
			// #!/usr/bin/env node: the bridge runs the Plaud CLI with its
			// own folder first on PATH, everything else with its own PATH.
			for _, pp := range []string{plaudPATH(abs), os.Getenv("PATH")} {
				if node := lookPathIn("node", pp); node != "" {
					out.Files = append(out.Files, node, fsx.Resolve(node))
				}
			}
		}
	}
	out.Files = append(out.Files, rcloneFallbacks()...)
	out.Dirs = append(out.Dirs, filepath.Join(home, nvmNodeVersions))
	for _, d := range path {
		// exec.LookPath skips relative entries.
		if d = filepath.Clean(d); filepath.IsAbs(d) {
			out.Path = append(out.Path, d)
		}
	}
	for _, s := range []*[]string{&out.Files, &out.Dirs, &out.Path} {
		slices.Sort(*s)
		*s = slices.Compact(*s)
	}
	return out
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
func AbsPath(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
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

type sandboxConfig struct {
	sandbox.Settings
	// claudePath, rclonePath, plaudCLIPath and viewer name the programs the
	// bridge itself runs outside the sandbox; see bridgeProgramsOf.
	claudePath, codexPath, rclonePath, plaudCLIPath string
	viewer                                          bool

	// brainDirs are the configured brain folders, as configured and by
	// their real paths: the bridge runs nothing from a folder of its PATH
	// inside one (see setBridgePATH).
	brainDirs []string
}

func sandboxContextNow(home string) sandbox.Context {
	cwd, err := os.Getwd()
	return sandbox.Context{Home: home, WorkingDir: cwd, WorkingDirErr: err, TempDir: os.TempDir(), TmuxSocket: filepath.Join(cmp.Or(os.Getenv("TMUX_TMPDIR"), "/tmp"), fmt.Sprintf("tmux-%d", os.Getuid()))}
}

func sandboxInstallLayoutOf(l platform.BridgeLayout) sandbox.InstallLayout {
	return sandbox.InstallLayout{Dir: l.Dir, Source: l.Source}
}

func (sp *SandboxProfiler) Profile(id string) (string, error) {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	ctx := sandboxContextNow(sp.home)
	var layout sandbox.InstallLayout
	if sp.bridgeExe != "" {
		layout = sandboxInstallLayoutOf(platform.LayoutOf(sp.bridgeExe))
	}
	return sandbox.Profile(sandbox.Policy{Settings: sp.Settings, Context: ctx, WorkDirs: sp.workDirs, ConfigFile: sp.configFile, ConfigEnvFile: config.DotEnvPath(sp.configFile), BridgeExe: sp.bridgeExe, Layout: layout, Programs: sp.bridgeProgramsOf(ctx.Home)}, id)
}

func (p sandboxConfig) protected(home string, l platform.BridgeLayout) []sandbox.ProtectedPath {
	return p.Settings.Protected(sandboxContextNow(home), sandboxInstallLayoutOf(l), p.bridgeProgramsOf(home))
}

func protectedIn(dir string, paths []sandbox.ProtectedPath) (string, bool) {
	return sandbox.ProtectedIn(sandboxContextNow(""), dir, paths)
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

func withinAny(p string, dirs []string) bool {
	return slices.ContainsFunc(dirs, func(d string) bool { return fsx.Within(p, d) })
}
