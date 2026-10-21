package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/version"
)

const nodeUsage = `fridolin node — run this machine as a node of a Fridolin core (docs/NODES.md)

Usage:
  fridolin node join CODE --core HOST:PORT [flags]
                       join the core with a code from fridolin nodes join-code, then
                       install and start the service (--no-service: only join)
  fridolin node run    run the agent in the foreground (what the service runs)
  fridolin node rollback
                       put back the fridolin an update from the core replaced
                       (<binary>.prev); restart the service after
  fridolin node install|start|stop|status|uninstall
                       the service: a launchd user agent on macOS, a systemd user
                       unit on Linux, a Windows Service on Windows
  fridolin node backup on [--root PATH]… | off
                       lend this machine's disks to the core's backups (or stop);
                       restart the service after (fridolin node stop, start)
  fridolin node workstation on --root PATH… [--allow-home] [--no-agent|--no-terminal] | off
                       let the core run Claude Code or Codex sessions and a terminal
                       in these folders (docs/NODES.md, Workstation); restart the
                       service after, then approve it on the core
  fridolin node fride pair --url http://127.0.0.1:PORT | status
                       FrIDE on this machine, offered to the core while it runs;
                       fridolin node fride help for more

Join flags:
  --name NAME          how the core shows this node (default: the host name)
  --allow PROG[=RE,…]  allow a program: PROG alone with any arguments, PROG= with
                       none, PROG=RE,RE with arguments that each match a pattern;
                       repeat it; replaces the default allowlist (report-only
                       commands). Shells are never allowed.
  --no-exec            allow no commands at all
  --root PATH[:rw]     grant a folder for reading, :rw for writing too; repeat it
  --backup             lend this machine's disks to the core's backups: the owner
                       picks a folder for each backup target on the core
  --backup-root PATH   lend only PATH (a drive or folder) for backups; repeat it
  --workstation        offer agent and terminal (the workstation mode) in the
                       --workstation-root folders, else in the --root folders
  --workstation-root PATH
                       a folder for the workstation mode; repeat it
  --allow-home         let a workstation root be the home folder itself
  --no-host            do not report host facts
  --no-notify          do not show notifications
  --no-service         join only; start the agent yourself (fridolin node run)

Every command takes --state-dir DIR (default: the OS's per-user state folder,
` + "`fridolin node status`" + ` shows it). What the node offers lives in DIR/node.json;
offering more after an edit needs the owner's approval again.
`

// nodeCmd runs `fridolin node` and returns its exit status.
func nodeCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, nodeUsage)
		return 0
	}
	sub, args := args[0], args[1:]
	if sub == "fride" {
		return nodeFrideCmd(args, stdout, stderr)
	}
	fs := flag.NewFlagSet("node "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, nodeUsage) }
	stateDir := fs.String("state-dir", "", "the node's state folder")
	var o joinFlags
	var backupRoots multiFlag
	var ws workstationFlags
	switch sub {
	case "workstation":
		fs.Var(&ws.roots, "root", "a folder for agent and terminal (repeatable)")
		fs.BoolVar(&ws.allowHome, "allow-home", false, "let a root be the home folder itself")
		fs.BoolVar(&ws.noAgent, "no-agent", false, "offer the terminal only")
		fs.BoolVar(&ws.noTerminal, "no-terminal", false, "offer agent sessions only")
	case "join":
		o.register(fs)
	case "backup":
		fs.Var(&backupRoots, "root", "lend only this drive or folder (repeatable)")
	}
	pos, err := parseInterspersed(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return 2
	}
	dir := *stateDir
	if dir == "" {
		if dir, err = platform.NodeStateDir(); err != nil {
			fmt.Fprintf(stderr, "fridolin node: %v; pass --state-dir\n", err)
			return 1
		}
	}
	if dir, err = filepath.Abs(dir); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch sub {
	case "join":
		if len(pos) != 1 {
			fmt.Fprintf(stderr, "fridolin node join takes the join code\n\n%s", nodeUsage)
			return 2
		}
		return nodeJoin(pos[0], dir, o, stdout, stderr)
	case "backup":
		return nodeBackup(pos, backupRoots, dir, stdout, stderr)
	case "workstation":
		return nodeWorkstation(pos, ws, dir, stdout, stderr)
	case "run", "install", "start", "stop", "status", "uninstall", "rollback":
		if len(pos) != 0 {
			fmt.Fprintf(stderr, "fridolin node %s takes no arguments\n", sub)
			return 2
		}
	default:
		fmt.Fprintf(stderr, "unknown node command %q\n\n%s", sub, nodeUsage)
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	svc := nodeagent.ServiceConfig(runtime.GOOS, exe, dir)
	switch sub {
	case "run":
		a, err := nodeagent.Load(dir, version.String())
		if err != nil {
			fmt.Fprintf(stderr, "fridolin node run: %v\n", err)
			return 1
		}
		// What updates from the core replace, and restart with.
		a.Executable, a.Args, a.Environ = exe, os.Args[1:], os.Environ()
		if err := nodeagent.RunService(a, svc); err != nil {
			fmt.Fprintf(stderr, "fridolin node run: %v\n", err)
			return 1
		}
	case "status":
		fmt.Fprintf(stdout, "State dir: %s\n", dir)
		if cfg, err := nodeagent.LoadConfig(dir); err != nil {
			fmt.Fprintf(stdout, "Not joined: %v\n", err)
		} else {
			id := "?"
			if key, err := nodeagent.LoadKey(dir); err == nil {
				id = nodeagent.DeviceID(key)
			}
			fmt.Fprintf(stdout, "Node: %s (%s), core %s\nOffers: %s\n", cfg.Name, id, cfg.Core, cfg.Capabilities.Summary())
			updates := "offered to the core"
			if cfg.Update != nil && !*cfg.Update {
				updates = "off (\"update\": false in node.json)"
			}
			if fp := nodeagent.CoreKeyFingerprint(dir); fp != "" {
				updates += ", signed by the core's key " + fp
			} else {
				updates += ", the core's key is trusted at the first update"
			}
			fmt.Fprintf(stdout, "Updates: %s\nThis binary: %s (%s)\n", updates, exe, version.String())
		}
		st, err := nodeagent.ServiceStatus(svc)
		if err != nil {
			st = "unknown: " + err.Error()
		}
		fmt.Fprintf(stdout, "Service %s: %s\n", svc.Name, st)
	case "rollback":
		if err := nodeagent.Rollback(exe); err != nil {
			fmt.Fprintf(stderr, "fridolin node rollback: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Put %s.prev back in place of %s. Restart the agent: fridolin node stop, then fridolin node start.\n", exe, exe)
	default:
		if sub == "install" {
			if _, err := nodeagent.LoadConfig(dir); err != nil {
				fmt.Fprintf(stderr, "fridolin node install: %v\n", err)
				return 1
			}
		}
		if err := nodeagent.ControlService(svc, sub); err != nil {
			fmt.Fprintf(stderr, "fridolin node %s: %v\n", sub, err)
			return 1
		}
		fmt.Fprintf(stdout, "fridolin node %s: done (service %s)\n", sub, svc.Name)
	}
	return 0
}

// joinFlags are join's flags.
type joinFlags struct {
	core, name                      string
	allow, roots, backupRoots       multiFlag
	noExec, noHost, noNotify, noSvc bool
	backup                          bool
	workstation, allowHome          bool
	workRoots                       multiFlag
}

func (o *joinFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&o.core, "core", "", "the core's node port, host:port")
	fs.StringVar(&o.name, "name", "", "how the core shows this node")
	fs.Var(&o.allow, "allow", "allow a program (repeatable)")
	fs.Var(&o.roots, "root", "grant a folder, PATH or PATH:rw (repeatable)")
	fs.BoolVar(&o.noExec, "no-exec", false, "allow no commands")
	fs.BoolVar(&o.backup, "backup", false, "lend this machine's disks to the core's backups")
	fs.Var(&o.backupRoots, "backup-root", "lend only this drive or folder for backups (repeatable)")
	fs.BoolVar(&o.workstation, "workstation", false, "offer agent and terminal in the workstation roots")
	fs.Var(&o.workRoots, "workstation-root", "a folder for the workstation mode (repeatable)")
	fs.BoolVar(&o.allowHome, "allow-home", false, "let a workstation root be the home folder itself")
	fs.BoolVar(&o.noHost, "no-host", false, "do not report host facts")
	fs.BoolVar(&o.noNotify, "no-notify", false, "do not show notifications")
	fs.BoolVar(&o.noSvc, "no-service", false, "join only, without installing the service")
}

// capabilities builds what the node declares from the flags.
func (o joinFlags) capabilities(goos string) (nodeproto.Capabilities, error) {
	c := nodeagent.DefaultCapabilities(goos)
	c.Host, c.Notify = !o.noHost, !o.noNotify
	switch {
	case o.noExec:
		c.Exec = nil
	case len(o.allow) > 0:
		c.Exec = &nodeproto.ExecCap{}
		for _, a := range o.allow {
			c.Exec.Allow = append(c.Exec.Allow, parseAllow(a))
		}
	}
	for _, r := range o.roots {
		root := nodeproto.FSRoot{Path: r}
		if p, ok := strings.CutSuffix(r, ":rw"); ok {
			root = nodeproto.FSRoot{Path: p, Write: true}
		} else if p, ok := strings.CutSuffix(r, ":ro"); ok {
			root.Path = p
		}
		abs, err := filepath.Abs(root.Path)
		if err != nil {
			return c, err
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return c, fmt.Errorf("--root %s: not a folder", r)
		}
		root.Path = abs
		if c.FS == nil {
			c.FS = &nodeproto.FSCap{}
		}
		c.FS.Roots = append(c.FS.Roots, root)
	}
	if o.backup || len(o.backupRoots) > 0 {
		b, err := backupCap(o.backupRoots)
		if err != nil {
			return c, err
		}
		c.Backup = b
	}
	if o.workstation || len(o.workRoots) > 0 {
		roots := o.workRoots
		if len(roots) == 0 {
			for _, r := range o.roots {
				r, _ = strings.CutSuffix(r, ":rw")
				r, _ = strings.CutSuffix(r, ":ro")
				roots = append(roots, r)
			}
		}
		w, err := workstationCap(roots, o.allowHome)
		if err != nil {
			return c, err
		}
		c.Agent, c.Terminal = w, w
	}
	return c, c.Check()
}

// backupCap is the backup capability lending roots, every drive for none.
func backupCap(roots []string) (*nodeproto.BackupCap, error) {
	b := &nodeproto.BackupCap{}
	for _, r := range roots {
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, err
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("--backup-root %s: not a folder", r)
		}
		b.Roots = append(b.Roots, abs)
	}
	return b, nil
}

// nodeBackup turns the backup capability on or off in node.json.
func nodeBackup(pos []string, roots multiFlag, dir string, stdout, stderr io.Writer) int {
	if len(pos) != 1 || pos[0] != "on" && pos[0] != "off" {
		fmt.Fprintln(stderr, "fridolin node backup takes on or off")
		return 2
	}
	cfg, err := nodeagent.LoadConfig(dir)
	if err != nil {
		fmt.Fprintf(stderr, "fridolin node backup: %v\n", err)
		return 1
	}
	if pos[0] == "off" {
		cfg.Capabilities.Backup = nil
	} else {
		if cfg.Capabilities.Backup, err = backupCap(roots); err != nil {
			fmt.Fprintf(stderr, "fridolin node backup: %v\n", err)
			return 2
		}
	}
	if err := nodeagent.SaveConfig(dir, cfg); err != nil {
		fmt.Fprintf(stderr, "fridolin node backup: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "This node offers: %s\nRestart the agent (fridolin node stop, then fridolin node start) so the core hears it", cfg.Capabilities.Summary())
	if pos[0] == "on" {
		fmt.Fprint(stdout, "; then approve it on the core: fridolin nodes approve <id> --commands")
	}
	fmt.Fprintln(stdout, ".")
	return 0
}

// parseAllow reads PROG, PROG= or PROG=RE,RE.
func parseAllow(s string) nodeproto.ExecRule {
	name, pats, ok := strings.Cut(s, "=")
	if !ok {
		return nodeproto.ExecRule{Name: name}
	}
	r := nodeproto.ExecRule{Name: name, Args: []string{}}
	for p := range strings.SplitSeq(pats, ",") {
		if p != "" {
			r.Args = append(r.Args, p)
		}
	}
	return r
}

func nodeJoin(code, dir string, o joinFlags, stdout, stderr io.Writer) int {
	if o.core == "" {
		fmt.Fprintln(stderr, "fridolin node join: --core HOST:PORT is missing (the line fridolin nodes join-code printed has it)")
		return 2
	}
	caps, err := o.capabilities(runtime.GOOS)
	if err != nil {
		fmt.Fprintf(stderr, "fridolin node join: %v\n", err)
		return 2
	}
	name := o.name
	if name == "" {
		name, _ = os.Hostname()
		if i := strings.IndexByte(name, '.'); i > 0 {
			name = name[:i]
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	j, err := nodeagent.Join(ctx, nodeagent.JoinOptions{Dir: dir, Core: o.core, Code: code, Name: name, Capabilities: caps, Version: version.String()})
	if err != nil {
		fmt.Fprintf(stderr, "fridolin node join: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Joined the core at %s as %s (%s); this node waits for approval.\nIt offers: %s\nState dir: %s\nOn the core, approve it:\n\n  fridolin nodes approve %s\n  fridolin nodes approve %s --commands\n\n", o.core, name, j.ID, caps.Summary(), dir, j.ID, j.ID)
	if o.noSvc {
		fmt.Fprintf(stdout, "Start the agent with: fridolin node run --state-dir %q\n", dir)
		return 0
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	svc := nodeagent.ServiceConfig(runtime.GOOS, exe, dir)
	for _, action := range []string{"install", "start"} {
		if err := nodeagent.ControlService(svc, action); err != nil {
			fmt.Fprintf(stderr, "fridolin node join: %s the service: %v\nRun the agent yourself with: fridolin node run --state-dir %q\n", action, err, dir)
			return 1
		}
	}
	fmt.Fprintf(stdout, "The service %s runs the agent from %s.\n", svc.Name, exe)
	return 0
}

// workstationFlags are `node workstation on`'s flags.
type workstationFlags struct {
	roots                          multiFlag
	allowHome, noAgent, noTerminal bool
}

// workstationCap is the workstation capability in roots, each an existing
// folder a node may grant (nodeagent.CheckWorkRoot).
func workstationCap(roots []string, allowHome bool) (*nodeproto.WorkstationCap, error) {
	if len(roots) == 0 {
		return nil, errors.New("the workstation mode needs at least one folder: --workstation-root PATH (or --root PATH)")
	}
	w := &nodeproto.WorkstationCap{AllowHome: allowHome}
	for _, r := range roots {
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, err
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("workstation root %s: not a folder", r)
		}
		if err := nodeagent.CheckWorkRoot(abs, allowHome, ""); err != nil {
			return nil, fmt.Errorf("workstation root %w", err)
		}
		w.Roots = append(w.Roots, abs)
	}
	return w, nil
}

// nodeWorkstation turns the workstation mode on or off in node.json.
func nodeWorkstation(pos []string, o workstationFlags, dir string, stdout, stderr io.Writer) int {
	if len(pos) != 1 || pos[0] != "on" && pos[0] != "off" {
		fmt.Fprintln(stderr, "fridolin node workstation takes on or off")
		return 2
	}
	cfg, err := nodeagent.LoadConfig(dir)
	if err != nil {
		fmt.Fprintf(stderr, "fridolin node workstation: %v\n", err)
		return 1
	}
	cfg.Capabilities.Agent, cfg.Capabilities.Terminal = nil, nil
	if pos[0] == "on" {
		if o.noAgent && o.noTerminal {
			fmt.Fprintln(stderr, "fridolin node workstation: --no-agent and --no-terminal leave nothing to offer; use off")
			return 2
		}
		w, err := workstationCap(o.roots, o.allowHome)
		if err != nil {
			fmt.Fprintf(stderr, "fridolin node workstation: %v\n", err)
			return 2
		}
		if !o.noAgent {
			cfg.Capabilities.Agent = w
		}
		if !o.noTerminal {
			cfg.Capabilities.Terminal = w
		}
	}
	if err := nodeagent.SaveConfig(dir, cfg); err != nil {
		fmt.Fprintf(stderr, "fridolin node workstation: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "This node offers: %s\nRestart the agent (fridolin node stop, then fridolin node start) so the core hears it", cfg.Capabilities.Summary())
	if pos[0] == "on" {
		fmt.Fprint(stdout, "; then approve it on the core: fridolin nodes approve <id> --commands")
	}
	fmt.Fprintln(stdout, ".")
	return 0
}
