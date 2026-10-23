package tailnet

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"tailscale.com/client/local"
	"tailscale.com/tsnet"
)

// tsnetNode is a Node on tailscale.com/tsnet: Tailscale in this process,
// with a userspace network stack, so it needs no TUN device, no root and
// no Tailscale on the host.
type tsnetNode struct {
	srv *tsnet.Server

	// used is set before the first call that starts tsnet.
	used atomic.Bool

	mu sync.Mutex
	lc *local.Client
}

// New makes the core's node; nothing reaches the network before Up or a
// Listen.
func New(opts Options) (Node, error) {
	if opts.Hostname == "" || opts.Dir == "" {
		return nil, errors.New("tailnet: a hostname and a state folder are required")
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &tsnetNode{srv: &tsnet.Server{
		Hostname: opts.Hostname,
		Dir:      opts.Dir,
		AuthKey:  opts.AuthKey,
		// The library logs a great deal at its own level; only what it
		// means for a person (an auth URL, say) goes to the bridge's log.
		Logf:     func(string, ...any) {},
		UserLogf: func(format string, args ...any) { logf("tailnet: "+format, args...) },
	}}, nil
}

func (n *tsnetNode) client() (*local.Client, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.lc != nil {
		return n.lc, nil
	}
	lc, err := n.srv.LocalClient()
	if err != nil {
		return nil, err
	}
	n.lc = lc
	return lc, nil
}

func (n *tsnetNode) Up(ctx context.Context) (Status, error) {
	n.used.Store(true)
	st, err := n.srv.Up(ctx)
	if err != nil {
		return Status{}, err
	}
	out := Status{HTTPS: len(st.CertDomains) > 0}
	if st.CurrentTailnet != nil {
		out.MagicDNS = st.CurrentTailnet.MagicDNSEnabled
	}
	if st.Self != nil {
		out.DNSName = strings.TrimSuffix(st.Self.DNSName, ".")
	}
	for _, ip := range st.TailscaleIPs {
		out.IPs = append(out.IPs, ip.String())
	}
	out.HTTPS = out.HTTPS && out.MagicDNS
	return out, nil
}

func (n *tsnetNode) Listen(network, addr string) (net.Listener, error) {
	n.used.Store(true)
	return n.srv.Listen(network, addr)
}

func (n *tsnetNode) ListenTLS(network, addr string) (net.Listener, error) {
	n.used.Store(true)
	return n.srv.ListenTLS(network, addr)
}

func (n *tsnetNode) WhoIs(ctx context.Context, remoteAddr string) (Identity, error) {
	lc, err := n.client()
	if err != nil {
		return Identity{}, err
	}
	who, err := lc.WhoIs(ctx, remoteAddr)
	if err != nil {
		return Identity{}, err
	}
	var id Identity
	if who.UserProfile != nil {
		id.LoginName = who.UserProfile.LoginName
	}
	if who.Node != nil {
		id.Node = strings.TrimSuffix(who.Node.ComputedName, ".")
		if id.Node == "" {
			id.Node = strings.TrimSuffix(who.Node.Name, ".")
		}
		id.Tags = append(id.Tags, who.Node.Tags...)
		if len(id.Tags) > 0 {
			// A tagged device belongs to its tags, not to a user: the
			// profile then is a placeholder ("tagged-devices").
			id.LoginName = strings.Join(id.Tags, ",")
		}
	}
	return id, nil
}

func (n *tsnetNode) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	n.used.Store(true)
	return n.srv.Dial(ctx, network, addr)
}

// Close leaves the tailnet. tsnet must not be closed before or while it
// starts, so a node that started waits for that first; one never used
// has nothing to close.
func (n *tsnetNode) Close() error {
	if !n.used.Load() {
		return nil
	}
	_ = n.srv.Start()
	return n.srv.Close()
}
