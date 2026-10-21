package nodes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// Timing of the node port.
const (
	// handshakeTimeout bounds hello, challenge and auth together.
	handshakeTimeout = 15 * time.Second
	// idleTimeout closes a connection that sent nothing this long; agents
	// send a heartbeat every HeartbeatEvery.
	idleTimeout = 90 * time.Second
	// HeartbeatEvery is how often an agent reports.
	HeartbeatEvery = 20 * time.Second
	// callTimeout bounds a call that is not exec, whose own timeout counts.
	callTimeout = 60 * time.Second
	// writeTimeout bounds one message sent.
	writeTimeout = 15 * time.Second
)

// Options configure a Service.
type Options struct {
	// Dir is the nodes folder (config.Layout.Nodes()).
	Dir string
	Now func() time.Time
	// Logf logs; nil logs with the standard logger.
	Logf func(format string, args ...any)
	// OnEvent hears the news nodes report (nodeproto.TypeEvent), in the
	// order each node sent it; nil drops it. It must not block for long:
	// the node's connection waits for it.
	OnEvent func(nodeID string, ev nodeproto.Event)
	// OnDeclare hears that a node's declaration changed while it was
	// connected, after the registry recorded it; nil for nothing.
	OnDeclare func(n *Node)
	// OnJoin hears that a device it did not know joined with a join code
	// and waits for approval, once, after the registry recorded it; nil
	// for nothing. It must not block for long.
	OnJoin func(n *Node)
	// Version is the core's own version, which nodes are updated to, and
	// BinariesDir the folder with its builds for each platform
	// (BinariesManifest); "" for none (update.go).
	Version     string
	BinariesDir string
	// Busy says why node id should not be updated now (a FrIDE task, a
	// backup running on it), "" when it may be; nil for never busy.
	Busy func(id string) string
	// Hold says why no node may be updated now (a lockdown), "" when they
	// may; nil for never.
	Hold func() string
}

// Service is the node port and the registry behind it.
type Service struct {
	reg       *Registry
	logf      func(string, ...any)
	onEvent   func(string, nodeproto.Event)
	onDeclare func(*Node)
	onJoin    func(*Node)

	// Updates (update.go).
	dir, version, binDir string
	busy                 func(string) string
	hold                 func() string
	keyMu                sync.Mutex
	key                  ed25519.PrivateKey

	// ctx ends with Stop, and with it every connection.
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	sessions map[string]*session
	srv      *http.Server
	addr     string
	closed   bool
	wg       sync.WaitGroup
	nextID   atomic.Uint64
	// updating are the nodes an update runs on, reports the failures
	// they reported meanwhile.
	updating map[string]bool
	reports  map[string]nodeproto.UpdateReport
}

// New opens the registry in opts.Dir.
func New(opts Options) (*Service, error) {
	reg, err := OpenRegistry(opts.Dir, opts.Now)
	if err != nil {
		return nil, err
	}
	logf := opts.Logf
	if logf == nil {
		logf = log.Printf
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		reg: reg, logf: logf, onEvent: opts.OnEvent, onDeclare: opts.OnDeclare, onJoin: opts.OnJoin, ctx: ctx, cancel: cancel, sessions: map[string]*session{},
		dir: opts.Dir, version: opts.Version, binDir: opts.BinariesDir, busy: opts.Busy, hold: opts.Hold,
		updating: map[string]bool{}, reports: map[string]nodeproto.UpdateReport{},
	}, nil
}

// Version is the core's version, which nodes are updated to.
func (s *Service) Version() string { return s.version }

// Registry is the service's registry.
func (s *Service) Registry() *Registry { return s.reg }

// Serve accepts node connections on ln until Stop; a tsnet listener works
// as well as a TCP one. It returns at once; the connections run in the
// background.
func (s *Service) Serve(ln net.Listener) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		_ = ln.Close()
		return errors.New("the node port is stopped")
	}
	if s.srv != nil {
		_ = ln.Close()
		return errors.New("the node port is already open")
	}
	// The node port serves one path and none of the dashboard's routes.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != nodeproto.Path {
			http.NotFound(w, r)
			return
		}
		s.accept(w, r)
	})
	s.srv = &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	s.addr = ln.Addr().String()
	srv := s.srv
	s.wg.Go(func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logf("nodes: the node port %s stopped: %v", ln.Addr(), err)
		}
	})
	s.wg.Go(s.autoLoop)
	s.logf("nodes: listening for node agents on %s", s.addr)
	return nil
}

// Addr is the address the port listens on, "" before Serve.
func (s *Service) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// Stop closes the port and every connection, telling each agent the core
// shuts down.
func (s *Service) Stop() {
	s.mu.Lock()
	s.closed = true
	srv := s.srv
	sessions := make([]*session, 0, len(s.sessions))
	for _, ss := range s.sessions {
		sessions = append(sessions, ss)
	}
	s.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
	for _, ss := range sessions {
		ss.close(nodeproto.CloseShutdown, "the core is stopping")
	}
	s.cancel()
	s.wg.Wait()
}

// Online reports whether node id is connected.
func (s *Service) Online(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[id]
	return ok
}

// session is one authenticated node connection.
type session struct {
	id   string
	conn *websocket.Conn
	ctx  context.Context
	stop context.CancelFunc

	mu      sync.Mutex
	pending map[string]chan nodeproto.Response
	gone    bool
}

func (ss *session) send(typ, id string, body any) error {
	data, err := nodeproto.Encode(typ, id, body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ss.ctx, writeTimeout)
	defer cancel()
	return ss.conn.Write(ctx, websocket.MessageText, data)
}

// close tells the agent why and closes the socket at once: a revoked
// node is off the port the moment it is revoked.
func (ss *session) close(reason, message string) {
	_ = ss.send(nodeproto.TypeClose, "", nodeproto.Close{Reason: reason, Message: message})
	_ = ss.conn.Close(websocket.StatusPolicyViolation, reason)
	_ = ss.conn.CloseNow()
	ss.stop()
}

func (ss *session) fail() {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.gone = true
	for id, ch := range ss.pending {
		ch <- nodeproto.Response{Error: nodeproto.Errorf(nodeproto.CodeUnavailable, "the node disconnected")}
		delete(ss.pending, id)
	}
}

func (s *Service) accept(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{nodeproto.Subprotocol}})
	if err != nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = conn.Close(websocket.StatusGoingAway, nodeproto.CloseShutdown)
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	defer conn.CloseNow()
	if conn.Subprotocol() != nodeproto.Subprotocol {
		_ = conn.Close(websocket.StatusPolicyViolation, "subprotocol "+nodeproto.Subprotocol+" required")
		return
	}
	conn.SetReadLimit(nodeproto.MaxPreAuthBytes)
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	ss := &session{conn: conn, ctx: ctx, stop: cancel, pending: map[string]chan nodeproto.Response{}}
	node, err := s.handshake(ss, r.RemoteAddr)
	if err != nil {
		return
	}
	ss.id = node.ID
	conn.SetReadLimit(nodeproto.MaxMessageBytes)

	s.mu.Lock()
	old := s.sessions[node.ID]
	s.sessions[node.ID] = ss
	s.mu.Unlock()
	if old != nil {
		old.close(nodeproto.CloseReplaced, "a newer connection of this node replaced this one")
	}
	defer func() {
		s.mu.Lock()
		if s.sessions[node.ID] == ss {
			delete(s.sessions, node.ID)
		}
		s.mu.Unlock()
		ss.fail()
		if err := s.reg.Flush(node.ID); err != nil {
			s.logf("nodes: recording when %s was last heard from: %v", node.ID, err)
		}
	}()
	// A revocation between the handshake and here closes it now.
	if cur, err := s.reg.Get(node.ID); err == nil && cur.Device == nodeproto.DeviceRevoked {
		ss.close(nodeproto.CloseRevoked, "this node was revoked")
		return
	}
	s.logf("nodes: %s (%s, %s) connected from %s, device %s, commands %s", node.Name, node.ID, node.OS, r.RemoteAddr, node.Device, node.Commands())
	if err := ss.send(nodeproto.TypeWelcome, "", s.stateOf(node)); err != nil {
		return
	}
	// A node that runs an older version than the core, with auto updates
	// on, is updated now: after the core itself was updated, every node
	// connects again.
	s.wg.Go(func() { s.maybeAuto(node.ID) })
	s.readLoop(ss)
	s.logf("nodes: %s (%s) disconnected", node.Name, node.ID)
}

// handshake reads hello, checks the device key with a challenge and
// records the node. It closes the connection with a reason when the node
// may not connect.
func (s *Service) handshake(ss *session, remote string) (*Node, error) {
	ctx, cancel := context.WithTimeout(ss.ctx, handshakeTimeout)
	defer cancel()
	env, err := readEnvelope(ctx, ss.conn)
	if err != nil || env.Type != nodeproto.TypeHello {
		ss.close(nodeproto.CloseProtocol, "the first message must be a hello")
		return nil, errors.New("no hello")
	}
	hello, err := nodeproto.DecodeBody[nodeproto.Hello](env)
	if err == nil {
		err = hello.Check()
	}
	if err != nil {
		ss.close(nodeproto.CloseProtocol, err.Error())
		return nil, err
	}
	nonce := make([]byte, nodeproto.NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	if err := ss.send(nodeproto.TypeChallenge, "", nodeproto.Challenge{Nonce: nonce}); err != nil {
		return nil, err
	}
	env, err = readEnvelope(ctx, ss.conn)
	if err != nil || env.Type != nodeproto.TypeAuth {
		ss.close(nodeproto.CloseProtocol, "the answer to a challenge must be an auth")
		return nil, errors.New("no auth")
	}
	auth, err := nodeproto.DecodeBody[nodeproto.Auth](env)
	if err != nil || !nodeproto.Verify(hello.PublicKey, hello.DeviceID, nonce, auth.Signature) {
		s.logf("nodes: refused %s from %s: its signature does not verify", hello.DeviceID, remote)
		ss.close(nodeproto.CloseBadAuth, "the signature does not verify")
		return nil, errors.New("bad signature")
	}
	node, joined, err := s.reg.hello(hello, remote)
	switch {
	case errors.Is(err, ErrRevoked):
		s.logf("nodes: refused %s (%s) from %s: it is revoked", hello.Name, hello.DeviceID, remote)
		ss.close(nodeproto.CloseRevoked, "this node was revoked; join again with a new code")
		return nil, err
	case errors.Is(err, ErrBadJoinCode):
		s.logf("nodes: refused %s (%s) from %s: an unknown device without a valid join code", hello.Name, hello.DeviceID, remote)
		ss.close(nodeproto.CloseUnknownDevice, "this core does not know this device, and the join code is unknown, used or expired: send !join to the bridge in your WhatsApp chat, or run fridolin nodes join-code on the core")
		return nil, err
	case err != nil:
		s.logf("nodes: refused %s from %s: %v", hello.DeviceID, remote, err)
		ss.close(nodeproto.CloseProtocol, "the core could not record this node")
		return nil, err
	}
	if joined {
		s.logf("nodes: %s (%s) joined; approve it with: fridolin nodes approve %s", node.Name, node.ID, node.ID)
		if s.onJoin != nil {
			s.onJoin(clone(node))
		}
	}
	return node, nil
}

func readEnvelope(ctx context.Context, conn *websocket.Conn) (nodeproto.Envelope, error) {
	typ, data, err := conn.Read(ctx)
	if err != nil {
		return nodeproto.Envelope{}, err
	}
	if typ != websocket.MessageText {
		return nodeproto.Envelope{}, errors.New("a binary message")
	}
	return nodeproto.Decode(data)
}

func (s *Service) readLoop(ss *session) {
	for {
		ctx, cancel := context.WithTimeout(ss.ctx, idleTimeout)
		env, err := readEnvelope(ctx, ss.conn)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				ss.close(nodeproto.CloseIdle, "nothing heard for "+idleTimeout.String())
			} else if !errors.Is(err, context.Canceled) && websocket.CloseStatus(err) == -1 && ss.ctx.Err() == nil {
				ss.close(nodeproto.CloseProtocol, err.Error())
			}
			return
		}
		switch env.Type {
		case nodeproto.TypeHeartbeat:
			hb, err := nodeproto.DecodeBody[nodeproto.Heartbeat](env)
			if err != nil {
				ss.close(nodeproto.CloseProtocol, err.Error())
				return
			}
			if err := s.reg.Seen(ss.id, hb.Stats, false); err != nil {
				s.logf("nodes: recording a heartbeat of %s: %v", ss.id, err)
			}
		case nodeproto.TypeResponse:
			resp, err := nodeproto.DecodeBody[nodeproto.Response](env)
			if err != nil {
				resp = nodeproto.Response{Error: nodeproto.Errorf(nodeproto.CodeFailed, "unreadable response: %v", err)}
			}
			ss.mu.Lock()
			ch := ss.pending[env.ID]
			delete(ss.pending, env.ID)
			ss.mu.Unlock()
			if ch != nil {
				ch <- resp
			}
		case nodeproto.TypeDeclare:
			d, err := nodeproto.DecodeBody[nodeproto.Declare](env)
			if err == nil {
				err = d.Capabilities.Check()
			}
			if err != nil {
				ss.close(nodeproto.CloseProtocol, err.Error())
				return
			}
			n, err := s.reg.Declare(ss.id, d.Capabilities)
			if err != nil {
				s.logf("nodes: recording what %s offers now: %v", ss.id, err)
				continue
			}
			s.logf("nodes: %s (%s) offers now: %s; commands %s", n.Name, n.ID, n.Declared.Summary(), n.Commands())
			s.pushState(n)
			if s.onDeclare != nil {
				s.onDeclare(n)
			}
		case nodeproto.TypeEvent:
			ev, err := nodeproto.DecodeBody[nodeproto.Event](env)
			if err != nil {
				ss.close(nodeproto.CloseProtocol, err.Error())
				return
			}
			// Only news a node may serve counts: FrIDE's needs fride
			// approved, so a pending node has nothing to report.
			if n, err := s.reg.Get(ss.id); err != nil || !n.Effective().Allows(eventMethod(ev.Kind)) {
				s.logf("nodes: dropped a %q event of %s, which may not serve it", ev.Kind, ss.id)
				continue
			}
			if ev.Kind == nodeproto.EventUpdate {
				s.onUpdateReport(ss.id, ev)
				continue
			}
			if s.onEvent != nil {
				s.onEvent(ss.id, ev)
			}
		case nodeproto.TypeClose:
			return
		default:
			ss.close(nodeproto.CloseProtocol, "unexpected "+env.Type)
			return
		}
	}
}

// eventMethod is the method whose approval lets a node report events of
// kind: FrIDE's news needs fride approved. Other kinds need what nothing
// grants. An update's report needs update approved.
func eventMethod(kind string) string {
	switch kind {
	case nodeproto.EventFride:
		return nodeproto.MethodFrideStatus
	case nodeproto.EventUpdate:
		return nodeproto.MethodUpdateApply
	}
	return ""
}

// pushState sends the node its state after an approval changed it.
func (s *Service) pushState(n *Node) {
	s.mu.Lock()
	ss := s.sessions[n.ID]
	if ss != nil && n.Device == nodeproto.DeviceRevoked {
		// Off the port before Revoke returns: no call reaches it after.
		delete(s.sessions, n.ID)
	}
	s.mu.Unlock()
	if ss == nil {
		return
	}
	if n.Device == nodeproto.DeviceRevoked {
		ss.close(nodeproto.CloseRevoked, "this node was revoked")
		return
	}
	if err := ss.send(nodeproto.TypeState, "", s.stateOf(n)); err != nil {
		s.logf("nodes: telling %s its new state: %v", n.ID, err)
	}
}

// ApproveDevice approves node id's device and tells it.
func (s *Service) ApproveDevice(id string) (*Node, error) {
	n, err := s.reg.ApproveDevice(id)
	if err == nil {
		s.logf("nodes: approved the device %s (%s)", n.Name, n.ID)
		s.pushState(n)
	}
	return n, err
}

// ApproveCommands approves what node id declares and tells it.
func (s *Service) ApproveCommands(id string) (*Node, error) {
	n, err := s.reg.ApproveCommands(id)
	if err == nil {
		s.logf("nodes: approved what %s (%s) offers: %s", n.Name, n.ID, n.Approved.Summary())
		s.pushState(n)
	}
	return n, err
}

// Revoke revokes node id and closes its connection at once.
func (s *Service) Revoke(id string) (*Node, error) {
	n, err := s.reg.Revoke(id)
	if err == nil {
		s.logf("nodes: revoked %s (%s)", n.Name, n.ID)
		s.pushState(n)
	}
	return n, err
}

// SetTags replaces node id's tags.
func (s *Service) SetTags(id string, tags []string) (*Node, error) { return s.reg.SetTags(id, tags) }

// NewJoinCode makes a join code.
func (s *Service) NewJoinCode() (string, time.Time, error) { return s.reg.NewJoinCode() }

// ErrCall is a call the core refuses before sending it.
var ErrCall = errors.New("refused")

// Call calls method on node id with params and decodes the result into
// out (nil to drop it). The approvals are checked here before anything is
// sent, and again on the node. A failure on the node is a
// *nodeproto.Error.
func (s *Service) Call(ctx context.Context, id, method string, params, out any) error {
	n, err := s.reg.Get(id)
	if err != nil {
		return err
	}
	if n.Device != nodeproto.DeviceApproved {
		return nodeproto.Errorf(nodeproto.CodeDenied, "node %s: the device is %s", id, n.Device)
	}
	eff := n.Effective()
	if !eff.Allows(method) {
		return nodeproto.Errorf(nodeproto.CodeDenied, "node %s: %s is not approved", id, method)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	timeout := callTimeout
	if t, ok := slowCalls[method]; ok {
		timeout = t
	}
	if method == nodeproto.MethodExec {
		var p nodeproto.ExecParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
		}
		if err := eff.PermitsExec(p.Argv); err != nil {
			return nodeproto.Errorf(nodeproto.CodeDenied, "node %s: %v", id, err)
		}
		timeout = time.Duration(eff.Exec.Timeout())*time.Second + 15*time.Second
	}
	s.mu.Lock()
	ss := s.sessions[id]
	s.mu.Unlock()
	if ss == nil {
		return nodeproto.Errorf(nodeproto.CodeUnavailable, "node %s is not connected", id)
	}
	reqID := strconv.FormatUint(s.nextID.Add(1), 10)
	ch := make(chan nodeproto.Response, 1)
	ss.mu.Lock()
	if ss.gone {
		ss.mu.Unlock()
		return nodeproto.Errorf(nodeproto.CodeUnavailable, "node %s disconnected", id)
	}
	ss.pending[reqID] = ch
	ss.mu.Unlock()
	defer func() {
		ss.mu.Lock()
		delete(ss.pending, reqID)
		ss.mu.Unlock()
	}()
	if err := ss.send(nodeproto.TypeRequest, reqID, nodeproto.Request{Method: method, Params: raw}); err != nil {
		return nodeproto.Errorf(nodeproto.CodeUnavailable, "sending to node %s: %v", id, err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case <-ctx.Done():
		return nodeproto.Errorf(nodeproto.CodeTimeout, "node %s did not answer %s within %v", id, method, timeout)
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return nodeproto.Errorf(nodeproto.CodeFailed, "node %s answered %s with %v", id, method, err)
		}
		return nil
	}
}

// Exec runs argv on node id.
func (s *Service) Exec(ctx context.Context, id string, p nodeproto.ExecParams) (nodeproto.ExecResult, error) {
	var out nodeproto.ExecResult
	return out, s.Call(ctx, id, nodeproto.MethodExec, p, &out)
}

// List lists a folder on node id.
func (s *Service) List(ctx context.Context, id, path string) (nodeproto.FSListResult, error) {
	var out nodeproto.FSListResult
	return out, s.Call(ctx, id, nodeproto.MethodFSList, nodeproto.PathParams{Path: path}, &out)
}

// ReadFile reads a file on node id.
func (s *Service) ReadFile(ctx context.Context, id string, p nodeproto.FSReadParams) (nodeproto.FSReadResult, error) {
	var out nodeproto.FSReadResult
	return out, s.Call(ctx, id, nodeproto.MethodFSRead, p, &out)
}

// WriteFile writes a file on node id.
func (s *Service) WriteFile(ctx context.Context, id string, p nodeproto.FSWriteParams) (nodeproto.FSWriteResult, error) {
	var out nodeproto.FSWriteResult
	return out, s.Call(ctx, id, nodeproto.MethodFSWrite, p, &out)
}

// HostInfo asks node id about its host.
func (s *Service) HostInfo(ctx context.Context, id string) (nodeproto.HostInfo, error) {
	var out nodeproto.HostInfo
	return out, s.Call(ctx, id, nodeproto.MethodHostInfo, struct{}{}, &out)
}

// Notify shows a notification on node id.
func (s *Service) Notify(ctx context.Context, id string, p nodeproto.NotifyParams) (nodeproto.NotifyResult, error) {
	var out nodeproto.NotifyResult
	return out, s.Call(ctx, id, nodeproto.MethodNotify, p, &out)
}

// View is a node as the dashboard, the CLI and !fleet show it.
type View struct {
	*Node
	Commands  string                 `json:"commands"`
	Effective nodeproto.Capabilities `json:"effective"`
	Online    bool                   `json:"online"`
	// UpdateTo is the core's version when the node runs an older one and
	// the core can update it; UpdateNote says why it cannot.
	UpdateTo   string `json:"update_to,omitempty"`
	UpdateNote string `json:"update_note,omitempty"`
}

// Views lists every node with whether it is connected.
func (s *Service) Views() ([]View, error) {
	list, err := s.reg.List()
	out := make([]View, 0, len(list))
	for _, n := range list {
		v := View{Node: n, Commands: n.Commands(), Effective: n.Effective(), Online: s.Online(n.ID)}
		v.UpdateTo, v.UpdateNote = s.UpdateTarget(n)
		out = append(out, v)
	}
	return out, err
}

// ListenAndServe binds addr with listen (net.Listen when nil) and serves
// it.
func (s *Service) ListenAndServe(addr string, listen func(network, addr string) (net.Listener, error)) error {
	if listen == nil {
		listen = net.Listen
	}
	ln, err := listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening for nodes on %s: %w", addr, err)
	}
	return s.Serve(ln)
}
