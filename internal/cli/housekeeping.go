package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/Icewater1337/fridolin/internal/app"
	"github.com/Icewater1337/fridolin/internal/controlcli"
	"github.com/Icewater1337/fridolin/internal/housekeeping"
	"github.com/Icewater1337/fridolin/internal/platform"
)

const housekeepingUsage = `fridolin housekeeping — find and stop what the bridge left running

Usage:
  fridolin housekeeping run [--dry-run] [--json]
                              run the job now in the running bridge and print its
                              report; --dry-run only reports, whatever
                              housekeeping.dry_run says
  fridolin housekeeping status [--json]
                              the last report and when the next run is due

With no bridge running, run only looks at bridge.lock: a lock nobody holds
that names a process which is gone is emptied (not with --dry-run), and
status prints what the bridge last wrote.

Flags:
  --config PATH     config file (default: $FRIDOLIN_CONFIG, else
                    ~/.fridolin/config.json)
`

func housekeepingCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, housekeepingUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("housekeeping "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", configFlagHelp)
	asJSON := fs.Bool("json", false, "print JSON")
	dry := fs.Bool("dry-run", false, "run: only report")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case err != nil:
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "fridolin housekeeping %s takes no arguments, got %q\n", sub, fs.Args())
		return 2
	}
	cfg, err := app.LoadExistingConfig(configPath(*cfgPath))
	if err != nil {
		fmt.Fprintf(stderr, "loading config: %v\n", err)
		return 1
	}
	var raw json.RawMessage
	switch sub {
	case "status":
		err = controlcli.DashboardCall(cfg, "fridolin housekeeping", http.MethodGet, "/api/housekeeping", nil, &raw, 30*time.Second)
		if errors.Is(err, controlcli.ErrBridgeDown) {
			data, ferr := app.ReadHousekeepingFile(cfg.DataDir, os.ReadFile)
			if ferr != nil {
				fmt.Fprintf(stderr, "%v, and it wrote no report here yet (%v)\n", err, ferr)
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
		text, err := housekeeping.StatusText(raw, time.Now())
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprint(stdout, text)
		return 0
	case "run":
		body := map[string]any{"dry_run": *dry, "trigger": "cli"}
		err = controlcli.DashboardCall(cfg, "fridolin housekeeping", http.MethodPost, "/api/housekeeping/run", body, &raw, 10*time.Minute)
		if errors.Is(err, controlcli.ErrBridgeDown) {
			fmt.Fprintf(stderr, "%v; looking at bridge.lock only\n", err)
			return offlineLockCheck(cfg.LockPath(), *dry, stdout, stderr)
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *asJSON {
			fmt.Fprintln(stdout, string(raw))
			return 0
		}
		text, err := housekeeping.ReportText(raw, time.Now())
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprint(stdout, text)
		return 0
	}
	fmt.Fprintf(stderr, "unknown housekeeping command %q\n\n%s", sub, housekeepingUsage)
	return 2
}

// offlineLockCheck empties bridge.lock when nobody holds it and the PID
// it names is gone (only reports with dry).
func offlineLockCheck(path string, dry bool, stdout, stderr io.Writer) int {
	pid, stale, err := platform.ClearStaleLock(path, !dry)
	switch {
	case err != nil:
		fmt.Fprintf(stderr, "bridge.lock: %v\n", err)
		return 1
	case !stale:
		fmt.Fprintln(stdout, "bridge.lock: nothing to do")
	case dry:
		fmt.Fprintf(stdout, "bridge.lock names PID %d, which is gone, and nobody holds it: would empty it\n", pid)
	default:
		fmt.Fprintf(stdout, "bridge.lock named PID %d, which is gone, and nobody held it: emptied\n", pid)
	}
	return 0
}
