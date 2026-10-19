package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/logx"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/textx"
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
  fridolin init [--claude-path PATH] [--data-dir DIR]
                                           write a starter config (if there is none)
  fridolin paths [--json]                  the config file and data dir in use, and
                                           where each came from
  fridolin service install|rollback|restart|status|uninstall
                                           run the bridge in the background (launchd or
                                           systemd) from a copy outside the checkout;
                                           fridolin service --help for more
  fridolin version

Run by brains, inside a run (each prints JSON; -h shows its flags):
  fridolin history QUERY     search the brain's conversation history
  fridolin polls             the results of the polls the brain sent
  fridolin calendar COMMAND  the brain's Google Calendar: calendars, list,
                             create, free, delete

Common flags:
  --config PATH     config file (default: $FRIDOLIN_CONFIG, else
                    ~/.fridolin/config.json)

Run flags:
  --no-whatsapp     dev mode: dashboard and brains without a WhatsApp connection
  --open            open the dashboard once the bridge is up
  --log-file PATH   the file stderr goes to, kept under 10 MiB (PATH.1 holds the
                    one before); the launchd agent passes it
`

func main() {
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	switch cmd {
	case "run":
		runCmd(args)
	case "check":
		checkCmd(args)
	case "open":
		openCmd(args)
	case "rotate-token":
		rotateTokenCmd(args)
	case "init":
		initCmd(args)
	case "paths":
		os.Exit(pathsCmd(args, os.Stdout, os.Stderr))
	case "mcp-site":
		mcpSiteCmd()
	case "service":
		serviceCmd(args)
	case "history":
		os.Exit(historyCmd(args, processToolIO()))
	case "polls":
		os.Exit(pollsCmd(args, processToolIO()))
	case "calendar":
		os.Exit(calendarCmd(args, processToolIO()))
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

// Where the config file came from, as the log and `fridolin paths` name it.
const (
	configFromFlag    = "--config"
	configFromEnv     = "FRIDOLIN_CONFIG"
	configFromDefault = "default"
)

// configPathFrom is configPath, and says where the file came from.
func configPathFrom(flagValue string) (path, source string) {
	return configPathFromPaths(flagValue, configPaths())
}

func configPathFromPaths(flagValue string, paths config.Paths) (path, source string) {
	if old := os.Getenv("CONFIG_FILE"); old != "" {
		log.Printf("warning: CONFIG_FILE=%s is ignored: set FRIDOLIN_CONFIG instead, or pass --config", old)
	}
	if flagValue != "" {
		return flagValue, configFromFlag
	}
	if p := os.Getenv("FRIDOLIN_CONFIG"); p != "" {
		return p, configFromEnv
	}
	return config.DefaultPath(paths), configFromDefault
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
	home := homeDir()
	if home == "" {
		return resolvedPaths{}, errors.New("getting home dir: $HOME is not set")
	}
	paths := configPathsFor(home)
	path, source := configPathFromPaths(configFlag, paths)
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
	return resolvedPaths{Config: absPath(path), ConfigSource: source, DataDir: absPath(dir), DataDirSource: dirSource}, nil
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
	return o, fs.Parse(args)
}

func runCmd(args []string) {
	opts, err := parseRunFlags(args, os.Stderr)
	switch {
	case errors.Is(err, flag.ErrHelp):
		os.Exit(0)
	case err != nil:
		os.Exit(2) // the flag package has said what is wrong
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
	inputs := configOptions()
	ring, path := runLogWithPaths(console, opts.config, inputs.Paths)
	if logErr != nil {
		// Once the log reaches the dashboard too.
		log.Printf("%v; logging to stderr without rotating it", logErr)
	}
	if err := config.EnsureFile(path, inputs.Paths, inputs.GOOS); err != nil {
		log.Fatalf("creating config: %v", err)
	}
	cfgs, err := config.Open(path, inputs)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}
	cfg := cfgs.Get()
	logDataDir(cfgs)

	// A signal and the dashboard's restart both end ctx, and serve stops
	// the bridge then; the restart's cause says to run it again.
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	// NewBridge takes the data dir's instance lock, so dev mode with the live
	// data dir is refused next to the running bridge too.
	bridge, err := NewBridge(cfgs, BridgeOptions{NoWhatsApp: opts.noWhatsApp, Logs: ring, Restart: func() { cancel(errRestart) }})
	if err != nil {
		log.Fatalf("creating bridge: %v", err)
	}
	if cfg.AgentProvider == "codex" {
		go func() { log.Printf("Codex CLI: %+v", codexInfo(cfg.CodexPath, cfg.ChildEnvPassthrough)) }()
	} else {
		go logClaudeCLI(cfg.ClaudePath, cfg.ChildEnvPassthrough)
	}

	signalled := stopOnSignals(cancel, bridge.brainMgr)

	if platform.IsTerminal(os.Stdout) && cfg.HTTPEnabled() && !cfg.Dashboard.Disabled {
		// Only on an interactive terminal: even a code that works once
		// must not end up in a log file.
		code, _ := bridge.sessions.newCode()
		fmt.Printf("\n  Dashboard: %s\n  (the link works once, within a minute; after that, run: fridolin open)\n\n", httpapi.LoginURL(cfg.HTTPListen, code))
	}
	if opts.open && cfg.HTTPEnabled() {
		code, _ := bridge.sessions.newCode()
		go func() { _ = platform.OpenBrowser(httpapi.LoginURL(cfg.HTTPListen, code)) }()
	}

	os.Exit(serve(ctx, bridge, signalled))
}

// errRestart is the cause runCmd's context ends with when the dashboard
// asks for a restart (apiRestart): serve stops the bridge, then runs it
// again in place.
var errRestart = errors.New("restart requested")

// execSelf replaces this process with the program at argv0, a variable so
// that a test can see a restart without one.
var execSelf = platform.Exec

// serve runs b until ctx ends or Start fails, then stops it. When ctx
// ended for a restart, and no signal asked for a stop meanwhile
// (signalled), it then runs the bridge again in place: the same binary,
// arguments and environment, in this process, which Stop has given the
// instance lock back for. It returns main's exit code: 0 after a stop, 1
// when Start or the restart failed.
func serve(ctx context.Context, b *Bridge, signalled func() bool) int {
	err := b.Start(ctx)
	if err != nil {
		log.Printf("bridge stopped: %v", err)
	}
	b.Stop()
	switch {
	case err != nil:
		return 1
	case !errors.Is(context.Cause(ctx), errRestart):
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

// stopOnSignals stops the bridge on SIGINT or SIGTERM. The first ends ctx
// through cancel, and serve stops the bridge in order, which lets the
// brains' runs finish for a while (shutdownGrace); a restart under way
// stops instead of running the bridge again. A second while that stop is
// under way stops the runs still going at once and exits 1, leaving the
// rest of the stop undone. The function returned reports whether a signal
// came.
func stopOnSignals(cancel context.CancelCauseFunc, brains *BrainManager) func() bool {
	var got atomic.Bool
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		got.Store(true)
		log.Printf("Received signal %s, shutting down… (send it again to stop at once)", sig)
		cancel(fmt.Errorf("received signal %s", sig))
		sig = <-sigCh
		log.Printf("Received signal %s again: stopping the runs still going and exiting", sig)
		brains.KillRuns()
		os.Exit(1)
	}()
	return got.Load
}

// runLog sends the log to console and to a ring the dashboard shows, logs
// the version, then picks the config file and names it and where it came
// from. In that order: a warning configPath logs, such as the one about
// CONFIG_FILE, must reach the dashboard's log too, the only one many people
// read, and the version line comes first even when the bridge then refuses
// to start (no config, one that does not load, another bridge on the data
// dir).
func runLog(console io.Writer, configFlag string) (*logx.Ring, string) {
	return runLogWithPaths(console, configFlag, configPaths())
}

func runLogWithPaths(console io.Writer, configFlag string, paths config.Paths) (*logx.Ring, string) {
	ring := logx.NewRing(logRingLines)
	log.SetOutput(io.MultiWriter(console, ring))
	log.Printf("Starting %s", version.Line())
	path, source := configPathFromPaths(configFlag, paths)
	log.Printf("Config file: %s (%s)", absPath(path), source)
	return ring, path
}

// logDataDir names the data dir the bridge runs on, and with it the
// WhatsApp session, and where it came from: without data_dir in the
// config, which folders exist decides it.
func logDataDir(cfgs *config.Store) {
	home := cfgs.Paths().Home
	dir := cfgs.Get().DataDir
	log.Printf("Data dir: %s (%s)", absPath(dir), config.DataDirSource(cfgs.FileCopy().DataDir, dir, home))
}

func initCmd(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	cfgPath := fs.String("config", "", configFlagHelp)
	claudePath := fs.String("claude-path", "", "path to the Claude Code CLI")
	dataDir := fs.String("data-dir", "", "data folder to write into the config (default ~/.fridolin, or the folder from before the rename when only that exists)")
	_ = fs.Parse(args)
	inputs := configOptions()
	path, _ := configPathFromPaths(*cfgPath, inputs.Paths)
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
	inputs := configOptions()
	path, _ := configPathFromPaths(*cfgPath, inputs.Paths)

	cfg, err := config.LoadExisting(path, inputs)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}
	if !cfg.HTTPEnabled() || cfg.Dashboard.Disabled {
		log.Fatalf("the dashboard is turned off in %s", path)
	}
	url, err := dashboardLink(cfg)
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

// dashboardLink is the address `fridolin open` opens for cfg's bridge. It
// carries a code that works once, within a minute, not the token: a
// browser keeps every address it opened. A bridge older than this binary
// has no login codes, and its dashboard took the token in the link; there
// is no falling back to that, since `fridolin open --print` is what an
// agent runs and pastes into a chat.
func dashboardLink(cfg *config.Config) (string, error) {
	tokenPath := filepath.Join(cfg.DataDir, "dashboard.token")
	token, err := httpapi.ReadToken(cfg.DataDir)
	if err != nil {
		return "", fmt.Errorf("no dashboard token yet — start the bridge first (%w)", err)
	}
	code, err := askLoginCode(cfg.HTTPListen, token)
	if errors.Is(err, errOlderBridge) {
		return "", fmt.Errorf("%w. Restart it on this build (./setup.sh installs it and restarts the bridge), or open its dashboard with the fridolin that runs it, or at http://%s/ with the token from %s", err, cfg.HTTPListen, tokenPath)
	}
	if err != nil {
		return "", err
	}
	return httpapi.LoginURL(cfg.HTTPListen, code), nil
}

// rotateTokenCmd replaces the dashboard token and signs out every browser:
// through the running bridge, or, when none runs, in the data dir itself.
func rotateTokenCmd(args []string) {
	fs := flag.NewFlagSet("rotate-token", flag.ExitOnError)
	cfgPath := fs.String("config", "", configFlagHelp)
	_ = fs.Parse(args)
	cfg, err := loadExistingConfig(configPath(*cfgPath))
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}
	if err := rotateToken(cfg); err != nil {
		log.Fatal(err)
	}
	fmt.Println("The dashboard token was replaced and every browser signed out. Open the dashboard again with: fridolin open")
}

// rotateToken does rotateTokenCmd's work for cfg.
func rotateToken(cfg *config.Config) error {
	token, err := httpapi.ReadToken(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("no dashboard token yet — start the bridge first (%w)", err)
	}
	if cfg.HTTPEnabled() {
		var resp struct{ Status string }
		err := callBridge(cfg.HTTPListen, token, "/api/token/rotate", &resp)
		if errors.Is(err, errOlderBridge) && cfg.Dashboard.Disabled {
			// It serves no dashboard API, so it answers 404 whatever its
			// build. Nothing takes the token until the dashboard is on.
			return fmt.Errorf("the dashboard is turned off (dashboard.disabled), so the bridge at http://%s cannot replace the token while it runs. Run fridolin rotate-token while the bridge is stopped, or once the dashboard is on and the bridge restarted", cfg.HTTPListen)
		}
		if errors.Is(err, errOlderBridge) {
			// It keeps the token in memory and has no way to replace it.
			return fmt.Errorf("%w, so the token was not replaced. Restart it on this build (./setup.sh installs it and restarts the bridge), then run fridolin rotate-token again", err)
		}
		if !errors.Is(err, errBridgeDown) {
			return err
		}
	}
	// No bridge answers. The instance lock says whether one runs anyway
	// (with its server off, say); only without one are the files changed
	// here, since a running bridge keeps the token in memory.
	lock, err := platform.AcquireLock(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("the bridge runs but does not answer, so the token was not replaced: %w", err)
	}
	defer lock.Release()
	tok, err := httpapi.LoadDashboardToken(cfg.DataDir)
	if err != nil {
		return err
	}
	if err := tok.Rotate(); err != nil {
		return fmt.Errorf("writing a new token: %w", err)
	}
	sessions, err := loadDashSessions(cfg.DataDir)
	if err != nil {
		return err
	}
	return sessions.dropAll()
}

// checkOptions are the check command's flags.
type checkOptions struct {
	config     string
	routes     []string
	sandboxFor string
	redact     bool
}

func checkCmd(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	var o checkOptions
	var routes multiFlag
	fs.StringVar(&o.config, "config", "", configFlagHelp)
	fs.Var(&routes, "route", "show which brain handles this chat JID (repeatable)")
	fs.StringVar(&o.sandboxFor, "print-sandbox", "", "print the sandbox profile for this brain id")
	fs.BoolVar(&o.redact, "redact", false, "mask phone numbers, JIDs and email addresses, and write ~ for the home folder, to paste the output into a bug report")
	_ = fs.Parse(args)
	o.routes = routes
	if err := writeCheck(os.Stdout, o); err != nil {
		if errors.Is(err, errBrainsLeftOut) {
			log.Print(err)
			os.Exit(checkBrainsLeftOut)
		}
		log.Fatal(err)
	}
}

// checkBrainsLeftOut is check's exit status when the config loads but a
// brain in it will not run, so that setup.sh stops before it installs a
// bridge that leaves the brain out; 1 is for a config that does not load.
const checkBrainsLeftOut = 3

// errBrainsLeftOut is what check ends with when an approved brain cannot
// run: its folder is missing or cannot hold a brain, or it clashes with
// another. The bridge runs the others, and the issue is shown on the
// Brains page, but that was easy to miss: the DM brain left out like that
// used to hand your DMs to the default brain.
var errBrainsLeftOut = errors.New("cannot run")

// leftOutBrains returns the ids of the approved brains issues leaves out,
// each once, in their order.
func leftOutBrains(issues []BrainIssue) []string {
	var ids []string
	for _, is := range issues {
		if is.leavesOut() && !slices.Contains(ids, is.ID) {
			ids = append(ids, is.ID)
		}
	}
	return ids
}

// writeCheck writes check's report to w. With o.redact the report, what
// is logged meanwhile and the error that ends it, if one does, are
// redacted: the output then goes into a public bug report as it is.
func writeCheck(w io.Writer, o checkOptions) error {
	inputs := configOptions()
	var red *redactor
	if o.redact {
		red = newRedactor(inputs.Paths.Home)
		red.addChats(o.routes...)
		// The log entries of loading the config name paths, and the
		// allowed chats it finds odd, as they are written.
		prev := log.Writer()
		log.SetOutput(redactingWriter{prev, red})
		defer log.SetOutput(prev)
	}
	path, _ := configPathFromPaths(o.config, inputs.Paths)
	if red != nil {
		red.addConfig(path)
	}
	cfg, err := config.LoadExisting(path, inputs)
	if err != nil {
		return red.err(fmt.Errorf("loading config: %w", err))
	}
	if red == nil {
		return checkReport(w, path, cfg, o)
	}
	red.addChats(cfg.AllowedNumbers...)
	red.addGroups(cfg.AllowedGroups...)
	red.addGroupMembers(cfg.GroupMembers)
	var buf bytes.Buffer
	err = checkReport(&buf, path, cfg, o)
	if _, werr := io.WriteString(w, red.text(buf.String())); werr != nil && err == nil {
		err = werr
	}
	return red.err(err)
}

// checkReport writes what check says of cfg, the config loaded from path.
func checkReport(w io.Writer, path string, cfg *config.Config, o checkOptions) error {
	// Before even the version probe: it runs outside the sandbox, so a
	// Claude planted in a brain's PATH folder must not be selected.
	ownPATH := setBridgePATH(cfg)
	fmt.Fprintf(w, "Config OK: %s\n", path)
	if cfg.AgentProvider == "codex" {
		st := codexInfo(cfg.CodexPath, cfg.ChildEnvPassthrough)
		fmt.Fprintf(w, "  codex:     %s (found: %t, %s), model %s, effort %s\n", st.Path, st.Found, st.Version, cfg.CodexModel, cfg.CodexEffort)
	} else {
		fmt.Fprintf(w, "  claude:    %s\n", claudeSummary(cfg.ClaudePath, cfg.ChildEnvPassthrough))
	}
	fmt.Fprintf(w, "  data dir:  %s\n", cfg.DataDir)
	fmt.Fprintf(w, "  dashboard: http://%s/\n", cfg.HTTPListen)
	fmt.Fprintf(w, "  DM allow:  %s\n", dmAllowSummary(cfg))
	fmt.Fprintf(w, "  groups:    %v\n", cfg.AllowedGroups)
	for _, g := range cfg.AllowedGroups {
		fmt.Fprintf(w, "  heard in %s: %s\n", g, groupHeardSummary(cfg, g))
	}
	trig, _ := json.Marshal(cfg.Triggers)
	fmt.Fprintf(w, "  triggers:  %s\n", trig)
	// With the same PATH as startup, the brains and profiles match what
	// the bridge would run.
	specs, issues := ResolveBrains(cfg)
	if len(specs) == 0 {
		fmt.Fprintln(w, "  brains:    none yet — add one in the dashboard")
	}
	for _, s := range specs {
		m := s.Manifest
		fmt.Fprintf(w, "  brain %-14s → %s (dms=%v default=%v groups=%v)\n", m.ID, s.Dir, m.Routing.HandlesDMs, m.Routing.Default, m.Routing.Groups)
	}
	for _, is := range issues {
		fmt.Fprintf(w, "  ! %s %s: %s\n", is.Kind, is.Path, is.Detail)
	}

	if len(o.routes) == 0 && o.sandboxFor == "" {
		return leftOutError(issues)
	}
	mgr := &BrainManager{brains: map[string]*Brain{}, byGroup: map[string]*Brain{}, profiler: NewSandboxProfiler(nil, cfg), hooks: &BrainHooks{}}
	mgr.profiler.SetConfigFile(path)
	if err := mgr.reload(specs, issues, cfg, ownPATH); err != nil {
		return err
	}
	for _, jid := range o.routes {
		isGroup := chat.IsGroupJID(jid)
		allowed := ""
		if isGroup && !slices.Contains(cfg.AllowedGroups, jid) {
			allowed = " (not in allowed_groups: ignored)"
		}
		if !isGroup && !numberAllowed(cfg, chat.NormalizeJID(jid)) {
			allowed = " (not in allowed_numbers: ignored)"
		}
		if br := mgr.BrainFor(jid, isGroup); br != nil {
			fmt.Fprintf(w, "  route %s → %s%s\n", jid, br.ID(), allowed)
		} else if id := mgr.HeldFor(jid, isGroup); id != "" {
			fmt.Fprintf(w, "  route %s → no brain: %s handles it but cannot run%s\n", jid, id, allowed)
		} else {
			fmt.Fprintf(w, "  route %s → no brain%s\n", jid, allowed)
		}
	}
	if o.sandboxFor != "" {
		profile, err := mgr.profiler.Profile(o.sandboxFor)
		if err != nil {
			return fmt.Errorf("sandbox profile: %w", err)
		}
		fmt.Fprintln(w)
		fmt.Fprint(w, profile)
	}
	return leftOutError(issues)
}

// leftOutError is errBrainsLeftOut for the brains issues leaves out, or nil.
func leftOutError(issues []BrainIssue) error {
	switch ids := leftOutBrains(issues); len(ids) {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("brain %s %w; see the issue above", ids[0], errBrainsLeftOut)
	default:
		return fmt.Errorf("brains %s %w; see the issues above", strings.Join(ids, ", "), errBrainsLeftOut)
	}
}

// claudeSummary is the CLI brains run and its version, with a warning when
// it is missing or older than the release the bridge was tested with.
func claudeSummary(path string, passthrough []string) string {
	st := claudeInfo(path, passthrough)
	switch {
	case !st.Found:
		return path + " — not found, so brains cannot run"
	case st.Version == "":
		return st.Path + " (`--version` gave no answer)"
	}
	s := st.Path + " (" + st.Version + ")"
	if claudecli.VersionWarning(st) != "" {
		s += " — older than " + claudecli.TestedClaudeVersion + ", the oldest release tested; update it (claude update)"
	}
	return s
}

// dmAllowSummary says who may DM the bridge. A bare empty list would read
// as "nobody" even when allow_any_dm lets everyone in.
func dmAllowSummary(cfg *config.Config) string {
	switch {
	case cfg.AnyoneCanDM():
		return "anyone (allow_any_dm is true and allowed_numbers is empty)"
	case len(cfg.AllowedNumbers) == 0:
		return "nobody (allowed_numbers is empty)"
	}
	return fmt.Sprint(cfg.AllowedNumbers)
}

// redactor masks what check --redact must not show: phone numbers down to
// the country code and the last two digits, group JIDs and LIDs down to
// their last two digits, email addresses as maskEmail does, and the home
// folder, written as ~ in every path. Two numbers or groups still tell
// apart, and paths still say where under the home folder they are.
type redactor struct {
	// The configured chats, which are printed without a JID too, each
	// with its masked form; the longest first, since one may hold another.
	chats []redactedChat
	homes []*regexp.Regexp
}

type redactedChat struct{ value, masked string }

// jidExpr is a WhatsApp JID or LID: its user part, with a device or
// creator suffix, then the server.
const jidExpr = `([0-9]+)((?:[-.:][0-9]+)*)@(s\.whatsapp\.net|g\.us|lid)`

// redactPattern finds a JID or LID, a number in international format, or
// an email address.
var redactPattern = regexp.MustCompile(jidExpr + `\b|\+[0-9]{7,15}\b|[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)

// trailingJID finds a JID at the end of what redactPattern took for an
// email address: one right after a name, "brain-120363…@g.us" say, which
// the address's pattern matches from the name's start.
var trailingJID = regexp.MustCompile(jidExpr + `$`)

// newRedactor redacts home, as given and with its symlinks resolved, in
// each way a path is written: plain, in a regular expression, and quoted
// in a sandbox profile.
func newRedactor(home string) *redactor {
	r := &redactor{}
	if home == "" || home == string(filepath.Separator) {
		return r // every path would match
	}
	var spellings []string
	for _, h := range []string{home, fsx.Resolve(home)} {
		spellings = append(spellings, h, textx.EscapeSBPL(h), regexp.QuoteMeta(h), textx.EscapeSBPL(regexp.QuoteMeta(h)))
	}
	// The longest first, since one may begin with another; equal ones next
	// to each other, for Compact.
	slices.SortFunc(spellings, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	for _, h := range slices.Compact(spellings) {
		// Only the folder itself, not a sibling whose name begins like it.
		r.homes = append(r.homes, regexp.MustCompile(regexp.QuoteMeta(h)+`([^A-Za-z0-9._-]|$)`))
	}
	return r
}

// addConfig adds the allowed numbers and groups and group_members of the
// config file at path as they are written, read before it loads: loading
// logs the entries it finds odd, a number with a note after it say, as
// they are. A file that does not parse adds nothing.
func (r *redactor) addConfig(path string) {
	var file struct {
		AllowedNumbers []string            `json:"allowed_numbers"`
		AllowedGroups  []string            `json:"allowed_groups"`
		GroupMembers   map[string][]string `json:"group_members"`
	}
	if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &file) == nil {
		r.addChats(file.AllowedNumbers...)
		r.addGroups(file.AllowedGroups...)
		r.addGroupMembers(file.GroupMembers)
	}
}

// addGroupMembers adds the groups of group_members and the numbers each
// lists, which check prints as digits alone.
func (r *redactor) addGroupMembers(members map[string][]string) {
	for g, list := range members {
		r.addGroups(g)
		r.addChats(list...)
	}
}

// addChats adds phone numbers and chat JIDs to mask wherever they are
// printed without a JID too, as written and as digits alone.
func (r *redactor) addChats(chats ...string) { r.add(false, chats) }

// addGroups adds group JIDs as addChats does, also when one is written
// without @g.us.
func (r *redactor) addGroups(groups ...string) { r.add(true, groups) }

func (r *redactor) add(groups bool, chats []string) {
	for _, c := range chats {
		id, server, _ := strings.Cut(strings.TrimSpace(c), "@")
		digits := digitsOf(id)
		if len(digits) < 6 {
			continue
		}
		masked := maskPhone(digits)
		if groups || server == "g.us" || server == "lid" {
			masked = maskDigits(digits, 0)
		}
		written := masked
		if strings.HasPrefix(id, "+") {
			written = "+" + masked
		}
		r.chats = append(r.chats, redactedChat{id, written}, redactedChat{digits, masked})
	}
	slices.SortStableFunc(r.chats, func(a, b redactedChat) int { return len(b.value) - len(a.value) })
}

// text returns s redacted.
func (r *redactor) text(s string) string {
	s = redactPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := redactPattern.FindStringSubmatch(m)
		switch {
		case sub[3] != "":
			return maskJID(sub[1], sub[2], sub[3])
		case strings.HasPrefix(m, "+"):
			return "+" + maskPhone(m[1:])
		case isWhatsAppServer(m[strings.LastIndex(m, "@")+1:]):
			// No address: a JID after a name, or no JID either
			// ("digits@g.us", say).
			if j := trailingJID.FindStringSubmatch(m); j != nil {
				return strings.TrimSuffix(m, j[0]) + maskJID(j[1], j[2], j[3])
			}
			return m
		}
		return maskEmail(m)
	})
	for _, c := range r.chats {
		s = strings.ReplaceAll(s, c.value, c.masked)
	}
	for _, re := range r.homes {
		s = re.ReplaceAllString(s, "~$1")
	}
	return s
}

// maskJID masks a JID's user part and the suffix after it: a phone
// number's down to its country code and last two digits, without the
// device, a group's or a LID's down to the last two digits of the two
// together, which hides the number of whoever made an old-style group.
func maskJID(user, suffix, server string) string {
	if server == "s.whatsapp.net" {
		return maskPhone(user) + "@" + server
	}
	return maskDigits(user+suffix, 0) + "@" + server
}

// isWhatsAppServer reports whether domain is one of WhatsApp's JID servers.
func isWhatsAppServer(domain string) bool {
	return domain == "s.whatsapp.net" || domain == "g.us" || domain == "lid"
}

// err returns err redacted, or err itself without a redactor. The
// redacted error still wraps err, so that what check exits with is the
// same with --redact: errors.Is still finds errBrainsLeftOut in it.
func (r *redactor) err(err error) error {
	if r == nil || err == nil {
		return err
	}
	return redactedError{r.text(err.Error()), err}
}

// redactedError is an error whose text is redacted; it unwraps to the
// error it was made from.
type redactedError struct {
	text string
	err  error
}

func (e redactedError) Error() string { return e.text }
func (e redactedError) Unwrap() error { return e.err }

// redactingWriter writes what it is given redacted. The log package writes
// each entry whole, so a number is never split between two writes.
type redactingWriter struct {
	w   io.Writer
	red *redactor
}

func (rw redactingWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(rw.w, rw.red.text(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// maskPhone keeps a phone number's country code and its last two digits.
func maskPhone(n string) string {
	return maskDigits(n, countryCodeLen(n))
}

// maskDigits keeps the first keep and the last two characters of n, or
// nothing when that would leave fewer than four of them masked.
func maskDigits(n string, keep int) string {
	if len(n) < keep+2+4 {
		return "…"
	}
	return n[:keep] + "…" + n[len(n)-2:]
}

// countryCodeLen is how many leading digits of an international number
// are its country calling code (ITU-T E.164): 1 and 7 stand alone, the
// codes below take two digits, every other code three.
func countryCodeLen(n string) int {
	if n == "" {
		return 0
	}
	if n[0] == '1' || n[0] == '7' {
		return 1
	}
	if len(n) >= 2 && slices.Contains(twoDigitCountryCodes, n[:2]) {
		return 2
	}
	return 3
}

var twoDigitCountryCodes = []string{
	"20", "27",
	"30", "31", "32", "33", "34", "36", "39",
	"40", "41", "43", "44", "45", "46", "47", "48", "49",
	"51", "52", "53", "54", "55", "56", "57", "58",
	"60", "61", "62", "63", "64", "65", "66",
	"81", "82", "84", "86",
	"90", "91", "92", "93", "94", "95", "98",
}

// digitsOf is s without anything but its digits.
func digitsOf(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }
