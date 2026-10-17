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
  fridolin check [--route JID ...]
                                           validate the configuration and show routing
  fridolin init [--claude-path PATH] [--data-dir DIR]
                                           write a starter config (if there is none)
  fridolin service install|rollback|restart|status|uninstall
                                           run the bridge in the background (launchd or
                                           systemd) from a copy outside the checkout;
                                           fridolin service --help for more
  fridolin version

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
	case "init":
		initCmd(args)
	case "mcp-site":
		mcpSiteCmd()
	case "service":
		serviceCmd(args)
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
	if old := os.Getenv("CONFIG_FILE"); old != "" {
		log.Printf("warning: CONFIG_FILE=%s is ignored: set FRIDOLIN_CONFIG instead, or pass --config", old)
	}
	if flagValue != "" {
		return flagValue
	}
	if p := os.Getenv("FRIDOLIN_CONFIG"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(defaultDataDir(home), "config.json")
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
		// Only on an interactive terminal: the token must not end up in a log file.
		fmt.Printf("\n  Dashboard: %s\n\n", dashboardURL(cfg.HTTPListen, bridge.token.Value()))
	}
	if opts.open && cfg.HTTPEnabled() {
		go func() { _ = openBrowser(dashboardURL(cfg.HTTPListen, bridge.token.Value())) }()
	}

	if err := bridge.Start(ctx); err != nil {
		log.Printf("bridge stopped: %v", err)
		bridge.Stop()
		os.Exit(1)
	}

	bridge.Stop()
}

// runLog sends the log to console and to a ring the dashboard shows, logs
// the version, then picks the config file. In that order: a warning
// configPath logs, such as the one about CONFIG_FILE, must reach the
// dashboard's log too, the only one many people read, and the version line
// comes first even when the bridge then refuses to start (no config, one
// that does not load, another bridge on the data dir).
func runLog(console io.Writer, configFlag string) (*LogRing, string) {
	ring := NewLogRing(2000)
	log.SetOutput(io.MultiWriter(console, ring))
	log.Printf("Starting %s", versionLine())
	return ring, configPath(configFlag)
}

func initCmd(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	cfgPath := fs.String("config", "", configFlagHelp)
	claudePath := fs.String("claude-path", "", "path to the Claude Code CLI")
	dataDir := fs.String("data-dir", "", "data folder to write into the config (default ~/.fridolin)")
	_ = fs.Parse(args)
	path := configPath(*cfgPath)
	if _, err := os.Stat(path); err == nil {
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
	data, err := os.ReadFile(filepath.Join(cfg.DataDir, "dashboard.token"))
	if err != nil {
		log.Fatalf("no dashboard token yet — start the bridge first (%v)", err)
	}
	url := dashboardURL(cfg.HTTPListen, strings.TrimSpace(string(data)))
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
			isGroup := strings.HasSuffix(jid, "@g.us")
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
