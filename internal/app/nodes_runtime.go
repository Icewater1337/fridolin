package app

import (
	"context"
	"log"
	"net"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/provenance"
	"github.com/Icewater1337/fridolin/internal/version"
)

// newNodes opens the node registry in the data dir's nodes folder. The
// port opens in Start, and only with nodes.enabled; the registry is there
// either way, so the dashboard and the CLI can show and revoke nodes while
// the port is closed.
// A machine that joins is announced to the owner (nodeJoined); the news
// of FrIDE and of agent sessions goes to their runtimes.
func newNodes(b *App) (*nodes.Service, error) {
	onEvent := func(node string, ev nodeproto.Event) {
		b.fride.onEvent(node, ev)
		b.work.onEvent(node, ev)
	}
	return nodes.New(nodes.Options{
		Dir: b.cfgs.Boot().Layout().Nodes(), OnEvent: onEvent, OnDeclare: b.fride.onDeclare, OnJoin: b.nodeJoined, OnCoreHost: b.nodeCoreHost,
		// A node that connects takes the programs of its command agents.
		OnConnect: func(n *nodes.Node) { b.programs.connected(n.ID) },
		// Updates: the core's own version, its builds for the nodes, and
		// what keeps a node from being updated now (nodes_update.go).
		Version: version.String(), BinariesDir: nodeBinariesDir(b.exe), Busy: b.nodeBusy, Hold: b.nodeHold,
		// A node in a box is updated to the images of this version.
		BoxImages: boxImages(httpapi.BoxSourceFromEnv(os.Getenv, version.String()).Registry, provenance.Registry{}),
	})
}

// boxImages names the images of a version on registry (ghcr.io/OWNER),
// pinned by digest, for box updates; nil when the core's image does not
// say where it is published (a local build).
func boxImages(registry string, reg provenance.Registry) func(context.Context, string) (string, string, error) {
	if registry == "" {
		return nil
	}
	return func(ctx context.Context, v string) (string, string, error) {
		node, err := reg.Digest(ctx, registry+"/fridolin-core", v)
		if err != nil {
			return "", "", err
		}
		fride, err := reg.Digest(ctx, registry+"/fridolin-fride", v)
		if err != nil {
			return "", "", err
		}
		return node, fride, nil
	}
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
// at: on the tailnet, the core's full MagicDNS name once it joined
// (dnsName, fridolin-core.example.ts.net), which resolves on every device
// of the tailnet, else its short name; off the tailnet, the host the port
// is bound to, or this machine's name when it is bound to every
// interface.
func nodeCoreAddress(listen string, tailnet config.TailnetConfig, dnsName, hostname string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	switch {
	case tailnet.Enabled && dnsName != "":
		host = strings.TrimSuffix(dnsName, ".")
	case tailnet.Enabled && tailnet.Hostname != "":
		host = tailnet.Hostname
	case host == "" || host == "0.0.0.0" || host == "::":
		host = hostname
	}
	return net.JoinHostPort(host, port)
}

// joinCore is the core's node address a joining machine is given
// (`fridolin nodes join-code`).
func (b *App) joinCore() string {
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
	dnsName := ""
	if st := b.tailnetStatus(); st != nil && st.State == "up" {
		dnsName = st.DNSName
	}
	return nodeCoreAddress(listen, b.cfgs.Get().Core.Tailnet, dnsName, hostname)
}

func (b *App) newNodesAPI() *httpapi.NodesAPI {
	return &httpapi.NodesAPI{
		Nodes:       func() httpapi.NodesService { return lockdownNodes{Service: b.nodes, locked: b.locked, work: b.work} },
		Version:     b.nodes.Version(),
		Enabled:     b.nodesEnabled,
		Listen:      b.nodes.Addr,
		CoreAddress: b.joinCore,
		Box:         b.boxSource(),
	}
}

// boxSource is where a node in a box of this core's version comes from,
// as the core's image says (httpapi.BoxSourceFromEnv).
func (b *App) boxSource() httpapi.BoxSource {
	return httpapi.BoxSourceFromEnv(os.Getenv, b.nodes.Version())
}

// lockdownNodes is the node registry as the core's callers reach it: in
// a lockdown (M5) it refuses the calls that act on a node, exec and
// fs.write, as the policy refuses a run's writing tools; reading a node
// (host facts, a granted folder) and a notification still go.
type lockdownNodes struct {
	*nodes.Service
	locked func() bool
	work   *workRuntime
}

// nodeWrites are the methods a lockdown refuses: a build runs the build
// scripts it is sent.
// Starting an agent or a terminal, or acting in one, is writing too.
// Moving or forgetting host secrets (core-host) is writing too.
var nodeWrites = slices.Concat([]string{nodeproto.MethodExec, nodeproto.MethodFSWrite, nodeproto.MethodAndroidBuild}, nodeproto.WorkstationStarts, nodeproto.CoreSecretsMethods)

// CoreSecretsTransfer refuses a transfer that is to run during a
// lockdown; a dry run only reads.
func (n lockdownNodes) CoreSecretsTransfer(ctx context.Context, src, dst string, o nodes.CoreTransferOptions) (nodes.CoreTransferReport, error) {
	if o.Run && n.locked() {
		return nodes.CoreTransferReport{Problems: []string{errLockedNode.Error()}}, errLockedNode
	}
	return n.Service.CoreSecretsTransfer(ctx, src, dst, o)
}

func (n lockdownNodes) Call(ctx context.Context, id, method string, params, out any) error {
	if n.locked() && slices.Contains(nodeWrites, method) {
		return errLockedNode
	}
	if n.work != nil {
		if handled, err := n.work.dashboardCall(ctx, id, method, params, out); handled {
			return err
		}
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
			CoreHostPending: v.CoreHost != nil && v.CoreHost.Pending,
		}
		if v.LastSeen != nil {
			n.LastSeen = *v.LastSeen
		}
		list = append(list, n)
	}
	return outboundmsg.FleetReply(b.nodesEnabled, list, time.Now())
}
