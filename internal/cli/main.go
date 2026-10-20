package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"os"
	"strings"

	"github.com/Icewater1337/fridolin/internal/app"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/controlcli"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/logx"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/toolcli"
	"github.com/Icewater1337/fridolin/internal/version"
)

const usage = `fridolin — talk to Claude Code over WhatsApp

Usage:
  fridolin [run] [flags]     start the bridge (default)
  fridolin open              open the dashboard in your browser
  fridolin rotate-token      replace the dashboard token and sign out every browser
  fridolin check [--route JID ...] [--redact]
                                           validate the configuration and show routing;
                                           --redact masks numbers, JIDs, emails and home paths;
                                           exits 3 when a configured brain cannot run
  fridolin doctor [--json] [--redact]      check what the bridge and the coming parts
                                           need on this machine (CLIs, Tailscale,
                                           Docker, host secrets, disk), each with a
                                           fix; exits 1 when a required check fails
  fridolin init [--claude-path PATH] [--data-dir DIR]
                                           write a starter config (if there is none)
  fridolin paths [--json]                  the config file and data dir in use, and
                                           where each came from
  fridolin service install|rollback|restart|status|uninstall
                                           run the bridge in the background (launchd or
                                           systemd) from a copy outside the checkout;
                                           fridolin service --help for more
  fridolin core start|stop|update|run|export|import|promote|status
                                           run the core in a container, move it
                                           between machines, show the core lease;
                                           fridolin core --help for more
  fridolin nodes [list|join-code|approve ID [--commands]|revoke ID|tags ID TAGS|call ID METHOD JSON]
                                           the core's fleet of nodes, through the running
                                           bridge; fridolin nodes help for more
  fridolin node join CODE --core HOST:PORT | run | install|start|stop|status|uninstall
                                           run this machine as a node of a core;
                                           fridolin node help for more
  fridolin version

Run by brains, inside a run (each prints JSON; -h shows its flags):
  fridolin history QUERY     search the brain's conversation history
  fridolin polls             the results of the polls the brain sent
  fridolin calendar COMMAND  the brain's Google Calendar: calendars, list,
                             create, free, delete
  fridolin mcp --run ID      the MCP server with Fridolin's tools for the run
                             (the bridge has each run start it)

Common flags:
  --config PATH     config file (default: $FRIDOLIN_CONFIG, else
                    ~/.fridolin/config.json)

Run flags:
  --no-whatsapp     dev mode: dashboard and brains without a WhatsApp connection
  --open            open the dashboard once the bridge is up
  --log-file PATH   the file stderr goes to, kept under 10 MiB (PATH.1 holds the
                    one before); the launchd agent passes it
  --container       inside the fridolin-core image: bind the dashboard's port on
                    the container's interfaces (Docker publishes it on the
                    host's 127.0.0.1 only); the image passes it
`

func Main() {
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	switch cmd {
	case "run":
		os.Exit(runCmd(args))
	case "check":
		checkCmd(args)
	case "doctor":
		os.Exit(doctorCmd(args, os.Stdout))
	case "open":
		openCmd(args)
	case "rotate-token":
		rotateTokenCmd(args)
	case "init":
		initCmd(args)
	case "paths":
		os.Exit(pathsCmd(args, os.Stdout, os.Stderr))
	case "mcp":
		os.Exit(mcpCmd(args))
	case "mcp-site":
		mcpSiteCmd()
	case "service":
		serviceCmd(args)
	case "core":
		os.Exit(coreCmd(args))
	case "nodes":
		os.Exit(nodesCmd(args, os.Stdout, os.Stderr))
	case "node":
		os.Exit(nodeCmd(args, os.Stdout, os.Stderr))
	case "history":
		os.Exit(toolcli.History(args, processToolIO()))
	case "polls":
		os.Exit(toolcli.Polls(args, processToolIO()))
	case "calendar":
		os.Exit(toolcli.Calendar(args, processToolIO()))
	case "version":
		fmt.Println(version.String())
	case "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}

// configFlagHelp describes --config for every command's flag set.
const configFlagHelp = "config file (default: $FRIDOLIN_CONFIG, else ~/.fridolin/config.json)"

// configPath picks the config file: the --config value, else
// FRIDOLIN_CONFIG, else config.json in the data dir. Never one in the
// working directory: a command run from some project folder would take that
// project's config.json, and a dashboard save would rewrite it. CONFIG_FILE,
// the variable's old name, is ignored, and said so.
func configPath(flagValue string) string {
	path, _ := configPathFrom(flagValue)
	return path
}

// configPathFrom is configPath, and says where the file came from.
func configPathFrom(flagValue string) (path, source string) {
	return app.ConfigPathFromPaths(flagValue, app.ConfigPaths())
}

// resolvedPaths are the config file and the data dir a command uses, both
// absolute, and where each came from.
type resolvedPaths struct {
	Config        string `json:"config"`
	ConfigSource  string `json:"config_source"`
	DataDir       string `json:"data_dir"`
	DataDirSource string `json:"data_dir_source"`
}

// resolvePaths finds the config file as configPath does and the data dir it
// names, as loading it would, without creating either.
func resolvePaths(configFlag string) (resolvedPaths, error) {
	home := app.HomeDir()
	if home == "" {
		return resolvedPaths{}, errors.New("getting home dir: $HOME is not set")
	}
	paths := app.ConfigPathsFor(home)
	path, source := app.ConfigPathFromPaths(configFlag, paths)
	// A missing file is a fresh install, which gets the default data dir.
	var file struct {
		DataDir string `json:"data_dir"`
	}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &file); err != nil {
			return resolvedPaths{}, fmt.Errorf("parsing config file %q: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return resolvedPaths{}, fmt.Errorf("reading config file %q: %w", path, err)
	}
	dir, dirSource := config.ResolveDataDir(file.DataDir, paths)
	return resolvedPaths{Config: app.AbsPath(path), ConfigSource: source, DataDir: app.AbsPath(dir), DataDirSource: dirSource}, nil
}

// pathsCmd prints the config file and the data dir the other commands use
// with this --config and environment, and where each came from, as
// name=value lines, or with --json as one object. setup.sh asks it rather
// than repeat the rules in shell.
func pathsCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("paths", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", configFlagHelp)
	asJSON := fs.Bool("json", false, "print one JSON object")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case err != nil:
		return 2 // the flag package has said what is wrong
	case fs.NArg() > 0:
		fmt.Fprintf(stderr, "fridolin paths takes no arguments, got %q\n", fs.Args())
		return 2
	}
	p, err := resolvePaths(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "fridolin paths: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(p); err != nil {
			fmt.Fprintf(stderr, "fridolin paths: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "config=%s\nconfig_source=%s\ndata_dir=%s\ndata_dir_source=%s\n", p.Config, p.ConfigSource, p.DataDir, p.DataDirSource)
	return 0
}

// runOptions are the run command's flags.
type runOptions struct {
	config     string
	noWhatsApp bool
	open       bool
	logFile    string
	container  bool
}

// parseRunFlags reads the run command's flags, which are also what
// `fridolin` without a command takes. -h and --help print the usage text,
// so `fridolin -h` shows every command and not only these flags.
func parseRunFlags(args []string, output io.Writer) (runOptions, error) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.Usage = func() { fmt.Fprint(fs.Output(), usage) }
	var o runOptions
	fs.StringVar(&o.config, "config", "", configFlagHelp)
	fs.BoolVar(&o.noWhatsApp, "no-whatsapp", false, "dev mode: run without connecting to WhatsApp")
	fs.BoolVar(&o.open, "open", false, "open the dashboard in the browser once started")
	fs.StringVar(&o.logFile, "log-file", "", "the file stderr goes to, rotated at 10 MiB (the launchd agent passes it)")
	fs.BoolVar(&o.container, "container", false, "inside the fridolin-core container: bind the dashboard's port on the container's interfaces, which Docker publishes on the host's loopback only")
	return o, fs.Parse(args)
}

func runCmd(args []string) int {
	opts, err := parseRunFlags(args, os.Stderr)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case err != nil:
		return 2 // the flag package has said what is wrong
	}

	// Under launchd stderr is a file, which --log-file keeps small.
	var console io.Writer = os.Stderr
	var logErr error
	if opts.logFile != "" {
		var lf *rotatingLog
		if lf, logErr = openRotatingLog(opts.logFile, int(os.Stderr.Fd()), maxLogBytes); logErr == nil {
			console = lf
		}
	}
	inputs := app.ConfigOptions()
	ring, path := runLogWithPaths(console, opts.config, inputs.Paths)
	if logErr != nil {
		// Once the log reaches the dashboard too.
		log.Printf("%v; logging to stderr without rotating it", logErr)
	}
	if err := config.EnsureFile(path, inputs.Paths, inputs.GOOS); err != nil {
		log.Printf("creating config: %v", err)
		return 1
	}
	cfgs, err := config.Open(path, inputs)
	if err != nil {
		log.Printf("loading config: %v", err)
		return 1
	}
	cfg := cfgs.Get()
	logDataDir(cfgs)

	// A signal and the dashboard's restart both end ctx, and serve stops
	// the bridge then; the restart's cause says to run it again.
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	// NewBridge takes the data dir's instance lock, so dev mode with the live
	// data dir is refused next to the running bridge too.
	appOpts := app.Options{Logger: slog.Default(), Config: cfgs, NoWhatsApp: opts.noWhatsApp, Logs: ring}
	if opts.container {
		appOpts.Listen = containerListen
	}
	bridge, err := app.New(appOpts)
	if err != nil {
		log.Printf("creating bridge: %v", err)
		return 1
	}
	if cfg.AgentProvider == "codex" {
		go app.LogCodexCLI(cfg.CodexPath, cfg.ChildEnvPassthrough)
	} else {
		go app.LogClaudeCLI(cfg.ClaudePath, cfg.ChildEnvPassthrough)
	}

	signalled, stopSignals := stopOnSignals(cancel, bridge.KillRuns)
	defer stopSignals()

	if platform.IsTerminal(os.Stdout) && cfg.HTTPEnabled && !cfg.Dashboard.Disabled {
		// Only on an interactive terminal: even a code that works once
		// must not end up in a log file.
		code, _ := bridge.NewLoginCode()
		fmt.Printf("\n  Dashboard: %s\n  (the link works once, within a minute; after that, run: fridolin open)\n\n", httpapi.LoginURL(cfg.HTTPListen, code))
	}
	if opts.open && cfg.HTTPEnabled {
		code, _ := bridge.NewLoginCode()
		go func() { _ = platform.OpenBrowser(httpapi.LoginURL(cfg.HTTPListen, code)) }()
	}

	return Serve(ctx, bridge, signalled, execSelf)
}

// containerListen binds the port of http_listen, which stays a loopback
// address, on every interface of the container: Docker forwards a
// published port to the container's own address, not to its loopback.
// compose.yaml publishes it on the host's 127.0.0.1 only, and the Host
// guard still wants 127.0.0.1 or localhost, so what reaches the dashboard
// is what reached it before: this machine, now through Docker.
func containerListen(network, addr string) (net.Listener, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	return net.Listen(network, net.JoinHostPort("", port))
}

// execSelf replaces this process with the program at argv0, a variable so
// that a test can see a restart without one.
var execSelf = platform.Exec

// serve runs b until ctx ends or Start fails, then stops it. When ctx
// ended for a restart, and no signal asked for a stop meanwhile
// (signalled), it then runs the bridge again in place: the same binary,
// arguments and environment, in this process, which Stop has given the
// instance lock back for. It returns main's exit code: 0 after a stop, 1
// when Start or the restart failed.
func Serve(ctx context.Context, b *app.App, signalled func() bool, execSelf func(string, []string, []string) error) int {
	err := b.Run(ctx)
	if err != nil && !errors.Is(err, app.ErrRestart) {
		log.Printf("bridge stopped: %v", err)
	}
	switch {
	case err != nil && !errors.Is(err, app.ErrRestart):
		return 1
	case !errors.Is(err, app.ErrRestart):
		return 0
	case signalled():
		log.Printf("A signal came during the restart: staying stopped")
		return 0
	}
	exe, err := os.Executable()
	if err == nil {
		err = execSelf(exe, os.Args, os.Environ())
	}
	// execSelf returns only when it failed. The service manager starts
	// the bridge again after an exit 1.
	log.Printf("restart failed: %v", err)
	return 1
}

// runLog sends the log to console and to a ring the dashboard shows, logs
// the version, then picks the config file and names it and where it came
// from. In that order: a warning configPath logs, such as the one about
// CONFIG_FILE, must reach the dashboard's log too, the only one many people
// read, and the version line comes first even when the bridge then refuses
// to start (no config, one that does not load, another bridge on the data
// dir).
func runLog(console io.Writer, configFlag string) (*logx.Ring, string) {
	return runLogWithPaths(console, configFlag, app.ConfigPaths())
}

func runLogWithPaths(console io.Writer, configFlag string, paths config.Paths) (*logx.Ring, string) {
	ring := logx.NewRing(app.LogRingLines)
	slog.SetDefault(logx.NewLogger(io.MultiWriter(console, ring)))
	log.Printf("Starting %s", version.Line())
	path, source := app.ConfigPathFromPaths(configFlag, paths)
	log.Printf("Config file: %s (%s)", app.AbsPath(path), source)
	return ring, path
}

// logDataDir names the data dir the bridge runs on, and with it the
// WhatsApp session, and where it came from: without data_dir in the
// config, which folders exist decides it.
func logDataDir(cfgs *config.Store) {
	home := cfgs.Paths().Home
	dir := cfgs.Get().DataDir
	log.Printf("Data dir: %s (%s)", app.AbsPath(dir), config.DataDirSource(cfgs.FileCopy().DataDir, dir, home))
}

func initCmd(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	cfgPath := fs.String("config", "", configFlagHelp)
	claudePath := fs.String("claude-path", "", "path to the Claude Code CLI")
	dataDir := fs.String("data-dir", "", "data folder to write into the config (default ~/.fridolin, or the folder from before the rename when only that exists)")
	_ = fs.Parse(args)
	inputs := app.ConfigOptions()
	path, _ := app.ConfigPathFromPaths(*cfgPath, inputs.Paths)
	if fsx.Exists(path) {
		fmt.Printf("%s already exists — leaving it alone.\n", path)
		return
	}
	if err := config.WriteStarter(path, *claudePath, *dataDir, inputs.Paths, inputs.GOOS); err != nil {
		log.Fatalf("writing %s: %v", path, err)
	}
	if _, err := config.Load(path, inputs); err != nil {
		log.Fatalf("the starter config does not load: %v", err)
	}
}

func openCmd(args []string) {
	fs := flag.NewFlagSet("open", flag.ExitOnError)
	cfgPath := fs.String("config", "", configFlagHelp)
	printOnly := fs.Bool("print", false, "print the URL instead of opening it")
	_ = fs.Parse(args)
	inputs := app.ConfigOptions()
	path, _ := app.ConfigPathFromPaths(*cfgPath, inputs.Paths)

	cfg, err := config.LoadExisting(path, inputs)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}
	if !cfg.HTTPEnabled || cfg.Dashboard.Disabled {
		log.Fatalf("the dashboard is turned off in %s", path)
	}
	url, err := controlcli.DashboardLink(cfg)
	if err != nil {
		log.Fatal(err)
	}
	if *printOnly {
		fmt.Println(url)
		return
	}
	if err := platform.OpenBrowser(url); err != nil {
		fmt.Println(url)
		return
	}
	fmt.Printf("Opened the dashboard at http://%s/\n", cfg.HTTPListen)
}

// rotateTokenCmd replaces the dashboard token and signs out every browser:
// through the running bridge, or, when none runs, in the data dir itself.
func rotateTokenCmd(args []string) {
	fs := flag.NewFlagSet("rotate-token", flag.ExitOnError)
	cfgPath := fs.String("config", "", configFlagHelp)
	_ = fs.Parse(args)
	cfg, err := app.LoadExistingConfig(configPath(*cfgPath))
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}
	if err := controlcli.RotateToken(cfg); err != nil {
		log.Fatal(err)
	}
	fmt.Println("The dashboard token was replaced and every browser signed out. Open the dashboard again with: fridolin open")
}

func checkCmd(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	var o app.CheckOptions
	var routes multiFlag
	fs.StringVar(&o.Config, "config", "", configFlagHelp)
	fs.Var(&routes, "route", "show which brain handles this chat JID (repeatable)")
	fs.StringVar(&o.SandboxFor, "print-sandbox", "", "print the sandbox profile for this brain id")
	fs.BoolVar(&o.Redact, "redact", false, "mask phone numbers, JIDs and email addresses, and write ~ for the home folder, to paste the output into a bug report")
	_ = fs.Parse(args)
	o.Routes = routes
	if err := app.WriteCheck(os.Stdout, o); err != nil {
		if errors.Is(err, app.ErrBrainsLeftOut) {
			log.Print(err)
			os.Exit(checkBrainsLeftOut)
		}
		log.Fatal(err)
	}
}

// doctorCmd runs `fridolin doctor` and returns its exit status: 0 when
// every required check passes, 1 when one fails or the report cannot be
// written, 2 for a bad flag.
func doctorCmd(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	var o app.DoctorOptions
	fs.StringVar(&o.Config, "config", "", configFlagHelp)
	fs.BoolVar(&o.JSON, "json", false, "print the checks as JSON")
	fs.BoolVar(&o.Redact, "redact", false, "mask phone numbers, JIDs and email addresses, and write ~ for the home folder, to paste the output into a bug report")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(fs.Output(), "doctor takes no arguments, got %q\n", fs.Args())
		return 2
	}
	if err := app.WriteDoctor(stdout, o); err != nil {
		if !errors.Is(err, app.ErrDoctorFailed) {
			log.Print(err)
		}
		return 1
	}
	return 0
}

// checkBrainsLeftOut is check's exit status when the config loads but a
// brain in it will not run, so that setup.sh stops before it installs a
// bridge that leaves the brain out; 1 is for a config that does not load.
const checkBrainsLeftOut = 3

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }
