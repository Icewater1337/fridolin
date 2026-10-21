package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/memindex"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// DoctorOptions are the doctor command's flags.
type DoctorOptions struct {
	Config string
	JSON   bool
	Redact bool
}

// ErrDoctorFailed is what doctor ends with when a required check fails;
// the command then exits 1.
var ErrDoctorFailed = errors.New("a required check failed")

// The statuses a doctor check ends with. Only a required check fails the
// command; an optional one that is not ready says missing or warn, with
// the fix, until the milestone that needs it makes it required.
const (
	DoctorOK      = "ok"
	DoctorMissing = "missing"
	DoctorWarn    = "warn"
	DoctorFail    = "fail"
)

// DoctorResult is what one check found: its status, what it saw, and the
// command that fixes it when the status is not ok.
type DoctorResult struct {
	Status string
	Detail string
	Fix    string
}

// DoctorCheck is one thing doctor looks at. Applies, when set, leaves the
// check out where it does not concern this setup (the Codex CLI in Claude
// mode). Needed names the milestone of docs/PLAN.md that will make an
// optional check required; that milestone flips Required in the same pull
// request as the feature, since every milestone's gate is a doctor check.
type DoctorCheck struct {
	ID       string
	Title    string
	Required bool
	Needed   string
	Applies  func(env *DoctorEnv) bool
	Run      func(ctx context.Context, env *DoctorEnv) DoctorResult
}

// DoctorEnv is what checks ask of the system, as functions so that tests
// fake every one of them: no check calls exec, os or syscall itself.
type DoctorEnv struct {
	// ConfigGOOS is the platform configs are resolved for (ConfigGOOS):
	// config.ContainerGOOS in the fridolin-core image.
	ConfigGOOS string
	GOOS       string
	Home       string
	// ConfigPath is the config file; Config is it loaded, or nil with
	// ConfigErr saying why it did not load.
	ConfigPath string
	Config     *config.Settings
	ConfigErr  error

	Getenv   func(name string) string
	LookPath func(file string) (string, error)
	// Run runs a program and returns its standard output. A program that
	// ran and exited with a status other than 0 returns an error with an
	// ExitCode() int method, as *exec.ExitError has.
	Run       func(ctx context.Context, name string, args ...string) ([]byte, error)
	Stat      func(path string) (fs.FileInfo, error)
	ReadFile  func(path string) ([]byte, error)
	Glob      func(pattern string) ([]string, error)
	FreeBytes func(path string) (uint64, error)
	// WriteProbe creates a file in dir and removes it again.
	WriteProbe func(dir string) error
	// Dial opens a TCP connection to address and closes it again.
	Dial func(ctx context.Context, address string) error
	// Now is the time the checks judge ages by; nil is time.Now.
	Now func() time.Time
	// MemorySelfTest runs the memory index's provenance gate on a
	// scratch brain (memindex.SelfTest); MemoryStale lists what a brain's
	// index does not hold as it is on disk (memindex.Stale).
	MemorySelfTest func(ctx context.Context) error
	MemoryStale    func(ctx context.Context, b memindex.Brain) ([]string, error)
	// HasHostSecret asks the Windows Credential Manager whether it holds
	// a secret (macOS and Linux are asked through Run and Stat).
	HasHostSecret func(name string) (bool, error)
	// LiveNodes asks the running bridge for its nodes as it knows them
	// now (GET /api/nodes); an error when it does not answer, and the
	// node checks read nodes.json instead. nil never asks.
	LiveNodes func(ctx context.Context, cfg *config.Settings) ([]byte, error)
	// AndroidToolchain says why this machine cannot build the Plaud phone
	// app (no JDK 17+ or Android SDK), nil when it can.
	AndroidToolchain func() error
}

// DoctorRegistry is every check, in the order doctor prints them. Later
// milestones append theirs here.
var DoctorRegistry = []DoctorCheck{
	{ID: "config", Title: "config loads", Required: true, Run: checkConfigLoads},
	{ID: "data-dir", Title: "data dir writable", Required: true, Run: checkDataDirWritable},
	{ID: "disk", Title: "free disk in the data dir", Required: true, Run: checkDiskFree},
	{ID: "claude", Title: "Claude CLI", Required: true, Applies: claudeMode, Run: checkClaudeCLI},
	{ID: "claude-login", Title: "Claude login for sandboxed brains", Required: true, Applies: claudeMode, Run: checkClaudeLogin},
	{ID: "codex", Title: "Codex CLI", Required: true, Applies: codexMode, Run: checkCodexCLI},
	{ID: "agent-path", Title: "agent CLI by absolute path", Required: true, Run: checkAgentPath},
	{ID: "tailscale", Title: "Tailscale", Needed: "M1", Applies: onHost, Run: checkTailscale},
	{ID: "tailscale-authkey", Title: "Tailscale auth key in host secrets", Needed: "M1", Applies: onHost, Run: checkSecrets(tailscaleAuthKeySecret)},
	{ID: "b2-keys", Title: "Backblaze B2 keys in host secrets", Needed: "M3", Applies: onHost, Run: checkSecrets(b2Secrets...)},
	{ID: "docker", Title: "Docker", Needed: "M1", Applies: onHost, Run: checkDocker},
	toolCheck("restic", "M3", []string{"version"}, "restic", "restic"),
	toolCheck("rclone", "M3", []string{"version"}, "rclone", "rclone"),
	toolCheck("ffmpeg", "", []string{"-version"}, "ffmpeg", "ffmpeg"),
	toolCheck("litestream", "M3", []string{"version"}, "benbjohnson/litestream/litestream", ""),
	toolCheck("age", "M1", []string{"--version"}, "age", "age"),
	{ID: "nodes-port", Title: "node port", Required: true, Applies: nodesOn, Run: checkNodesPort},
	{ID: "nodes-seen", Title: "approved nodes heard from", Applies: nodesOn, Run: checkNodesSeen},
	{ID: "memory-index", Title: "memory index and its provenance gate", Required: true, Applies: memoryEnabled, Run: checkMemoryIndex},
	{ID: "approvals", Title: "approvals waiting", Run: checkPendingApprovals},
	{ID: "lockdown", Title: "lockdown", Run: checkLockdown},
	{ID: "nodes-fride", Title: "FrIDE on the nodes", Required: true, Applies: nodesOn, Run: checkNodesFride},
}

// The host secrets doctor looks for (docs/PLAN.md, M1 and M3). It asks
// whether each is there and never reads one.
const (
	hostSecretAccount      = "fridolin"
	tailscaleAuthKeySecret = "fridolin.tailscale.authkey"
)

var b2Secrets = []string{
	"fridolin.b2.vault.keyid", "fridolin.b2.vault.key",
	"fridolin.b2.snapshots.keyid", "fridolin.b2.snapshots.key",
}

// minCodexVersion is the oldest Codex CLI the bridge supports (AGENTS.md).
const minCodexVersion = "0.163.0"

// The free space below which doctor warns, and below which it fails: the
// vault's hot tier wants room, and SQLite stops at a full disk.
const (
	diskWarnBytes = 20 << 30
	diskFailBytes = 2 << 30
)

// doctorCheckTimeout bounds each check, so a hung `docker info` or
// `tailscale status` cannot stall the command.
const doctorCheckTimeout = 15 * time.Second

// DoctorReport is what doctor found, as --json prints it.
type DoctorReport struct {
	Config string              `json:"config"`
	OK     bool                `json:"ok"`
	Checks []DoctorCheckReport `json:"checks"`
}

// DoctorCheckReport is one check's line.
type DoctorCheckReport struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Required bool   `json:"required"`
	Needed   string `json:"needed,omitempty"`
	Status   string `json:"status"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix,omitempty"`
}

// RunDoctor runs each check that applies to env, in order.
func RunDoctor(ctx context.Context, env *DoctorEnv, checks []DoctorCheck) DoctorReport {
	rep := DoctorReport{Config: env.ConfigPath, OK: true, Checks: []DoctorCheckReport{}}
	for _, c := range checks {
		if c.Applies != nil && !c.Applies(env) {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, doctorCheckTimeout)
		res := c.Run(cctx, env)
		cancel()
		if res.Status == DoctorOK {
			res.Fix = ""
		}
		if res.Status == DoctorFail && c.Required {
			rep.OK = false
		}
		rep.Checks = append(rep.Checks, DoctorCheckReport{
			ID: c.ID, Title: c.Title, Required: c.Required, Needed: c.Needed,
			Status: res.Status, Detail: res.Detail, Fix: res.Fix,
		})
	}
	return rep
}

// WriteText writes the report one line per check, the fix indented below
// a check that is not ok, and a count at the end.
func (r DoctorReport) WriteText(w io.Writer) error {
	var b strings.Builder
	counts := map[string]int{}
	for _, c := range r.Checks {
		counts[c.Status]++
		title := c.Title
		if !c.Required && c.Status != DoctorOK && c.Needed != "" {
			title += " (needed from " + c.Needed + ")"
		}
		fmt.Fprintf(&b, "%-8s %s: %s\n", c.Status, title, c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(&b, "         fix: %s\n", c.Fix)
		}
	}
	fmt.Fprintf(&b, "\n%d ok, %d warn, %d missing, %d fail", counts[DoctorOK], counts[DoctorWarn], counts[DoctorMissing], counts[DoctorFail])
	if r.OK {
		b.WriteString("; every required check passes\n")
	} else {
		b.WriteString("; a required check fails\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteDoctor runs the checks of DoctorRegistry on this system and writes
// the report to w, as text or with o.JSON as JSON. It returns
// ErrDoctorFailed when a required check fails. With o.Redact the report
// and what is logged meanwhile are redacted as check --redact does.
func WriteDoctor(w io.Writer, o DoctorOptions) error {
	return writeDoctor(w, o, SystemDoctorEnv(o.Config), DoctorRegistry)
}

// writeDoctor is WriteDoctor on env, which it loads the config into, with
// checks.
func writeDoctor(w io.Writer, o DoctorOptions, env *DoctorEnv, checks []DoctorCheck) error {
	var red *redactor
	if o.Redact {
		red = newRedactor(env.Home)
		red.addConfig(env.ConfigPath)
		prev := log.Writer()
		log.SetOutput(redactingWriter{prev, red})
		defer log.SetOutput(prev)
	}
	env.loadConfig()
	if env.Config != nil && red != nil {
		red.addChats(env.Config.AllowedNumbers...)
		red.addGroups(env.Config.AllowedGroups...)
		red.addGroupMembers(env.Config.GroupMembers)
	}
	rep := RunDoctor(context.Background(), env, checks)
	var buf bytes.Buffer
	if o.JSON {
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return err
		}
	} else if err := rep.WriteText(&buf); err != nil {
		return err
	}
	out := buf.String()
	if red != nil {
		out = red.text(out)
	}
	if _, err := io.WriteString(w, out); err != nil {
		return err
	}
	if !rep.OK {
		return ErrDoctorFailed
	}
	return nil
}

// SystemDoctorEnv is a DoctorEnv on the real system, for the config file
// the --config value names (or the default one); loadConfig loads it.
func SystemDoctorEnv(configFlag string) *DoctorEnv {
	inputs := ConfigOptions()
	path, _ := ConfigPathFromPaths(configFlag, inputs.Paths)
	return &DoctorEnv{
		GOOS:       runtime.GOOS,
		ConfigGOOS: ConfigGOOS(),
		Home:       inputs.Paths.Home,
		ConfigPath: path,
		Getenv:     os.Getenv,
		LookPath:   exec.LookPath,
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Stdin = nil
			return cmd.Output()
		},
		Stat:      os.Stat,
		ReadFile:  os.ReadFile,
		Glob:      filepath.Glob,
		FreeBytes: platform.FreeBytes,
		MemorySelfTest: func(ctx context.Context) error {
			dir, err := os.MkdirTemp("", "fridolin-doctor-memory-*")
			if err != nil {
				return err
			}
			defer os.RemoveAll(dir)
			return memindex.SelfTest(ctx, dir)
		},
		MemoryStale:   memindex.Stale,
		HasHostSecret: hostsecrets.WinCred{}.Has,
		AndroidToolchain: func() error {
			_, _, err := androidToolchain()
			return err
		},
		WriteProbe: func(dir string) error {
			f, err := os.CreateTemp(dir, ".fridolin-doctor-*")
			if err != nil {
				return err
			}
			name := f.Name()
			cerr := f.Close()
			if err := os.Remove(name); err != nil {
				return err
			}
			return cerr
		},
		Dial: func(ctx context.Context, address string) error {
			c, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
			if err != nil {
				return err
			}
			return c.Close()
		},
		Now:       time.Now,
		LiveNodes: askBridgeNodes,
	}
}

// loadConfig loads the config file as the bridge would, and puts the
// PATH in place that it would run the CLIs with.
func (env *DoctorEnv) loadConfig() {
	cfg, err := config.LoadExisting(env.ConfigPath, ConfigOptions())
	if err != nil {
		env.ConfigErr = err
		return
	}
	// As check does, before any CLI is probed: a Claude planted in a
	// brain's PATH folder must not be the one found.
	SetBridgePATH(cfg)
	env.Config = cfg
}

func claudeMode(env *DoctorEnv) bool {
	return env.Config == nil || env.Config.AgentProvider != "codex"
}

func codexMode(env *DoctorEnv) bool {
	return env.Config != nil && env.Config.AgentProvider == "codex"
}

// noConfig is the result of a check that needs the config when it did
// not load.
var noConfig = DoctorResult{Status: DoctorWarn, Detail: "not checked: the config does not load", Fix: "fix the config first (see the config line)"}

func checkConfigLoads(_ context.Context, env *DoctorEnv) DoctorResult {
	if env.ConfigErr != nil {
		return DoctorResult{Status: DoctorFail, Detail: env.ConfigErr.Error(), Fix: "fridolin check --config " + env.ConfigPath + " (or fridolin init to write a starter config)"}
	}
	return DoctorResult{Status: DoctorOK, Detail: env.ConfigPath}
}

// existingDir returns dir, or the nearest folder above it that exists,
// and whether that is dir itself.
func (env *DoctorEnv) existingDir(dir string) (string, bool) {
	for d := dir; ; d = filepath.Dir(d) {
		if st, err := env.Stat(d); err == nil && st.IsDir() {
			return d, d == dir
		}
		if d == filepath.Dir(d) {
			return d, false
		}
	}
}

func checkDataDirWritable(_ context.Context, env *DoctorEnv) DoctorResult {
	if env.Config == nil {
		return noConfig
	}
	dir := env.Config.DataDir
	at, exists := env.existingDir(dir)
	if err := env.WriteProbe(at); err != nil {
		fix := "chmod u+w " + at
		if !exists {
			fix = "mkdir -p " + dir
		}
		return DoctorResult{Status: DoctorFail, Detail: fmt.Sprintf("cannot write in %s: %v", at, err), Fix: fix}
	}
	if !exists {
		return DoctorResult{Status: DoctorOK, Detail: dir + " does not exist yet; the bridge creates it in " + at}
	}
	return DoctorResult{Status: DoctorOK, Detail: dir}
}

func checkDiskFree(_ context.Context, env *DoctorEnv) DoctorResult {
	if env.Config == nil {
		return noConfig
	}
	at, _ := env.existingDir(env.Config.DataDir)
	free, err := env.FreeBytes(at)
	if err != nil {
		return DoctorResult{Status: DoctorWarn, Detail: fmt.Sprintf("cannot tell the free space at %s: %v", at, err), Fix: "df -h " + at}
	}
	detail := fmt.Sprintf("%s free at %s", formatGB(free), at)
	switch {
	case free < diskFailBytes:
		return DoctorResult{Status: DoctorFail, Detail: detail + ", under " + formatGB(diskFailBytes), Fix: "free up space on that disk, or move data_dir to a larger one"}
	case free < diskWarnBytes:
		return DoctorResult{Status: DoctorWarn, Detail: detail + ", under " + formatGB(diskWarnBytes), Fix: "free up space on that disk, or move data_dir to a larger one"}
	}
	return DoctorResult{Status: DoctorOK, Detail: detail}
}

func formatGB(n uint64) string {
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}

// firstLine is the first line of a program's output, trimmed and cut
// short, for a version.
func firstLine(out []byte) string {
	s, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	s = strings.TrimSpace(s)
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

// installFix is the command that installs a tool on goos: brew on macOS,
// apt on Linux; "" where none is known.
func installFix(goos, brew, apt string) string {
	switch {
	case goos == "darwin" && brew != "":
		return "brew install " + brew
	case goos == "linux" && apt != "":
		return "sudo apt install " + apt
	}
	return ""
}

func checkClaudeCLI(ctx context.Context, env *DoctorEnv) DoctorResult {
	path := "claude"
	if env.Config != nil {
		path = env.Config.ClaudePath
	}
	fix := "curl -fsSL https://claude.ai/install.sh | bash"
	if path != "claude" {
		fix += ", or point claude_path in " + env.ConfigPath + " at it"
	}
	resolved, err := env.LookPath(path)
	if err != nil {
		return DoctorResult{Status: DoctorFail, Detail: path + " not found, so brains cannot run", Fix: fix}
	}
	out, err := env.Run(ctx, resolved, "--version")
	st := claudecli.Status{Path: resolved, Found: true, Version: firstLine(out)}
	if err != nil || st.Version == "" {
		return DoctorResult{Status: DoctorWarn, Detail: resolved + ": `--version` gave no answer", Fix: "claude update"}
	}
	if claudecli.VersionWarning(st) != "" {
		return DoctorResult{Status: DoctorWarn, Detail: fmt.Sprintf("%s (%s), older than %s, the oldest release tested", resolved, st.Version, claudecli.TestedClaudeVersion), Fix: "claude update"}
	}
	return DoctorResult{Status: DoctorOK, Detail: fmt.Sprintf("%s (%s)", resolved, st.Version)}
}

// checkClaudeLogin looks for the login sandboxed brains need, which cannot
// be the keychain one: one of brainLoginVars, in this shell's environment
// or in the service's (the launchd agent or the systemd unit and its
// drop-ins). It looks for the names only, never at the values.
func checkClaudeLogin(_ context.Context, env *DoctorEnv) DoctorResult {
	if env.Config == nil {
		return noConfig
	}
	if !env.Config.Sandbox.Enabled {
		return DoctorResult{Status: DoctorOK, Detail: "the sandbox is off, so brains use the login you made with `claude`"}
	}
	for _, name := range brainLoginVars {
		if env.Getenv(name) != "" {
			return DoctorResult{Status: DoctorOK, Detail: name + " is set in this shell"}
		}
	}
	for _, file := range env.serviceFiles() {
		data, err := env.ReadFile(file)
		if err != nil {
			continue
		}
		for _, name := range brainLoginVars {
			if serviceSetsVar(data, name) {
				return DoctorResult{Status: DoctorOK, Detail: name + " is set in " + file}
			}
		}
	}
	return DoctorResult{
		Status: DoctorFail,
		Detail: "the sandbox is on, and neither this shell nor the service sets CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY: brains fail with \"Not logged in\"",
		Fix:    "claude setup-token, then set CLAUDE_CODE_OAUTH_TOKEN in the service's environment (docs/CONFIGURATION.md, \"Where to set them\")",
	}
}

// serviceFiles are the files that hold the service's environment: the
// launchd agent on macOS, the systemd unit and its drop-ins on Linux.
func (env *DoctorEnv) serviceFiles() []string {
	switch env.GOOS {
	case "darwin":
		return []string{filepath.Join(env.Home, "Library", "LaunchAgents", "ai.fridolin.bridge.plist")}
	case "linux":
		dir := filepath.Join(env.Home, ".config", "systemd", "user")
		files := []string{filepath.Join(dir, "fridolin.service")}
		dropins, _ := env.Glob(filepath.Join(dir, "fridolin.service.d", "*.conf"))
		return append(files, dropins...)
	}
	return nil
}

// serviceSetsVar reports whether a launchd plist or a systemd unit sets
// the variable name.
func serviceSetsVar(data []byte, name string) bool {
	s := string(data)
	return strings.Contains(s, "<key>"+name+"</key>") ||
		strings.Contains(s, "Environment="+name+"=") ||
		strings.Contains(s, "Environment=\""+name+"=")
}

func checkCodexCLI(ctx context.Context, env *DoctorEnv) DoctorResult {
	path := env.Config.CodexPath
	resolved, err := env.LookPath(path)
	if err != nil {
		return DoctorResult{Status: DoctorFail, Detail: path + " not found, and AI mode is codex, so brains cannot run", Fix: "npm install -g @openai/codex"}
	}
	out, err := env.Run(ctx, resolved, "--version")
	version := firstLine(out)
	if err != nil || version == "" {
		return DoctorResult{Status: DoctorWarn, Detail: resolved + ": `--version` gave no answer", Fix: "npm install -g @openai/codex"}
	}
	fields := strings.Fields(version)
	have, ok := claudecli.ParseClaudeVersion(fields[len(fields)-1])
	want, _ := claudecli.ParseClaudeVersion(minCodexVersion)
	if ok && versionLess(have, want) {
		return DoctorResult{Status: DoctorWarn, Detail: fmt.Sprintf("%s (%s), older than %s", resolved, version, minCodexVersion), Fix: "npm install -g @openai/codex"}
	}
	return DoctorResult{Status: DoctorOK, Detail: fmt.Sprintf("%s (%s)", resolved, version)}
}

// checkAgentPath checks that the AI mode's CLI setting resolves, on PATH,
// to the absolute path of an executable, which is what a run starts
// (resolveAgentBinary) and what the sandboxes need: bubblewrap refuses a
// bare name. exec.LookPath finds executables only.
func checkAgentPath(_ context.Context, env *DoctorEnv) DoctorResult {
	key, path := "claude_path", "claude"
	if env.Config != nil {
		path = env.Config.ClaudePath
		if codexMode(env) {
			key, path = "codex_path", env.Config.CodexPath
		}
	}
	fix := "set " + key + " in " + env.ConfigPath + " to the CLI's absolute path"
	resolved, err := env.LookPath(path)
	if err != nil {
		return DoctorResult{Status: DoctorFail, Detail: fmt.Sprintf("%s %q is no executable on PATH, so every run is refused", key, path), Fix: fix}
	}
	if !filepath.IsAbs(resolved) {
		return DoctorResult{Status: DoctorFail, Detail: fmt.Sprintf("%s %q resolves to %s, not an absolute path, which the sandbox refuses", key, path, resolved), Fix: fix}
	}
	if resolved == path {
		return DoctorResult{Status: DoctorOK, Detail: fmt.Sprintf("%s %s", key, resolved)}
	}
	return DoctorResult{Status: DoctorOK, Detail: fmt.Sprintf("%s %q runs as %s", key, path, resolved)}
}

func versionLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// tailscaleAppCLI is the CLI inside the Mac App Store and standalone
// Tailscale apps, which do not put one on PATH.
const tailscaleAppCLI = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"

// tailscaleWindowsCLI is where Tailscale's installer puts its CLI on
// Windows, for a PATH that does not have it yet (a shell opened before).
const tailscaleWindowsCLI = `C:\Program Files\Tailscale\tailscale.exe`

// TailscaleCheck is the check of the host's Tailscale, for other roles
// of a machine than the bridge's (`fridolin doctor --node`).
func TailscaleCheck(required bool) DoctorCheck {
	return DoctorCheck{ID: "tailscale", Title: "Tailscale", Required: required, Run: checkTailscale}
}

// DockerCheck is the check of Docker, for `fridolin setup-machine --core`.
func DockerCheck() DoctorCheck {
	return DoctorCheck{ID: "docker", Title: "Docker", Required: true, Run: checkDocker}
}

func checkTailscale(ctx context.Context, env *DoctorEnv) DoctorResult {
	cli, err := env.LookPath("tailscale")
	if err != nil {
		for goos, p := range map[string]string{"darwin": tailscaleAppCLI, "windows": tailscaleWindowsCLI} {
			if env.GOOS != goos {
				continue
			}
			if _, serr := env.Stat(p); serr == nil {
				cli, err = p, nil
			}
		}
	}
	if err != nil {
		fix := installFix(env.GOOS, "--cask tailscale", "")
		switch env.GOOS {
		case "linux":
			fix = "curl -fsSL https://tailscale.com/install.sh | sh"
		case "windows":
			fix = "winget install --id Tailscale.Tailscale -e"
		}
		return DoctorResult{Status: DoctorMissing, Detail: "the tailscale CLI is not installed", Fix: fix}
	}
	out, err := env.Run(ctx, cli, "status", "--json")
	var st struct{ BackendState string }
	// tailscale status exits 1 when it is stopped, and still says why.
	if jerr := json.Unmarshal(out, &st); jerr != nil || st.BackendState == "" {
		detail := cli + ": `status --json` gave no state"
		if err != nil {
			detail = fmt.Sprintf("%s: `status --json` failed: %v", cli, err)
		}
		return DoctorResult{Status: DoctorWarn, Detail: detail, Fix: "start Tailscale, then: tailscale up"}
	}
	if st.BackendState != "Running" {
		return DoctorResult{Status: DoctorWarn, Detail: cli + " is installed, but its state is " + st.BackendState, Fix: "tailscale up"}
	}
	return DoctorResult{Status: DoctorOK, Detail: cli + ", running"}
}

func checkDocker(ctx context.Context, env *DoctorEnv) DoctorResult {
	cli, err := env.LookPath("docker")
	if err != nil {
		fix := installFix(env.GOOS, "--cask orbstack", "docker.io")
		if env.GOOS == "windows" {
			fix = "wsl --install (as administrator, then restart), then: winget install --id Docker.DockerDesktop -e"
		}
		return DoctorResult{Status: DoctorMissing, Detail: "the docker CLI is not installed", Fix: fix}
	}
	out, err := env.Run(ctx, cli, "info", "--format", "{{.ServerVersion}}")
	version := firstLine(out)
	if err != nil || version == "" {
		fix := "start Docker Desktop or OrbStack (open -a OrbStack)"
		switch env.GOOS {
		case "linux":
			fix = "sudo systemctl start docker"
		case "windows":
			fix = `start Docker Desktop (Start-Process "$env:ProgramFiles\Docker\Docker\Docker Desktop.exe") and finish its first run`
		}
		return DoctorResult{Status: DoctorWarn, Detail: cli + " is installed, but the daemon does not answer", Fix: fix}
	}
	return DoctorResult{Status: DoctorOK, Detail: fmt.Sprintf("%s, daemon %s", cli, version)}
}

// toolCheck is the check of a program on PATH: missing with the command
// that installs it, else ok with its version.
func toolCheck(name, needed string, versionArgs []string, brew, apt string) DoctorCheck {
	return DoctorCheck{
		ID: name, Title: name, Needed: needed,
		Run: func(ctx context.Context, env *DoctorEnv) DoctorResult {
			cli, err := env.LookPath(name)
			if err != nil {
				fix := installFix(env.GOOS, brew, apt)
				if fix == "" {
					fix = "install " + name + " and put it on PATH"
				}
				return DoctorResult{Status: DoctorMissing, Detail: name + " is not installed", Fix: fix}
			}
			out, err := env.Run(ctx, cli, versionArgs...)
			if v := firstLine(out); err == nil && v != "" {
				return DoctorResult{Status: DoctorOK, Detail: fmt.Sprintf("%s (%s)", cli, v)}
			}
			return DoctorResult{Status: DoctorOK, Detail: cli}
		},
	}
}

// checkSecrets is the check that each of the host secrets is there:
// a Keychain item (account fridolin) on macOS, a 0600 file under
// $XDG_CONFIG_HOME/fridolin/secrets/ on Linux. It never reads a value:
// `security find-generic-password` without -w or -g prints the item's
// attributes only, and those are dropped.
func checkSecrets(names ...string) func(context.Context, *DoctorEnv) DoctorResult {
	return func(ctx context.Context, env *DoctorEnv) DoctorResult {
		var missing, loose []string
		for _, name := range names {
			switch ok, err := env.hasSecret(ctx, name); {
			case err != nil:
				return DoctorResult{Status: DoctorWarn, Detail: fmt.Sprintf("cannot look for %s: %v", name, err), Fix: env.secretsAccessFix()}
			case !ok:
				missing = append(missing, name)
			case env.GOOS != "darwin" && env.GOOS != "windows":
				if st, err := env.Stat(env.secretFile(name)); err == nil && st.Mode().Perm()&0o077 != 0 {
					loose = append(loose, env.secretFile(name))
				}
			}
		}
		switch {
		case len(missing) > 0:
			return DoctorResult{Status: DoctorMissing, Detail: "not in " + env.secretsPlace() + ": " + strings.Join(missing, ", "), Fix: env.addSecretFix(missing)}
		case len(loose) > 0:
			return DoctorResult{Status: DoctorWarn, Detail: "readable by others: " + strings.Join(loose, ", "), Fix: "chmod 600 " + strings.Join(loose, " ")}
		}
		return DoctorResult{Status: DoctorOK, Detail: strings.Join(names, ", ") + " in " + env.secretsPlace()}
	}
}

func (env *DoctorEnv) secretsPlace() string {
	switch env.GOOS {
	case "darwin":
		return "the Keychain"
	case "windows":
		return "the Windows Credential Manager"
	}
	return env.secretsDir()
}

// secretsDir is where host secrets live off macOS.
func (env *DoctorEnv) secretsDir() string {
	base := env.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(env.Home, ".config")
	}
	return filepath.Join(base, "fridolin", "secrets")
}

func (env *DoctorEnv) secretFile(name string) string {
	return filepath.Join(env.secretsDir(), name)
}

// hasSecret reports whether the host secret name is there, without
// reading it.
func (env *DoctorEnv) hasSecret(ctx context.Context, name string) (bool, error) {
	switch env.GOOS {
	case "darwin":
		_, err := env.Run(ctx, "/usr/bin/security", "find-generic-password", "-a", hostSecretAccount, "-s", name)
		var exit interface{ ExitCode() int }
		switch {
		case err == nil:
			return true, nil
		case errors.As(err, &exit) && exit.ExitCode() == 44: // errSecItemNotFound
			return false, nil
		}
		return false, err
	case "windows":
		if env.HasHostSecret == nil {
			return false, errors.New("no access to the Windows Credential Manager")
		}
		return env.HasHostSecret(name)
	}
	_, err := env.Stat(env.secretFile(name))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// secretsAccessFix is what to try when the host secrets cannot be asked.
func (env *DoctorEnv) secretsAccessFix() string {
	switch env.GOOS {
	case "darwin":
		return "security unlock-keychain, in a terminal of the user the bridge runs as"
	case "windows":
		return "run fridolin core secrets in a PowerShell of the user the core runs as"
	}
	return "ls -ld " + env.secretsDir()
}

// addSecretFix is the command that adds the missing secrets: security
// prompts for each value, so it never lands in the shell's history.
func (env *DoctorEnv) addSecretFix(missing []string) string {
	var cmds []string
	for _, name := range missing {
		switch env.GOOS {
		case "darwin":
			cmds = append(cmds, "security add-generic-password -a "+hostSecretAccount+" -s "+name+" -w")
		case "windows":
			cmds = append(cmds, "fridolin core secrets set "+name)
		default:
			cmds = append(cmds, "(umask 077; mkdir -p "+env.secretsDir()+" && cat > "+env.secretFile(name)+")")
		}
	}
	return strings.Join(cmds, "; ")
}

// memoryBrains are the brains whose approved brain.json uses the memory
// index (memory.inject or memory.consolidate), by id, with their folders.
func memoryBrains(env *DoctorEnv) map[string]string {
	out := map[string]string{}
	if env.Config == nil {
		return out
	}
	for _, ref := range env.Config.BrainDirs {
		if ref.Approved == nil {
			continue
		}
		if mem := manifest.MemoryOf(*ref.Approved); mem.Inject || mem.Consolidate != "" {
			out[ref.Approved.ID] = ref.Dir(env.Home)
		}
	}
	return out
}

func memoryEnabled(env *DoctorEnv) bool { return len(memoryBrains(env)) > 0 }

// checkMemoryIndex is M7's gate: the provenance rule holds on this build
// (an untrusted fact is never recalled, the owner's is, with its anchor),
// and each brain that uses the index has one that holds its files as they
// are. The bridge syncs it before every run that recalls and every 30
// seconds, so a stale one means the bridge is not running or cannot read
// the folder.
func checkMemoryIndex(ctx context.Context, env *DoctorEnv) DoctorResult {
	if env.MemorySelfTest != nil {
		if err := env.MemorySelfTest(ctx); err != nil {
			return DoctorResult{Status: DoctorFail, Detail: "the provenance gate failed on this build: " + err.Error(), Fix: "report it: untrusted notes could reach a prompt; turn memory.inject off meanwhile"}
		}
	}
	brains := memoryBrains(env)
	ids := slices.Sorted(maps.Keys(brains))
	var notes []string
	status, fix := DoctorOK, ""
	for _, id := range ids {
		b := memoryBrain(env.Config.Layout(), env.Config, id, brains[id])
		stale, err := env.MemoryStale(ctx, b)
		switch {
		case err != nil:
			status, fix = DoctorWarn, "fridolin memory reindex --brain "+id
			notes = append(notes, id+": no index yet")
		case len(stale) > 0:
			status, fix = DoctorWarn, "fridolin memory reindex --brain "+id+" (or start the bridge, which keeps it up to date)"
			notes = append(notes, fmt.Sprintf("%s: %d files changed since it was indexed", id, len(stale)))
		default:
			notes = append(notes, id+": up to date")
		}
	}
	return DoctorResult{Status: status, Detail: "gate ok; " + strings.Join(notes, "; "), Fix: fix}
}
