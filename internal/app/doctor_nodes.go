package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/doctor"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	"github.com/Icewater1337/fridolin/internal/version"
)

// nodeStaleAfter is how long an approved node may go unheard before
// doctor warns: agents report every 20 seconds.
const nodeStaleAfter = 5 * time.Minute

// The node checks, where nodes.enabled is on: doctor prints FrIDE's
// after the others.
var (
	nodesDoctorChecks = []doctor.Check{
		{ID: "nodes-port", Title: "node port", Required: true, Applies: nodesOn, Run: checkNodesPort},
		{ID: "nodes-seen", Title: "approved nodes heard from", Applies: nodesOn, Run: checkNodesSeen},
		{ID: "nodes-version", Title: "nodes run the core's version", Applies: nodesOn, Run: checkNodesVersion},
		{ID: "nodes-programs", Title: "command agents' programs allowed on their nodes", Applies: nodesOn, Run: checkNodesPrograms},
	}
	nodesFrideDoctorCheck = doctor.Check{ID: "nodes-fride", Title: "FrIDE on the nodes", Required: true, Applies: nodesOn, Run: checkNodesFride}
)

// nodesOn applies the node checks where nodes.enabled is on.
func nodesOn(env *doctor.Env) bool { return env.Config != nil && env.Config.Nodes.Enabled }

// localAddress is where doctor reaches a port bound to listen: loopback
// for every interface.
func localAddress(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return net.JoinHostPort(host, port)
}

// checkNodesPort is M2's gate on this machine: the node port answers.
func checkNodesPort(ctx context.Context, env *doctor.Env) doctor.Result {
	listen := env.Config.Nodes.Listen
	if env.Config.Core.Tailnet.Enabled {
		return checkNodesPortOnTailnet(env, listen)
	}
	addr := localAddress(listen)
	if err := env.Dial(ctx, addr); err != nil {
		return doctor.Result{Status: doctor.Fail, Detail: fmt.Sprintf("nothing answers on %s (nodes.listen %s): %v", addr, listen, err),
			Fix: "start the bridge (fridolin service status); its log says why the node port did not open, such as another program on the port"}
	}
	return doctor.Result{Status: doctor.OK, Detail: "the node port answers on " + addr}
}

// checkNodesPortOnTailnet: with core.tailnet on, the node port listens
// on the core's tailnet node only (App.NodeListener), which doctor does
// not reach from here; it answers once that node is up.
func checkNodesPortOnTailnet(env *doctor.Env, listen string) doctor.Result {
	var st tailnetStateRecord
	ok, err := readCoreState(env, tailnetStateFile, &st)
	switch {
	case err != nil:
		return doctor.Result{Status: doctor.Fail, Detail: "reading the core's tailnet state: " + err.Error()}
	case !ok || st.State != "up":
		return doctor.Result{Status: doctor.Fail, Detail: fmt.Sprintf("the node port (nodes.listen %s) listens on the tailnet, and the core's tailnet node is not up", listen),
			Fix: "see the tailnet check; nodes connect once the core is on the tailnet"}
	}
	_, port, _ := net.SplitHostPort(listen)
	return doctor.Result{Status: doctor.OK, Detail: "the node port listens on the tailnet at " + net.JoinHostPort(strings.TrimSuffix(st.DNSName, "."), port)}
}

// doctorNode is a node as doctor reads it from the running bridge or
// from nodes.json, which has no online.
type doctorNode struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Device   string     `json:"device"`
	LastSeen *time.Time `json:"last_seen"`
	Online   bool       `json:"online"`
	Version  string     `json:"version"`
	Update   *struct {
		State string `json:"state"`
		To    string `json:"to"`
		Error string `json:"error"`
	} `json:"update"`
	Declared struct {
		Exec         *nodeproto.ExecCap         `json:"exec"`
		Fride        *nodeproto.FrideCap        `json:"fride"`
		AndroidBuild *nodeproto.AndroidBuildCap `json:"android_build"`
	} `json:"declared"`
	Approved struct {
		Exec         *nodeproto.ExecCap         `json:"exec"`
		Fride        *nodeproto.FrideCap        `json:"fride"`
		AndroidBuild *nodeproto.AndroidBuildCap `json:"android_build"`
	} `json:"approved"`
}

// node is n as much as nodes.ResolveIn needs to find it by its ref.
func (n doctorNode) node() *nodes.Node {
	return &nodes.Node{ID: n.ID, Name: n.Name, Device: n.Device}
}

// stale reports whether the core has not heard from n for nodeStaleAfter
// at now: never while it is connected.
func (n doctorNode) stale(now time.Time) bool {
	return !n.Online && (n.LastSeen == nil || now.Sub(*n.LastSeen) > nodeStaleAfter)
}

// liveNodesTimeout bounds asking the running bridge for its nodes.
const liveNodesTimeout = 5 * time.Second

// askBridgeNodes is doctor.Env.LiveNodes on the real system: GET /api/nodes
// of the bridge on cfg's http_listen, with the dashboard token, as
// `fridolin nodes` asks it.
func askBridgeNodes(ctx context.Context, cfg *config.Settings) ([]byte, error) {
	return askBridge(ctx, cfg, "/api/nodes")
}

// askBridgeBrains is doctor.Env.LiveBrains on the real system: GET
// /api/brains, as askBridgeNodes asks.
func askBridgeBrains(ctx context.Context, cfg *config.Settings) ([]byte, error) {
	return askBridge(ctx, cfg, "/api/brains")
}

// askBridge GETs path of the bridge on cfg's http_listen, with the
// dashboard token.
func askBridge(ctx context.Context, cfg *config.Settings, path string) ([]byte, error) {
	if !cfg.HTTPEnabled || cfg.HTTPListen == "" {
		return nil, errors.New("the bridge's HTTP server is off")
	}
	token, err := httpapi.ReadToken(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, liveNodesTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+localAddress(cfg.HTTPListen)+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Bridge-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the bridge answered %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

// readNodes reads the nodes for a check: from the running bridge, which
// knows which are connected now, and else from the registry file, whose
// last_seen the bridge writes once a minute. A result is what to report
// when there is nothing to read.
func readNodes(ctx context.Context, env *doctor.Env) ([]doctorNode, *doctor.Result) {
	list, _, res := readNodesVersion(ctx, env)
	return list, res
}

// readNodesVersion is readNodes with the core's version: the running
// bridge's, else this binary's.
func readNodesVersion(ctx context.Context, env *doctor.Env) ([]doctorNode, string, *doctor.Result) {
	var live struct {
		Version string       `json:"version"`
		Nodes   []doctorNode `json:"nodes"`
	}
	if env.LiveNodes != nil {
		if data, err := env.LiveNodes(ctx, env.Config); err == nil && json.Unmarshal(data, &live) == nil {
			return live.Nodes, cmp.Or(live.Version, version.String()), nil
		}
	}
	list, res := readNodesFile(env)
	return list, version.String(), res
}

// readNodesFile reads the registry file, which knows nothing of who is
// connected.
func readNodesFile(env *doctor.Env) ([]doctorNode, *doctor.Result) {
	path := filepath.Join(env.Config.Layout().Nodes(), nodes.RegistryFile)
	data, err := env.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &doctor.Result{Status: doctor.OK, Detail: "no nodes yet"}
	}
	if err != nil {
		return nil, &doctor.Result{Status: doctor.Warn, Detail: err.Error(), Fix: "check the permissions of " + path}
	}
	var file struct {
		Nodes []doctorNode `json:"nodes"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, &doctor.Result{Status: doctor.Warn, Detail: fmt.Sprintf("%s does not read: %v", path, err), Fix: "fix the JSON by hand, or restore it from a backup"}
	}
	for i := range file.Nodes {
		// Only the bridge knows which are connected.
		file.Nodes[i].Online = false
	}
	return file.Nodes, nil
}

// checkNodesFride is M6's gate on the core: every node whose FrIDE the
// owner approved offers it now, which its agent does only while FrIDE
// answers there, and was heard from lately.
func checkNodesFride(ctx context.Context, env *doctor.Env) doctor.Result {
	list, res := readNodes(ctx, env)
	if res != nil {
		return *res
	}
	now := env.Time()
	var ok, down []string
	noLogin := false
	for _, n := range list {
		if n.Device != nodeproto.DeviceApproved || n.Approved.Fride == nil {
			continue
		}
		switch {
		case n.stale(now):
			down = append(down, n.Name+" ("+n.ID+", not heard from)")
		case n.Declared.Fride == nil:
			down = append(down, n.Name+" ("+n.ID+", FrIDE does not answer)")
		case n.Declared.Fride.NotLoggedIn:
			down = append(down, n.Name+" ("+n.ID+", FrIDE's Claude is not logged in)")
			noLogin = true
		default:
			ok = append(ok, n.Name+" ("+n.Declared.Fride.URL+")")
		}
	}
	if len(ok)+len(down) == 0 {
		return doctor.Result{Status: doctor.OK, Detail: "no node offers FrIDE yet; coding work waits until one does"}
	}
	if len(down) > 0 {
		slices.Sort(down)
		fix := "on each, start FrIDE with fride serve --under-fridolin, and check fridolin node fride status and fridolin node status"
		if noLogin {
			fix += "; where its Claude is not logged in, " + nodeproto.FrideLoginFix
		}
		return doctor.Result{Status: doctor.Warn,
			Detail: fmt.Sprintf("%d of %d nodes with FrIDE cannot take work: %s", len(down), len(ok)+len(down), strings.Join(down, "; ")),
			Fix:    fix}
	}
	slices.Sort(ok)
	return doctor.Result{Status: doctor.OK, Detail: "FrIDE answers on " + strings.Join(ok, ", ")}
}

// checkNodesPrograms warns about approved command agents whose node does
// not run their program: not in its allowlist, and not added by the core.
func checkNodesPrograms(ctx context.Context, env *doctor.Env) doctor.Result {
	cmds := approvedCommands(env.Config.BrainDirs)
	if len(cmds) == 0 {
		return doctor.Result{Status: doctor.OK, Detail: "no approved agent runs a program on a node"}
	}
	list, res := readNodes(ctx, env)
	if res != nil {
		return *res
	}
	var bad []string
	fix := "approve the node's commands on the Nodes page, and add the program's folder to exec.accept_from_core in the node's node.json (docs/NODES.md, Programs from the core)"
	ambiguous := false
	for _, c := range cmds {
		// The node as the agent's runs find it (#251).
		n, err := nodes.ResolveIn(list, c.Node, doctorNode.node)
		switch {
		case errors.Is(err, nodes.ErrAmbiguousNode):
			bad = append(bad, fmt.Sprintf("%s: %v", c.Agent, err))
			ambiguous = true
			continue
		case err != nil:
			bad = append(bad, fmt.Sprintf("%s: there is no node %q", c.Agent, c.Node))
			continue
		}
		eff := nodeproto.Intersect(nodeproto.Capabilities{Exec: n.Declared.Exec}, nodeproto.Capabilities{Exec: n.Approved.Exec})
		switch {
		case n.Device != nodeproto.DeviceApproved:
			bad = append(bad, fmt.Sprintf("%s: node %s is %s", c.Agent, n.Name, n.Device))
		case eff.PermitsExec(c.Argv) == nil:
		case eff.Allows(nodeproto.MethodExecPrograms) && slices.ContainsFunc(eff.Exec.AcceptFromCore, func(f string) bool { return under(f, c.Argv[0]) }):
			bad = append(bad, fmt.Sprintf("%s: node %s accepts %s, and has not taken it yet (it does when it connects; the Nodes page says why it refused one)", c.Agent, n.Name, c.Argv[0]))
		default:
			bad = append(bad, fmt.Sprintf("%s: node %s does not allow %s", c.Agent, n.Name, c.Argv[0]))
		}
	}
	if len(bad) > 0 {
		slices.Sort(bad)
		if ambiguous {
			fix += "; where more than one node has the name an agent gives, give the node's id in the agent's command instead"
		}
		return doctor.Result{Status: doctor.Warn, Detail: fmt.Sprintf("%d of %d command agents cannot run: %s", len(bad), len(cmds), strings.Join(bad, "; ")), Fix: fix}
	}
	return doctor.Result{Status: doctor.OK, Detail: fmt.Sprintf("the nodes allow the programs of all %d command agents", len(cmds))}
}

// checkNodesSeen warns about approved nodes the core has not heard from
// for nodeStaleAfter.
func checkNodesSeen(ctx context.Context, env *doctor.Env) doctor.Result {
	list, res := readNodes(ctx, env)
	if res != nil {
		return *res
	}
	file := struct{ Nodes []doctorNode }{list}
	now := env.Time()
	approved := 0
	var stale []string
	for _, n := range file.Nodes {
		if n.Device != nodeproto.DeviceApproved {
			continue
		}
		approved++
		switch {
		case !n.stale(now):
		case n.LastSeen == nil:
			stale = append(stale, n.Name+" ("+n.ID+", never)")
		default:
			stale = append(stale, fmt.Sprintf("%s (%s, %v ago)", n.Name, n.ID, now.Sub(*n.LastSeen).Round(time.Minute)))
		}
	}
	if approved == 0 {
		return doctor.Result{Status: doctor.OK, Detail: "no approved nodes yet"}
	}
	if len(stale) > 0 {
		slices.Sort(stale)
		return doctor.Result{Status: doctor.Warn,
			Detail: fmt.Sprintf("%d of %d approved nodes not heard from in %v: %s", len(stale), approved, nodeStaleAfter, strings.Join(stale, "; ")),
			Fix:    "on each, fridolin node status shows whether its agent runs; fridolin node start starts it"}
	}
	return doctor.Result{Status: doctor.OK, Detail: fmt.Sprintf("%d approved nodes, each heard from in the last %v", approved, nodeStaleAfter)}
}

// checkNodesVersion warns about approved nodes that run an older fridolin
// than the core, and about updates that failed (docs/NODES.md, Updates).
func checkNodesVersion(ctx context.Context, env *doctor.Env) doctor.Result {
	list, core, res := readNodesVersion(ctx, env)
	if res != nil {
		return *res
	}
	var older, failed []string
	approved := 0
	for _, n := range list {
		if n.Device != nodeproto.DeviceApproved {
			continue
		}
		approved++
		if u := n.Update; u != nil && u.State == nodes.UpdateFailed {
			failed = append(failed, fmt.Sprintf("%s (%s, to %s: %s)", n.Name, n.ID, u.To, u.Error))
		}
		if n.Version != "" && nodeproto.Outdated(n.Version, core) {
			older = append(older, fmt.Sprintf("%s (%s, %s)", n.Name, n.ID, n.Version))
		}
	}
	if approved == 0 {
		return doctor.Result{Status: doctor.OK, Detail: "no approved nodes yet"}
	}
	slices.Sort(older)
	slices.Sort(failed)
	var parts []string
	if len(older) > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d approved nodes run an older or other fridolin than the core's %s: %s", len(older), approved, core, strings.Join(older, "; ")))
	}
	if len(failed) > 0 {
		parts = append(parts, "the last update failed on "+strings.Join(failed, "; "))
	}
	if len(parts) > 0 {
		return doctor.Result{Status: doctor.Warn, Detail: strings.Join(parts, "; and "),
			Fix: "fridolin nodes update <id> (or --all), or Update on the Nodes page; a node whose fridolin predates updates is updated by hand once (docs/NODES.md, Updates)"}
	}
	return doctor.Result{Status: doctor.OK, Detail: fmt.Sprintf("%d approved nodes run %s, the core's version", approved, core)}
}
