package app

import (
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
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
)

// nodeStaleAfter is how long an approved node may go unheard before
// doctor warns: agents report every 20 seconds.
const nodeStaleAfter = 5 * time.Minute

// nodesOn applies the node checks where nodes.enabled is on.
func nodesOn(env *DoctorEnv) bool { return env.Config != nil && env.Config.Nodes.Enabled }

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
func checkNodesPort(ctx context.Context, env *DoctorEnv) DoctorResult {
	listen := env.Config.Nodes.Listen
	if env.Config.Core.Tailnet.Enabled {
		return checkNodesPortOnTailnet(env, listen)
	}
	addr := localAddress(listen)
	if err := env.Dial(ctx, addr); err != nil {
		return DoctorResult{Status: DoctorFail, Detail: fmt.Sprintf("nothing answers on %s (nodes.listen %s): %v", addr, listen, err),
			Fix: "start the bridge (fridolin service status); its log says why the node port did not open, such as another program on the port"}
	}
	return DoctorResult{Status: DoctorOK, Detail: "the node port answers on " + addr}
}

// checkNodesPortOnTailnet: with core.tailnet on, the node port listens
// on the core's tailnet node only (App.NodeListener), which doctor does
// not reach from here; it answers once that node is up.
func checkNodesPortOnTailnet(env *DoctorEnv, listen string) DoctorResult {
	var st tailnetStateRecord
	ok, err := env.readCoreState(tailnetStateFile, &st)
	switch {
	case err != nil:
		return DoctorResult{Status: DoctorFail, Detail: "reading the core's tailnet state: " + err.Error()}
	case !ok || st.State != "up":
		return DoctorResult{Status: DoctorFail, Detail: fmt.Sprintf("the node port (nodes.listen %s) listens on the tailnet, and the core's tailnet node is not up", listen),
			Fix: "see the tailnet check; nodes connect once the core is on the tailnet"}
	}
	_, port, _ := net.SplitHostPort(listen)
	return DoctorResult{Status: DoctorOK, Detail: "the node port listens on the tailnet at " + net.JoinHostPort(strings.TrimSuffix(st.DNSName, "."), port)}
}

// doctorNode is a node as doctor reads it from the running bridge or
// from nodes.json, which has no online.
type doctorNode struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Device   string     `json:"device"`
	LastSeen *time.Time `json:"last_seen"`
	Online   bool       `json:"online"`
	Declared struct {
		Fride        *nodeproto.FrideCap        `json:"fride"`
		AndroidBuild *nodeproto.AndroidBuildCap `json:"android_build"`
	} `json:"declared"`
	Approved struct {
		Fride        *nodeproto.FrideCap        `json:"fride"`
		AndroidBuild *nodeproto.AndroidBuildCap `json:"android_build"`
	} `json:"approved"`
}

// stale reports whether the core has not heard from n for nodeStaleAfter
// at now: never while it is connected.
func (n doctorNode) stale(now time.Time) bool {
	return !n.Online && (n.LastSeen == nil || now.Sub(*n.LastSeen) > nodeStaleAfter)
}

// liveNodesTimeout bounds asking the running bridge for its nodes.
const liveNodesTimeout = 5 * time.Second

// askBridgeNodes is DoctorEnv.LiveNodes on the real system: GET /api/nodes
// of the bridge on cfg's http_listen, with the dashboard token, as
// `fridolin nodes` asks it.
func askBridgeNodes(ctx context.Context, cfg *config.Settings) ([]byte, error) {
	if !cfg.HTTPEnabled || cfg.HTTPListen == "" {
		return nil, errors.New("the bridge's HTTP server is off")
	}
	token, err := httpapi.ReadToken(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, liveNodesTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+localAddress(cfg.HTTPListen)+"/api/nodes", nil)
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
func readNodes(ctx context.Context, env *DoctorEnv) ([]doctorNode, *DoctorResult) {
	var live struct {
		Nodes []doctorNode `json:"nodes"`
	}
	if env.LiveNodes != nil {
		if data, err := env.LiveNodes(ctx, env.Config); err == nil && json.Unmarshal(data, &live) == nil {
			return live.Nodes, nil
		}
	}
	path := filepath.Join(env.Config.Layout().Nodes(), nodes.RegistryFile)
	data, err := env.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &DoctorResult{Status: DoctorOK, Detail: "no nodes yet"}
	}
	if err != nil {
		return nil, &DoctorResult{Status: DoctorWarn, Detail: err.Error(), Fix: "check the permissions of " + path}
	}
	var file struct {
		Nodes []doctorNode `json:"nodes"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, &DoctorResult{Status: DoctorWarn, Detail: fmt.Sprintf("%s does not read: %v", path, err), Fix: "fix the JSON by hand, or restore it from a backup"}
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
func checkNodesFride(ctx context.Context, env *DoctorEnv) DoctorResult {
	list, res := readNodes(ctx, env)
	if res != nil {
		return *res
	}
	now := env.now()
	var ok, down []string
	for _, n := range list {
		if n.Device != nodeproto.DeviceApproved || n.Approved.Fride == nil {
			continue
		}
		switch {
		case n.stale(now):
			down = append(down, n.Name+" ("+n.ID+", not heard from)")
		case n.Declared.Fride == nil:
			down = append(down, n.Name+" ("+n.ID+", FrIDE does not answer)")
		default:
			ok = append(ok, n.Name+" ("+n.Declared.Fride.URL+")")
		}
	}
	if len(ok)+len(down) == 0 {
		return DoctorResult{Status: DoctorOK, Detail: "no node offers FrIDE yet; coding work waits until one does"}
	}
	if len(down) > 0 {
		slices.Sort(down)
		return DoctorResult{Status: DoctorWarn,
			Detail: fmt.Sprintf("%d of %d nodes with FrIDE cannot take work: %s", len(down), len(ok)+len(down), strings.Join(down, "; ")),
			Fix:    "on each, start FrIDE with fride serve --under-fridolin, and check fridolin node fride status and fridolin node status"}
	}
	slices.Sort(ok)
	return DoctorResult{Status: DoctorOK, Detail: "FrIDE answers on " + strings.Join(ok, ", ")}
}

// checkNodesSeen warns about approved nodes the core has not heard from
// for nodeStaleAfter.
func checkNodesSeen(ctx context.Context, env *DoctorEnv) DoctorResult {
	list, res := readNodes(ctx, env)
	if res != nil {
		return *res
	}
	file := struct{ Nodes []doctorNode }{list}
	now := env.now()
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
		return DoctorResult{Status: DoctorOK, Detail: "no approved nodes yet"}
	}
	if len(stale) > 0 {
		slices.Sort(stale)
		return DoctorResult{Status: DoctorWarn,
			Detail: fmt.Sprintf("%d of %d approved nodes not heard from in %v: %s", len(stale), approved, nodeStaleAfter, strings.Join(stale, "; ")),
			Fix:    "on each, fridolin node status shows whether its agent runs; fridolin node start starts it"}
	}
	return DoctorResult{Status: DoctorOK, Detail: fmt.Sprintf("%d approved nodes, each heard from in the last %v", approved, nodeStaleAfter)}
}
