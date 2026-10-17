package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

const usage = `fridolin — talk to Claude Code over WhatsApp

Usage:
  fridolin [run] [flags]     start the bridge (default)
  fridolin open              open the dashboard in your browser
  fridolin check [--route JID ...]
                                           validate the configuration and show routing
  fridolin migrate [--dry-run] [--force]
                                           move brains configured inline in config.json
                                           into their folders (brain.json)
  fridolin init [--claude-path PATH]
                                           write a starter config (if there is none)
  fridolin version

Common flags:
  --config PATH     config file (default: $CONFIG_FILE, ./config.json if present,
                    else ~/.fridolin/config.json)

Run flags:
  --no-whatsapp     dev mode: dashboard and brains without a WhatsApp connection
  --open            open the dashboard once the bridge is up
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
	case "migrate":
		migrateCmd(args)
	case "open":
		openCmd(args)
	case "init":
		initCmd(args)
	case "version":
		fmt.Println(bridgeVersion())
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}

// defaultConfigPath picks the config file when --config is not given.
func defaultConfigPath() string {
	if p := os.Getenv("CONFIG_FILE"); p != "" {
		return p
	}
	if _, err := os.Stat("config.json"); err == nil {
		return "config.json"
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
		BrainMode:                  true,
		DangerouslySkipPermissions: true,
		HTTPListen:                 defaultHTTPListen,
		OTPGuard:                   true,
		Sandbox:                    SandboxConfig{Enabled: runtime.GOOS == "darwin", DenyPaths: []string{}, AllowPaths: []string{}},
	}
}

func ensureConfigFile(path string) error {
	return writeStarterConfig(path, "")
}

func writeStarterConfig(path, claudePath string) error {
	if _, err := os.Stat(path); err == nil || !os.IsNotExist(err) {
		return err
	}
	cfg := starterConfig()
	cfg.ClaudePath = claudePath
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	log.Printf("No config found — writing a starter config to %s", path)
	return writeFileAtomic(path, append(data, '\n'), 0o600, false)
}

func runCmd(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "path to config.json")
	noWA := fs.Bool("no-whatsapp", false, "dev mode: run without connecting to WhatsApp")
	dashOnly := fs.Bool("dashboard-only", false, "alias for --no-whatsapp")
	openDash := fs.Bool("open", false, "open the dashboard in the browser once started")
	legacyCheck := fs.Bool("check", false, "validate the configuration and exit (same as `check`)")
	_ = fs.Parse(args)

	if *legacyCheck {
		checkCmd([]string{"--config", *cfgPath})
		return
	}

	ring := NewLogRing(2000)
	log.SetOutput(io.MultiWriter(os.Stderr, ring))

	if err := ensureConfigFile(*cfgPath); err != nil {
		log.Fatalf("creating config: %v", err)
	}
	cfgs, err := OpenConfigStore(*cfgPath)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}
	cfg := cfgs.Get()

	// Dev mode with the live data dir would open the real WhatsApp session's
	// database next to the running bridge; the lock refuses that too.
	if err := acquireInstanceLock(cfg.DataDir); err != nil {
		log.Fatalf("%v", err)
	}
	defer releaseInstanceLock()

	bridge, err := NewBridge(cfgs, BridgeOptions{NoWhatsApp: *noWA || *dashOnly, Logs: ring})
	if err != nil {
		log.Fatalf("creating bridge: %v", err)
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
		// Only on an interactive terminal: the token must not end up in a log file.
		fmt.Printf("\n  Dashboard: %s\n\n", dashboardURL(cfg.HTTPListen, bridge.token.Value()))
	}
	if *openDash && cfg.HTTPEnabled() {
		go func() { _ = openBrowser(dashboardURL(cfg.HTTPListen, bridge.token.Value())) }()
	}

	if err := bridge.Start(ctx); err != nil {
		log.Printf("bridge stopped: %v", err)
		bridge.Stop()
		releaseInstanceLock()
		os.Exit(1)
	}

	bridge.Stop()
}

func initCmd(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "path to config.json")
	claudePath := fs.String("claude-path", "", "path to the Claude Code CLI")
	_ = fs.Parse(args)
	if _, err := os.Stat(*cfgPath); err == nil {
		fmt.Printf("%s already exists — leaving it alone.\n", *cfgPath)
		return
	}
	if err := writeStarterConfig(*cfgPath, *claudePath); err != nil {
		log.Fatalf("writing %s: %v", *cfgPath, err)
	}
	if _, err := LoadConfig(*cfgPath); err != nil {
		log.Fatalf("the starter config does not load: %v", err)
	}
}

func openCmd(args []string) {
	fs := flag.NewFlagSet("open", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "path to config.json")
	printOnly := fs.Bool("print", false, "print the URL instead of opening it")
	_ = fs.Parse(args)

	cfgs, err := OpenConfigStore(*cfgPath)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}
	cfg := cfgs.Get()
	if !cfg.HTTPEnabled() || cfg.Dashboard.Disabled {
		log.Fatalf("the dashboard is turned off in %s", *cfgPath)
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
	cfgPath := fs.String("config", defaultConfigPath(), "path to config.json")
	var routes multiFlag
	fs.Var(&routes, "route", "show which brain handles this chat JID (repeatable)")
	sandboxFor := fs.String("print-sandbox", "", "print the sandbox profile for this brain id")
	_ = fs.Parse(args)

	cfg, err := LoadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	fmt.Printf("Config OK: %s\n", *cfgPath)
	fmt.Printf("  claude:    %s\n", cfg.ClaudePath)
	fmt.Printf("  data dir:  %s\n", cfg.DataDir)
	fmt.Printf("  dashboard: http://%s/\n", cfg.HTTPListen)
	fmt.Printf("  DM allow:  %s\n", dmAllowSummary(cfg))
	fmt.Printf("  groups:    %v\n", cfg.AllowedGroups)
	trig, _ := json.Marshal(cfg.Triggers)
	fmt.Printf("  triggers:  %s\n", trig)
	if !cfg.BrainMode {
		fmt.Printf("  mode:      classic (one session per chat), work dir %s\n", cfg.WorkDir)
		return
	}
	specs, issues := ResolveBrains(cfg)
	if len(specs) == 0 {
		fmt.Println("  brains:    none yet — add one in the dashboard")
	}
	for _, s := range specs {
		m := s.Manifest
		fmt.Printf("  brain %-14s → %s [%s] (dms=%v default=%v groups=%v)\n", m.ID, s.Dir, s.Source, m.Routing.HandlesDMs, m.Routing.Default, m.Routing.Groups)
	}
	for _, is := range issues {
		fmt.Printf("  ! %s %s: %s\n", is.Kind, is.Path, is.Detail)
	}

	if len(routes) > 0 || *sandboxFor != "" {
		mgr := &BrainManager{brains: map[string]*Brain{}, byGroup: map[string]*Brain{}, profiler: NewSandboxProfiler(nil, cfg), hooks: &BrainHooks{}}
		mgr.profiler.SetConfigFile(*cfgPath)
		if err := mgr.reload(specs, issues, cfg); err != nil {
			log.Fatalf("%v", err)
		}
		for _, jid := range routes {
			isGroup := strings.HasSuffix(jid, "@g.us")
			allowed := ""
			if isGroup && !contains(cfg.AllowedGroups, jid) {
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

func migrateCmd(args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "path to config.json")
	dry := fs.Bool("dry-run", false, "show what would change without writing anything")
	force := fs.Bool("force", false, "overwrite a brain.json that differs")
	_ = fs.Parse(args)

	cfgs, err := OpenConfigStore(*cfgPath)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}
	rep, err := MigrateInlineBrains(cfgs, *dry, *force)
	if err != nil {
		log.Fatalf("migrate: %v", err)
	}
	if len(rep.Brains) == 0 {
		fmt.Println("Nothing to migrate: no brains are configured inline.")
		return
	}
	for _, r := range rep.Brains {
		fmt.Printf("  %-14s %-8s %s", r.ID, r.Action, r.Dir)
		if r.Detail != "" {
			fmt.Printf("  (%s)", r.Detail)
		}
		fmt.Println()
		if r.Warning != "" {
			fmt.Printf("      warning: %s\n", r.Warning)
		}
	}
	if *dry {
		fmt.Println("Dry run: nothing was written.")
	}
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }
