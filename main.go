package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	defaultCfg := os.Getenv("CONFIG_FILE")
	if defaultCfg == "" {
		defaultCfg = "config.json"
	}
	cfgPath := flag.String("config", defaultCfg, "path to config.json")
	checkOnly := flag.Bool("check", false, "validate the configuration and exit")
	flag.Parse()

	cfg, err := LoadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	if *checkOnly {
		fmt.Printf("Config OK: %s\n", *cfgPath)
		fmt.Printf("  claude:    %s\n", cfg.ClaudePath)
		fmt.Printf("  data dir:  %s\n", cfg.DataDir)
		fmt.Printf("  DM allow:  %v\n", cfg.AllowedNumbers)
		fmt.Printf("  groups:    %v\n", cfg.AllowedGroups)
		fmt.Printf("  triggers:  %v\n", cfg.Triggers)
		if cfg.BrainMode {
			for _, b := range cfg.Brains {
				fmt.Printf("  brain %-10s → %s (dms=%v groups=%v)\n", b.Name, b.WorkDir, b.HandlesDMs, b.Groups)
			}
		} else {
			fmt.Printf("  mode:      classic (one session per chat), work dir %s\n", cfg.WorkDir)
		}
		return
	}

	bridge, err := NewBridge(cfg)
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

	if err := bridge.Start(ctx); err != nil {
		log.Printf("bridge stopped: %v", err)
	}

	bridge.Stop()
}
