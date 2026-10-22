package nodeagent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// A workstation session (agent or terminal) runs inside the platform's
// sandbox where there is one, which keeps its writes in its folder, its
// temp folder and the provider's own state, and keeps it from reading
// deniedPaths: sandbox-exec with a profile of its own on macOS, bubblewrap
// on Linux. Windows has none: there a Claude session runs with Claude
// Code's own permission checks, and what they refuse is relayed to the
// owner (workstation_agent.go); a terminal is an ordinary shell of the
// agent's user (docs/NODES.md, "Workstation").

const sandboxExecPath = "/usr/bin/sandbox-exec"

// detectSandbox is the sandbox this machine offers: sandbox-exec on
// macOS, bwrap on Linux when it runs, none otherwise.
func detectSandbox() string {
	switch runtime.GOOS {
	case "darwin":
		if _, err := os.Stat(sandboxExecPath); err == nil {
			return nodeproto.SandboxExec
		}
	case "linux":
		if p, err := platform.LookTool("bwrap"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if exec.CommandContext(ctx, p, "--ro-bind", "/", "/", "--dev", "/dev", "--", "true").Run() == nil {
				return nodeproto.SandboxBwrap
			}
		}
	}
	return nodeproto.SandboxNone
}

// providerState are the home-relative places a provider writes its own
// state to (its login, sessions, settings): open to its sessions.
var providerState = map[string][]string{
	nodeproto.ProviderClaude: {".claude", ".claude.json", ".config/claude", "Library/Caches/claude-cli-nodejs", ".cache/claude-cli-nodejs", ".cache/claude"},
	nodeproto.ProviderCodex:  {".codex"},
}

// sandboxPlan is what a session may write besides its folder.
type sandboxPlan struct {
	folder string
	tmp    string
	home   string
	// writable are further places to write: the provider's state.
	writable []string
	// denied may not be read.
	denied []string
}

func newSandboxPlan(folder, tmp, stateDir, provider string) sandboxPlan {
	p := sandboxPlan{folder: folder, tmp: tmp, denied: deniedPaths(stateDir)}
	if home, err := homeDir(); err == nil {
		p.home = home
		for _, s := range providerState[provider] {
			p.writable = append(p.writable, filepath.Join(home, filepath.FromSlash(s)))
		}
		// The keychains hold the provider's login on macOS (Claude Code
		// keeps its OAuth token there and refreshes it), so they stay
		// readable and writable to a session, as to the owner's own.
		if runtime.GOOS == "darwin" {
			p.writable = append(p.writable, filepath.Join(home, "Library", "Keychains"))
		}
	}
	return p
}

// sbQuote quotes s for a sandbox profile.
func sbQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// pathForms are p and its real path when that differs (/var and
// /private/var on macOS).
func pathForms(p string) []string {
	out := []string{filepath.Clean(p)}
	if r, err := realPath(p); err == nil && r != out[0] {
		out = append(out, r)
	}
	return out
}

// darwinProfile is the sandbox-exec profile of a session: everything is
// allowed but writing outside its places and reading the denied ones.
func (p sandboxPlan) darwinProfile() string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	b.WriteString("(deny file-write* (subpath \"/\"))\n")
	for _, d := range []string{"/dev/null", "/dev/zero", "/dev/tty", "/dev/dtracehelper", "/dev/urandom", "/dev/random"} {
		fmt.Fprintf(&b, "(allow file-write* (literal %s))\n", sbQuote(d))
	}
	b.WriteString("(allow file-write* (regex #\"^/dev/(ttys[0-9]+|ptmx|fd/[0-9]+)$\"))\n")
	for _, w := range append([]string{p.folder, p.tmp}, p.writable...) {
		for _, f := range pathForms(w) {
			fmt.Fprintf(&b, "(allow file-write* (subpath %s))\n", sbQuote(f))
		}
	}
	if p.home != "" {
		// Claude Code rewrites ~/.claude.json through a temp file and a
		// lock beside it.
		for _, h := range pathForms(p.home) {
			fmt.Fprintf(&b, "(allow file-write* (regex #\"^%s/\\.claude\\.json[^/]*$\"))\n", regexp.QuoteMeta(h))
		}
	}
	// Claude Code's Bash tool tracks the shell's folder in a file of its
	// own in /tmp, whatever TMPDIR says (found in the first real try on a
	// Mac, #159): only those files, by their name.
	b.WriteString("(allow file-write* (regex #\"^/private/tmp/claude-[0-9a-f]+-cwd$\"))\n")
	for _, d := range p.denied {
		for _, f := range pathForms(d) {
			fmt.Fprintf(&b, "(deny file-read* file-write* (subpath %s))\n", sbQuote(f))
		}
	}
	// Starting code outside the sandbox: launchd, LaunchServices, Apple
	// events.
	b.WriteString("(deny appleevent-send)\n(deny lsopen)\n(deny user-preference-write)\n")
	return b.String()
}

// bwrapArgs are bubblewrap's arguments for a session: the whole file
// system read-only, its places writable, the denied ones hidden.
func (p sandboxPlan) bwrapArgs() []string {
	args := []string{"--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp", "--die-with-parent", "--new-session"}
	// The folder (confinedWorkFolder checked it is a folder under its
	// root) and the session's temp folder exist; the provider's state
	// places, all below the home folder, are bound where they exist.
	args = append(args, "--bind", p.folder, p.folder, "--bind", p.tmp, p.tmp)
	for _, w := range p.writable {
		if _, err := os.Stat(w); err == nil {
			args = append(args, "--bind", w, w)
		}
	}
	for _, d := range p.denied {
		if st, err := os.Stat(d); err == nil {
			if st.IsDir() {
				args = append(args, "--tmpfs", d)
			} else {
				args = append(args, "--ro-bind", "/dev/null", d)
			}
		}
	}
	return append(args, "--chdir", p.folder, "--")
}

// wrap is prog with args run in the sandbox: the program to start and
// its arguments. prog is always a program the node resolved itself (the
// provider's CLI, git, the shell), never a name from a request; what a
// request brings (the folder, a model) only ever lands in args, as whole
// arguments, never in a shell string. A sandbox it cannot start is an
// error, never prog unconfined: a session in one runs without the
// provider's own permission checks.
func (p sandboxPlan) wrap(sandbox, prog string, args []string) (string, []string, error) {
	if !filepath.IsAbs(prog) {
		return "", nil, fmt.Errorf("%q is not a program the node resolved", prog)
	}
	switch sandbox {
	case nodeproto.SandboxExec:
		return sandboxExecPath, append([]string{"-p", p.darwinProfile(), prog}, args...), nil
	case nodeproto.SandboxBwrap:
		bw, err := platform.LookTool("bwrap")
		if err != nil {
			return "", nil, fmt.Errorf("the sandbox (bwrap) is gone: %w", err)
		}
		return bw, append(append(p.bwrapArgs(), prog), args...), nil
	case nodeproto.SandboxNone:
		return prog, args, nil
	}
	return "", nil, fmt.Errorf("no sandbox %q", sandbox)
}
