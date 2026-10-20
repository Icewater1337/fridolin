// Package tailnet makes the core its own device on the owner's tailnet:
// the bridge embeds Tailscale (tailscale.com/tsnet) and answers under one
// MagicDNS name, fridolin-core by default, wherever it runs, in a container
// or not, without the host's Tailscale (docs/CORE.md).
//
// The bridge talks to a Node, which New makes with tsnet and Fake makes on
// the loopback interface, so that tests never reach a network.
package tailnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
)

// Node is the core on the tailnet.
type Node interface {
	// Up joins the tailnet and waits until the node runs, then says how it
	// is known. It may take long the first time, while the auth key is
	// used; it ends early when ctx does.
	Up(ctx context.Context) (Status, error)
	// Listen opens a TCP listener on the tailnet only, at addr (":80").
	// It works before Up returns: connections arrive once the node runs.
	Listen(network, addr string) (net.Listener, error)
	// ListenTLS is Listen with TLS, the certificate of the node's MagicDNS
	// name fetched through Tailscale. It needs MagicDNS and HTTPS on in the
	// tailnet (Status.HTTPS).
	ListenTLS(network, addr string) (net.Listener, error)
	// WhoIs names the tailnet user and device behind remoteAddr, the
	// RemoteAddr of a request that came in on one of the listeners.
	WhoIs(ctx context.Context, remoteAddr string) (Identity, error)
	// Close leaves the tailnet and closes the listeners.
	Close() error
}

// Status is how the node is known on the tailnet.
type Status struct {
	// DNSName is the node's MagicDNS name without the final dot, such as
	// fridolin-core.example.ts.net.
	DNSName string `json:"dns_name"`
	// IPs are the node's Tailscale addresses.
	IPs []string `json:"ips"`
	// MagicDNS says whether the tailnet resolves DNSName for its devices.
	MagicDNS bool `json:"magic_dns"`
	// HTTPS says whether the tailnet issues certificates for DNSName, so
	// that ListenTLS works.
	HTTPS bool `json:"https"`
}

// ShortName is the first label of DNSName, the name MagicDNS answers to
// on its own.
func (s Status) ShortName() string {
	name, _, _ := strings.Cut(s.DNSName, ".")
	return name
}

// Identity is who connected: a tailnet user and one of their devices.
type Identity struct {
	// LoginName is the user's login (you@example.com), or the tags of a
	// tagged device, which has no user.
	LoginName string `json:"login_name"`
	// Node is the device's name.
	Node string `json:"node"`
	// Tags are the device's ACL tags.
	Tags []string `json:"tags,omitempty"`
}

func (i Identity) String() string {
	who := i.LoginName
	if who == "" {
		who = "unknown user"
	}
	if i.Node != "" {
		who += " on " + i.Node
	}
	return who
}

// Options configure a node.
type Options struct {
	// Hostname is the node's name on the tailnet (core.tailnet.hostname).
	Hostname string
	// Dir keeps the node's key and state between starts (Layout.Tsnet()),
	// so a restart is the same device and needs no auth key.
	Dir string
	// AuthKey joins the tailnet the first time (TS_AUTHKEY, which `fridolin
	// core start` passes from the host's keychain). Once Dir holds a node
	// key it is not needed.
	AuthKey string
	// Logf receives what the node says a person should see (sign-in
	// links, errors); the library's own chatter is dropped.
	Logf func(format string, args ...any)
}

// Factory makes a node; New in production, a fake in tests.
type Factory func(Options) (Node, error)

// ErrClosed is returned by a closed node.
var ErrClosed = errors.New("tailnet: node closed")

// Fake is a Node on the loopback interface for tests: Listen binds
// 127.0.0.1 on a free port and remembers which tailnet port it stands
// for, Up returns Status at once, and WhoIs names Who for every caller.
type Fake struct {
	Status Status
	Who    Identity
	// UpErr, when set, is what Up returns.
	UpErr error

	mu     sync.Mutex
	ports  map[string]string // tailnet addr (":80") → loopback addr
	lns    []net.Listener
	closed bool
}

// FakeFactory returns a Factory that hands out f, ignoring the options.
func FakeFactory(f *Fake) Factory { return func(Options) (Node, error) { return f, nil } }

// Up implements Node.
func (f *Fake) Up(ctx context.Context) (Status, error) {
	if f.UpErr != nil {
		return Status{}, f.UpErr
	}
	return f.Status, ctx.Err()
}

// Listen implements Node.
func (f *Fake) Listen(network, addr string) (net.Listener, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, ErrClosed
	}
	ln, err := net.Listen(network, "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	if f.ports == nil {
		f.ports = map[string]string{}
	}
	f.ports[addr] = ln.Addr().String()
	f.lns = append(f.lns, ln)
	return ln, nil
}

// ListenTLS implements Node; the fake serves plain TCP, and only when
// Status.HTTPS says the tailnet would issue a certificate.
func (f *Fake) ListenTLS(network, addr string) (net.Listener, error) {
	if !f.Status.HTTPS {
		return nil, errors.New("tailnet: HTTPS is off in this tailnet")
	}
	return f.Listen(network, addr)
}

// Addr is the loopback address that stands for the tailnet address addr
// (":80"), "" when nothing listens there.
func (f *Fake) Addr(addr string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ports[addr]
}

// WhoIs implements Node.
func (f *Fake) WhoIs(context.Context, string) (Identity, error) { return f.Who, nil }

// Close implements Node.
func (f *Fake) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	var errs []error
	for _, ln := range f.lns {
		if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Port returns the port of a listen address such as ":443".
func Port(addr string) (string, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("tailnet: listen address %q: %w", addr, err)
	}
	return port, nil
}
