package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"time"

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

// checkNodesSeen warns about approved nodes the core has not heard from
// for nodeStaleAfter.
func checkNodesSeen(_ context.Context, env *DoctorEnv) DoctorResult {
	path := filepath.Join(env.Config.Layout().Nodes(), nodes.RegistryFile)
	data, err := env.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return DoctorResult{Status: DoctorOK, Detail: "no nodes yet"}
	}
	if err != nil {
		return DoctorResult{Status: DoctorWarn, Detail: err.Error(), Fix: "check the permissions of " + path}
	}
	var file struct {
		Nodes []struct {
			ID       string     `json:"id"`
			Name     string     `json:"name"`
			Device   string     `json:"device"`
			LastSeen *time.Time `json:"last_seen"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return DoctorResult{Status: DoctorWarn, Detail: fmt.Sprintf("%s does not read: %v", path, err), Fix: "fix the JSON by hand, or restore it from a backup"}
	}
	now := env.Now()
	approved := 0
	var stale []string
	for _, n := range file.Nodes {
		if n.Device != nodeproto.DeviceApproved {
			continue
		}
		approved++
		switch {
		case n.LastSeen == nil:
			stale = append(stale, n.Name+" ("+n.ID+", never)")
		case now.Sub(*n.LastSeen) > nodeStaleAfter:
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
