package main

import (
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
  fridolin check [--route JID ...]
                                           validate the configuration and show routing
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
// file there. dataDir is written into it when set: a data folder given only
// in the environment of setup.sh would not reach the service.
func writeStarterConfig(path, claudePath, dataDir string) error {
	if _, err := os.Stat(path); err == nil || !os.IsNotExist(err) {
		return err
	}
	cfg := starterConfig()
	cfg.ClaudePath = claudePath
	cfg.DataDir = dataDir
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	log.Printf("No config found — writing a starter config to %s", path)
	return writeFileAtomic(path, append(data, '\n'), 0o600, false)
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
	go logClaudeCLI(cfg.ClaudePath, cfg.ChildEnvPassthrough)

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
	dataDir := fs.String("data-dir", "", "data folder to write into the config (default ~/.fridolin)")
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

	cfgs, err := OpenConfigStore(path)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}
	cfg := cfgs.Get()
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
	cfgs, err := OpenConfigStore(configPath(*cfgPath))
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}
	if err := rotateToken(cfgs.Get()); err != nil {
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

func checkCmd(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	cfgPath := fs.String("config", "", configFlagHelp)
	var routes multiFlag
	fs.Var(&routes, "route", "show which brain handles this chat JID (repeatable)")
	sandboxFor := fs.String("print-sandbox", "", "print the sandbox profile for this brain id")
	_ = fs.Parse(args)
	path := configPath(*cfgPath)

	cfg, err := LoadConfig(path)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	fmt.Printf("Config OK: %s\n", path)
	fmt.Printf("  claude:    %s\n", claudeSummary(cfg.ClaudePath, cfg.ChildEnvPassthrough))
	fmt.Printf("  data dir:  %s\n", cfg.DataDir)
	fmt.Printf("  dashboard: http://%s/\n", cfg.HTTPListen)
	fmt.Printf("  DM allow:  %s\n", dmAllowSummary(cfg))
	fmt.Printf("  groups:    %v\n", cfg.AllowedGroups)
	trig, _ := json.Marshal(cfg.Triggers)
	fmt.Printf("  triggers:  %s\n", trig)
	specs, issues := ResolveBrains(cfg)
	if len(specs) == 0 {
		fmt.Println("  brains:    none yet — add one in the dashboard")
	}
	for _, s := range specs {
		m := s.Manifest
		fmt.Printf("  brain %-14s → %s (dms=%v default=%v groups=%v)\n", m.ID, s.Dir, m.Routing.HandlesDMs, m.Routing.Default, m.Routing.Groups)
	}
	for _, is := range issues {
		fmt.Printf("  ! %s %s: %s\n", is.Kind, is.Path, is.Detail)
	}

	if len(routes) > 0 || *sandboxFor != "" {
		mgr := &BrainManager{brains: map[string]*Brain{}, byGroup: map[string]*Brain{}, profiler: NewSandboxProfiler(nil, cfg), hooks: &BrainHooks{}}
		mgr.profiler.SetConfigFile(path)
		if err := mgr.reload(specs, issues, cfg); err != nil {
			log.Fatalf("%v", err)
		}
		for _, jid := range routes {
			isGroup := isGroupJID(jid)
			allowed := ""
			if isGroup && !slices.Contains(cfg.AllowedGroups, jid) {
				allowed = " (not in allowed_groups: ignored)"
			}
			if !isGroup && !numberAllowed(cfg, normalizeJID(jid)) {
				allowed = " (not in allowed_numbers: ignored)"
			}
			if br := mgr.BrainFor(jid, isGroup); br != nil {
				fmt.Printf("  route %s → %s%s\n", jid, br.ID(), allowed)
			} else {
				fmt.Printf("  route %s → no brain%s\n", jid, allowed)
			}
		}
		if *sandboxFor != "" {
			profile, err := mgr.profiler.Profile(*sandboxFor)
			if err != nil {
				log.Fatalf("sandbox profile: %v", err)
			}
			fmt.Println()
			fmt.Print(profile)
		}
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

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }
