package cli

import (
	"cmp"
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
  fridolin nodes approve NODE              approve a node's device (first stage)
  fridolin nodes approve NODE --commands   approve what it offers (second stage; again
                                           whenever it offers more)
  fridolin nodes revoke NODE               disconnect it and refuse it from now on
  fridolin nodes tags NODE [TAG,...]       replace its tags ("" clears them)
  fridolin nodes call NODE METHOD [JSON]   call a capability: exec, fs.list, fs.read,
                                           fs.write, host.info, notify
  fridolin nodes update NODE|--all [--force] [--no-wait]
                                           update a node's fridolin to the core's version
                                           (--all: every node that runs an older one);
                                           waits until it runs it or went back
  fridolin nodes auto-update NODE on|off   update it by itself whenever it runs an
                                           older version than the core
  fridolin nodes core-secrets-transfer SRC DST [--yes] [--replace]
                                           move the host secrets from core host SRC to
                                           core host DST over the node channel, end to
                                           end; without --yes only checks both sides
  fridolin nodes move-check SRC DST        the pre-flight of moving the core from core
                                           host SRC to DST: versions, image, disk,
                                           secrets, the lease, what stays behind
  fridolin nodes move SRC DST [--yes] [--no-wait]
                                           move the core: image, secrets, rehearsal,
                                           then a signed plan both hosts carry out;
                                           without --yes only the pre-flight
  fridolin nodes move-status ID            how move ID goes, from the core that runs

NODE, SRC and DST name a node by its id or by its name, in any case, as
fridolin nodes lists them. A name more than one node has is refused, with
their ids; a revoked node answers to its name only when no other has it.

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
	yes := fs.Bool("yes", false, "core-secrets-transfer: move the secrets (without it, a dry run)")
	replace := fs.Bool("replace", false, "core-secrets-transfer: let a value replace a different one on the target")
	fs.Usage = func() { fmt.Fprint(stderr, nodesUsage) }
	pos, err := parseInterspersed(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return 2
	}
	want := map[string][2]int{"list": {0, 0}, "join-code": {0, 0}, "approve": {1, 1}, "revoke": {1, 1}, "tags": {1, 2}, "call": {2, 3}, "update": {0, 1}, "auto-update": {2, 2}, "core-secrets-transfer": {2, 2}, "move-check": {2, 2}, "move": {2, 2}, "move-status": {1, 1}}
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
	if (*all || *force) && sub != "update" || *noWait && sub != "update" && sub != "move" {
		fmt.Fprintln(stderr, "--all and --force go with update, --no-wait with update and move")
		return 2
	}
	if *replace && sub != "core-secrets-transfer" || *yes && sub != "core-secrets-transfer" && sub != "move" {
		fmt.Fprintln(stderr, "--replace goes with core-secrets-transfer, --yes with it and move")
		return 2
	}
	if sub == "update" && *all == (len(pos) == 1) {
		fmt.Fprintln(stderr, "fridolin nodes update takes a node (its id or name), or --all")
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
			if n.Approved.CoreHost != nil {
				fmt.Fprintf(stdout, "That includes core-host. %s\nDevice key: %s. Revoke it with: fridolin nodes revoke %s\n", nodeproto.CoreHostApproval, nodeproto.Fingerprint(n.PublicKey), n.ID)
			}
		default:
			fmt.Fprintf(stdout, "Approved the device %s (%s).\n", n.Name, n.ID)
			if n.Commands() == nodeproto.CommandsPending {
				fmt.Fprintf(stdout, "It offers %s. Approve that with: fridolin nodes approve %s --commands\n", n.Declared.Summary(), n.ID)
				if n.Declared.CoreHost != nil {
					fmt.Fprintf(stdout, "It asks for core-host. %s\nDevice key: %s\n", nodeproto.CoreHostApproval, nodeproto.Fingerprint(n.PublicKey))
				}
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
	case "core-secrets-transfer":
		return nodesCoreSecretsTransfer(cfg, pos[0], pos[1], *yes, *replace, *asJSON, stdout, stderr)
	case "move-check", "move", "move-status":
		api := func(method, path string, body, out any) error { return controlcli.API(cfg, method, path, body, out) }
		switch sub {
		case "move-check":
			return nodesMoveCheck(api, pos[0], pos[1], *asJSON, stdout, stderr)
		case "move":
			return nodesMove(api, pos[0], pos[1], *yes, *noWait, *asJSON, stdout, stderr)
		}
		return nodesMoveStatus(api, pos[0], *asJSON, stdout, stderr)
	}
	return 0
}

// nodeUpdatePoll and nodeUpdateWait pace `fridolin nodes update`'s wait.
// nodeUpdateWait holds until the core says how long it waits itself (the
// update's wait_until: an hour and a half for a box), and a little after
// that, for the core to record how it ended. nodeUpdateProgress is how
// often the wait says it is still going.
var (
	nodeUpdatePoll     = 2 * time.Second
	nodeUpdateWait     = 12 * time.Minute
	nodeUpdateGrace    = time.Minute
	nodeUpdateProgress = time.Minute
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
		// The core answers with the id of the node a name names.
		started[cmp.Or(resp.ID, id)] = resp.To
		fmt.Fprintf(stdout, "Updating %s to %s…\n", id, resp.To)
	}
	if noWait || len(started) == 0 {
		if failed {
			return 1
		}
		return 0
	}
	begun := time.Now()
	deadlines := map[string]time.Time{}
	for id := range started {
		deadlines[id] = begun.Add(nodeUpdateWait)
	}
	waiting := func() bool {
		for id := range started {
			if time.Now().Before(deadlines[id]) {
				return true
			}
		}
		return false
	}
	progress := begun.Add(nodeUpdateProgress)
	for len(started) > 0 && waiting() {
		time.Sleep(nodeUpdatePoll)
		var resp httpapi.NodesResponse
		if !call(http.MethodGet, "/api/nodes", nil, &resp) {
			return 1
		}
		say := time.Now().After(progress)
		if say {
			progress = time.Now().Add(nodeUpdateProgress)
		}
		for _, n := range resp.Nodes {
			to, ok := started[n.ID]
			if !ok || n.Update == nil || n.Update.To != to {
				continue
			}
			if u := n.Update.WaitUntil; u != nil && u.Add(nodeUpdateGrace).After(deadlines[n.ID]) {
				deadlines[n.ID] = u.Add(nodeUpdateGrace)
			}
			if say && n.Update.Running() {
				fmt.Fprintf(stdout, "%s (%s): still updating (%s, %v so far), the core waits until %s\n", n.Name, n.ID, n.Update.State, time.Since(begun).Round(time.Second), deadlines[n.ID].Add(-nodeUpdateGrace).Local().Format("15:04"))
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
		fmt.Fprintf(stderr, "%s: still updating after %v; fridolin nodes shows how it ends\n", id, time.Since(begun).Round(time.Second))
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
	if resp.BoxCommand != "" {
		// A core that knows where its box script is published offers a
		// node in a box first, as !join does (docs/NODES.md).
		fmt.Fprintf(w, "A node in a box (Docker; it asks for the rest):\n  macOS, Linux (sh):\n    %s\n", resp.BoxCommand)
		if resp.BoxCommandPowerShell != "" {
			fmt.Fprintf(w, "  Windows (PowerShell):\n    %s\n", resp.BoxCommandPowerShell)
		}
		fmt.Fprint(w, "\nOr natively, with fridolin installed on it:\n")
	}
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
