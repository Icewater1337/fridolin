package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// frideSandboxTimeout bounds a command: create builds a template and
// FrIDE, which the first time downloads a few GB.
func frideSandboxTimeout(sub string) time.Duration {
	switch sub {
	case "create":
		return 45 * time.Minute
	case "update":
		return 15 * time.Minute
	case "start":
		return 5 * time.Minute
	}
	return 2 * time.Minute
}

// runFrideSandbox runs a command of fridolin node fride sandbox with
// the machine's sbx.
func runFrideSandbox(sub string, pos []string, f frideSandboxFlags, stdout, stderr io.Writer) int {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		fmt.Fprintf(stderr, "fridolin node fride sandbox: Docker Sandboxes run on macOS (and Linux with KVM), not on %s\n", runtime.GOOS)
		return 1
	}
	dir := f.stateDir
	var err error
	if dir == "" {
		if dir, err = frideSandboxStateDir(); err != nil {
			fmt.Fprintf(stderr, "fridolin node fride sandbox: %v; pass --state-dir\n", err)
			return 1
		}
	}
	if dir, err = filepath.Abs(dir); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if sub == "join" {
		return frideSandboxJoin(pos, dir, f, stdout, stderr)
	}
	return frideSandbox(sub, f, dir, findSbx(), stdout, stderr)
}

// frideSandbox runs sub with the sbx at sbx ("" for none).
func frideSandbox(sub string, f frideSandboxFlags, dir, sbx string, stdout, stderr io.Writer) int {
	fail := func(format string, args ...any) int {
		fmt.Fprintf(stderr, "fridolin node fride sandbox "+sub+": "+format+"\n", args...)
		return 1
	}
	if sbx == "" {
		return fail("sbx is not installed: install Docker Sandboxes (on a Mac: brew install --cask sbx), run sbx login, then this again")
	}
	o := nodeagent.FrideSandboxOptions{Dir: dir, Sbx: sbx, Docker: findDocker(), Out: stdout,
		FrideRepo: f.frideRepo, GitName: f.gitName, GitEmail: f.gitEmail}
	if git, err := exec.LookPath("git"); err == nil {
		o.Git = git
	}
	if sub == "create" || sub == "update" {
		repo := f.repo
		if repo == "" {
			repo = "."
		}
		var err error
		if o.Repo, err = filepath.Abs(repo); err != nil {
			return fail("%v", err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, frideSandboxTimeout(sub))
	defer cancel()
	switch sub {
	case "create":
		return frideSandboxCreate(ctx, o, f, fail)
	case "update":
		if err := nodeagent.FrideSandboxUpdate(ctx, o); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintln(stdout, "Updated the sandboxed FrIDE.")
	case "login":
		return frideSandboxLogin(ctx, o, f, stdout, fail)
	case "start":
		info, err := nodeagent.FrideSandboxStart(ctx, o)
		if err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "FrIDE answers in the sandbox, paired with this node (repositories: %s).\nThe node offers fride to the core within half a minute. On the core, approve it once (!approve in your chat, the Nodes page, or):\n\n  fridolin nodes approve %s --commands\n\n", orNone(info.Repos), nodeID(dir))
	case "stop":
		if err := nodeagent.FrideSandboxStop(ctx, o); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintln(stdout, "Stopped the sandboxed FrIDE; the agent starts it again only after fridolin node fride sandbox start. The sandbox stops by itself 30 s later.")
	case "status":
		return frideSandboxStatus(ctx, o, stdout, fail)
	case "rm":
		return frideSandboxRemove(ctx, o, f, stdout, stderr, fail)
	}
	return 0
}

// frideSandboxCreate is create, with the default base folder.
func frideSandboxCreate(ctx context.Context, o nodeagent.FrideSandboxOptions, f frideSandboxFlags, fail func(string, ...any) int) int {
	if o.Docker == "" {
		return fail("docker is not installed: create builds the template with Docker (Docker Desktop or OrbStack)")
	}
	base := f.base
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fail("%v; pass --base", err)
		}
		base = filepath.Join(home, "fride-sandbox", "repos")
	}
	var err error
	if o.Base, err = filepath.Abs(platform.ExpandHome(base)); err != nil {
		return fail("%v", err)
	}
	if err := nodeagent.FrideSandboxCreate(ctx, o); err != nil {
		return fail("%v", err)
	}
	return 0
}

// frideSandboxRemove is rm: it asks first, unless --yes.
func frideSandboxRemove(ctx context.Context, o nodeagent.FrideSandboxOptions, f frideSandboxFlags, stdout, stderr io.Writer, fail func(string, ...any) int) int {
	if !f.yes {
		term := stdinTerminal()
		if term.readLine == nil {
			return fail("removing the sandbox removes its workspaces and any work in them that is not committed: pass --yes")
		}
		fmt.Fprintf(stderr, "This removes the sandbox %s with FrIDE's workspaces and any work in them that is not committed; the repositories on this Mac stay.\n", nodeagent.FrideSandboxName)
		answer, err := term.readLine("Remove it? [y/N] ")
		if err != nil || !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
			fmt.Fprintln(stdout, "Nothing removed.")
			return 1
		}
	}
	if err := nodeagent.FrideSandboxRemove(ctx, o); err != nil {
		return fail("%v", err)
	}
	fmt.Fprintf(stdout, "Removed the sandbox %s. The node's pairing stays until FrIDE is made again (create, start); the node stops offering fride within half a minute.\n", nodeagent.FrideSandboxName)
	return 0
}

// orDash is s, or a dash for nothing.
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
