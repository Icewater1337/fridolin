package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/lease"
	"github.com/Icewater1337/fridolin/internal/tailnet"
	"github.com/Icewater1337/fridolin/internal/version"
)

// The core's two startup resources (docs/CORE.md): the tailnet node, which
// serves the dashboard and the node port on the owner's tailnet under the
// core's MagicDNS name, and the core lease, which keeps a second copy of
// the core from running the same WhatsApp session. Both are restart-only
// settings (Boot), off by default.

// ErrLeaseHeld is what New returns, wrapping the lease's *HeldError, when
// another device holds the core lease: this copy must not start its
// channels. The command exits with status 3 then, as core promote does.
var ErrLeaseHeld = errors.New("another device holds the core lease")

// The files under <data>/core/ where the running core says how its tailnet
// node and its lease are, for `fridolin doctor`, which runs as another
// process (docker exec) and must not reach the network itself.
const (
	tailnetStateFile = "tailnet.json"
	leaseStateFile   = "lease-state.json"
)

// coreRuntime is the App's part for the tailnet and the lease.
type coreRuntime struct {
	tailnetFactory tailnet.Factory
	tailnetAuthKey string
	leaseStore     cloud.ObjectStore
	leaseSettle    time.Duration

	mu         sync.Mutex
	node       tailnet.Node
	tailnet    *httpapi.TailnetStatus
	servers    []*http.Server
	seen       map[string]bool // tailnet identities already logged
	lease      *lease.Lease
	leaseState *httpapi.LeaseStatus

	leaseRun sync.WaitGroup
	// lost is set once the lease is lost: the channels stay stopped.
	lost atomic.Bool
	// stopConnect ends the WhatsApp connect loop.
	stopConnect context.CancelFunc
}

// coreStateDir is <data>/core, made on first use.
func (b *App) coreStateDir() (string, error) {
	return config.EnsureDir(b.cfgs.Boot().Layout().Core())
}

// writeCoreState writes v as name in <data>/core.
func (b *App) writeCoreState(name string, v any) {
	dir, err := b.coreStateDir()
	if err != nil {
		log.Printf("core: %v", err)
		return
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return
	}
	if err := fsx.WriteFileAtomic(filepath.Join(dir, name), append(data, '\n'), 0o600, false); err != nil {
		log.Printf("core: writing %s: %v", name, err)
	}
}

// ── the lease ─────────────────────────────────────────────────────────────

// leaseStateRecord is <data>/core/lease-state.json.
type leaseStateRecord struct {
	httpapi.LeaseStatus
	RenewSeconds int       `json:"renew_seconds"`
	StaleSeconds int       `json:"stale_seconds"`
	PID          int       `json:"pid"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (b *App) setLeaseState(st httpapi.LeaseStatus) {
	b.core.mu.Lock()
	b.core.leaseState = &st
	b.core.mu.Unlock()
	l := b.cfgs.Boot().Lease()
	b.writeCoreState(leaseStateFile, leaseStateRecord{LeaseStatus: st, RenewSeconds: l.RenewSeconds,
		StaleSeconds: l.StaleSeconds, PID: os.Getpid(), UpdatedAt: time.Now().UTC()})
}

// leaseStatus is the lease for /ready and the Overview, nil when it is off.
func (b *App) leaseStatus() *httpapi.LeaseStatus {
	b.core.mu.Lock()
	defer b.core.mu.Unlock()
	if b.core.leaseState == nil {
		return nil
	}
	st := *b.core.leaseState
	return &st
}

// acquireLease takes the core lease when core.lease is on, before any
// channel starts; ErrLeaseHeld when another live device holds it.
func (b *App) acquireLease() error {
	boot := b.cfgs.Boot()
	if !boot.LeaseEnabled {
		return nil
	}
	if b.core.leaseStore == nil {
		return errors.New("core.lease is on but the bridge has no vault bucket to keep it in: set B2_VAULT_KEY_ID and B2_VAULT_KEY (fridolin core start passes them), or FRIDOLIN_CORE_STORE")
	}
	// A copy another device has taken over stays down, even once that
	// device releases the lease (to update, say): its bridge.db is older
	// than the one that ran since. `fridolin core promote` clears it.
	if data, err := os.ReadFile(filepath.Join(boot.Layout().Core(), SupersededFile)); err == nil {
		return fmt.Errorf("%w: this copy of the core was taken over by device %s; run fridolin core promote here to take it back (after importing a newer export)",
			ErrLeaseHeld, strings.TrimSpace(string(data)))
	}
	id, err := lease.DeviceID(boot.DataDir)
	if err != nil {
		return fmt.Errorf("the core lease: %w", err)
	}
	host, _ := os.Hostname()
	l := boot.Lease()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	held, err := lease.Acquire(ctx, lease.Options{
		Store: b.core.leaseStore, DeviceID: id, Hostname: host, PID: os.Getpid(), Version: version.String(),
		Renew: l.Renew(), Stale: l.Stale(), Settle: b.core.leaseSettle,
		OnLost:  b.leaseLost,
		OnRenew: func(r lease.Record) { b.setLeaseState(leaseHeld(r)) },
		Logf:    log.Printf,
	})
	if held, ok := errors.AsType[*lease.HeldError](err); ok {
		b.markSuperseded(held.Holder)
		return fmt.Errorf("%w: %v", ErrLeaseHeld, held)
	}
	if err != nil {
		return fmt.Errorf("taking the core lease: %w", err)
	}
	b.core.lease = held
	b.setLeaseState(leaseHeld(held.Record()))
	return nil
}

// SupersededFile, in <data>/core, says this copy of the core was taken
// over by another device: it names that device, and the bridge refuses to
// start until `fridolin core promote` removes it.
const SupersededFile = "superseded"

// markSuperseded records that holder took the core over from this copy.
func (b *App) markSuperseded(holder lease.Record) {
	dir, err := b.coreStateDir()
	if err == nil {
		err = fsx.WriteFileAtomic(filepath.Join(dir, SupersededFile), []byte(holder.DeviceID+" ("+holder.Hostname+")\n"), 0o600, false)
	}
	if err != nil {
		log.Printf("core: recording that device %s took the core over: %v", holder.DeviceID, err)
	}
}

func leaseHeld(r lease.Record) httpapi.LeaseStatus {
	at := r.RenewedAt
	return httpapi.LeaseStatus{State: "held", DeviceID: r.DeviceID, Hostname: r.Hostname, RenewedAt: &at}
}

// runLease renews the lease until ctx ends (Background of Start).
func (b *App) runLease(ctx context.Context) {
	if b.core.lease == nil {
		return
	}
	b.core.leaseRun.Go(func() { _ = b.core.lease.Run(ctx) })
}

// leaseLost stops the channels: WhatsApp disconnects and does not
// reconnect, the outbox stops sending, and every send is refused from now
// on (leaseGate). The dashboard stays up and says so; replies the brains
// still make wait in the outbox. A restart takes the lease again only once
// it is free or stale.
func (b *App) leaseLost(err error) {
	if !b.core.lost.CompareAndSwap(false, true) {
		return
	}
	st := httpapi.LeaseStatus{State: "lost", Error: err.Error()}
	if cur := b.leaseStatus(); cur != nil {
		st.DeviceID, st.Hostname, st.RenewedAt = cur.DeviceID, cur.Hostname, cur.RenewedAt
	}
	if lost, ok := errors.AsType[*lease.LostError](err); ok && lost.Holder.DeviceID != "" {
		st.Holder = lost.Holder.DeviceID + " (" + lost.Holder.Hostname + ")"
		b.markSuperseded(lost.Holder)
	}
	b.setLeaseState(st)
	log.Printf("CORE LEASE LOST: %v. WhatsApp and outbound sending are stopped on this machine; the dashboard stays up. Restart this core only once it may hold the lease again (fridolin core status).", err)
	b.core.mu.Lock()
	stop := b.core.stopConnect
	b.core.mu.Unlock()
	if stop != nil {
		stop()
	}
	b.wa.Disconnect()
	b.outbox.Stop()
}

// releaseLease frees the lease on a clean stop, after the channels have
// stopped, unless it was lost.
func (b *App) releaseLease() {
	if b.core.lease == nil {
		return
	}
	b.core.leaseRun.Wait()
	if b.core.lost.Load() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := b.core.lease.Release(ctx); err != nil {
		log.Printf("releasing the core lease: %v (it goes stale on its own)", err)
		return
	}
	st := leaseHeld(b.core.lease.Record())
	st.State = "released"
	b.setLeaseState(st)
	log.Printf("Released the core lease.")
}

// leaseGate refuses every send and connection once the lease is lost.
type leaseGate struct {
	Messenger
	lost func() bool
}

var errLeaseLost = errors.New("the core lease is lost: this machine no longer runs the core's channels")

func (g leaseGate) Connect() error {
	if g.lost() {
		return errLeaseLost
	}
	return g.Messenger.Connect()
}

func (g leaseGate) StartPairing() error {
	if g.lost() {
		return errLeaseLost
	}
	return g.Messenger.StartPairing()
}

func (g leaseGate) SendMessage(ctx context.Context, jid, text string) error {
	if g.lost() {
		return errLeaseLost
	}
	return g.Messenger.SendMessage(ctx, jid, text)
}

func (g leaseGate) SendChunked(ctx context.Context, jid, text string) error {
	if g.lost() {
		return errLeaseLost
	}
	return g.Messenger.SendChunked(ctx, jid, text)
}

func (g leaseGate) SendPoll(ctx context.Context, jid, q string, opts []string, max int) (string, error) {
	if g.lost() {
		return "", errLeaseLost
	}
	return g.Messenger.SendPoll(ctx, jid, q, opts, max)
}

// ── the tailnet ───────────────────────────────────────────────────────────

// tailnetStateRecord is <data>/core/tailnet.json.
type tailnetStateRecord struct {
	httpapi.TailnetStatus
	PID       int       `json:"pid"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (b *App) setTailnetState(st httpapi.TailnetStatus) {
	b.core.mu.Lock()
	b.core.tailnet = &st
	b.core.mu.Unlock()
	b.writeCoreState(tailnetStateFile, tailnetStateRecord{TailnetStatus: st, PID: os.Getpid(), UpdatedAt: time.Now().UTC()})
}

func (b *App) tailnetStatus() *httpapi.TailnetStatus {
	b.core.mu.Lock()
	defer b.core.mu.Unlock()
	if b.core.tailnet == nil {
		return nil
	}
	st := *b.core.tailnet
	return &st
}

// startTailnet makes the tailnet node when core.tailnet is on (Background
// of Start, first, so that the node port can listen on it), then joins in
// the background: joining may wait for the auth key, and WhatsApp must not
// wait for it.
func (b *App) startTailnet(ctx context.Context) {
	boot := b.cfgs.Boot()
	if !boot.TailnetEnabled {
		return
	}
	st := httpapi.TailnetStatus{State: "starting", Hostname: boot.TailnetHostname}
	dir, err := config.EnsureDir(boot.Layout().Tsnet())
	var node tailnet.Node
	if err == nil {
		factory := b.core.tailnetFactory
		if factory == nil {
			factory = tailnet.New
		}
		node, err = factory(tailnet.Options{Hostname: boot.TailnetHostname, Dir: dir, AuthKey: b.core.tailnetAuthKey, Logf: log.Printf})
	}
	if err != nil {
		st.State, st.Error = "error", err.Error()
		b.setTailnetState(st)
		log.Printf("tailnet: %v; the dashboard stays on this machine only", err)
		return
	}
	if b.core.tailnetAuthKey == "" && !fsx.Exists(filepath.Join(dir, "tailscaled.state")) {
		st.Error = "no TS_AUTHKEY and no node key yet: store fridolin.tailscale.authkey in the host's keychain and restart with fridolin core start"
	}
	b.core.mu.Lock()
	b.core.node = node
	b.core.mu.Unlock()
	b.setTailnetState(st)
	go b.joinTailnet(ctx, node, st)
}

// joinTailnet waits for the node to run, then serves the dashboard and
// the API on it: with TLS on 443 (and a redirect on 80) when the tailnet
// issues certificates, else plain HTTP on 80, inside WireGuard either way.
func (b *App) joinTailnet(ctx context.Context, node tailnet.Node, st httpapi.TailnetStatus) {
	up, err := node.Up(ctx)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		st.State, st.Error = "error", err.Error()
		b.setTailnetState(st)
		log.Printf("tailnet: joining failed: %v", err)
		return
	}
	st.State, st.Error = "up", ""
	st.DNSName, st.IPs, st.MagicDNS, st.HTTPS = up.DNSName, up.IPs, up.MagicDNS, up.HTTPS
	hosts := tailnetHosts(up)
	handler := b.tailnetIdentity(node, httpapi.New(b.httpDeps(b.listen, hosts)))
	name := up.DNSName
	if name == "" {
		name = st.Hostname
	}
	if up.HTTPS {
		if ln, err := node.ListenTLS("tcp", ":443"); err != nil {
			log.Printf("tailnet: HTTPS on 443: %v; serving plain HTTP on 80 instead", err)
		} else {
			b.serveTailnet(ln, handler)
			st.URL = "https://" + name + "/"
			redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "https://"+name+r.URL.RequestURI(), http.StatusPermanentRedirect)
			})
			if ln80, err := node.Listen("tcp", ":80"); err == nil {
				b.serveTailnet(ln80, httpapi.HostGuard(hosts, redirect))
			}
		}
	}
	if st.URL == "" {
		ln, err := node.Listen("tcp", ":80")
		if err != nil {
			st.State, st.Error = "error", err.Error()
			b.setTailnetState(st)
			log.Printf("tailnet: listening on 80: %v", err)
			return
		}
		b.serveTailnet(ln, handler)
		st.URL = "http://" + name + "/"
	}
	if !up.MagicDNS {
		st.Error = "MagicDNS is off in this tailnet, so " + up.ShortName() + " does not resolve: turn it on in the admin console's DNS page"
	}
	b.setTailnetState(st)
	log.Printf("Tailnet: dashboard at %s (the dashboard token or a session is required there)", st.URL)
}

// tailnetHosts are the Host values the tailnet listener answers to.
func tailnetHosts(up tailnet.Status) map[string]bool {
	hosts := map[string]bool{}
	add := func(h string) {
		if h == "" {
			return
		}
		hosts[h] = true
		hosts[h+":80"] = true
		hosts[h+":443"] = true
	}
	add(up.DNSName)
	add(up.ShortName())
	for _, ip := range up.IPs {
		if addr := net.ParseIP(ip); addr != nil && addr.To4() == nil {
			ip = "[" + ip + "]"
		}
		add(ip)
	}
	return hosts
}

func (b *App) serveTailnet(ln net.Listener, h http.Handler) {
	srv := httpapi.NewServer(h)
	b.core.mu.Lock()
	b.core.servers = append(b.core.servers, srv)
	b.core.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("tailnet: HTTP server: %v", err)
		}
	}()
}

// stopTailnet stops the tailnet's HTTP servers and leaves the tailnet.
func (b *App) stopTailnet() {
	b.core.mu.Lock()
	servers, node := b.core.servers, b.core.node
	b.core.servers, b.core.node = nil, nil
	b.core.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), httpStopWait)
	defer cancel()
	for _, srv := range servers {
		if err := srv.Shutdown(ctx); err != nil {
			_ = srv.Close()
		}
	}
	if node != nil {
		if err := node.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			log.Printf("tailnet: leaving: %v", err)
		}
	}
}

// NodeListener binds the node port: on the tailnet, at the port of addr,
// when core.tailnet is on (nodes reach the core under its MagicDNS name
// and nowhere else), else as the HTTP server binds. It is the bind
// function the nodes service takes (nodes.listen, internal/nodes).
func (b *App) NodeListener(network, addr string) (net.Listener, error) {
	if !b.cfgs.Boot().TailnetEnabled {
		bind := b.bind
		if bind == nil {
			bind = net.Listen
		}
		return bind(network, addr)
	}
	b.core.mu.Lock()
	node := b.core.node
	b.core.mu.Unlock()
	if node == nil {
		return nil, errors.New("the tailnet node did not start, so the node port cannot listen on it")
	}
	port, err := tailnet.Port(addr)
	if err != nil {
		return nil, err
	}
	return node.Listen(network, ":"+port)
}

// tailnetIdentity asks the tailnet who sent each request, refuses one it
// cannot name, logs each user and device the first time, and logs and
// audits every request that changes something. The dashboard token or a
// session is still required (httpapi.Deps.Remote): the tailnet identity is
// recorded, not trusted on its own.
func (b *App) tailnetIdentity(node tailnet.Node, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := node.WhoIs(r.Context(), r.RemoteAddr)
		if err != nil {
			log.Printf("tailnet: refusing %s %s from %s: %v", r.Method, r.URL.Path, r.RemoteAddr, err)
			http.Error(w, "unknown tailnet peer", http.StatusForbidden)
			return
		}
		b.core.mu.Lock()
		first := !b.core.seen[id.String()]
		if first {
			if b.core.seen == nil {
				b.core.seen = map[string]bool{}
			}
			b.core.seen[id.String()] = true
		}
		b.core.mu.Unlock()
		if first {
			log.Printf("tailnet: %s reached the dashboard", id)
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		log.Printf("tailnet: %s %s by %s: %d", r.Method, r.URL.Path, id, rec.status)
		b.audit.append(auditEntry{Kind: "tailnet", From: id.LoginName, To: r.Method + " " + r.URL.Path, ID: id.Node,
			Outcome: strconv.Itoa(rec.status), DurationMS: time.Since(start).Milliseconds()})
	})
}

// statusRecorder remembers the status a handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// ── both, for the Overview ────────────────────────────────────────────────

// coreStatus is the Overview's core card, nil when neither is on.
func (b *App) coreStatus() *httpapi.CoreStatus {
	tn, ls := b.tailnetStatus(), b.leaseStatus()
	if tn == nil && ls == nil {
		return nil
	}
	return &httpapi.CoreStatus{Tailnet: tn, Lease: ls}
}

// coreWarnings say on the Overview what keeps the core from working.
func (b *App) coreWarnings() []string {
	var out []string
	if ls := b.leaseStatus(); ls != nil && ls.State == "lost" {
		msg := "The core lease was lost (" + ls.Error + "): WhatsApp and outbound sending are stopped on this machine."
		if ls.Holder != "" {
			msg += " Device " + ls.Holder + " runs the core now."
		}
		out = append(out, msg+" Stop this copy (fridolin core stop), or restart it once it may hold the lease again (fridolin core status).")
	}
	if tn := b.tailnetStatus(); tn != nil && tn.Error != "" {
		out = append(out, "Tailnet: "+tn.Error)
	}
	return out
}
