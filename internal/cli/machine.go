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
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/app"
	"github.com/Icewater1337/fridolin/internal/doctor"
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

// nodeService is what the node checks ask of the node service and of the
// app package this process runs in; tests set their own.
type nodeService struct {
	status func() (string, error)
	// stateDir is the state dir the installed service runs with
	// (nodeagent.ServiceStateDir), "" where it is not known; nil for none.
	stateDir func() (string, error)
	pkg      appPackage
	// sandbox looks at Docker Sandboxes once; nil leaves their checks out.
	sandbox func() nodeagent.SandboxStatus
}

// boxHealthMaxAge is how old a box agent's last word may be for it to
// count as connected (fridolin node health, the box's healthcheck).
const boxHealthMaxAge = 90 * time.Second

// nodeChecks are the checks of a node machine: Tailscale, node.json, the
// device key, the core's node port, the service. A node in a box has no
// Tailscale, service or systemd unit of its own (docker compose runs it),
// and reaches the core over a tailnet device inside its agent, which a
// dial from here does not use: there the agent's own word on its
// connection stands in for the port and the service.
func nodeChecks(dir string, svc nodeService) []doctor.Check {
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
	notJoined := doctor.Result{Status: doctor.Warn, Detail: "not checked: this machine has not joined a core", Fix: "join first (the node-joined line)"}
	inBox := func(*doctor.Env) bool {
		c, err := loaded()
		return err == nil && c.Box != nil
	}
	native := func(env *doctor.Env) bool { return !inBox(env) }
	nativeLinux := func(env *doctor.Env) bool { return onLinux(env) && !inBox(env) }
	tailscale := doctor.TailscaleCheck(true)
	tailscale.Applies = native
	checks := []doctor.Check{
		tailscale,
		{ID: "node-joined", Title: "joined a core (node.json)", Required: true, Run: func(context.Context, *doctor.Env) doctor.Result {
			c, err := loaded()
			if err != nil {
				if stray := svc.pkg.strayCopies(dir); stray != "" {
					return doctor.Result{Status: doctor.Fail, Detail: err.Error() + "; " + stray,
						Fix: fmt.Sprintf("from a PowerShell started outside the app, copy %s and %s from there to %s; inside the app, copy them to a folder outside AppData, such as %s, and pass it as --state-dir (%s)",
							nodeagent.ConfigFile, nodeagent.KeyFile, dir, svc.pkg.outsideDir(), packageDoc)}
				}
				return doctor.Result{Status: doctor.Fail, Detail: err.Error(),
					Fix: "send !join to the bridge in your WhatsApp chat (or on the core: fridolin nodes join-code), then paste the line it gives here"}
			}
			return doctor.Result{Status: doctor.OK, Detail: fmt.Sprintf("%s, core %s, offers %s (%s)", c.Name, c.Core, c.Capabilities.Summary(), dir)}
		}},
		{ID: "node-key", Title: "device key", Required: true, Run: func(context.Context, *doctor.Env) doctor.Result {
			if _, err := loaded(); err != nil {
				return notJoined
			}
			key, err := nodeagent.LoadKey(dir)
			if err != nil {
				return doctor.Result{Status: doctor.Fail, Detail: err.Error(), Fix: "join again with a new code: fridolin node join CODE --core HOST:PORT"}
			}
			return doctor.Result{Status: doctor.OK, Detail: "device " + nodeagent.DeviceID(key)}
		}},
		{ID: "node-core", Title: "core's node port reachable", Required: true, Applies: native, Run: func(ctx context.Context, env *doctor.Env) doctor.Result {
			c, err := loaded()
			if err != nil {
				return notJoined
			}
			addr := nodeDialAddress(c.Core)
			if err := env.Dial(ctx, addr); err != nil {
				return doctor.Result{Status: doctor.Fail, Detail: fmt.Sprintf("%s: %v", addr, err),
					Fix: "tailscale status (is the core online on the tailnet?); on the core, nodes.enabled and core.tailnet.enabled must be on"}
			}
			return doctor.Result{Status: doctor.OK, Detail: addr + " answers"}
		}},
		{ID: "node-box-core", Title: "connected to the core (the box's tailnet device)", Required: true, Applies: inBox, Run: func(_ context.Context, env *doctor.Env) doctor.Result {
			c, _ := loaded()
			if err := nodeagent.BoxHealthy(dir, boxHealthMaxAge, env.Time()); err != nil {
				return doctor.Result{Status: doctor.Fail, Detail: fmt.Sprintf("%s: %v", c.Core, err),
					Fix: "on the host, in the box's folder: docker compose logs node; a tailnet policy that restricts devices must let " + c.Box.Hostname + " reach fridolin-core on port 8766"}
			}
			return doctor.Result{Status: doctor.OK, Detail: c.Core + " as " + c.Box.Hostname}
		}},
		{ID: "node-service", Title: "node service", Required: true, Applies: native, Run: func(context.Context, *doctor.Env) doctor.Result {
			if _, err := loaded(); err != nil {
				return notJoined
			}
			st, err := svc.status()
			if err != nil {
				return doctor.Result{Status: doctor.Warn, Detail: "cannot tell: " + err.Error(), Fix: "fridolin node status"}
			}
			sd := ""
			if svc.stateDir != nil {
				sd, _ = svc.stateDir()
			}
			if st != "running" {
				if detail, fix := svc.pkg.serviceDirProblem(sd); detail != "" {
					return doctor.Result{Status: doctor.Fail, Detail: st + ": " + detail, Fix: fix}
				}
				fix := "fridolin node install, then fridolin node start"
				if runtime.GOOS == "windows" {
					fix += " (in PowerShell as administrator)"
				}
				return doctor.Result{Status: doctor.Fail, Detail: st, Fix: fix}
			}
			if sd != "" && !samePath(sd, dir) {
				st += ", with --state-dir " + sd
			}
			return doctor.Result{Status: doctor.OK, Detail: st}
		}},
		{ID: "node-boot", Title: "node service starts at boot (systemd)", Required: true, Applies: nativeLinux, Run: func(_ context.Context, env *doctor.Env) doctor.Result {
			if _, err := loaded(); err != nil {
				return notJoined
			}
			if _, err := env.Stat(nodeagent.LinuxUnitPath(env.Home)); err != nil {
				return doctor.Result{Status: doctor.Fail, Detail: "no systemd user unit " + nodeagent.ServiceName + ".service", Fix: "fridolin node install, then fridolin node start"}
			}
			want := nodeagent.LinuxWantsLink(env.Home, nodeagent.LinuxBootTarget)
			if _, err := env.Stat(want); err == nil {
				return doctor.Result{Status: doctor.OK, Detail: "enabled in " + filepath.Base(filepath.Dir(want))}
			}
			if _, err := env.Stat(nodeagent.LinuxWantsLink(env.Home, "multi-user.target")); err == nil {
				return doctor.Result{Status: doctor.Fail, Detail: "enabled in multi-user.target.wants, which a user's systemd never starts: the node stays off after a reboot",
					Fix: "fridolin node start (moves it to " + nodeagent.LinuxBootTarget + ")"}
			}
			return doctor.Result{Status: doctor.Fail, Detail: "not enabled: the node stays off after a reboot", Fix: "systemctl --user enable " + nodeagent.ServiceName}
		}},
		{ID: "node-linger", Title: "systemd keeps your services without a login (linger)", Applies: nativeLinux, Run: func(ctx context.Context, env *doctor.Env) doctor.Result {
			user := env.Getenv("USER")
			if user == "" {
				return doctor.Result{Status: doctor.Warn, Detail: "cannot tell: USER is not set", Fix: "loginctl show-user $USER -p Linger"}
			}
			fix := "sudo loginctl enable-linger " + user
			out, err := env.Run(ctx, "loginctl", "show-user", user, "-p", "Linger")
			if err != nil {
				return doctor.Result{Status: doctor.Warn, Detail: "cannot tell: " + err.Error(), Fix: fix}
			}
			if v := strings.TrimSpace(string(out)); v != "Linger=yes" {
				return doctor.Result{Status: doctor.Warn, Detail: v + ": the node starts only once " + user + " logs in, and stops at logout", Fix: fix}
			}
			return doctor.Result{Status: doctor.OK, Detail: "on for " + user}
		}},
		{ID: "node-core-leftovers", Title: "what core moves left on this machine", Run: func(context.Context, *doctor.Env) doctor.Result {
			return coreLeftoversResult(nodeagent.ReadCoreLeftovers(dir))
		}},
	}
	return append(checks, sandboxChecks(svc.sandbox)...)
}

// coreLeftoversResult flags a former core host that still holds the
// core's data or credentials (#266), from what its agent recorded as
// moves brought them: what forgetting removes, and the data dir that
// forgetting keeps, the machine's own from before the moves.
func coreLeftoversResult(l nodeagent.CoreLeftovers) doctor.Result {
	byHand := "docs/CORE.md, \"Forgetting a former core host\""
	if !l.Former {
		detail := "no core moved away from this machine"
		if l.Holds() {
			detail += "; it holds what moves brought it, for the core that runs or may run here"
		}
		return doctor.Result{Status: doctor.OK, Detail: detail}
	}
	var held []string
	if len(l.Secrets) > 0 {
		held = append(held, "host secrets "+strings.Join(l.Secrets, ", "))
	}
	if len(l.Files) > 0 {
		held = append(held, "secret files "+strings.Join(l.Files, ", "))
	}
	if l.DataMove != "" {
		held = append(held, "the core's data dir that move "+l.DataMove+" brought (bridge.db holds the WhatsApp session)")
	}
	switch {
	case len(held) > 0:
		return doctor.Result{Status: doctor.Warn, Detail: fmt.Sprintf("the core moved away in move %s, and this machine still holds %s", l.LastMove, strings.Join(held, "; ")),
			Fix: "on the core, vote forget in the keep/forget poll, or !revoke this node (it forgets them first); by hand: " + byHand}
	case l.Unrecorded:
		return doctor.Result{Status: doctor.Warn, Detail: "the core moved away in move " + l.LastMove + "; a move imported its data dir here before fridolin recorded what a move brings, so forgetting leaves it (bridge.db holds the WhatsApp session)",
			Fix: "remove it by hand once the core runs elsewhere: " + byHand}
	case !l.DataForgotten.IsZero():
		return doctor.Result{Status: doctor.OK, Detail: "the core moved away in move " + l.LastMove + "; what moves brought this machine is forgotten (" + l.DataForgotten.Format(time.DateOnly) + ")"}
	}
	why := "this machine ran the core before any move"
	if l.OwnDataMove != "" {
		why = "it held the core's data before move " + l.OwnDataMove + " imported into it"
	}
	return doctor.Result{Status: doctor.Warn, Detail: "the core moved away in move " + l.LastMove + "; its own data dir stays here, which forgetting never removes: " + why + " (bridge.db holds an older WhatsApp session)",
		Fix: "keep it to move the core back here; to remove it once you will not: " + byHand}
}

func onLinux(env *doctor.Env) bool { return env.GOOS == "linux" }

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
func coreMachineChecks(d hostDeps) []doctor.Check {
	tag := imageTag(d.version)
	image := "fridolin-core:" + tag
	docker := func(ctx context.Context, env *doctor.Env, args ...string) (string, error) {
		if d.docker == "" {
			return "", errors.New("no docker")
		}
		out, err := env.Run(ctx, d.docker, args...)
		return strings.TrimSpace(string(out)), err
	}
	secretCheck := func(id, title string, required bool, fix string, names ...string) doctor.Check {
		return doctor.Check{ID: id, Title: title, Required: required, Run: func(context.Context, *doctor.Env) doctor.Result {
			var missing []string
			for _, n := range names {
				ok, err := d.secrets.Has(n)
				if err != nil {
					return doctor.Result{Status: doctor.Fail, Detail: err.Error(), Fix: "fridolin core secrets"}
				}
				if !ok {
					missing = append(missing, n)
				}
			}
			if len(missing) > 0 {
				st := doctor.Missing
				if required {
					st = doctor.Fail
				}
				return doctor.Result{Status: st, Detail: "not in " + d.place() + ": " + strings.Join(missing, ", "), Fix: fix}
			}
			return doctor.Result{Status: doctor.OK, Detail: "in " + d.place()}
		}}
	}
	receive := "fridolin core receive here, and fridolin core send --to … on the machine that runs the core now (or, one by one: fridolin core secrets set NAME)"
	return []doctor.Check{
		doctor.DockerCheck(),
		doctor.TailscaleCheck(true),
		secretCheck("secrets-age", "post-quantum age identity and export key (decrypt the exports)", true, receive, hostsecrets.AgeIdentityPQ, hostsecrets.ExportKey),
		secretCheck("secrets-b2", "Backblaze B2 vault key (fetches the exports, holds the lease)", true, receive, hostsecrets.B2VaultKeyID, hostsecrets.B2VaultKey),
		secretCheck("secrets-claude", "Claude token", false, receive, hostsecrets.ClaudeOAuthToken),
		secretCheck("secrets-vault", "vault and snapshot secrets", false, receive,
			hostsecrets.B2SnapshotsKeyID, hostsecrets.B2SnapshotsKey, hostsecrets.VaultCryptPassword, hostsecrets.VaultCryptSalt, hostsecrets.VaultSealKey,
			hostsecrets.ResticPassword, hostsecrets.SnapshotsCryptPassword, hostsecrets.LitestreamSealKey),
		{ID: "image", Title: "core image " + image, Required: true, Run: func(ctx context.Context, env *doctor.Env) doctor.Result {
			if _, err := docker(ctx, env, "image", "inspect", "--format", "{{.Id}}", image); err != nil {
				return doctor.Result{Status: doctor.Fail, Detail: "not built here",
					Fix: "in the repository's folder: fridolin core build (docker build of container/Dockerfile, tagged " + image + ")"}
			}
			return doctor.Result{Status: doctor.OK, Detail: image}
		}},
		{ID: "volume", Title: "volume " + coreVolume, Required: true, Run: func(ctx context.Context, env *doctor.Env) doctor.Result {
			if _, err := docker(ctx, env, "volume", "inspect", "--format", "{{.Name}}", coreVolume); err != nil {
				return doctor.Result{Status: doctor.Fail, Detail: "none yet: the core's data has not been imported",
					Fix: "fridolin core run core import --latest (after fridolin core export on the machine that runs the core now)"}
			}
			return doctor.Result{Status: doctor.OK, Detail: "exists (fridolin core run core status shows the lease)"}
		}},
		{ID: "container", Title: "core container", Required: true, Run: func(ctx context.Context, env *doctor.Env) doctor.Result {
			out, err := docker(ctx, env, "ps", "--filter", "name=^fridolin-core$", "--format", "{{.Status}}")
			if err != nil || out == "" {
				return doctor.Result{Status: doctor.Fail, Detail: "not running",
					Fix: "stop the old core first (fridolin core stop there), then here: fridolin core run core promote, and fridolin core start"}
			}
			return doctor.Result{Status: doctor.OK, Detail: out}
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
	var checks []doctor.Check
	var where string
	if *node {
		dir, err := nodeStateDirFlag(*stateDir)
		if err != nil {
			fmt.Fprintf(stderr, "fridolin setup-machine: %v\n", err)
			return 1
		}
		checks, where = nodeChecks(dir, systemNodeService(dir)), dir
	} else {
		d := defaultHostDeps(coreEnv{environ: os.Environ(), stdout: stdout, stderr: stderr})
		checks, where = coreMachineChecks(d), filepath.Dir(d.composePath)
	}
	return writeMachineReport(stdout, doctor.Run(context.Background(), env, checks), where, *asJSON)
}

// writeMachineReport prints a report of checks, with the next step: the
// fix of the first required check that is not ok, else of the first
// optional one.
func writeMachineReport(w io.Writer, rep doctor.Report, where string, asJSON bool) int {
	rep.Config = where
	next := ""
	for _, required := range []bool{true, false} {
		for _, c := range rep.Checks {
			if next == "" && c.Required == required && c.Status != doctor.OK && c.Fix != "" {
				next = c.Fix
			}
		}
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			doctor.Report
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

// systemNodeService is this machine's node service, for the node checks.
func systemNodeService(dir string) nodeService {
	exe, err := os.Executable()
	if err != nil {
		failed := func() (string, error) { return "", err }
		return nodeService{status: failed, stateDir: failed, pkg: thisAppPackage()}
	}
	cfg := nodeagent.ServiceConfig(runtime.GOOS, exe, dir)
	return nodeService{
		status:   func() (string, error) { return nodeagent.ServiceStatus(cfg) },
		stateDir: func() (string, error) { return nodeagent.ServiceStateDir(cfg) },
		pkg:      thisAppPackage(),
		sandbox:  sync.OnceValue(func() nodeagent.SandboxStatus { return lookAtSandbox(dir) }),
	}
}

// nodeDoctor is `fridolin doctor --node`: the node machine's checks.
func nodeDoctor(stateDir string, asJSON bool, stdout io.Writer) int {
	dir, err := nodeStateDirFlag(stateDir)
	if err != nil {
		fmt.Fprintf(stdout, "fridolin doctor --node: %v\n", err)
		return 1
	}
	rep := doctor.Run(context.Background(), app.SystemDoctorEnv(""), nodeChecks(dir, systemNodeService(dir)))
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
