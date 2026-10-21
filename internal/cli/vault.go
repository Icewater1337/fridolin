package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/Icewater1337/fridolin/internal/app"
	"github.com/Icewater1337/fridolin/internal/controlcli"
)

const vaultUsage = `fridolin vault — the tiered store and its snapshots (docs/PLAN.md, M3)

Usage:
  fridolin vault status [--json]      hot usage against the budget, cold size, the last
                                      upload, snapshot, check and restore drill, Litestream
  fridolin vault sync                 upload what changed to the cold tier now
  fridolin vault evict                send what the policy lets go cold now
  fridolin vault get [--allow-over-budget] PATH
                                      bring PATH (relative to the data dir) back from the
                                      cold tier and print where it is
  fridolin vault rebuild              rebuild vault.db from the cold tier and the disk, then
                                      bring back every pinned file that is only cold
  fridolin vault restore-drill [--target NAME]
                                      restore the latest snapshot (of node target NAME)
                                      into an empty folder and check a brain folder and
                                      the database copies
  fridolin vault snapshot [--target NAME] [--hourly]
                                      take a snapshot now (to node target NAME)
  fridolin vault check [--target NAME]
                                      check the snapshots' repository (of node target NAME)

Node targets (vault.targets, docs/VAULT.md "Node targets") are restic
repositories in folders on nodes; status lists them, and the Vault page adds
them.

Each asks the running bridge; sync, evict, rebuild, restore-drill, snapshot
and check start a job and return, and status shows how it went. status reads
what the bridge last wrote when none runs.

Flags:
  --config PATH     config file (default: $FRIDOLIN_CONFIG, else
                    ~/.fridolin/config.json)
`

// vaultJobs are the subcommands that start a job, by their route.
var vaultJobs = map[string]string{
	"sync": "/api/vault/sync", "evict": "/api/vault/evict", "rebuild": "/api/vault/rebuild", "restore-drill": "/api/vault/restore-drill",
	"snapshot": "/api/vault/snapshot", "check": "/api/vault/check",
}

func vaultCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, vaultUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("vault "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", configFlagHelp)
	asJSON := fs.Bool("json", false, "status: print the status as JSON")
	overBudget := fs.Bool("allow-over-budget", false, "get: bring the file back even when the hot tier goes over its budget")
	target := fs.String("target", "", "restore-drill, snapshot, check: the node target (vault.targets) to act on")
	hourly := fs.Bool("hourly", false, "snapshot: an hourly snapshot instead of a nightly one")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case err != nil:
		return 2
	}
	cfg, err := app.LoadExistingConfig(configPath(*cfgPath))
	if err != nil {
		fmt.Fprintf(stderr, "loading config: %v\n", err)
		return 1
	}
	switch sub {
	case "status":
		var raw json.RawMessage
		err := controlcli.VaultCall(cfg, http.MethodGet, "/api/vault/status", nil, &raw, 30*time.Second)
		if errors.Is(err, controlcli.ErrBridgeDown) {
			data, ferr := app.ReadVaultStatusWithTargets(cfg.DataDir, os.ReadFile)
			if ferr != nil {
				fmt.Fprintf(stderr, "%v, and %v\n", err, ferr)
				return 1
			}
			fmt.Fprintf(stderr, "%v; this is what it last wrote:\n", err)
			raw, err = data, nil
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *asJSON {
			fmt.Fprintln(stdout, string(raw))
			return 0
		}
		text, err := app.VaultStatusText(raw, time.Now())
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprint(stdout, text)
		return 0
	case "get":
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "fridolin vault get: name one vault path, such as media/main/recording.m4a")
			return 2
		}
		var resp struct{ Path string }
		body := map[string]any{"path": fs.Arg(0), "allow_over_budget": *overBudget}
		if err := controlcli.VaultCall(cfg, http.MethodPost, "/api/vault/get", body, &resp, 2*time.Hour); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, resp.Path)
		return 0
	}
	route, ok := vaultJobs[sub]
	if !ok {
		fmt.Fprintf(stderr, "unknown vault command %q\n\n%s", sub, vaultUsage)
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "fridolin vault %s takes no arguments, got %q\n", sub, fs.Args())
		return 2
	}
	var body any
	if sub == "snapshot" {
		kind := "nightly"
		if *hourly {
			kind = "hourly"
		}
		body = map[string]string{"kind": kind}
	}
	if *target != "" {
		if sub != "restore-drill" && sub != "snapshot" && sub != "check" {
			fmt.Fprintf(stderr, "fridolin vault %s takes no --target\n", sub)
			return 2
		}
		route = "/api/vault/targets/" + url.PathEscape(*target) + "/" + sub
		sub += " of target " + *target
	}
	var resp struct{ Status string }
	if err := controlcli.VaultCall(cfg, http.MethodPost, route, body, &resp, 30*time.Second); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "%s started; fridolin vault status shows how it went\n", sub)
	return 0
}
