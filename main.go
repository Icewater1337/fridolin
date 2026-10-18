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
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"syscall"
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
		fmt.Println(bridgeVersion())
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
	if old := os.Getenv("CONFIG_FILE"); old != "" {
		log.Printf("warning: CONFIG_FILE=%s is ignored: set FRIDOLIN_CONFIG instead, or pass --config", old)
	}
	if flagValue != "" {
		return flagValue, configFromFlag
	}
	if p := os.Getenv("FRIDOLIN_CONFIG"); p != "" {
		return p, configFromEnv
	}
	return filepath.Join(defaultDataDir(homeDir()), "config.json"), configFromDefault
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
	path, source := configPathFrom(configFlag)
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
	dir, dirSource := resolveDataDir(file.DataDir, home)
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

// starterConfig is written on the very first run, so the bridge can start and
// everything else can be set up in the dashboard.
func starterConfig() *Config {
	f := false
	return &Config{
		Comment:                    "Edit this in the dashboard (fridolin open). Every option: docs/CONFIGURATION.md",
		Version:                    currentConfigVersion,
		AllowedNumbers:             []string{},
		AllowAnyDM:                 &f,
		AllowedGroups:              []string{},
		Triggers:                   TriggersFromStrings([]string{"@claude"}),
		DangerouslySkipPermissions: true,
		HTTPListen:                 defaultHTTPListen,
		OTPGuard:                   true,
		Sandbox:                    SandboxConfig{Enabled: runtime.GOOS == "darwin", DenyPaths: []string{}, AllowPaths: []string{}},
		// A new install has no scripts written before tokens.
		ScriptAPI: ScriptAPIConfig{TokenlessSend: &f},
	}
}

func ensureConfigFile(path string) error {
	return writeStarterConfig(path, "", "")
}

// writeStarterConfig writes the starter config to path unless there is a
// file there. Its data_dir is dataDir, else starterDataDir's: without one
// every start would pick the data dir again, by which folders exist.
func writeStarterConfig(path, claudePath, dataDir string) error {
	if _, err := os.Stat(path); err == nil || !os.IsNotExist(err) {
		return err
	}
	cfg := starterConfig()
	cfg.ClaudePath = claudePath
	cfg.DataDir = dataDir
	if dataDir == "" {
		cfg.DataDir = starterDataDir(homeDir())
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	log.Printf("No config found — writing a starter config to %s", path)
	return writeFileAtomic(path, append(data, '\n'), 0o600, false)
}

// starterDataDir is the data dir a new config gets when none is given: the
// folder the bridge would pick now, written with ~ so that the config
// still holds when a backup of it is restored under another home folder.
func starterDataDir(home string) string {
	if _, source := resolveDataDir("", home); source == dataDirFromLegacy {
		return "~/" + legacyDataDirName
	}
	return "~/" + dataDirName
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
	ring, path := runLog(console, opts.config)
	if logErr != nil {
		// Once the log reaches the dashboard too.
		log.Printf("%v; logging to stderr without rotating it", logErr)
	}
	if err := ensureConfigFile(path); err != nil {
		log.Fatalf("creating config: %v", err)
	}
	cfgs, err := OpenConfigStore(path)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}
	cfg := cfgs.Get()
	logDataDir(cfgs)

	// NewBridge takes the data dir's instance lock, so dev mode with the live
	// data dir is refused next to the running bridge too.
	bridge, err := NewBridge(cfgs, BridgeOptions{NoWhatsApp: opts.noWhatsApp, Logs: ring})
	if err != nil {
		log.Fatalf("creating bridge: %v", err)
	}
	if cfg.AgentProvider == "codex" {
		go func() { log.Printf("Codex CLI: %+v", codexInfo(cfg.CodexPath, cfg.ChildEnvPassthrough)) }()
	} else {
		go logClaudeCLI(cfg.ClaudePath, cfg.ChildEnvPassthrough)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %s, shutting down…", sig)
		cancel()
	}()

	if isTerminal(os.Stdout) && cfg.HTTPEnabled() && !cfg.Dashboard.Disabled {
		// Only on an interactive terminal: even a code that works once
		// must not end up in a log file.
		code, _ := bridge.sessions.newCode()
		fmt.Printf("\n  Dashboard: %s\n  (the link works once, within a minute; after that, run: fridolin open)\n\n", loginURL(cfg.HTTPListen, code))
	}
	if opts.open && cfg.HTTPEnabled() {
		code, _ := bridge.sessions.newCode()
		go func() { _ = openBrowser(loginURL(cfg.HTTPListen, code)) }()
	}

	if err := bridge.Start(ctx); err != nil {
		log.Printf("bridge stopped: %v", err)
		bridge.Stop()
		os.Exit(1)
	}

	bridge.Stop()
}

// runLog sends the log to console and to a ring the dashboard shows, logs
// the version, then picks the config file and names it and where it came
// from. In that order: a warning configPath logs, such as the one about
// CONFIG_FILE, must reach the dashboard's log too, the only one many people
// read, and the version line comes first even when the bridge then refuses
// to start (no config, one that does not load, another bridge on the data
// dir).
func runLog(console io.Writer, configFlag string) (*LogRing, string) {
	ring := NewLogRing(logRingLines)
	log.SetOutput(io.MultiWriter(console, ring))
	log.Printf("Starting %s", versionLine())
	path, source := configPathFrom(configFlag)
	log.Printf("Config file: %s (%s)", absPath(path), source)
	return ring, path
}

// logDataDir names the data dir the bridge runs on, and with it the
// WhatsApp session, and where it came from: without data_dir in the
// config, which folders exist decides it.
func logDataDir(cfgs *ConfigStore) {
	home := homeDir()
	dir := cfgs.Get().DataDir
	log.Printf("Data dir: %s (%s)", absPath(dir), dataDirSource(cfgs.FileCopy().DataDir, dir, home))
}

func initCmd(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	cfgPath := fs.String("config", "", configFlagHelp)
	claudePath := fs.String("claude-path", "", "path to the Claude Code CLI")
	dataDir := fs.String("data-dir", "", "data folder to write into the config (default ~/.fridolin, or the folder from before the rename when only that exists)")
	_ = fs.Parse(args)
	path := configPath(*cfgPath)
	if fileExists(path) {
		fmt.Printf("%s already exists — leaving it alone.\n", path)
		return
	}
	if err := writeStarterConfig(path, *claudePath, *dataDir); err != nil {
		log.Fatalf("writing %s: %v", path, err)
	}
	if _, err := LoadConfig(path); err != nil {
		log.Fatalf("the starter config does not load: %v", err)
	}
}

func openCmd(args []string) {
	fs := flag.NewFlagSet("open", flag.ExitOnError)
	cfgPath := fs.String("config", "", configFlagHelp)
	printOnly := fs.Bool("print", false, "print the URL instead of opening it")
	_ = fs.Parse(args)
	path := configPath(*cfgPath)

	cfg, err := LoadExistingConfig(path)
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
	if err := openBrowser(url); err != nil {
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
func dashboardLink(cfg *Config) (string, error) {
	tokenPath := filepath.Join(cfg.DataDir, "dashboard.token")
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return "", fmt.Errorf("no dashboard token yet — start the bridge first (%w)", err)
	}
	code, err := askLoginCode(cfg.HTTPListen, strings.TrimSpace(string(data)))
	if errors.Is(err, errOlderBridge) {
		return "", fmt.Errorf("%w. Restart it on this build (./setup.sh installs it and restarts the bridge), or open its dashboard with the fridolin that runs it, or at http://%s/ with the token from %s", err, cfg.HTTPListen, tokenPath)
	}
	if err != nil {
		return "", err
	}
	return loginURL(cfg.HTTPListen, code), nil
}

// rotateTokenCmd replaces the dashboard token and signs out every browser:
// through the running bridge, or, when none runs, in the data dir itself.
func rotateTokenCmd(args []string) {
	fs := flag.NewFlagSet("rotate-token", flag.ExitOnError)
	cfgPath := fs.String("config", "", configFlagHelp)
	_ = fs.Parse(args)
	cfg, err := LoadExistingConfig(configPath(*cfgPath))
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}
	if err := rotateToken(cfg); err != nil {
		log.Fatal(err)
	}
	fmt.Println("The dashboard token was replaced and every browser signed out. Open the dashboard again with: fridolin open")
}

// rotateToken does rotateTokenCmd's work for cfg.
func rotateToken(cfg *Config) error {
	data, err := os.ReadFile(filepath.Join(cfg.DataDir, "dashboard.token"))
	if err != nil {
		return fmt.Errorf("no dashboard token yet — start the bridge first (%w)", err)
	}
	if cfg.HTTPEnabled() {
		var resp struct{ Status string }
		err := callBridge(cfg.HTTPListen, strings.TrimSpace(string(data)), "/api/token/rotate", &resp)
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
	lock, err := acquireInstanceLock(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("the bridge runs but does not answer, so the token was not replaced: %w", err)
	}
	defer lock.release()
	tok, err := LoadDashboardToken(cfg.DataDir)
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

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Run()
	case "linux":
		return exec.Command("xdg-open", url).Run()
	}
	return fmt.Errorf("don't know how to open a browser on %s", runtime.GOOS)
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
	var red *redactor
	if o.redact {
		red = newRedactor(homeDir())
		red.addChats(o.routes...)
		// The log entries of loading the config name paths, and the
		// allowed chats it finds odd, as they are written.
		prev := log.Writer()
		log.SetOutput(redactingWriter{prev, red})
		defer log.SetOutput(prev)
	}
	path := configPath(o.config)
	if red != nil {
		red.addConfig(path)
	}
	cfg, err := LoadExistingConfig(path)
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
func checkReport(w io.Writer, path string, cfg *Config, o checkOptions) error {
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
	// As the bridge does before it loads the brains, so that the brains
	// and profiles are the ones it would run.
	ownPATH := setBridgePATH(cfg)
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
		isGroup := isGroupJID(jid)
		allowed := ""
		if isGroup && !slices.Contains(cfg.AllowedGroups, jid) {
			allowed = " (not in allowed_groups: ignored)"
		}
		if !isGroup && !numberAllowed(cfg, normalizeJID(jid)) {
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
	if claudeVersionWarning(st) != "" {
		s += " — older than " + testedClaudeVersion + ", the oldest release tested; update it (claude update)"
	}
	return s
}

// dmAllowSummary says who may DM the bridge. A bare empty list would read
// as "nobody" even when allow_any_dm lets everyone in.
func dmAllowSummary(cfg *Config) string {
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
	for _, h := range []string{home, resolvePath(home)} {
		spellings = append(spellings, h, sbplEscaper.Replace(h), regexp.QuoteMeta(h), sbplEscaper.Replace(regexp.QuoteMeta(h)))
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
