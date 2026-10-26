package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/Icewater1337/fridolin/internal/claudecli"
)

// SetupChecks are the checks of the machine and of the config the bridge
// runs with, in the order doctor prints them: the config, the data dir
// and its disk, the AI mode's CLI and the login of sandboxed brains, and
// what the host needs (Tailscale, the host secrets, Docker, the tools).
// loginVars are the variables of the bridge's environment that log a
// sandboxed brain's Claude CLI in.
func SetupChecks(loginVars []string) []Check {
	return []Check{
		{ID: "config", Title: "config loads", Required: true, Run: checkConfigLoads},
		{ID: "data-dir", Title: "data dir writable", Required: true, Run: checkDataDirWritable},
		{ID: "disk", Title: "free disk in the data dir", Required: true, Run: checkDiskFree},
		{ID: "claude", Title: "Claude CLI", Required: true, Applies: claudeMode, Run: checkClaudeCLI},
		{ID: "claude-login", Title: "Claude login for sandboxed brains", Required: true, Applies: claudeMode, Run: checkClaudeLogin(loginVars)},
		{ID: "codex", Title: "Codex CLI", Required: true, Applies: codexMode, Run: checkCodexCLI},
		{ID: "agent-path", Title: "agent CLI by absolute path", Required: true, Run: checkAgentPath},
		{ID: "tailscale", Title: "Tailscale", Needed: "M1", Applies: OnHost, Run: checkTailscale},
		{ID: "tailscale-authkey", Title: "Tailscale auth key in host secrets", Needed: "M1", Applies: OnHost, Run: checkSecrets(tailscaleAuthKeySecret)},
		{ID: "b2-keys", Title: "Backblaze B2 keys in host secrets", Needed: "M3", Applies: OnHost, Run: checkSecrets(b2Secrets...)},
		{ID: "docker", Title: "Docker", Needed: "M1", Applies: OnHost, Run: checkDocker},
		toolCheck("restic", "M3", []string{"version"}, "restic", "restic"),
		toolCheck("rclone", "M3", []string{"version"}, "rclone", "rclone"),
		toolCheck("ffmpeg", "", []string{"-version"}, "ffmpeg", "ffmpeg"),
		toolCheck("litestream", "M3", []string{"version"}, "benbjohnson/litestream/litestream", ""),
		toolCheck("age", "M1", []string{"--version"}, "age", "age"),
	}
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

func claudeMode(env *Env) bool {
	return env.Config == nil || env.Config.AgentProvider != "codex"
}

func codexMode(env *Env) bool {
	return env.Config != nil && env.Config.AgentProvider == "codex"
}

func checkConfigLoads(_ context.Context, env *Env) Result {
	if env.ConfigErr != nil {
		return Result{Status: Fail, Detail: env.ConfigErr.Error(), Fix: "fridolin check --config " + env.ConfigPath + " (or fridolin init to write a starter config)"}
	}
	return Result{Status: OK, Detail: env.ConfigPath}
}

func checkDataDirWritable(_ context.Context, env *Env) Result {
	if env.Config == nil {
		return NoConfig
	}
	dir := env.Config.DataDir
	at, exists := env.existingDir(dir)
	if err := env.WriteProbe(at); err != nil {
		fix := "chmod u+w " + at
		if !exists {
			fix = "mkdir -p " + dir
		}
		return Result{Status: Fail, Detail: fmt.Sprintf("cannot write in %s: %v", at, err), Fix: fix}
	}
	if !exists {
		return Result{Status: OK, Detail: dir + " does not exist yet; the bridge creates it in " + at}
	}
	return Result{Status: OK, Detail: dir}
}

func checkDiskFree(_ context.Context, env *Env) Result {
	if env.Config == nil {
		return NoConfig
	}
	at, _ := env.existingDir(env.Config.DataDir)
	free, err := env.FreeBytes(at)
	if err != nil {
		return Result{Status: Warn, Detail: fmt.Sprintf("cannot tell the free space at %s: %v", at, err), Fix: "df -h " + at}
	}
	detail := fmt.Sprintf("%s free at %s", formatGB(free), at)
	switch {
	case free < diskFailBytes:
		return Result{Status: Fail, Detail: detail + ", under " + formatGB(diskFailBytes), Fix: "free up space on that disk, or move data_dir to a larger one"}
	case free < diskWarnBytes:
		return Result{Status: Warn, Detail: detail + ", under " + formatGB(diskWarnBytes), Fix: "free up space on that disk, or move data_dir to a larger one"}
	}
	return Result{Status: OK, Detail: detail}
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

// InstallFix is the command that installs a tool on goos: brew on macOS,
// apt on Linux; "" where none is known.
func InstallFix(goos, brew, apt string) string {
	switch {
	case goos == "darwin" && brew != "":
		return "brew install " + brew
	case goos == "linux" && apt != "":
		return "sudo apt install " + apt
	}
	return ""
}

func checkClaudeCLI(ctx context.Context, env *Env) Result {
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
		return Result{Status: Fail, Detail: path + " not found, so brains cannot run", Fix: fix}
	}
	out, err := env.Run(ctx, resolved, "--version")
	st := claudecli.Status{Path: resolved, Found: true, Version: firstLine(out)}
	if err != nil || st.Version == "" {
		return Result{Status: Warn, Detail: resolved + ": `--version` gave no answer", Fix: "claude update"}
	}
	if claudecli.VersionWarning(st) != "" {
		return Result{Status: Warn, Detail: fmt.Sprintf("%s (%s), older than %s, the oldest release tested", resolved, st.Version, claudecli.TestedClaudeVersion), Fix: "claude update"}
	}
	return Result{Status: OK, Detail: fmt.Sprintf("%s (%s)", resolved, st.Version)}
}

// checkClaudeLogin looks for the login sandboxed brains need, which cannot
// be the keychain one: one of loginVars, in this shell's environment or
// in the service's (the launchd agent or the systemd unit and its
// drop-ins). It looks for the names only, never at the values.
func checkClaudeLogin(loginVars []string) func(context.Context, *Env) Result {
	return func(_ context.Context, env *Env) Result {
		if env.Config == nil {
			return NoConfig
		}
		if !env.Config.Sandbox.Enabled {
			return Result{Status: OK, Detail: "the sandbox is off, so brains use the login you made with `claude`"}
		}
		for _, name := range loginVars {
			if env.Getenv(name) != "" {
				return Result{Status: OK, Detail: name + " is set in this shell"}
			}
		}
		for _, file := range env.serviceFiles() {
			data, err := env.ReadFile(file)
			if err != nil {
				continue
			}
			for _, name := range loginVars {
				if serviceSetsVar(data, name) {
					return Result{Status: OK, Detail: name + " is set in " + file}
				}
			}
		}
		return Result{
			Status: Fail,
			Detail: "the sandbox is on, and neither this shell nor the service sets CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY: brains fail with \"Not logged in\"",
			Fix:    "claude setup-token, then set CLAUDE_CODE_OAUTH_TOKEN in the service's environment (docs/CONFIGURATION.md, \"Where to set them\")",
		}
	}
}

// serviceFiles are the files that hold the service's environment: the
// launchd agent on macOS, the systemd unit and its drop-ins on Linux.
func (env *Env) serviceFiles() []string {
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

func checkCodexCLI(ctx context.Context, env *Env) Result {
	path := env.Config.CodexPath
	resolved, err := env.LookPath(path)
	if err != nil {
		return Result{Status: Fail, Detail: path + " not found, and AI mode is codex, so brains cannot run", Fix: "npm install -g @openai/codex"}
	}
	out, err := env.Run(ctx, resolved, "--version")
	version := firstLine(out)
	if err != nil || version == "" {
		return Result{Status: Warn, Detail: resolved + ": `--version` gave no answer", Fix: "npm install -g @openai/codex"}
	}
	fields := strings.Fields(version)
	have, ok := claudecli.ParseClaudeVersion(fields[len(fields)-1])
	want, _ := claudecli.ParseClaudeVersion(minCodexVersion)
	if ok && versionLess(have, want) {
		return Result{Status: Warn, Detail: fmt.Sprintf("%s (%s), older than %s", resolved, version, minCodexVersion), Fix: "npm install -g @openai/codex"}
	}
	return Result{Status: OK, Detail: fmt.Sprintf("%s (%s)", resolved, version)}
}

// checkAgentPath checks that the AI mode's CLI setting resolves, on PATH,
// to the absolute path of an executable, which is what a run starts
// (internal/app's resolveAgentBinary) and what the sandboxes need:
// bubblewrap refuses a bare name. exec.LookPath finds executables only.
func checkAgentPath(_ context.Context, env *Env) Result {
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
		return Result{Status: Fail, Detail: fmt.Sprintf("%s %q is no executable on PATH, so every run is refused", key, path), Fix: fix}
	}
	if !filepath.IsAbs(resolved) {
		return Result{Status: Fail, Detail: fmt.Sprintf("%s %q resolves to %s, not an absolute path, which the sandbox refuses", key, path, resolved), Fix: fix}
	}
	if resolved == path {
		return Result{Status: OK, Detail: fmt.Sprintf("%s %s", key, resolved)}
	}
	return Result{Status: OK, Detail: fmt.Sprintf("%s %q runs as %s", key, path, resolved)}
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
func TailscaleCheck(required bool) Check {
	return Check{ID: "tailscale", Title: "Tailscale", Required: required, Run: checkTailscale}
}

// DockerCheck is the check of Docker, for `fridolin setup-machine --core`.
func DockerCheck() Check {
	return Check{ID: "docker", Title: "Docker", Required: true, Run: checkDocker}
}

func checkTailscale(ctx context.Context, env *Env) Result {
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
		fix := InstallFix(env.GOOS, "--cask tailscale", "")
		switch env.GOOS {
		case "linux":
			fix = "curl -fsSL https://tailscale.com/install.sh | sh"
		case "windows":
			fix = "winget install --id Tailscale.Tailscale -e"
		}
		return Result{Status: Missing, Detail: "the tailscale CLI is not installed", Fix: fix}
	}
	out, err := env.Run(ctx, cli, "status", "--json")
	var st struct{ BackendState string }
	// tailscale status exits 1 when it is stopped, and still says why.
	if jerr := json.Unmarshal(out, &st); jerr != nil || st.BackendState == "" {
		detail := cli + ": `status --json` gave no state"
		if err != nil {
			detail = fmt.Sprintf("%s: `status --json` failed: %v", cli, err)
		}
		return Result{Status: Warn, Detail: detail, Fix: "start Tailscale, then: tailscale up"}
	}
	if st.BackendState != "Running" {
		return Result{Status: Warn, Detail: cli + " is installed, but its state is " + st.BackendState, Fix: "tailscale up"}
	}
	return Result{Status: OK, Detail: cli + ", running"}
}

func checkDocker(ctx context.Context, env *Env) Result {
	cli, err := env.LookPath("docker")
	if err != nil {
		fix := InstallFix(env.GOOS, "--cask orbstack", "docker.io")
		if env.GOOS == "windows" {
			fix = "wsl --install (as administrator, then restart), then: winget install --id Docker.DockerDesktop -e"
		}
		return Result{Status: Missing, Detail: "the docker CLI is not installed", Fix: fix}
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
		return Result{Status: Warn, Detail: cli + " is installed, but the daemon does not answer", Fix: fix}
	}
	return Result{Status: OK, Detail: fmt.Sprintf("%s, daemon %s", cli, version)}
}

// toolCheck is the check of a program on PATH: missing with the command
// that installs it, else ok with its version.
func toolCheck(name, needed string, versionArgs []string, brew, apt string) Check {
	return Check{
		ID: name, Title: name, Needed: needed,
		Run: func(ctx context.Context, env *Env) Result {
			cli, err := env.LookPath(name)
			if err != nil {
				fix := InstallFix(env.GOOS, brew, apt)
				if fix == "" {
					fix = "install " + name + " and put it on PATH"
				}
				return Result{Status: Missing, Detail: name + " is not installed", Fix: fix}
			}
			out, err := env.Run(ctx, cli, versionArgs...)
			if v := firstLine(out); err == nil && v != "" {
				return Result{Status: OK, Detail: fmt.Sprintf("%s (%s)", cli, v)}
			}
			return Result{Status: OK, Detail: cli}
		},
	}
}

// checkSecrets is the check that each of the host secrets is there:
// a Keychain item (account fridolin) on macOS, a 0600 file under
// $XDG_CONFIG_HOME/fridolin/secrets/ on Linux. It never reads a value:
// `security find-generic-password` without -w or -g prints the item's
// attributes only, and those are dropped.
func checkSecrets(names ...string) func(context.Context, *Env) Result {
	return func(ctx context.Context, env *Env) Result {
		var missing, loose []string
		for _, name := range names {
			switch ok, err := env.hasSecret(ctx, name); {
			case err != nil:
				return Result{Status: Warn, Detail: fmt.Sprintf("cannot look for %s: %v", name, err), Fix: env.secretsAccessFix()}
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
			return Result{Status: Missing, Detail: "not in " + env.secretsPlace() + ": " + strings.Join(missing, ", "), Fix: env.addSecretFix(missing)}
		case len(loose) > 0:
			return Result{Status: Warn, Detail: "readable by others: " + strings.Join(loose, ", "), Fix: "chmod 600 " + strings.Join(loose, " ")}
		}
		return Result{Status: OK, Detail: strings.Join(names, ", ") + " in " + env.secretsPlace()}
	}
}

func (env *Env) secretsPlace() string {
	switch env.GOOS {
	case "darwin":
		return "the Keychain"
	case "windows":
		return "the Windows Credential Manager"
	}
	return env.secretsDir()
}

// secretsDir is where host secrets live off macOS.
func (env *Env) secretsDir() string {
	base := env.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(env.Home, ".config")
	}
	return filepath.Join(base, "fridolin", "secrets")
}

func (env *Env) secretFile(name string) string {
	return filepath.Join(env.secretsDir(), name)
}

// hasSecret reports whether the host secret name is there, without
// reading it.
func (env *Env) hasSecret(ctx context.Context, name string) (bool, error) {
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
func (env *Env) secretsAccessFix() string {
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
func (env *Env) addSecretFix(missing []string) string {
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
