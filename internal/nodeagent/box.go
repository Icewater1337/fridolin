package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The box's fixed addresses (docs/NODES.md, "Node in a box"): FrIDE's
// service on the box network, and the ports FrIDE and its webhook have
// on both containers' loopback. container/fride/entrypoint.sh forwards
// the same ports on FrIDE's side.
const (
	BoxFride       = "fride:17840"
	BoxFridePort   = 17840
	BoxWebhookPort = 17841
)

// BoxConfig is node.json's box section, set for a node in a box: a
// container of the fridolin-core image, which reaches the core over a
// tailnet device of its own (tsnet, its state in the state dir's
// tailnet folder) instead of the host's Tailscale.
type BoxConfig struct {
	// Hostname is the box's device name on the tailnet.
	Hostname string `json:"hostname"`
	// Fride is FrIDE's service on the box network, host:port (BoxFride);
	// "" for a box without FrIDE. The agent forwards FrIDE's port from
	// its loopback to it, and the webhook port from the box network to
	// its loopback, so that FrIDE's section in node.json keeps its
	// loopback addresses.
	Fride string `json:"fride,omitempty"`
}

// Check reports what is wrong with the section.
func (b *BoxConfig) Check() error {
	if b == nil {
		return nil
	}
	if err := CheckTailnetHostname(b.Hostname); err != nil {
		return fmt.Errorf("box.hostname: %w", err)
	}
	if b.Fride != "" {
		host, port, err := net.SplitHostPort(b.Fride)
		if err != nil || host == "" {
			return fmt.Errorf("box.fride: %q is not host:port", b.Fride)
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("box.fride: %q has no valid port", b.Fride)
		}
	}
	return nil
}

// CheckTailnetHostname checks a device name for the tailnet: 1 to 63
// lower-case letters, digits and hyphens, not starting or ending with a
// hyphen.
func CheckTailnetHostname(name string) error {
	if name == "" || len(name) > 63 || name[0] == '-' || name[len(name)-1] == '-' {
		return fmt.Errorf("%q is not a tailnet device name (1 to 63 letters, digits and hyphens)", name)
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return fmt.Errorf("%q is not a tailnet device name (lower-case letters, digits and hyphens only)", name)
		}
	}
	return nil
}

// BoxHostname is the tailnet name of a box whose node is called name:
// fridolin-box-<name>, in lower case, with what the tailnet does not take
// turned into hyphens.
func BoxHostname(name string) string {
	var b strings.Builder
	b.WriteString("fridolin-box-")
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) > 63 {
		s = strings.TrimRight(s[:63], "-")
	}
	return s
}

// boxForward runs the box's forwarders until ctx ends: FrIDE's port on
// the loopback to FrIDE's service, and the webhook port of the box
// network to the loopback, where FrIDE's webhook listens. Only FrIDE's
// service may reach the webhook that way. It returns at once for a node
// that is not in a box or has no FrIDE.
func (a *Agent) boxForward(ctx context.Context, wg *sync.WaitGroup) {
	b := a.Config.Box
	if b == nil || b.Fride == "" {
		return
	}
	var lc net.ListenConfig
	lo := net.JoinHostPort("127.0.0.1", strconv.Itoa(portOf(b.Fride)))
	ln, err := lc.Listen(ctx, "tcp", lo)
	if err != nil {
		a.logf("node: box: FrIDE cannot be reached at %s: %v", lo, err)
	} else {
		wg.Go(func() {
			forward(ctx, ln, func(ctx context.Context) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", b.Fride)
			}, nil, a.logf)
		})
	}
	hook := net.JoinHostPort("127.0.0.1", strconv.Itoa(BoxWebhookPort))
	frideHost, _, _ := net.SplitHostPort(b.Fride)
	fromFride := func(addr net.Addr) bool {
		ta, ok := addr.(*net.TCPAddr)
		if !ok {
			return false
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, frideHost)
		if err != nil {
			return false
		}
		for _, ip := range ips {
			if ip.IP.Equal(ta.IP) {
				return true
			}
		}
		return false
	}
	for _, ip := range boxAddrs() {
		addr := net.JoinHostPort(ip, strconv.Itoa(BoxWebhookPort))
		ln, err := lc.Listen(ctx, "tcp", addr)
		if err != nil {
			a.logf("node: box: FrIDE's news cannot come in on %s: %v", addr, err)
			continue
		}
		wg.Go(func() {
			forward(ctx, ln, func(ctx context.Context) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", hook)
			}, fromFride, a.logf)
		})
	}
}

// boxAddrs are the container's own addresses on the box network: every
// interface address but the loopback's and link-local ones.
func boxAddrs() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() || n.IP.IsLinkLocalUnicast() || n.IP.IsLinkLocalMulticast() {
			continue
		}
		out = append(out, n.IP.String())
	}
	return out
}

func portOf(hostport string) int {
	_, p, _ := net.SplitHostPort(hostport)
	n, _ := strconv.Atoi(p)
	return n
}

// forward accepts on ln until ctx ends and copies each connection to and
// from one that dial makes. allow, when set, refuses connections whose
// remote address it does not accept.
func forward(ctx context.Context, ln net.Listener, dial func(context.Context) (net.Conn, error), allow func(net.Addr) bool, logf func(string, ...any)) {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				logf("node: box: forwarding on %s stopped: %v", ln.Addr(), err)
			}
			return
		}
		if allow != nil && !allow(c.RemoteAddr()) {
			c.Close()
			continue
		}
		wg.Go(func() {
			defer c.Close()
			dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			up, err := dial(dctx)
			cancel()
			if err != nil {
				return
			}
			defer up.Close()
			done := make(chan struct{}, 2)
			pipe := func(dst, src net.Conn) {
				_, _ = io.Copy(dst, src)
				if cw, ok := dst.(interface{ CloseWrite() error }); ok {
					_ = cw.CloseWrite()
				}
				done <- struct{}{}
			}
			go pipe(up, c)
			go pipe(c, up)
			select {
			case <-done:
				<-done
			case <-ctx.Done():
			}
		})
	}
}
