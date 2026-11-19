package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Icewater1337/fridolin/internal/app"
	"github.com/Icewater1337/fridolin/internal/controlcli"
)

const runsUsage = `fridolin runs — what brain runs changed in their folders, and undoing it

Usage:
  fridolin runs list [--brain ID] [--limit N] [--json]
                              the runs that changed files, newest first
  fridolin runs show RUN [--json]
                              what RUN created, changed and deleted, and what
                              undoing it would do now
  fridolin runs undo RUN [--yes] [--json]
                              put RUN back: write back what it changed or
                              deleted, move what it created to the folder's
                              trash; a file that changed since is left as it
                              is. Without --yes it only says what it would do

They talk to the running bridge's dashboard API (docs/BRAINS.md, "What a run
changed").

Flags:
  --config PATH     config file (default: $FRIDOLIN_CONFIG, else
                    ~/.fridolin/config.json)
`

// runFilesItem is a run of GET /api/run-files as the CLI prints it.
type runFilesItem struct {
	Run     string     `json:"run"`
	Brain   string     `json:"brain"`
	Ended   time.Time  `json:"ended"`
	Summary string     `json:"summary"`
	Skipped string     `json:"skipped"`
	Undone  *time.Time `json:"undone"`
}

// runFilesStep is one file of an undo, done or planned.
type runFilesStep struct {
	Path   string `json:"path"`
	Op     string `json:"op"`
	Action string `json:"action"`
	Why    string `json:"why"`
	To     string `json:"to"`
	Done   bool   `json:"done"`
	Error  string `json:"error"`
}

// runFilesUndo is an undo's answer.
type runFilesUndo struct {
	Run    string         `json:"run"`
	Brain  string         `json:"brain"`
	DryRun bool           `json:"dry_run"`
	Steps  []runFilesStep `json:"steps"`
}

func runsCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, runsUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("runs "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", configFlagHelp)
	asJSON := fs.Bool("json", false, "print JSON")
	brain := fs.String("brain", "", "list: only this brain's runs")
	limit := fs.Int("limit", 20, "list: at most this many runs")
	yes := fs.Bool("yes", false, "undo: do it, not only say what it would do")
	// The run comes first: flags after it are read too.
	var pos []string
	for len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		pos, args = append(pos, args[0]), args[1:]
	}
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case err != nil:
		return 2
	}
	pos = append(pos, fs.Args()...)
	want := 1
	if sub == "list" {
		want = 0
	}
	if len(pos) != want {
		fmt.Fprintf(stderr, "fridolin runs %s takes %d argument(s), got %q\n\n%s", sub, want, pos, runsUsage)
		return 2
	}
	cfg, err := app.LoadExistingConfig(configPath(*cfgPath))
	if err != nil {
		fmt.Fprintf(stderr, "loading config: %v\n", err)
		return 1
	}
	call := func(method, path string, body any, out *json.RawMessage) error {
		return controlcli.DashboardCall(cfg, "fridolin runs", method, path, body, out, 2*time.Minute)
	}
	var raw json.RawMessage
	switch sub {
	case "list":
		q := url.Values{}
		q.Set("limit", fmt.Sprint(*limit))
		if *brain != "" {
			q.Set("brain", *brain)
		}
		err = call(http.MethodGet, "/api/run-files?"+q.Encode(), nil, &raw)
	case "show":
		err = call(http.MethodGet, "/api/runs/"+url.PathEscape(pos[0])+"/files", nil, &raw)
	case "undo":
		err = call(http.MethodPost, "/api/runs/"+url.PathEscape(pos[0])+"/undo", map[string]bool{"dry_run": !*yes}, &raw)
	default:
		fmt.Fprintf(stderr, "unknown runs command %q\n\n%s", sub, runsUsage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *asJSON {
		fmt.Fprintln(stdout, string(raw))
		return 0
	}
	switch sub {
	case "list":
		var items []runFilesItem
		if err := json.Unmarshal(raw, &items); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		printRunFilesList(stdout, items, time.Now())
	case "show":
		var view struct {
			Running bool `json:"running"`
			Record  *struct {
				Brain     string    `json:"brain"`
				Ended     time.Time `json:"ended"`
				Skipped   string    `json:"skipped"`
				Alongside []string  `json:"alongside"`
				Undone    *struct {
					At time.Time `json:"at"`
					By string    `json:"by"`
				} `json:"undone"`
			} `json:"record"`
			Plan *runFilesUndo `json:"plan"`
		}
		if err := json.Unmarshal(raw, &view); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		switch {
		case view.Running:
			fmt.Fprintf(stdout, "Run %s has not ended yet.\n", pos[0])
			return 0
		case view.Record == nil:
			fmt.Fprintln(stderr, "no record")
			return 1
		}
		r := view.Record
		fmt.Fprintf(stdout, "Run %s of %s, ended %s\n", pos[0], r.Brain, r.Ended.Local().Format(time.DateTime))
		if r.Undone != nil {
			fmt.Fprintf(stdout, "Undone %s by %s\n", r.Undone.At.Local().Format(time.DateTime), r.Undone.By)
		}
		if len(r.Alongside) > 0 {
			fmt.Fprintf(stdout, "Ran alongside %s, whose changes in that time are in it too\n", strings.Join(r.Alongside, ", "))
		}
		if r.Skipped != "" {
			fmt.Fprintf(stdout, "Its files were not recorded: %s\n", r.Skipped)
			return 0
		}
		if view.Plan != nil {
			printUndoSteps(stdout, view.Plan.Steps)
		}
	case "undo":
		var res runFilesUndo
		if err := json.Unmarshal(raw, &res); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		printUndoSteps(stdout, res.Steps)
		if res.DryRun {
			fmt.Fprintf(stdout, "\nNothing was changed. Run fridolin runs undo %s --yes to do it.\n", pos[0])
		}
		for _, s := range res.Steps {
			if s.Error != "" {
				return 1
			}
		}
	}
	return 0
}

// printRunFilesList prints the runs of GET /api/run-files.
func printRunFilesList(w io.Writer, items []runFilesItem, now time.Time) {
	if len(items) == 0 {
		fmt.Fprintln(w, "No run changed files.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tBRAIN\tENDED\tCHANGES")
	for _, it := range items {
		what := it.Summary
		if it.Undone != nil {
			what += " (undone)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s ago\t%s\n", it.Run, it.Brain, now.Sub(it.Ended).Round(time.Minute), what)
	}
	tw.Flush()
}

// printUndoSteps prints what an undo does or did to each file.
func printUndoSteps(w io.Writer, steps []runFilesStep) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "FILE\tTHE RUN\tUNDO")
	for _, s := range steps {
		undo := s.Action
		switch {
		case s.Error != "":
			undo += ", failed: " + s.Error
		case s.Done && s.To != "":
			undo += ", done: " + s.To
		case s.Done:
			undo += ", done"
		case s.Why != "":
			undo += ": " + s.Why
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Path, s.Op, undo)
	}
	tw.Flush()
}
