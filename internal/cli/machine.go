package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Icewater1337/fridolin/internal/app"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/platform"
)

const setupMachineUsage = `fridolin setup-machine — what this machine still needs for its role (AGENTS.md)

Usage:
  fridolin setup-machine --node [--state-dir DIR] [--json]
                       a node of an existing core: Tailscale up, joined,
                       the core reachable, the node service running
  fridolin setup-machine --core [--json]
                       the machine the core moves to: Docker, Tailscale, the
                       host secrets, the image, the volume, the container

It only looks, changes nothing, and is safe to run again: each line says
what is there, and "Next:" the one step to take now. Exit status 0 when
every required step is done, 1 when one is not, 2 for a bad flag.
`

// nodeChecks are the checks of a node machine: Tailscale, node.json, the
// device key, the core's node port, the service.
func nodeChecks(dir string, serviceStatus func() (string, error)) []app.DoctorCheck {
	var cfg *nodeagent.Config
	loaded := func() (*nodeagent.Config, error) {
		if cfg != nil {
			return cfg, nil
		}
		c, err := nodeagent.LoadConfig(dir)
		if err != nil {
			return nil, err
		}
		cfg = &c
		return cfg, nil
	}
	notJoined := app.DoctorResult{Status: app.DoctorWarn, Detail: "not checked: this machine has not joined a core", Fix: "join first (the node-joined line)"}
	return []app.DoctorCheck{
		app.TailscaleCheck(true),
		{ID: "node-joined", Title: "joined a core (node.json)", Required: true, Run: func(context.Context, *app.DoctorEnv) app.DoctorResult {
			c, err := loaded()
			if err != nil {
				return app.DoctorResult{Status: app.DoctorFail, Detail: err.Error(),
					Fix: "on the core: fridolin nodes join-code (or Nodes → New join code in its dashboard), then paste the line it prints here"}
			}
			return app.DoctorResult{Status: app.DoctorOK, Detail: fmt.Sprintf("%s, core %s, offers %s (%s)", c.Name, c.Core, c.Capabilities.Summary(), dir)}
		}},
		{ID: "node-key", Title: "device key", Required: true, Run: func(context.Context, *app.DoctorEnv) app.DoctorResult {
			if _, err := loaded(); err != nil {
				return notJoined
			}
			key, err := nodeagent.LoadKey(dir)
			if err != nil {
				return app.DoctorResult{Status: app.DoctorFail, Detail: err.Error(), Fix: "join again with a new code: fridolin node join CODE --core HOST:PORT"}
			}
			return app.DoctorResult{Status: app.DoctorOK, Detail: "device " + nodeagent.DeviceID(key)}
		}},
		{ID: "node-core", Title: "core's node port reachable", Required: true, Run: func(ctx context.Context, env *app.DoctorEnv) app.DoctorResult {
			c, err := loaded()
			if err != nil {
				return notJoined
			}
			addr := nodeDialAddress(c.Core)
			if err := env.Dial(ctx, addr); err != nil {
				return app.DoctorResult{Status: app.DoctorFail, Detail: fmt.Sprintf("%s: %v", addr, err),
					Fix: "tailscale status (is the core online on the tailnet?); on the core, nodes.enabled and core.tailnet.enabled must be on"}
			}
			return app.DoctorResult{Status: app.DoctorOK, Detail: addr + " answers"}
		}},
		{ID: "node-service", Title: "node service", Required: true, Run: func(context.Context, *app.DoctorEnv) app.DoctorResult {
			if _, err := loaded(); err != nil {
				return notJoined
			}
			st, err := serviceStatus()
			if err != nil {
				return app.DoctorResult{Status: app.DoctorWarn, Detail: "cannot tell: " + err.Error(), Fix: "fridolin node status"}
			}
			if st != "running" {
				fix := "fridolin node install, then fridolin node start"
				if runtime.GOOS == "windows" {
					fix += " (in PowerShell as administrator)"
				}
				return app.DoctorResult{Status: app.DoctorFail, Detail: st, Fix: fix}
			}
			return app.DoctorResult{Status: app.DoctorOK, Detail: st}
		}},
	}
}

// nodeDialAddress is node.json's core as host:port, without a ws:// or
// wss:// in front.
func nodeDialAddress(core string) string {
	for _, p := range []string{"ws://", "wss://"} {
		if rest, ok := strings.CutPrefix(core, p); ok {
			core, _, _ = strings.Cut(rest, "/")
		}
	}
	return core
}

// coreMachineChecks are what a machine needs to take the core over.
func coreMachineChecks(d hostDeps) []app.DoctorCheck {
	tag := imageTag(d.version)
	image := "fridolin-core:" + tag
	docker := func(ctx context.Context, env *app.DoctorEnv, args ...string) (string, error) {
		if d.docker == "" {
			return "", errors.New("no docker")
		}
		out, err := env.Run(ctx, d.docker, args...)
		return strings.TrimSpace(string(out)), err
	}
	secretCheck := func(id, title string, required bool, fix string, names ...string) app.DoctorCheck {
		return app.DoctorCheck{ID: id, Title: title, Required: required, Run: func(context.Context, *app.DoctorEnv) app.DoctorResult {
			var missing []string
			for _, n := range names {
				ok, err := d.secrets.Has(n)
				if err != nil {
					return app.DoctorResult{Status: app.DoctorFail, Detail: err.Error(), Fix: "fridolin core secrets"}
				}
				if !ok {
					missing = append(missing, n)
				}
			}
			if len(missing) > 0 {
				st := app.DoctorMissing
				if required {
					st = app.DoctorFail
				}
				return app.DoctorResult{Status: st, Detail: "not in " + d.place() + ": " + strings.Join(missing, ", "), Fix: fix}
			}
			return app.DoctorResult{Status: app.DoctorOK, Detail: "in " + d.place()}
		}}
	}
	receive := "fridolin core receive here, and fridolin core send --to … on the machine that runs the core now (or, one by one: fridolin core secrets set NAME)"
	return []app.DoctorCheck{
		app.DockerCheck(),
		app.TailscaleCheck(true),
		secretCheck("secrets-age", "age identity (decrypts the exports)", true, receive, hostsecrets.AgeIdentity),
		secretCheck("secrets-b2", "Backblaze B2 vault key (fetches the exports, holds the lease)", true, receive, hostsecrets.B2VaultKeyID, hostsecrets.B2VaultKey),
		secretCheck("secrets-claude", "Claude token", false, receive, hostsecrets.ClaudeOAuthToken),
		secretCheck("secrets-vault", "vault and snapshot secrets", false, receive,
			hostsecrets.B2SnapshotsKeyID, hostsecrets.B2SnapshotsKey, hostsecrets.VaultCryptPassword, hostsecrets.VaultCryptSalt, hostsecrets.ResticPassword),
		{ID: "image", Title: "core image " + image, Required: true, Run: func(ctx context.Context, env *app.DoctorEnv) app.DoctorResult {
			if _, err := docker(ctx, env, "image", "inspect", "--format", "{{.Id}}", image); err != nil {
				return app.DoctorResult{Status: app.DoctorFail, Detail: "not built here",
					Fix: "in the repository's folder: fridolin core build (docker build of container/Dockerfile, tagged " + image + ")"}
			}
			return app.DoctorResult{Status: app.DoctorOK, Detail: image}
		}},
		{ID: "volume", Title: "volume " + coreVolume, Required: true, Run: func(ctx context.Context, env *app.DoctorEnv) app.DoctorResult {
			if _, err := docker(ctx, env, "volume", "inspect", "--format", "{{.Name}}", coreVolume); err != nil {
				return app.DoctorResult{Status: app.DoctorFail, Detail: "none yet: the core's data has not been imported",
					Fix: "fridolin core run core import --latest (after fridolin core export on the machine that runs the core now)"}
			}
			return app.DoctorResult{Status: app.DoctorOK, Detail: "exists (fridolin core run core status shows the lease)"}
		}},
		{ID: "container", Title: "core container", Required: true, Run: func(ctx context.Context, env *app.DoctorEnv) app.DoctorResult {
			out, err := docker(ctx, env, "ps", "--filter", "name=^fridolin-core$", "--format", "{{.Status}}")
			if err != nil || out == "" {
				return app.DoctorResult{Status: app.DoctorFail, Detail: "not running",
					Fix: "stop the old core first (fridolin core stop there), then here: fridolin core run core promote, and fridolin core start"}
			}
			return app.DoctorResult{Status: app.DoctorOK, Detail: out}
		}},
	}
}

// setupMachineCmd runs `fridolin setup-machine`.
func setupMachineCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("setup-machine", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, setupMachineUsage) }
	node := fs.Bool("node", false, "this machine is a node of a core")
	core := fs.Bool("core", false, "this machine takes the core over")
	stateDir := fs.String("state-dir", "", "the node's state folder")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *node == *core || fs.NArg() > 0 {
		fmt.Fprint(stderr, setupMachineUsage)
		return 2
	}
	env := app.SystemDoctorEnv("")
	var checks []app.DoctorCheck
	var where string
	if *node {
		dir, err := nodeStateDirFlag(*stateDir)
		if err != nil {
			fmt.Fprintf(stderr, "fridolin setup-machine: %v\n", err)
			return 1
		}
		checks, where = nodeChecks(dir, nodeServiceStatus(dir)), dir
	} else {
		d := defaultHostDeps(coreEnv{environ: os.Environ(), stdout: stdout, stderr: stderr})
		checks, where = coreMachineChecks(d), filepath.Dir(d.composePath)
	}
	return writeMachineReport(stdout, app.RunDoctor(context.Background(), env, checks), where, *asJSON)
}

// writeMachineReport prints a report of checks, with the next step: the
// fix of the first required check that is not ok, else of the first
// optional one.
func writeMachineReport(w io.Writer, rep app.DoctorReport, where string, asJSON bool) int {
	rep.Config = where
	next := ""
	for _, required := range []bool{true, false} {
		for _, c := range rep.Checks {
			if next == "" && c.Required == required && c.Status != app.DoctorOK && c.Fix != "" {
				next = c.Fix
			}
		}
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			app.DoctorReport
			Next string `json:"next,omitempty"`
		}{rep, next})
	} else {
		_ = rep.WriteText(w)
		if next != "" {
			fmt.Fprintf(w, "Next: %s\n", next)
		}
	}
	if !rep.OK {
		return 1
	}
	return 0
}

func nodeStateDirFlag(dir string) (string, error) {
	if dir == "" {
		var err error
		if dir, err = platform.NodeStateDir(); err != nil {
			return "", fmt.Errorf("%v; pass --state-dir", err)
		}
	}
	return filepath.Abs(dir)
}

func nodeServiceStatus(dir string) func() (string, error) {
	return func() (string, error) {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		return nodeagent.ServiceStatus(nodeagent.ServiceConfig(runtime.GOOS, exe, dir))
	}
}

// nodeDoctor is `fridolin doctor --node`: the node machine's checks.
func nodeDoctor(stateDir string, asJSON bool, stdout io.Writer) int {
	dir, err := nodeStateDirFlag(stateDir)
	if err != nil {
		fmt.Fprintf(stdout, "fridolin doctor --node: %v\n", err)
		return 1
	}
	rep := app.RunDoctor(context.Background(), app.SystemDoctorEnv(""), nodeChecks(dir, nodeServiceStatus(dir)))
	rep.Config = dir
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		fmt.Fprintf(stdout, "Node machine (state dir %s):\n", dir)
		_ = rep.WriteText(stdout)
	}
	if !rep.OK {
		return 1
	}
	return 0
}

// isNodeMachine says whether doctor without --node should check this
// machine as a node: it has no bridge config, and has joined a core.
func isNodeMachine(configFlag string) bool {
	if configFlag != "" {
		return false
	}
	if _, err := os.Stat(configPath("")); err == nil {
		return false
	}
	dir, err := platform.NodeStateDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, nodeagent.ConfigFile))
	return err == nil
}
