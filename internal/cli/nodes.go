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
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

const nodesUsage = `fridolin nodes — the core's fleet (docs/NODES.md)

Usage:
  fridolin nodes [list] [--json]           every node: approval, online, what it may do
  fridolin nodes join-code                 a single-use code (10 minutes) and the line
                                           to paste on the new machine
  fridolin nodes approve ID                approve a node's device (first stage)
  fridolin nodes approve ID --commands     approve what it offers (second stage; again
                                           whenever it offers more)
  fridolin nodes revoke ID                 disconnect it and refuse it from now on
  fridolin nodes tags ID [TAG,...]         replace its tags ("" clears them)
  fridolin nodes call ID METHOD [JSON]     call a capability: exec, fs.list, fs.read,
                                           fs.write, host.info, notify
  fridolin nodes update ID|--all [--force] [--no-wait]
                                           update a node's fridolin to the core's version
                                           (--all: every node that runs an older one);
                                           waits until it runs it or went back
  fridolin nodes auto-update ID on|off     update it by itself whenever it runs an
                                           older version than the core

Each asks the running bridge, with the dashboard token. Flags:
  --config PATH     config file (default: $FRIDOLIN_CONFIG, else
                    ~/.fridolin/config.json)
`

// nodesCmd runs `fridolin nodes` and returns its exit status.
func nodesCmd(args []string, stdout, stderr io.Writer) int {
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	if sub == "help" {
		fmt.Fprint(stdout, nodesUsage)
		return 0
	}
	fs := flag.NewFlagSet("nodes "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", configFlagHelp)
	asJSON := fs.Bool("json", false, "print JSON")
	commands := fs.Bool("commands", false, "approve what the node offers (the second stage)")
	all := fs.Bool("all", false, "update every node that runs an older version")
	force := fs.Bool("force", false, "update a busy node, or one newer than the core")
	noWait := fs.Bool("no-wait", false, "start the update and return")
	fs.Usage = func() { fmt.Fprint(stderr, nodesUsage) }
	pos, err := parseInterspersed(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return 2
	}
	want := map[string][2]int{"list": {0, 0}, "join-code": {0, 0}, "approve": {1, 1}, "revoke": {1, 1}, "tags": {1, 2}, "call": {2, 3}, "update": {0, 1}, "auto-update": {2, 2}}
	n, ok := want[sub]
	if !ok {
		fmt.Fprintf(stderr, "unknown nodes command %q\n\n%s", sub, nodesUsage)
		return 2
	}
	if len(pos) < n[0] || len(pos) > n[1] {
		fmt.Fprintf(stderr, "fridolin nodes %s: wrong number of arguments\n\n%s", sub, nodesUsage)
		return 2
	}
	if *commands && sub != "approve" {
		fmt.Fprintln(stderr, "--commands goes with approve")
		return 2
	}
	if (*all || *force || *noWait) && sub != "update" {
		fmt.Fprintln(stderr, "--all, --force and --no-wait go with update")
		return 2
	}
	if sub == "update" && *all == (len(pos) == 1) {
		fmt.Fprintln(stderr, "fridolin nodes update takes a node's id, or --all")
		return 2
	}
	if sub == "auto-update" && pos[1] != "on" && pos[1] != "off" {
		fmt.Fprintln(stderr, "fridolin nodes auto-update takes on or off")
		return 2
	}
	cfg, err := app.LoadExistingConfig(configPath(*cfgPath))
	if err != nil {
		fmt.Fprintf(stderr, "loading config: %v\n", err)
		return 1
	}
	call := func(method, path string, body, out any) bool {
		if err := controlcli.API(cfg, method, path, body, out); err != nil {
			fmt.Fprintf(stderr, "fridolin nodes %s: %v\n", sub, err)
			return false
		}
		return true
	}
	printJSON := func(v any) {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		_ = enc.Encode(v)
	}
	switch sub {
	case "list":
		var resp httpapi.NodesResponse
		if !call(http.MethodGet, "/api/nodes", nil, &resp) {
			return 1
		}
		if *asJSON {
			printJSON(resp)
			return 0
		}
		writeNodesTable(stdout, resp, time.Now())
	case "join-code":
		var resp httpapi.JoinCodeResponse
		if !call(http.MethodPost, "/api/nodes/join-code", struct{}{}, &resp) {
			return 1
		}
		if *asJSON {
			printJSON(resp)
			return 0
		}
		writeJoinCode(stdout, resp)
	case "approve", "revoke":
		path := "/api/nodes/" + url.PathEscape(pos[0]) + "/" + sub
		if *commands {
			path = "/api/nodes/" + url.PathEscape(pos[0]) + "/approve-commands"
		}
		var resp httpapi.NodeResponse
		if !call(http.MethodPost, path, struct{}{}, &resp) {
			return 1
		}
		n := resp.Node
		switch {
		case sub == "revoke":
			fmt.Fprintf(stdout, "Revoked %s (%s): it is disconnected and refused from now on.\n", n.Name, n.ID)
		case *commands:
			fmt.Fprintf(stdout, "Approved what %s (%s) offers: %s\n", n.Name, n.ID, n.Approved.Summary())
		default:
			fmt.Fprintf(stdout, "Approved the device %s (%s).\n", n.Name, n.ID)
			if n.Commands() == nodeproto.CommandsPending {
				fmt.Fprintf(stdout, "It offers %s. Approve that with: fridolin nodes approve %s --commands\n", n.Declared.Summary(), n.ID)
			}
		}
	case "tags":
		tags := []string{}
		if len(pos) == 2 {
			for t := range strings.SplitSeq(pos[1], ",") {
				if t = strings.TrimSpace(t); t != "" {
					tags = append(tags, t)
				}
			}
		}
		var resp httpapi.NodeResponse
		if !call(http.MethodPut, "/api/nodes/"+url.PathEscape(pos[0])+"/tags", map[string]any{"tags": tags}, &resp) {
			return 1
		}
		fmt.Fprintf(stdout, "%s (%s): %s\n", resp.Node.Name, resp.Node.ID, strings.Join(resp.Node.Tags, ", "))
	case "call":
		params := json.RawMessage(`{}`)
		if len(pos) == 3 {
			if !json.Valid([]byte(pos[2])) {
				fmt.Fprintln(stderr, "the parameters must be JSON, such as '{\"argv\":[\"uname\",\"-a\"]}'")
				return 2
			}
			params = json.RawMessage(pos[2])
		}
		var resp httpapi.NodeCallResponse
		if !call(http.MethodPost, "/api/nodes/"+url.PathEscape(pos[0])+"/call", map[string]any{"method": pos[1], "params": params}, &resp) {
			return 1
		}
		printJSON(resp.Result)
	case "auto-update":
		var resp httpapi.NodeResponse
		if !call(http.MethodPut, "/api/nodes/"+url.PathEscape(pos[0])+"/update-policy", map[string]any{"auto": pos[1] == "on"}, &resp) {
			return 1
		}
		fmt.Fprintf(stdout, "Auto updates of %s (%s): %s\n", resp.Node.Name, resp.Node.ID, pos[1])
	case "update":
		return nodesUpdate(pos, *all, *force, *noWait, call, stdout, stderr)
	}
	return 0
}

// nodeUpdatePoll and nodeUpdateWait pace `fridolin nodes update`'s wait.
var (
	nodeUpdatePoll = 2 * time.Second
	nodeUpdateWait = 12 * time.Minute
)

// nodesUpdate starts the updates and, unless noWait, waits for each to
// end, printing how it went.
func nodesUpdate(pos []string, all, force, noWait bool, call func(method, path string, body, out any) bool, stdout, stderr io.Writer) int {
	var ids []string
	if all {
		var resp httpapi.NodesResponse
		if !call(http.MethodGet, "/api/nodes", nil, &resp) {
			return 1
		}
		for _, n := range resp.Nodes {
			switch {
			case n.UpdateTo != "" && n.Online:
				ids = append(ids, n.ID)
			case n.UpdateNote != "":
				fmt.Fprintf(stdout, "%s (%s, %s): %s\n", n.Name, n.ID, n.Version, n.UpdateNote)
			case n.UpdateTo != "":
				fmt.Fprintf(stdout, "%s (%s, %s): offline; it is updated once it connects, with auto-update on\n", n.Name, n.ID, n.Version)
			}
		}
		if len(ids) == 0 {
			fmt.Fprintf(stdout, "No connected node to update to %s.\n", resp.Version)
			return 0
		}
	} else {
		ids = pos[:1]
	}
	started := map[string]string{}
	failed := false
	for _, id := range ids {
		var resp httpapi.NodeUpdateResponse
		if !call(http.MethodPost, "/api/nodes/"+url.PathEscape(id)+"/update", map[string]any{"force": force}, &resp) {
			failed = true
			continue
		}
		started[id] = resp.To
		fmt.Fprintf(stdout, "Updating %s to %s…\n", id, resp.To)
	}
	if noWait || len(started) == 0 {
		if failed {
			return 1
		}
		return 0
	}
	deadline := time.Now().Add(nodeUpdateWait)
	for len(started) > 0 && time.Now().Before(deadline) {
		time.Sleep(nodeUpdatePoll)
		var resp httpapi.NodesResponse
		if !call(http.MethodGet, "/api/nodes", nil, &resp) {
			return 1
		}
		for _, n := range resp.Nodes {
			to, ok := started[n.ID]
			if !ok || n.Update == nil || n.Update.To != to {
				continue
			}
			switch n.Update.State {
			case "ok": // nodes.UpdateOK
				fmt.Fprintf(stdout, "%s (%s) runs %s now.\n", n.Name, n.ID, to)
				delete(started, n.ID)
			case "failed": // nodes.UpdateFailed
				fmt.Fprintf(stderr, "%s (%s): the update to %s failed: %s\n", n.Name, n.ID, to, n.Update.Error)
				delete(started, n.ID)
				failed = true
			}
		}
	}
	for id := range started {
		fmt.Fprintf(stderr, "%s: still updating after %v; fridolin nodes shows how it ends\n", id, nodeUpdateWait)
		failed = true
	}
	if failed {
		return 1
	}
	return 0
}

// writeJoinCode prints a join code with the lines to paste on the new
// machine, for sh (macOS, Linux) and for PowerShell (Windows).
func writeJoinCode(w io.Writer, resp httpapi.JoinCodeResponse) {
	sh, ps := resp.Command, resp.CommandPowerShell
	if resp.Core != "" && ps == "" {
		// A core older than this fridolin sends only the sh line.
		sh, ps = httpapi.JoinCommands(resp.Code, resp.Core)
	}
	fmt.Fprintf(w, "Join code %s, valid once until %s (core %s).\nOn the new machine, paste one line:\n\n", resp.Code, resp.ExpiresAt.Local().Format("15:04"), resp.Core)
	fmt.Fprintf(w, "  macOS, Linux (sh):\n    %s\n", sh)
	if ps != "" {
		fmt.Fprintf(w, "  Windows (PowerShell):\n    %s\n", ps)
	}
	fmt.Fprintln(w, "\nAdd --root PATH[:rw] to share a folder, --no-exec to allow no commands (docs/NODES.md).\nThen approve it here: fridolin nodes approve <id>, and fridolin nodes approve <id> --commands")
}

// parseInterspersed parses fs from args with flags before, between and
// after the positional arguments, which it returns.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func writeNodesTable(w io.Writer, resp httpapi.NodesResponse, now time.Time) {
	if !resp.Enabled {
		fmt.Fprintln(w, "Nodes are off (nodes.enabled); the list is what the registry holds.")
	} else if resp.Listen != "" {
		fmt.Fprintf(w, "Node port: %s\n", resp.Listen)
	}
	if len(resp.Nodes) == 0 {
		fmt.Fprintln(w, "No nodes yet. Make a join code with: fridolin nodes join-code")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tOS\tDEVICE\tCOMMANDS\tSEEN\tTAGS\tMAY RUN")
	for _, n := range resp.Nodes {
		seen := "never"
		switch {
		case n.Online:
			seen = "online"
		case n.LastSeen != nil:
			seen = now.Sub(*n.LastSeen).Round(time.Second).String() + " ago"
		}
		tags := strings.Join(n.Tags, ",")
		if tags == "" {
			tags = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", n.ID, n.Name, n.OS, n.Device, n.Commands, seen, tags, n.Effective.Summary())
	}
	_ = tw.Flush()
}
