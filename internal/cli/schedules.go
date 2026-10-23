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
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/controlcli"
)

const schedulesUsage = `fridolin schedules — the brains' scheduled agents

Usage:
  fridolin schedules list [--json]
                              every agent with its schedule, next and last run,
                              and the reminders (standing intents with a time)
  fridolin schedules run AGENT [--wait] [--json]
                              run the agent now; --wait waits for the run to end
                              and prints its log
  fridolin schedules pause AGENT
  fridolin schedules resume AGENT
                              keep the agent from running on its schedule, or let
                              it again; Run now works either way
  fridolin schedules logs AGENT [RUN] [--runs] [--json]
                              the log of the agent's latest run, or of RUN;
                              --runs lists its runs instead

AGENT is BRAIN/NAME, or NAME alone when one brain has an agent of that name.
They talk to the running bridge's dashboard API. The agents themselves are
defined in each brain's brain.json (docs/BRAINS.md, "Scheduled agents").

Flags:
  --config PATH     config file (default: $FRIDOLIN_CONFIG, else
                    ~/.fridolin/config.json)
`

// scheduleRun is a run as the CLI prints it.
type scheduleRun struct {
	ID         string    `json:"id"`
	Trigger    string    `json:"trigger"`
	By         string    `json:"by"`
	Started    time.Time `json:"started"`
	DurationMS int64     `json:"duration_ms"`
	Status     string    `json:"status"`
	Result     string    `json:"result"`
	Error      string    `json:"error"`
	Sent       bool      `json:"sent"`
	SentTo     string    `json:"sent_to"`
	CostUSD    float64   `json:"cost_usd"`
	InTokens   int64     `json:"input_tokens"`
	OutTokens  int64     `json:"output_tokens"`
	ExitCode   *int      `json:"exit_code"`
}

type scheduleAgent struct {
	Brain   string       `json:"brain"`
	Name    string       `json:"name"`
	Task    string       `json:"task"`
	Report  string       `json:"report"`
	Enabled bool         `json:"enabled"`
	Paused  bool         `json:"paused"`
	Running bool         `json:"running"`
	Next    *time.Time   `json:"next"`
	LastRun *scheduleRun `json:"last_run"`
	Problem string       `json:"problem"`
	When    struct {
		When string `json:"when"`
	} `json:"schedule"`
}

type schedulesList struct {
	Enabled   bool            `json:"enabled"`
	Agents    []scheduleAgent `json:"agents"`
	Reminders []struct {
		Brain string     `json:"brain"`
		ID    string     `json:"id"`
		Text  string     `json:"text"`
		When  string     `json:"when"`
		Next  *time.Time `json:"next"`
	} `json:"reminders"`
}

func schedulesCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, schedulesUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("schedules "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", configFlagHelp)
	asJSON := fs.Bool("json", false, "print JSON")
	wait := fs.Bool("wait", false, "run: wait for the run to end and print its log")
	listRuns := fs.Bool("runs", false, "logs: list the agent's runs")
	// Flags may come after the agent: parse what is left again.
	var pos []string
	for {
		switch err := fs.Parse(args); {
		case errors.Is(err, flag.ErrHelp):
			return 0
		case err != nil:
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	want := map[string][2]int{"list": {0, 0}, "run": {1, 1}, "pause": {1, 1}, "resume": {1, 1}, "logs": {1, 2}}
	n, ok := want[sub]
	if !ok {
		fmt.Fprintf(stderr, "unknown schedules command %q\n\n%s", sub, schedulesUsage)
		return 2
	}
	if len(pos) < n[0] || len(pos) > n[1] {
		fmt.Fprintf(stderr, "fridolin schedules %s: wrong arguments %q\n\n%s", sub, pos, schedulesUsage)
		return 2
	}
	cfg, err := app.LoadExistingConfig(configPath(*cfgPath))
	if err != nil {
		fmt.Fprintf(stderr, "loading config: %v\n", err)
		return 1
	}
	c := schedulesClient{cfg: cfg}
	var list schedulesList
	if err := c.call(http.MethodGet, "/api/schedules", nil, &list); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if sub == "list" {
		if *asJSON {
			return c.printJSON(stdout, stderr, "/api/schedules")
		}
		fmt.Fprint(stdout, schedulesListText(list, time.Now()))
		return 0
	}
	brain, name, err := resolveAgent(list, pos[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	base := "/api/schedules/" + url.PathEscape(brain) + "/" + url.PathEscape(name)
	switch sub {
	case "pause", "resume":
		if err := c.call(http.MethodPost, base+"/"+sub, nil, nil); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "%s/%s %sd\n", brain, name, strings.TrimSuffix(sub, "e"))
		return 0
	case "run":
		var r scheduleRun
		if err := c.call(http.MethodPost, base+"/run", map[string]string{"trigger": "cli"}, &r); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if !*wait {
			if *asJSON {
				data, _ := json.Marshal(r)
				fmt.Fprintln(stdout, string(data))
				return 0
			}
			fmt.Fprintf(stdout, "started run %s of %s/%s; fridolin schedules logs %s/%s %s shows its log\n", r.ID, brain, name, brain, name, r.ID)
			return 0
		}
		ended, err := c.waitRun(base, r.ID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return c.printLog(stdout, stderr, base, ended, *asJSON)
	}
	// logs
	var runs []scheduleRun
	if err := c.call(http.MethodGet, base+"/runs?limit=1000", nil, &runs); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *listRuns {
		if *asJSON {
			return c.printJSON(stdout, stderr, base+"/runs?limit=1000")
		}
		fmt.Fprint(stdout, scheduleRunsText(runs))
		return 0
	}
	var r *scheduleRun
	for i := range runs {
		if len(pos) == 1 || runs[i].ID == pos[1] {
			r = &runs[i]
			break
		}
	}
	if r == nil {
		fmt.Fprintf(stderr, "%s/%s has no such run (fridolin schedules logs %s/%s --runs lists them)\n", brain, name, brain, name)
		return 1
	}
	return c.printLog(stdout, stderr, base, *r, *asJSON)
}

// resolveAgent finds the agent ref names: BRAIN/NAME, or NAME alone when
// only one brain has it.
func resolveAgent(list schedulesList, ref string) (string, string, error) {
	if brain, name, ok := strings.Cut(ref, "/"); ok {
		return brain, name, nil
	}
	var found []scheduleAgent
	for _, a := range list.Agents {
		if a.Name == ref {
			found = append(found, a)
		}
	}
	switch len(found) {
	case 0:
		return "", "", fmt.Errorf("no brain has a scheduled agent %q (fridolin schedules list)", ref)
	case 1:
		return found[0].Brain, found[0].Name, nil
	}
	return "", "", fmt.Errorf("several brains have an agent %q: name it as BRAIN/%s", ref, ref)
}

type schedulesClient struct{ cfg *config.Settings }

func (c schedulesClient) call(method, path string, body, out any) error {
	return controlcli.DashboardCall(c.cfg, "fridolin schedules", method, path, body, out, 30*time.Second)
}

func (c schedulesClient) printJSON(stdout, stderr io.Writer, path string) int {
	var raw json.RawMessage
	if err := c.call(http.MethodGet, path, nil, &raw); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, string(raw))
	return 0
}

// waitRun waits until run id has ended, and returns it.
func (c schedulesClient) waitRun(base, id string) (scheduleRun, error) {
	deadline := time.Now().Add(25 * time.Hour)
	for time.Now().Before(deadline) {
		var runs []scheduleRun
		if err := c.call(http.MethodGet, base+"/runs?limit=20", nil, &runs); err != nil {
			return scheduleRun{}, err
		}
		for _, r := range runs {
			if r.ID == id && r.Status != "running" {
				return r, nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return scheduleRun{}, fmt.Errorf("run %s did not end", id)
}

func (c schedulesClient) printLog(stdout, stderr io.Writer, base string, r scheduleRun, asJSON bool) int {
	var lg struct {
		Log string `json:"log"`
		Cut bool   `json:"cut"`
	}
	err := c.call(http.MethodGet, base+"/runs/"+url.PathEscape(r.ID)+"/log", nil, &lg)
	if err != nil && !strings.Contains(err.Error(), "no log") {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if asJSON {
		data, _ := json.Marshal(map[string]any{"run": r, "log": lg.Log, "cut": lg.Cut})
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	fmt.Fprint(stdout, scheduleRunsText([]scheduleRun{r}))
	if lg.Cut {
		fmt.Fprintln(stdout, "… (the start of the log is left out)")
	}
	fmt.Fprint(stdout, lg.Log)
	return exitOf(r)
}

// exitOf is the exit status a run's log ends with: 0 for a run that went
// well, 1 for one that did not.
func exitOf(r scheduleRun) int {
	switch r.Status {
	case "ok", "nothing", "running":
		return 0
	}
	return 1
}

func schedulesListText(l schedulesList, now time.Time) string {
	var b strings.Builder
	if !l.Enabled {
		b.WriteString("schedules.enabled is off: no agent runs on its schedule (Run now still works).\n\n")
	}
	if len(l.Agents) == 0 {
		b.WriteString("No scheduled agents. A brain gets them in its brain.json (docs/BRAINS.md, \"Scheduled agents\").\n")
	} else {
		w := tabwriter.NewWriter(&b, 2, 4, 2, ' ', 0)
		fmt.Fprintln(w, "AGENT\tWHEN\tSTATE\tNEXT\tLAST RUN")
		for _, a := range l.Agents {
			state := "on"
			switch {
			case a.Running:
				state = "running"
			case a.Paused:
				state = "paused"
			case !a.Enabled:
				state = "off"
			}
			if a.Problem != "" {
				state += " (!)"
			}
			next := "—"
			if a.Next != nil {
				next = "in " + a.Next.Sub(now).Round(time.Minute).String()
			}
			last := "never"
			if r := a.LastRun; r != nil {
				last = fmt.Sprintf("%s, %s ago", r.Status, now.Sub(r.Started).Round(time.Minute))
			}
			fmt.Fprintf(w, "%s/%s\t%s\t%s\t%s\t%s\n", a.Brain, a.Name, a.When.When, state, next, last)
		}
		_ = w.Flush()
		for _, a := range l.Agents {
			if a.Problem != "" {
				fmt.Fprintf(&b, "(!) %s/%s: %s\n", a.Brain, a.Name, a.Problem)
			}
		}
	}
	if len(l.Reminders) > 0 {
		b.WriteString("\nReminders (standing intents with a time):\n")
		for _, r := range l.Reminders {
			next := "done"
			if r.Next != nil {
				next = r.Next.Local().Format("2006-01-02 15:04")
			}
			fmt.Fprintf(&b, "  %s %s  %s  (%s, next %s)\n", r.Brain, r.ID, r.Text, r.When, next)
		}
	}
	return b.String()
}

func scheduleRunsText(runs []scheduleRun) string {
	if len(runs) == 0 {
		return "No runs yet.\n"
	}
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "RUN\tSTARTED\tTOOK\tSTATUS\tSENT\tCOST")
	for _, r := range runs {
		took := (time.Duration(r.DurationMS) * time.Millisecond).Round(time.Second).String()
		sent := "—"
		if r.Sent {
			sent = r.SentTo
		}
		cost := ""
		switch {
		case r.CostUSD > 0:
			cost = fmt.Sprintf("$%.4f", r.CostUSD)
		case r.InTokens+r.OutTokens > 0:
			cost = fmt.Sprintf("%d+%d tokens", r.InTokens, r.OutTokens)
		case r.ExitCode != nil:
			cost = fmt.Sprintf("exit %d", *r.ExitCode)
		}
		status := r.Status
		if r.Error != "" {
			status += ": " + r.Error
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Started.Local().Format("2006-01-02 15:04"), took, status, sent, cost)
	}
	_ = w.Flush()
	return b.String()
}
