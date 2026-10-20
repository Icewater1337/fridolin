package app

import (
	"context"
	"log"
	"net"
	"os"
	"slices"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
)

// newNodes opens the node registry in the data dir's nodes folder. The
// port opens in Start, and only with nodes.enabled; the registry is there
// either way, so the dashboard and the CLI can show and revoke nodes while
// the port is closed.
func newNodes(cfgs *config.Store) (*nodes.Service, error) {
	return nodes.New(nodes.Options{Dir: cfgs.Boot().Layout().Nodes()})
}

// startNodes opens the node port when nodes.enabled was on at startup
// (both settings wait for a restart). A port that cannot be bound is
// logged, not fatal: WhatsApp must keep working; `fridolin doctor` reports
// the port as down.
func (b *App) startNodes(context.Context) {
	if !b.nodesEnabled {
		return
	}
	// On the tailnet when core.tailnet is on (M1), else as the HTTP
	// server binds.
	if err := b.nodes.ListenAndServe(b.nodesListen, b.NodeListener); err != nil {
		log.Printf("nodes: the node port did not open, so nodes cannot connect: %v", err)
	}
}

// nodeCoreAddress is the address a new node reaches the core's node port
// at: the tailnet name when the core is on the tailnet, else the host the
// port is bound to, or this machine's name when it is bound to every
// interface.
func nodeCoreAddress(listen string, tailnet config.TailnetConfig, hostname string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	switch {
	case tailnet.Enabled && tailnet.Hostname != "":
		host = tailnet.Hostname
	case host == "" || host == "0.0.0.0" || host == "::":
		host = hostname
	}
	return net.JoinHostPort(host, port)
}

// joinCommand is the line `fridolin nodes join-code` prints for code.
func (b *App) joinCommand(code string) string {
	listen := b.nodesListen
	if addr := b.nodes.Addr(); addr != "" {
		// The port actually bound, for a listen address with port 0.
		if _, port, err := net.SplitHostPort(addr); err == nil {
			if host, _, err := net.SplitHostPort(listen); err == nil {
				listen = net.JoinHostPort(host, port)
			}
		}
	}
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "<this-machine>"
	}
	return "fridolin node join " + code + " --core " + nodeCoreAddress(listen, b.cfgs.Get().Core.Tailnet, hostname)
}

func (b *App) newNodesAPI() *httpapi.NodesAPI {
	return &httpapi.NodesAPI{
		Nodes:       func() httpapi.NodesService { return lockdownNodes{Service: b.nodes, locked: b.locked} },
		Enabled:     b.nodesEnabled,
		Listen:      b.nodes.Addr,
		JoinCommand: b.joinCommand,
	}
}

// lockdownNodes is the node registry as the core's callers reach it: in
// a lockdown (M5) it refuses the calls that act on a node, exec and
// fs.write, as the policy refuses a run's writing tools; reading a node
// (host facts, a granted folder) and a notification still go.
type lockdownNodes struct {
	*nodes.Service
	locked func() bool
}

// nodeWrites are the methods a lockdown refuses.
var nodeWrites = []string{nodeproto.MethodExec, nodeproto.MethodFSWrite}

func (n lockdownNodes) Call(ctx context.Context, id, method string, params, out any) error {
	if n.locked() && slices.Contains(nodeWrites, method) {
		return &nodeproto.Error{Code: nodeproto.CodeDenied, Message: "the bridge is in lockdown: only reading goes ahead; unlock it in the dashboard or with !unlock"}
	}
	return n.Service.Call(ctx, id, method, params, out)
}

// fleetText answers !fleet.
func (b *App) fleetText() string {
	views, err := b.nodes.Views()
	if err != nil {
		log.Printf("nodes: reading the registry for !fleet: %v", err)
	}
	list := make([]outboundmsg.FleetNode, 0, len(views))
	for _, v := range views {
		n := outboundmsg.FleetNode{
			ID: v.ID, Name: v.Name, OS: v.OS, Online: v.Online,
			Device: v.Device, Commands: v.Commands, Tags: v.Tags, Offers: v.Effective.Summary(),
		}
		if v.LastSeen != nil {
			n.LastSeen = *v.LastSeen
		}
		list = append(list, n)
	}
	return outboundmsg.FleetReply(b.nodesEnabled, list, time.Now())
}
