package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
)

// trySandboxScript runs in a brain's sandbox: it names its user and HOME,
// then, for each "want:path" argument, says whether the path exists for
// the run and whether that is what want ("visible" or "hidden") says.
const trySandboxScript = `echo "user: $(id -un 2>/dev/null || id -u) ($(id -u))"
echo "HOME: $HOME"
for t in claude codex plaud git python3 node gh cloudflared ffmpeg; do
	if command -v "$t" >/dev/null 2>&1; then echo "  tool $t: $(command -v "$t")"; else echo "  tool $t: not on PATH"; fi
done
bad=0
for arg in "$@"; do
	want=${arg%%:*}; p=${arg#*:}
	if [ -e "$p" ]; then got=visible; else got=hidden; fi
	if [ "$got" = "$want" ]; then echo "  ok   $got: $p"; else echo "  FAIL $got (want $want): $p"; bad=1; fi
done
exit $bad
`

// TrySandbox runs a shell as brain id in its sandbox, the way the bridge
// runs the brain's agent, and writes what it sees of the places that
// matter: its own folders (visible), and the other brains' folders, the
// database, the config file, the credentials folder and the data dir's
// secrets (hidden; those that exist outside the run only). It spends no
// agent's tokens: the program is /bin/sh.
func TrySandbox(w io.Writer, sp *SandboxProfiler, cfg *config.Settings, workDirs map[string][]string, id string) error {
	if !sp.Enabled() {
		return fmt.Errorf("sandbox.enabled is off: brain %s runs unsandboxed", id)
	}
	if _, err := prepareBrainState(AbsPath(cfg.DataDir), AbsPath(cfg.MediaDir), id, cfg.InheritClaudeUserConfig); err != nil {
		return err
	}
	var checks []string
	add := func(want, p string) {
		if p == "" {
			return
		}
		if _, err := os.Stat(p); err != nil {
			return
		}
		c := want + ":" + filepath.Clean(p)
		if !slices.Contains(checks, c) {
			checks = append(checks, c)
		}
	}
	for _, d := range workDirs[id] {
		add("visible", d)
	}
	for _, a := range cfg.Sandbox.AllowPaths {
		add("visible", AbsPath(a))
	}
	for other, dirs := range workDirs {
		if other != id {
			for _, d := range dirs {
				add("hidden", d)
			}
		}
	}
	l := config.Layout{Root: AbsPath(cfg.DataDir)}
	add("hidden", AbsPath(cfg.DBPath))
	add("hidden", l.Secrets())
	add("hidden", l.DashboardToken())
	add("hidden", l.ScriptsToken())
	if cfg.Sandbox.CredentialsDir != "" {
		add("hidden", AbsPath(cfg.Sandbox.CredentialsDir))
	}
	sp.mu.RLock()
	add("hidden", sp.configFile)
	if sp.hostHome != "" {
		// The links the image's entry point makes for scripts that name
		// ~/.local/bin/<tool> (container/entrypoint.sh).
		add("visible", filepath.Join(sp.hostHome, ".local", "bin"))
	}
	sp.mu.RUnlock()
	slices.Sort(checks)
	prog, args, err := sp.Command(id, "/bin/sh", append([]string{"-c", trySandboxScript, "sh"}, checks...))
	if err != nil {
		return fmt.Errorf("sandbox of brain %s: %w", id, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, prog, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr := cmd.Run()
	fmt.Fprintf(w, "\nA shell in brain %s's sandbox:\n%s", id, out.String())
	if runErr != nil {
		return fmt.Errorf("brain %s's sandbox: %w", id, runErr)
	}
	return nil
}

// TrySandboxCommand runs prog with args as brain id in its sandbox, with
// the environment a run of the brain has besides its tokens: its PATH (the
// brain's own folders first), its TMPDIR and caches, FRIDOLIN_TOOLS_DIR,
// and the bridge's TZ and LANG. What it writes goes to w; a program that fails returns its
// *exec.ExitError. It is how a tool a brain runs (a transcription, a
// calendar script) is tried where the brain runs it.
func TrySandboxCommand(w io.Writer, sp *SandboxProfiler, cfg *config.Settings, id string, command []string) error {
	if !sp.Enabled() {
		return fmt.Errorf("sandbox.enabled is off: brain %s runs unsandboxed", id)
	}
	stateEnv, err := prepareBrainState(AbsPath(cfg.DataDir), AbsPath(cfg.MediaDir), id, cfg.InheritClaudeUserConfig)
	if err != nil {
		return err
	}
	dirs := append(slices.Clone(SetBridgePATH(cfg)[id]), filepath.SplitList(os.Getenv("PATH"))...)
	path := strings.Join(dirs, string(filepath.ListSeparator))
	prog := command[0]
	if !filepath.IsAbs(prog) {
		if prog = lookPathIn(command[0], path); prog == "" {
			return fmt.Errorf("%s is not on brain %s's PATH (%s)", command[0], id, path)
		}
	}
	bin, args, err := sp.Command(id, prog, command[1:])
	if err != nil {
		return fmt.Errorf("sandbox of brain %s: %w", id, err)
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = append([]string{"PATH=" + path, "FRIDOLIN_BRAIN_ID=" + id}, stateEnv...)
	if tools := ownLayout().Tools; tools != "" {
		cmd.Env = append(cmd.Env, "FRIDOLIN_TOOLS_DIR="+tools)
	}
	for _, name := range []string{"TZ", "LANG"} {
		if v, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+v)
		}
	}
	cmd.Stdout, cmd.Stderr = w, w
	return cmd.Run()
}
