package secretxfer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"tailscale.com/client/local"
	"tailscale.com/ipn/ipnstate"
)

// Local is the tailnet of this machine's Tailscale, asked through its
// LocalAPI (the socket or named pipe the tailscale CLI uses).
type Local struct {
	// Client is the LocalAPI client; nil is the default one.
	Client *local.Client
}

func (l Local) client() *local.Client {
	if l.Client != nil {
		return l.Client
	}
	return &local.Client{}
}

func (l Local) status(ctx context.Context) (*ipnstate.Status, error) {
	st, err := l.client().Status(ctx) // with peers: an older tailscaled leaves the self user out without them
	if err != nil {
		return nil, fmt.Errorf("%w (is Tailscale installed, running and logged in? tailscale status)", err)
	}
	if st.BackendState != "Running" || st.Self == nil {
		return nil, fmt.Errorf("the state of Tailscale is %s, not Running: tailscale up", st.BackendState)
	}
	return st, nil
}

func (l Local) Self(ctx context.Context) (Peer, error) {
	st, err := l.status(ctx)
	if err != nil {
		return Peer{}, err
	}
	p := Peer{Node: strings.TrimSuffix(st.Self.DNSName, ".")}
	if st.Self.Tags != nil {
		p.Tags = st.Self.Tags.AsSlice()
	}
	if u, ok := st.User[st.Self.UserID]; ok {
		p.Login = u.LoginName
	}
	return p, nil
}

func (l Local) Listen(ctx context.Context) (net.Listener, string, error) {
	st, err := l.status(ctx)
	if err != nil {
		return nil, "", err
	}
	for _, ip := range st.Self.TailscaleIPs {
		if !ip.Is4() {
			continue
		}
		ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort(ip.String(), "0"))
		if err != nil {
			return nil, "", err
		}
		return ln, ln.Addr().String(), nil
	}
	return nil, "", errors.New("this device has no Tailscale IPv4 address")
}

func (l Local) Dial(ctx context.Context, addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
}

func (l Local) WhoIs(ctx context.Context, remoteAddr string) (Peer, error) {
	who, err := l.client().WhoIs(ctx, remoteAddr)
	if err != nil {
		return Peer{}, err
	}
	if who.Node == nil {
		return Peer{}, errors.New("no device")
	}
	p := Peer{Node: strings.TrimSuffix(who.Node.Name, "."), Tags: who.Node.Tags}
	if who.UserProfile != nil {
		p.Login = who.UserProfile.LoginName
	}
	return p, nil
}
