package cli

import (
	"context"
	"errors"
	"flag"
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
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/platform"
)

const nodeFrideHostUsage = `fridolin node fride host — FrIDE instances on this machine (docs/NODES.md, FrIDE instances)

The core starts FrIDE instances on a node that offers fride_host: each a
whole FrIDE in a Docker Sandbox of its own, for one trust domain. This
node's agent runs them; these commands set the machine up for it.

Usage:
  fridolin node fride host setup [--no-build] [--repo DIR] [--fride-repo URL]
                  [--docker-disk 40g] [--no-caches] [--reserve-mb N]
                  check sbx (installed, running, signed in), make sbx's global
                  settings fridolin's, build the template (FrIDE at the commit the
                  fridolin checkout in --repo pins, default .; Claude Code; ponytail)
                  with Docker and load it into sbx; the node then offers fride_host.
                  Again after updating the checkout, for a newer FrIDE.
                  With --no-build (the default where no Docker is found), it builds
                  nothing: each instance's sandbox is made from the template the
                  core publishes (fridolin-fride-sandbox), which sbx pulls
  fridolin node fride host status
                  sbx, the template, the memory and what the node offers

Flags:
  --state-dir DIR   the node's state folder
  --no-build        take the core's published template instead of building one
  --fride-repo URL  FrIDE's git URL (default: --repo's origin, fridolin → fride, on GitHub)
  --docker-disk SIZE
                    each sandbox's Docker disk (default 40g)
  --no-caches       no Go and npm caches kept between an instance's sandboxes
                    (default: ~/.cache/fridolin-sbx, a folder per instance; on
                    Windows always, which shares no caches yet)
  --reserve-mb N    the memory the machine keeps for itself, which no instance is
                    started into (default 6144; node.json's fride_host.reserve_mb)

On Windows (11, x64, with the Windows Hypervisor Platform), run setup as the
user who signs in: sbx and its sandboxes are that user's, and the node's
service runs sbx in their session (docs/NODES.md, FrIDE instances on Windows).
`

const nodeFrideInstanceUsage = `fridolin node fride instance — the FrIDE instances on this machine (docs/NODES.md, FrIDE instances)

The core starts and stops them (fride.instance.start); these commands
show them and stop or remove one by hand.

Usage:
  fridolin node fride instance ls
                  each instance: its state, memory, FrIDE's address and ports
  fridolin node fride instance stop NAME
                  stop it: the agent no longer keeps its FrIDE running, and its VM
                  stops; its disk stays, and the core's next start runs it again
  fridolin node fride instance rm NAME [--yes] [--no-history]
                  save the agents' sessions and FrIDE's tasks (the next sandbox of
                  that name gets them back), then remove the sandbox with its
                  workspaces and repositories, and its pairing and tokens (asks
                  first)
  fridolin node fride instance logs NAME [--lines N]
                  the end of its FrIDE's log, while it runs

Flags:
  --state-dir DIR   the node's state folder
`

// frideHostFlags are the flags of fridolin node fride host and instance.
type frideHostFlags struct {
	stateDir, repo, frideRepo, dockerDisk string
	noCaches, noBuild, yes, noHistory     bool
	reserveMB, lines                      int
	// set are the flags given.
	set map[string]bool
}

// nodeFrideHostCmd runs `fridolin node fride host` (instance false) or
// `fridolin node fride instance` (instance true).
func nodeFrideHostCmd(instance bool, args []string, stdout, stderr io.Writer) int {
	usage, what := nodeFrideHostUsage, "host"
	if instance {
		usage, what = nodeFrideInstanceUsage, "instance"
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	sub, args := args[0], args[1:]
	if !instance && sub == "sbx-secret" {
		// sbx runs it on Windows to read a git host's token (hidden).
		if len(args) != 1 {
			fmt.Fprintln(stderr, "fridolin node fride host sbx-secret takes a token file")
			return 2
		}
		if err := nodeagent.PrintSbxSecret(stdout, args[0]); err != nil {
			fmt.Fprintf(stderr, "fridolin node fride host sbx-secret: %v\n", err)
			return 1
		}
		return 0
	}
	fs := flag.NewFlagSet("node fride "+what+" "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	var f frideHostFlags
	fs.StringVar(&f.stateDir, "state-dir", "", "the node's state folder")
	names := 0
	switch {
	case !instance && sub == "setup":
		fs.StringVar(&f.repo, "repo", "", "the fridolin checkout")
		fs.StringVar(&f.frideRepo, "fride-repo", "", "FrIDE's git URL")
		fs.StringVar(&f.dockerDisk, "docker-disk", nodeagent.DefaultFrideInstanceDockerDisk, "each sandbox's Docker disk")
		fs.BoolVar(&f.noCaches, "no-caches", false, "no Go and npm caches kept between an instance's sandboxes")
		fs.BoolVar(&f.noBuild, "no-build", false, "take the core's published template instead of building one")
		fs.IntVar(&f.reserveMB, "reserve-mb", 0, "the memory the machine keeps for itself")
	case !instance && sub == "status", instance && sub == "ls":
	case instance && sub == "stop":
		names = 1
	case instance && sub == "rm":
		names = 1
		fs.BoolVar(&f.yes, "yes", false, "do not ask")
		fs.BoolVar(&f.noHistory, "no-history", false, "do not save the agents' sessions and FrIDE's tasks first")
	case instance && sub == "logs":
		names = 1
		fs.IntVar(&f.lines, "lines", 200, "how many lines")
	default:
		fmt.Fprintf(stderr, "unknown node fride %s command %q\n\n%s", what, sub, usage)
		return 2
	}
	pos, err := parseInterspersed(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return 2
	}
	f.set = map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { f.set[fl.Name] = true })
	if f.noBuild && (f.set["repo"] || f.set["fride-repo"]) {
		fmt.Fprintf(stderr, "fridolin node fride %s %s: --repo and --fride-repo are for a template built here; --no-build takes the core's\n", what, sub)
		return 2
	}
	if len(pos) != names {
		if names == 1 {
			fmt.Fprintf(stderr, "fridolin node fride %s %s takes the instance's name\n", what, sub)
		} else {
			fmt.Fprintf(stderr, "fridolin node fride %s %s: unexpected arguments %q\n", what, sub, pos)
		}
		return 2
	}
	if names == 1 {
		if err := nodeproto.CheckFrideInstanceName(pos[0]); err != nil {
			fmt.Fprintf(stderr, "fridolin node fride %s %s: %v\n", what, sub, err)
			return 2
		}
	}
	if f.reserveMB < 0 || f.lines < 0 {
		fmt.Fprintf(stderr, "fridolin node fride %s %s: a negative number\n", what, sub)
		return 2
	}
	if why := nodeagent.SbxUnsupported(); why != "" {
		fmt.Fprintf(stderr, "fridolin node fride %s: %s\n", what, why)
		return 1
	}
	dir := f.stateDir
	if dir == "" {
		if dir, err = platform.NodeStateDir(); err != nil {
			fmt.Fprintf(stderr, "fridolin node fride %s: %v; pass --state-dir\n", what, err)
			return 1
		}
	}
	if dir, err = filepath.Abs(dir); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return runFrideHost(what+" "+sub, pos, f, dir, findSbx(), stdout, stderr)
}

// frideHostTimeout bounds a command: setup builds the template, which
// the first time downloads a few GB.
func frideHostTimeout(cmd string) time.Duration {
	switch cmd {
	case "host setup":
		return 45 * time.Minute
	case "instance rm", "instance stop":
		return 10 * time.Minute
	}
	return 2 * time.Minute
}

// runFrideHost runs cmd ("host setup", "instance ls", …) with the sbx
// at sbx ("" for none).
func runFrideHost(cmd string, pos []string, f frideHostFlags, dir, sbx string, stdout, stderr io.Writer) int {
	fail := func(format string, args ...any) int {
		fmt.Fprintf(stderr, "fridolin node fride "+cmd+": "+format+"\n", args...)
		return 1
	}
	if sbx == "" && cmd != "host status" {
		return fail("sbx is not installed: %s; then this again", nodeagent.SbxInstallHint())
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, frideHostTimeout(cmd))
	defer cancel()
	o := nodeagent.FrideInstanceOptions{Dir: dir, Sbx: sbx, Out: stdout}
	switch cmd {
	case "host setup":
		return frideHostSetup(ctx, f, dir, sbx, findDocker(), runtime.GOOS, stdout, fail)
	case "host status":
		return frideHostStatus(ctx, dir, sbx, stdout, fail)
	case "instance ls":
		rows, err := nodeagent.ListFrideInstances(ctx, o)
		if err != nil {
			return fail("%v", err)
		}
		if len(rows) == 0 {
			fmt.Fprintln(stdout, "No FrIDE instances on this node: the core starts them (fride_spawn, or a task for an instance).")
			return 0
		}
		for _, r := range rows {
			fmt.Fprintf(stdout, "%s: %s, %d MB, sandbox %s (%s), ports %d and %d", r.Name, r.State, r.MemoryMB, r.Sandbox, orDash(r.Sbx), r.Port, r.WebhookPort)
			if r.URL != "" {
				fmt.Fprintf(stdout, ", FrIDE at %s, repositories %s", r.URL, orNone(r.Repos))
			}
			fmt.Fprintf(stdout, ", git hosts with a token: %s\n", orNone(r.Hosts))
			if r.Problem != "" {
				fmt.Fprintf(stdout, "  problem: %s\n", r.Problem)
			}
		}
	case "instance stop":
		if err := nodeagent.StopFrideInstance(ctx, o, pos[0]); err != nil {
			return fail("%v", instanceError(err))
		}
		fmt.Fprintf(stdout, "Stopped the FrIDE instance %s; the core's next start runs it again.\n", pos[0])
	case "instance rm":
		if !f.yes {
			term := stdinTerminal()
			if term.readLine == nil {
				return fail("removing an instance removes its workspaces and repositories, with any work in them that is not pushed: pass --yes")
			}
			fmt.Fprintf(stderr, "This removes the sandbox %s with its workspaces and repositories, and any work in them that is not pushed. %s\n",
				nodeagent.FrideInstanceSandbox(pos[0]), yesNo(f.noHistory, "The agents' sessions and FrIDE's tasks go with it (--no-history).", "The agents' sessions and FrIDE's tasks are saved first; its next sandbox gets them back."))
			answer, err := term.readLine("Remove it? [y/N] ")
			if err != nil || !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
				fmt.Fprintln(stdout, "Nothing removed.")
				return 1
			}
		}
		if err := nodeagent.RemoveFrideInstance(ctx, o, pos[0], f.noHistory); err != nil {
			return fail("%v", instanceError(err))
		}
		fmt.Fprintf(stdout, "Removed the FrIDE instance %s; the core's next start makes it anew.\n", pos[0])
	case "instance logs":
		out, err := nodeagent.FrideInstanceLogs(ctx, o, pos[0], f.lines)
		if err != nil {
			return fail("%v", instanceError(err))
		}
		_, _ = stdout.Write(out)
	}
	return 0
}

// instanceError is err, said for the owner.
func instanceError(err error) error {
	if nodeagent.IsNoFrideInstance(err) {
		return fmt.Errorf("%w (fridolin node fride instance ls)", err)
	}
	return err
}

// frideHostSetup is host setup on goos with the docker found ("" for
// none): with the default caches folder, and without a build where no
// Docker is.
func frideHostSetup(ctx context.Context, f frideHostFlags, dir, sbx, docker, goos string, stdout io.Writer, fail func(string, ...any) int) int {
	repo := f.repo
	if repo == "" {
		repo = "."
	}
	repo, err := filepath.Abs(repo)
	if err != nil {
		return fail("%v", err)
	}
	o := nodeagent.FrideHostSetupOptions{Dir: dir, Sbx: sbx, Docker: docker, Repo: repo, FrideRepo: f.frideRepo,
		DockerDisk: f.dockerDisk, ReserveMB: f.reserveMB, NoBuild: f.noBuild, Out: stdout}
	if docker == "" && !f.noBuild {
		if f.set["repo"] || f.set["fride-repo"] {
			return fail("docker is not installed: a template is built with Docker (Docker Desktop or OrbStack); without it, set up with --no-build, which takes the core's published template")
		}
		o.NoBuild = true
		fmt.Fprintln(stdout, "No Docker here: taking the template the core publishes (--no-build).")
	}
	if _, err := nodeagent.FridePin(repo); err != nil && !o.NoBuild && !f.set["repo"] {
		return fail("no fridolin checkout in %s: pass --repo DIR to build the template here with Docker, or --no-build to take the one the core publishes", repo)
	}
	if git, err := exec.LookPath("git"); err == nil {
		o.Git = git
	}
	switch {
	case f.noCaches:
	case goos == "windows":
		fmt.Fprintln(stdout, "Note: no shared Go and npm caches on Windows yet; each instance keeps its caches on its own disk.")
	default:
		home, err := os.UserHomeDir()
		if err != nil {
			return fail("%v; pass --no-caches", err)
		}
		o.Caches = filepath.Join(home, ".cache", "fridolin-sbx")
	}
	if err := nodeagent.FrideHostSetup(ctx, o); err != nil {
		return fail("%v", err)
	}
	return 0
}

// frideHostStatus prints fride_host: 1 when it is not set up or not
// ready.
func frideHostStatus(ctx context.Context, dir, sbx string, stdout io.Writer, fail func(string, ...any) int) int {
	r, err := nodeagent.FrideHostStatus(ctx, dir, sbx)
	if err != nil {
		return fail("%v", err)
	}
	fmt.Fprintf(stdout, "State dir: %s\n", dir)
	if r.Host == nil {
		fmt.Fprintln(stdout, "FrIDE instances: not set up (fridolin node fride host setup)")
		return 1
	}
	h := r.Host
	if h.FromCore {
		fmt.Fprintf(stdout, "Template: the core's (%s, which sbx pulls when an instance's sandbox is made), set up %s\n", nodeproto.FrideSandboxImage, h.SetUp.Local().Format("2006-01-02 15:04"))
	} else {
		fmt.Fprintf(stdout, "Template: %s (FrIDE %s from %s), set up %s\n", h.Template, h.FrideCommit, h.FrideRepo, h.SetUp.Local().Format("2006-01-02 15:04"))
	}
	fmt.Fprintf(stdout, "Docker disk: %s per instance; caches: %s\n", h.DockerDisk, yesNo(h.Caches != "", h.Caches+" (a folder per instance)", "none"))
	c := r.Declared
	if c == nil {
		fmt.Fprintln(stdout, "Offers fride_host: no (a box, or no Docker Sandboxes on this system)")
		return 1
	}
	fmt.Fprintf(stdout, "sbx: %s\n", orDash(c.Version))
	fmt.Fprintf(stdout, "Ready: %s\n", yesNo(c.Ready, "yes", "no: "+c.Problem))
	fmt.Fprintf(stdout, "Memory: %d MB, %d MB available now, %d MB kept for the machine\n", c.TotalMemoryMB, c.AvailableMemoryMB, c.ReserveMB)
	fmt.Fprintf(stdout, "Instances: %d", len(c.Instances))
	for _, in := range c.Instances {
		fmt.Fprintf(stdout, "; %s %s (%d MB)", in.Name, in.State, in.MemoryMB)
	}
	fmt.Fprintln(stdout)
	if !c.Ready {
		return 1
	}
	return 0
}

// orDash is s, or a dash for nothing.
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func yesNo(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}
