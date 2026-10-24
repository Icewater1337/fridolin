package nodeagent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// Agent is one node's connection to the core.
type Agent struct {
	// Dir is the node's state dir: its config, key and the disk it reports.
	Dir     string
	Config  Config
	Key     ed25519.PrivateKey
	Version string
	// Dial connects to the core, nil with the host's network. A node in a
	// box dials over its own tailnet device.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Logf logs; nil logs with the standard logger.
	Logf func(format string, args ...any)
	// Notifier shows a notification; nil shows it the OS's way.
	Notifier func(ctx context.Context, title, message string) (nodeproto.NotifyResult, error)
	// MinBackoff and MaxBackoff bound the wait between reconnects (1s
	// and 1m when zero); HeartbeatEvery is the report interval (20s).
	MinBackoff, MaxBackoff time.Duration
	HeartbeatEvery         time.Duration
	// Volumes lists the machine's drives for fs.roots; nil asks the OS
	// (platform.Volumes). Tests hand in their own.
	Volumes func() []platform.Volume
	// OnState hears every state the core sends, for tests and the CLI.
	OnState func(nodeproto.State)
	// FrideCheckEvery is how often the agent asks whether FrIDE answers
	// (DefaultFrideCheckEvery when zero). FrideToken is the companion
	// token of an agent without a state dir; one with a state dir reads
	// it, and node.json's fride section, from there at every check.
	FrideCheckEvery time.Duration
	FrideToken      string
	// AndroidToolchain finds the JDK and the Android SDK android-build
	// needs; nil looks where platform.FindAndroidToolchain does.
	AndroidToolchain func() (platform.AndroidToolchain, error)
	// Executable is the running binary, which an update from the core
	// replaces (update.go); Args (without the program) and Environ are
	// what it restarts with from a terminal. Updates fail without it.
	Executable    string
	Args, Environ []string
	// Restart starts the agent again after an update or a rollback; nil
	// restarts it the way it runs: in place from a terminal, by exiting
	// with ExitRestart under a service manager. Tests hand in their own.
	Restart func() error
	// UpdateTrial is how long a new binary has to connect before the node
	// puts the old one back (nodeproto.UpdateTrialMinutes when zero).
	UpdateTrial time.Duration
	// CheckBinary runs a new binary to see that it is version; nil runs
	// `<binary> version`.
	CheckBinary func(ctx context.Context, path, version string) error
	// WorkSandbox is the sandbox of workstation sessions ("" finds the
	// platform's; nodeproto.SandboxNone runs them without one).
	// AgentPaths, when set, are the agent CLIs by provider instead of the
	// PATH's; TerminalShell the shell's argv instead of the user's.
	// TerminalIdle and AgentMaxRuntime are the workstation's limits
	// (DefaultTerminalIdle, DefaultAgentMaxRuntime when zero).
	WorkSandbox     string
	AgentPaths      map[string]string
	TerminalShell   []string
	TerminalIdle    time.Duration
	AgentMaxRuntime time.Duration
	// CoreSecrets is where core-host keeps host secrets (CoreSecretsPlace
	// names it); nil is the box's sealed files or the host's store
	// (corehost.go). CoreSetup, when set, is setup-machine --core's
	// checks instead of running the agent's own binary.
	CoreSecrets      hostsecrets.Store
	CoreSecretsPlace string
	CoreSetup        func(ctx context.Context) ([]nodeproto.CoreCheckItem, bool, string)
	// CoreRunner does what a core move does to the core here; nil is
	// NativeCore with Executable (coremove.go). CoreScan, when set, is the
	// machine-bound scan instead of the files it reads. MovePoll is how
	// often a move reads the lease (10s), MoveGrace how long after the
	// deadline the source starts again on its own data
	// (nodeproto.MoveRollbackGrace), MoveExportWait how long it waits for
	// the target's export once the target released the lease (15m).
	CoreRunner                          CoreRunner
	CoreScan                            func() []string
	MovePoll, MoveGrace, MoveExportWait time.Duration

	core      coreHost
	moves     coreMoves
	upd       updater
	mu        sync.Mutex
	state     nodeproto.State
	connected bool
	fride     fride
	android   android
	work      workstation
}

// Load makes the agent of the node in dir.
func Load(dir, version string) (*Agent, error) {
	cfg, err := LoadConfig(dir)
	if err != nil {
		return nil, err
	}
	key, err := LoadKey(dir)
	if err != nil {
		return nil, fmt.Errorf("the device key: %w", err)
	}
	return &Agent{Dir: dir, Config: cfg, Key: key, Version: version}, nil
}

// ID is the node's device ID.
func (a *Agent) ID() string { return DeviceID(a.Key) }

// State is what the core last said the node may do.
func (a *Agent) State() nodeproto.State {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

// Connected reports whether the agent holds a connection to the core.
func (a *Agent) Connected() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.connected
}

func (a *Agent) logf(format string, args ...any) {
	if a.Logf != nil {
		a.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (a *Agent) setState(st nodeproto.State) {
	a.mu.Lock()
	a.state = st
	a.mu.Unlock()
	if st.Device != nodeproto.DeviceApproved {
		a.stopUnapproved(nodeproto.Capabilities{}, true)
	} else {
		a.stopUnapproved(nodeproto.Intersect(a.declared(), st.Approved), false)
	}
	if a.OnState != nil {
		a.OnState(st)
	}
}

func (a *Agent) setConnected(v bool) {
	a.mu.Lock()
	a.connected = v
	a.mu.Unlock()
}

// CloseError is the core closing the connection with a reason.
type CloseError struct {
	Reason, Message string
}

func (e *CloseError) Error() string {
	if e.Message != "" {
		return "the core closed the connection (" + e.Reason + "): " + e.Message
	}
	return "the core closed the connection (" + e.Reason + ")"
}

// Final reports whether the agent should stop reconnecting.
func (e *CloseError) Final() bool { return nodeproto.Final(e.Reason) }

// coreURL is the WebSocket address of the core.
func coreURL(core string) string {
	core = strings.TrimSpace(core)
	if strings.HasPrefix(core, "ws://") || strings.HasPrefix(core, "wss://") {
		rest := core[strings.Index(core, "://")+3:]
		if !strings.Contains(rest, "/") {
			return core + nodeproto.Path
		}
		return core
	}
	return "ws://" + core + nodeproto.Path
}

// conn is an authenticated connection.
type conn struct {
	ws *websocket.Conn
}

func (c *conn) send(ctx context.Context, typ, id string, body any) error {
	data, err := nodeproto.Encode(typ, id, body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return c.ws.Write(ctx, websocket.MessageText, data)
}

func (c *conn) read(ctx context.Context) (nodeproto.Envelope, error) {
	typ, data, err := c.ws.Read(ctx)
	if err != nil {
		return nodeproto.Envelope{}, err
	}
	if typ != websocket.MessageText {
		return nodeproto.Envelope{}, errors.New("a binary message")
	}
	return nodeproto.Decode(data)
}

// closeErr turns a close message into a *CloseError.
func closeErr(env nodeproto.Envelope) error {
	c, err := nodeproto.DecodeBody[nodeproto.Close](env)
	if err != nil {
		return err
	}
	return &CloseError{Reason: c.Reason, Message: c.Message}
}

// connect dials the core and goes through hello, challenge and auth, with
// joinCode for a device the core does not know yet. It returns the
// connection and the state of the welcome.
func (a *Agent) connect(ctx context.Context, joinCode string) (*conn, nodeproto.State, error) {
	hctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	opts := &websocket.DialOptions{Subprotocols: []string{nodeproto.Subprotocol}}
	if a.Dial != nil {
		opts.HTTPClient = &http.Client{Transport: &http.Transport{DialContext: a.Dial}}
	}
	ws, _, err := websocket.Dial(hctx, coreURL(a.Config.Core), opts)
	if err != nil {
		return nil, nodeproto.State{}, fmt.Errorf("connecting to the core at %s: %w", a.Config.Core, err)
	}
	ws.SetReadLimit(nodeproto.MaxMessageBytes)
	c := &conn{ws: ws}
	fail := func(err error) (*conn, nodeproto.State, error) {
		_ = ws.CloseNow()
		return nil, nodeproto.State{}, err
	}
	pub := a.Key.Public().(ed25519.PublicKey)
	hello := nodeproto.Hello{
		DeviceID: nodeproto.DeviceIDFor(pub), PublicKey: pub, Name: a.Config.Name,
		OS: runtime.GOOS, Arch: runtime.GOARCH, Version: a.Version,
		Capabilities: a.declared(), JoinCode: joinCode,
	}
	if err := c.send(hctx, nodeproto.TypeHello, "", hello); err != nil {
		return fail(err)
	}
	var st nodeproto.State
	for done := false; !done; {
		env, err := c.read(hctx)
		if err != nil {
			return fail(fmt.Errorf("the core's answer: %w", err))
		}
		switch env.Type {
		case nodeproto.TypeChallenge:
			ch, err := nodeproto.DecodeBody[nodeproto.Challenge](env)
			if err != nil {
				return fail(err)
			}
			sig := nodeproto.Sign(a.Key, hello.DeviceID, ch.Nonce)
			if err := c.send(hctx, nodeproto.TypeAuth, "", nodeproto.Auth{Signature: sig}); err != nil {
				return fail(err)
			}
		case nodeproto.TypeWelcome:
			if st, err = nodeproto.DecodeBody[nodeproto.State](env); err != nil {
				return fail(err)
			}
			done = true
		case nodeproto.TypeClose:
			return fail(closeErr(env))
		default:
			return fail(fmt.Errorf("unexpected %s during the handshake", env.Type))
		}
	}
	return c, st, nil
}

// Run connects to the core and serves it until ctx ends, reconnecting
// with exponential backoff and jitter. It returns nil when ctx ends, and
// a *CloseError when the core revoked the node or does not know it, after
// which reconnecting is pointless.
func (a *Agent) Run(ctx context.Context) error {
	minB, maxB := a.MinBackoff, a.MaxBackoff
	if minB <= 0 {
		minB = time.Second
	}
	if maxB <= 0 {
		maxB = time.Minute
	}
	backoff := minB
	// FrIDE and the Android toolchain first, so that the first hello
	// declares them when they are there.
	a.frideCheck(ctx)
	a.androidCheck(true)
	a.coreHostChanged()
	a.coreMovesStart(ctx)
	defer a.coreMovesStop()
	if a.Dir != "" {
		pruneAndroid(filepath.Join(a.Dir, androidTransfers), time.Now(), 0)
	}
	fctx, stopFride := context.WithCancel(ctx)
	var fwg sync.WaitGroup
	fwg.Go(func() { a.frideLoop(fctx) })
	defer fwg.Wait()
	defer stopFride()
	a.boxForward(fctx, &fwg)
	// A new binary on trial after an update from the core.
	a.updateStartup(fctx, &fwg)
	// A stopping agent leaves no session behind.
	defer a.stopUnapproved(nodeproto.Capabilities{}, true)
	for {
		start := time.Now()
		err := a.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if ce, ok := errors.AsType[*CloseError](err); ok && ce.Final() {
			a.logf("node: %v; not reconnecting", ce)
			return ce
		}
		if time.Since(start) > 2*maxB {
			backoff = minB
		}
		// Full jitter between half the backoff and all of it, so that
		// nodes cut off together do not come back together.
		wait := backoff/2 + rand.N(backoff/2+1)
		a.logf("node: %v; reconnecting in %v", err, wait.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		backoff = min(backoff*2, maxB)
	}
}

// session is one connection: the handshake, then heartbeats and requests
// until it ends.
func (a *Agent) session(ctx context.Context) error {
	c, st, err := a.connect(ctx, "")
	if err != nil {
		return err
	}
	defer c.ws.CloseNow()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.setState(st)
	a.setConnected(true)
	defer a.setConnected(false)
	a.logf("node: connected to the core at %s as %s (%s); device %s, commands %s%s", a.Config.Core, a.Config.Name, a.ID(), st.Device, st.Commands, messageSuffix(st))
	a.checkCoreKey(st)
	// An update that holds now, or one that did not: the core hears how
	// it went.
	if rep := a.updateConnected(); rep != nil {
		data, _ := json.Marshal(rep)
		if err := c.send(ctx, nodeproto.TypeEvent, "", nodeproto.Event{Kind: nodeproto.EventUpdate, Data: data}); err == nil {
			a.updateReported()
		}
	}
	if rep := a.boxResult(); rep != nil {
		data, _ := json.Marshal(rep)
		if err := c.send(ctx, nodeproto.TypeEvent, "", nodeproto.Event{Kind: nodeproto.EventUpdate, Data: data}); err == nil {
			a.boxResultReported()
		}
	}
	a.boxStatus(true)
	defer a.boxStatus(false)

	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	every := a.HeartbeatEvery
	if every <= 0 {
		every = 20 * time.Second
	}
	wg.Go(func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			if err := c.send(ctx, nodeproto.TypeHeartbeat, "", nodeproto.Heartbeat{Stats: a.stats()}); err != nil {
				cancel()
				return
			}
			a.boxStatus(true)
			// The box's updater writes its result while the new agent is
			// already connected.
			if rep := a.boxResult(); rep != nil {
				data, _ := json.Marshal(rep)
				if c.send(ctx, nodeproto.TypeEvent, "", nodeproto.Event{Kind: nodeproto.EventUpdate, Data: data}) == nil {
					a.boxResultReported()
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	})
	// FrIDE's news, and what the node offers when FrIDE comes or goes.
	a.fride.init()
	a.work.init()
	wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-a.fride.changed:
				if err := c.send(ctx, nodeproto.TypeDeclare, "", nodeproto.Declare{Capabilities: a.declared()}); err != nil {
					cancel()
					return
				}
			case news := <-a.work.news:
				data, _ := json.Marshal(news)
				if err := c.send(ctx, nodeproto.TypeEvent, "", nodeproto.Event{Kind: nodeproto.EventAgent, Data: data}); err != nil {
					a.logf("node: agent session %s's news did not reach the core: %v", news.ID, err)
					cancel()
					return
				}
			case ev := <-a.fride.events:
				data, _ := json.Marshal(ev)
				if err := c.send(ctx, nodeproto.TypeEvent, "", nodeproto.Event{Kind: nodeproto.EventFride, Data: data}); err != nil {
					a.logf("node: FrIDE's %s of thread %d did not reach the core: %v", ev.Event, ev.ThreadID, err)
					cancel()
					return
				}
			}
		}
	})
	// At most this many calls at once; more wait their turn.
	slots := make(chan struct{}, 8)
	for {
		env, err := c.read(ctx)
		if err != nil {
			return fmt.Errorf("the connection ended: %w", err)
		}
		switch env.Type {
		case nodeproto.TypeState:
			st, err := nodeproto.DecodeBody[nodeproto.State](env)
			if err != nil {
				return err
			}
			a.setState(st)
			a.checkCoreKey(st)
			a.logf("node: the core says: device %s, commands %s, may serve %s%s", st.Device, st.Commands, st.Approved.Summary(), messageSuffix(st))
		case nodeproto.TypeRequest:
			req, err := nodeproto.DecodeBody[nodeproto.Request](env)
			id := env.ID
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return ctx.Err()
			}
			wg.Go(func() {
				defer func() { <-slots }()
				var resp nodeproto.Response
				if err != nil {
					resp.Error = nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
				} else {
					resp = a.handle(ctx, req)
				}
				if err := c.send(ctx, nodeproto.TypeResponse, id, resp); err != nil {
					cancel()
				}
			})
		case nodeproto.TypeClose:
			return closeErr(env)
		default:
			return fmt.Errorf("unexpected %s from the core", env.Type)
		}
	}
}

func messageSuffix(st nodeproto.State) string {
	if st.Message == "" {
		return ""
	}
	return " — " + st.Message
}
