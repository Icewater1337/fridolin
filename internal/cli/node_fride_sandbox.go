package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/platform"
)

const nodeFrideSandboxUsage = `fridolin node fride sandbox — FrIDE in a Docker Sandbox on this machine (docs/NODES.md)

A whole FrIDE, with its coding agents, in one sbx microVM, beside the
native companion FrIDE. It is paired to a node of its own, in a state dir
of its own (default: the node's state dir with -sbx), which joins the core
like any node; its agent keeps FrIDE running.

Usage:
  fridolin node fride sandbox join CODE --core HOST:PORT [--name NAME]
                  join the core as the sandboxed FrIDE's node: it offers nothing
                  but fride, runs from its own copy of fridolin and as a service
                  of its own (fridolin-node-fride-sandbox; --no-service: none)
  fridolin node fride sandbox create [--repo DIR] [--base DIR] [--git-name NAME --git-email EMAIL]
                  [--docker-disk 40g] [--no-caches]
                  build the template, make the sandbox fride-sandbox (6 GB,
                  127.0.0.1:17850, egress for GitHub, Go, npm and Anthropic, --base
                  mounted read-write; default ~/fride-sandbox/repos; a 40 GB Docker
                  disk; Go and npm caches in ~/.cache/fridolin-sbx, shared with the
                  next sandbox) and build FrIDE inside at the commit the checkout in
                  --repo pins (default: .); with gh logged in, also what github does
  fridolin node fride sandbox github [--token-stdin] [--remove]
                  give the sandbox GitHub: this machine's gh auth token (or stdin)
                  goes to sbx's proxy, never into the VM, so that gh and git over
                  https work inside; again after gh auth refresh; --remove takes it
  fridolin node fride sandbox login [--setup-token]
                  the Claude login of FrIDE's agents: paste the token claude
                  setup-token printed (not shown), or pipe it in; --setup-token runs
                  claude setup-token in the sandbox first
  fridolin node fride sandbox start
                  have the agent keep FrIDE running, wait for it and pair the node
  fridolin node fride sandbox stop
                  stop FrIDE; the agent no longer starts it
  fridolin node fride sandbox update [--repo DIR]
                  build FrIDE again at the commit the checkout pins, and restart it
  fridolin node fride sandbox status
                  the sandbox, FrIDE, the pairing, the login
  fridolin node fride sandbox rm [--yes] [--no-history]
                  save the agents' sessions and FrIDE's tasks to the state dir
                  (create puts them back), then remove the sandbox, with its
                  workspaces (asks first)

Flags:
  --state-dir DIR   the sandboxed FrIDE's node (default: the node's state dir with -sbx)
  --fride-repo URL  FrIDE's git URL (default: --repo's origin, fridolin → fride, on GitHub)
`

// frideSandboxFlags are the flags of fridolin node fride sandbox.
type frideSandboxFlags struct {
	stateDir, repo, base, frideRepo, gitName, gitEmail string
	core, name, dockerDisk                             string
	setupToken, yes, noService, noCaches               bool
	remove, tokenStdin, noHistory                      bool
}

// frideSandboxStateDir is the sandboxed FrIDE's node's default state dir:
// the node's, with -sbx.
func frideSandboxStateDir() (string, error) {
	d, err := platform.NodeStateDir()
	if err != nil {
		return "", err
	}
	return d + "-sbx", nil
}

// nodeFrideSandboxCmd runs `fridolin node fride sandbox`.
func nodeFrideSandboxCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, nodeFrideSandboxUsage)
		return 0
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("node fride sandbox "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, nodeFrideSandboxUsage) }
	var f frideSandboxFlags
	fs.StringVar(&f.stateDir, "state-dir", "", "the sandboxed FrIDE's node's state folder")
	switch sub {
	case "join":
		fs.StringVar(&f.core, "core", "", "the core's node port, host:port")
		fs.StringVar(&f.name, "name", "", "how the core shows this node")
		fs.BoolVar(&f.noService, "no-service", false, "join only, without installing the service")
	case "create", "update":
		fs.StringVar(&f.repo, "repo", "", "the fridolin checkout")
		fs.StringVar(&f.frideRepo, "fride-repo", "", "FrIDE's git URL")
		fs.StringVar(&f.gitName, "git-name", "", "the git name FrIDE's agents commit with")
		fs.StringVar(&f.gitEmail, "git-email", "", "the git email FrIDE's agents commit with")
		if sub == "create" {
			fs.StringVar(&f.base, "base", "", "the host folder for FrIDE's repositories")
			fs.StringVar(&f.dockerDisk, "docker-disk", nodeagent.DefaultFrideSandboxDockerDisk, "the size of the sandbox's Docker disk")
			fs.BoolVar(&f.noCaches, "no-caches", false, "no Go and npm caches shared with other sandboxes")
		}
	case "github":
		fs.BoolVar(&f.tokenStdin, "token-stdin", false, "read the token from stdin instead of gh auth token")
		fs.BoolVar(&f.remove, "remove", false, "take GitHub's token from the sandbox")
	case "login":
		fs.BoolVar(&f.setupToken, "setup-token", false, "run claude setup-token in the sandbox first")
	case "rm":
		fs.BoolVar(&f.yes, "yes", false, "do not ask")
		fs.BoolVar(&f.noHistory, "no-history", false, "do not save the agents' sessions and FrIDE's tasks first")
	case "start", "stop", "status":
	default:
		fmt.Fprintf(stderr, "unknown node fride sandbox command %q\n\n%s", sub, nodeFrideSandboxUsage)
		return 2
	}
	pos, err := parseInterspersed(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return 2
	}
	if len(pos) != 0 && (sub != "join" || len(pos) != 1) {
		fmt.Fprintf(stderr, "fridolin node fride sandbox %s: unexpected arguments %q\n", sub, pos)
		return 2
	}
	return runFrideSandbox(sub, pos, f, stdout, stderr)
}
