package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// The core in a box (docs/NODES.md, "The core in a box"; #191 phase 4).
// A box's node container holds a standby core: the same image's binary,
// off until a core move hands the core to this box. The core then runs
// as a child of the agent, inside the node container, on the box's own
// data volume (/data) with the host secrets from the box's sealed store
// in its environment. No Docker socket and no second container: the role
// is a file in the node's volume, which the agent reads when it starts,
// so that a box that is the core starts it again after a restart.
const (
	// CoreRoleFile holds the box's role: RoleCore or RoleStandby.
	CoreRoleFile = "core-role.json"
	// coreRehearsalDir is the scratch data dir of a rehearsal, in the
	// node's volume: never the core's own.
	coreRehearsalDir = "core-rehearsal"
	// BoxCoreEnv is set by the box's core settings (container/box-core.yaml):
	// the data and home volumes, the published ports and the sandbox's
	// security profile. A box without them cannot take the core.
	BoxCoreEnv = "FRIDOLIN_BOX_CORE"
)

// The roles of a box.
const (
	RoleStandby = "standby"
	RoleCore    = "core"
)

// BoxCorePorts are the ports a box publishes for the core it runs: the
// dashboard and the site (container/box-core.yaml).
var BoxCorePorts = []int{8765, 8780}

// coreRole is CoreRoleFile.
type coreRole struct {
	Role  string    `json:"role"`
	Since time.Time `json:"since"`
}

// LoadCoreRole is the role of the box whose state dir is dir: RoleStandby
// when the file is missing.
func LoadCoreRole(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, CoreRoleFile))
	if errors.Is(err, os.ErrNotExist) {
		return RoleStandby, nil
	}
	if err != nil {
		return "", err
	}
	var r coreRole
	if err := json.Unmarshal(data, &r); err != nil {
		return "", fmt.Errorf("reading %s: %w", CoreRoleFile, err)
	}
	if r.Role != RoleCore && r.Role != RoleStandby {
		return "", fmt.Errorf("%s: %q is no role", CoreRoleFile, r.Role)
	}
	return r.Role, nil
}

func saveCoreRole(dir, role string) error {
	data, _ := json.Marshal(coreRole{Role: role, Since: time.Now().UTC().Truncate(time.Second)})
	return writeFile(filepath.Join(dir, CoreRoleFile), append(data, '\n'), 0o600)
}

// BoxSecretStore is a box's store of host secrets: sealed files in the
// node's volume, under a key derived from its device key.
func BoxSecretStore(dir string, key []byte) hostsecrets.Store {
	return hostsecrets.Sealed{Dir: filepath.Join(dir, coreSecretsDir), Key: key}
}

// BoxProcess is the core's process in the box.
type BoxProcess interface {
	Signal(os.Signal) error
	Kill() error
	Wait() error
}

// BoxCore is the CoreRunner of a box: every step runs the agent's own
// binary with a fixed `fridolin core` subcommand in this container, its
// arguments from typed, checked values only; the core itself runs as the
// agent's child (Supervise).
type BoxCore struct {
	// Exe is the agent's binary; Entrypoint the image's entrypoint.sh,
	// which starts the core as the core container does ("" runs Exe).
	Exe, Entrypoint string
	// StateDir is the node's (/node), DataDir the box's data volume
	// (/data), which the core runs on.
	StateDir, DataDir string
	// Secrets is the box's sealed store (BoxSecretStore).
	Secrets hostsecrets.Store
	// Environ is the container's environment, which the core and the
	// commands get with the data dir and the host secrets added.
	Environ []string
	// ReadyURL is the core's /ready; http://127.0.0.1:8765/ready when "".
	ReadyURL string
	// Fride is FrIDE's service on the box network, host:port ("" for a
	// box without FrIDE): its address never reaches the published ports.
	Fride string
	// Ports are the published ports the agent forwards from the box's
	// network to the core on the loopback (BoxCorePorts; none when nil).
	Ports []int
	Logf  func(format string, args ...any)
	// Exec runs Exe with args and env, stdin when not nil, and returns
	// its standard output; nil runs it. Spawn starts the core; nil runs
	// Entrypoint. Tests hand in their own.
	Exec  func(ctx context.Context, env []string, stdin []byte, args ...string) ([]byte, error)
	Spawn func(env []string, args ...string) (BoxProcess, error)
	// StopWait is how long a stop waits for the core to end before it is
	// killed (30s when zero); RestartWait how long the supervisor waits
	// before it starts a core that ended by itself again (10s).
	StopWait, RestartWait time.Duration
	// Probe checks that bubblewrap can make a brain's namespaces in this
	// container; nil runs it (BwrapProbe).
	Probe func(ctx context.Context) error

	mu      sync.Mutex
	proc    *boxProc
	ctx     context.Context
	stopped bool
}

// boxProc is one run of the core.
type boxProc struct {
	p        BoxProcess
	done     chan struct{}
	err      error
	stopping bool
	started  time.Time
}

// BoxCoreArgs are the core's arguments in a box: the container's run, its
// dashboard and site on the loopback.
var BoxCoreArgs = []string{"run", "--container", "--box"}

func (b *BoxCore) logf(format string, args ...any) {
	if b.Logf != nil {
		b.Logf(format, args...)
	}
}

func (b *BoxCore) stopWait() time.Duration {
	if b.StopWait > 0 {
		return b.StopWait
	}
	return 30 * time.Second
}

func (b *BoxCore) restartWait() time.Duration {
	if b.RestartWait > 0 {
		return b.RestartWait
	}
	return 10 * time.Second
}

// env is the core's environment: the container's, without what names a
// data dir or a host secret, then the box's data dir and the secrets of
// its store. A secret the store does not hold is left out, as core start
// leaves it out.
func (b *BoxCore) env(dataDir string) ([]string, error) {
	drop := []string{"FRIDOLIN_HOME", "FRIDOLIN_CONFIG"}
	for _, s := range hostsecrets.CoreEnv {
		drop = append(drop, s.Env)
	}
	env := slices.DeleteFunc(slices.Clone(b.Environ), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(drop, name)
	})
	env = append(env, "FRIDOLIN_HOME="+dataDir, "FRIDOLIN_CONFIG="+filepath.Join(dataDir, "config.json"))
	if b.Secrets == nil {
		return nil, errors.New("this box has no secret store")
	}
	for _, s := range hostsecrets.CoreEnv {
		v, err := b.Secrets.Get(s.Name)
		switch {
		case errors.Is(err, hostsecrets.ErrNotFound):
			continue
		case err != nil:
			return nil, fmt.Errorf("reading %s from the box's store: %w", s.Name, err)
		}
		env = append(env, s.Env+"="+v)
	}
	return env, nil
}

// run runs `fridolin args...` on dataDir with the secrets; the error
// carries the end of its standard error, which names no secret (the core
// commands never print one).
func (b *BoxCore) run(ctx context.Context, timeout time.Duration, dataDir string, stdin []byte, args ...string) ([]byte, error) {
	env, err := b.env(dataDir)
	if err != nil {
		return nil, err
	}
	defer clear(env)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if b.Exec != nil {
		return b.Exec(ctx, env, stdin, args...)
	}
	var stdout bytes.Buffer
	stderr := &limitedBuffer{max: 64 << 10}
	cmd := exec.CommandContext(ctx, b.Exe, args...)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, &stdout, stderr
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		what := "fridolin " + strings.Join(args[:min(len(args), 4)], " ")
		if tail := lastLine(stderr.String()); tail != "" {
			return stdout.Bytes(), fmt.Errorf("%s: %v: %s", what, err, tail)
		}
		return stdout.Bytes(), fmt.Errorf("%s: %v", what, err)
	}
	return stdout.Bytes(), nil
}

func (b *BoxCore) Status(ctx context.Context) (nodeproto.CoreRuntime, error) {
	var st nodeproto.CoreRuntime
	out, err := b.run(ctx, 3*time.Minute, b.DataDir, nil, "core", "status", "--json")
	if err != nil {
		return st, err
	}
	return st, decodeLast(out, &st)
}

func (b *BoxCore) Export(ctx context.Context, move string) (nodeproto.CoreExportInfo, error) {
	var info nodeproto.CoreExportInfo
	if err := nodeproto.CheckMoveID(move); err != nil {
		return info, err
	}
	out, err := b.run(ctx, time.Hour, b.DataDir, nil, "core", "export", "--move", move, "--json")
	if err != nil {
		return info, err
	}
	return info, decodeLast(out, &info)
}

// Import imports the latest export into the box's data volume, moving
// what is there aside (core import --force), while no core runs.
func (b *BoxCore) Import(ctx context.Context) error {
	if b.Running() {
		return errors.New("the core runs in this box: it is stopped before an import")
	}
	_, err := b.run(ctx, time.Hour, b.DataDir, nil, "core", "import", "--latest", "--force")
	return err
}

func (b *BoxCore) Promote(ctx context.Context) error {
	_, err := b.run(ctx, 3*time.Minute, b.DataDir, nil, "core", "promote")
	return err
}

func (b *BoxCore) Release(ctx context.Context) error {
	_, err := b.run(ctx, 3*time.Minute, b.DataDir, nil, "core", "release")
	return err
}

func (b *BoxCore) Ready(ctx context.Context) (bool, string) {
	url := b.ReadyURL
	if url == "" {
		url = "http://127.0.0.1:8765/ready"
	}
	if !b.Running() {
		return false, "the core does not run in this box"
	}
	return ReadyAt(ctx, http.DefaultClient, url)
}

// Rehearse imports the latest export into a scratch data dir in the
// node's volume, with the rehearsal's settings, and removes it: never the
// box's data volume, never the lease.
func (b *BoxCore) Rehearse(ctx context.Context, move string) (nodeproto.CoreRehearsal, error) {
	var res nodeproto.CoreRehearsal
	if err := nodeproto.CheckMoveID(move); err != nil {
		return res, err
	}
	if b.StateDir == "" {
		return res, errors.New("this box has no state dir")
	}
	scratch := filepath.Join(b.StateDir, coreRehearsalDir)
	_ = os.RemoveAll(scratch) // one left from a rehearsal that was cut off
	start := time.Now()
	_, err := b.run(ctx, time.Hour, scratch, nil, "core", "import", "--latest", "--rehearsal",
		"--data-dir", scratch, "--config", filepath.Join(scratch, "config.json"))
	res = nodeproto.CoreRehearsal{Move: move, OK: err == nil, Seconds: int(time.Since(start).Seconds())}
	if err != nil {
		res.Note = "the import failed: " + err.Error()
	}
	if rerr := os.RemoveAll(scratch); rerr != nil {
		res.Note = strings.TrimPrefix(res.Note+"; removing the scratch data dir failed: "+rerr.Error(), "; ")
		res.OK = false
	}
	return res, nil
}

// Image is the coreImage's business on a box: a box runs its image pinned
// by digest, which a box update changes.
func (b *BoxCore) Image(context.Context, string, string, bool) (nodeproto.CoreImageResult, error) {
	return nodeproto.CoreImageResult{}, errors.New("a box runs the image it was updated to: update the box (fridolin nodes update)")
}

// ExportFiles reads the secret files of the core's credentials folder,
// under the home of this container (the box's home volume).
func (b *BoxCore) ExportFiles(ctx context.Context) (string, map[string][]byte, error) {
	out, err := b.run(ctx, 3*time.Minute, b.DataDir, nil, "core", "files", "export")
	defer clear(out)
	if err != nil {
		return "", nil, err
	}
	var f struct {
		Dir   string            `json:"dir"`
		Files map[string][]byte `json:"files"`
	}
	if err := decodeLast(out, &f); err != nil {
		return "", nil, err
	}
	return f.Dir, f.Files, nil
}

// ImportFiles writes them under the box's home, keeping any there.
func (b *BoxCore) ImportFiles(ctx context.Context, dir string, files map[string][]byte) (stored, kept []string, err error) {
	in, err := json.Marshal(map[string]any{"dir": dir, "files": files})
	if err != nil {
		return nil, nil, err
	}
	defer clear(in)
	out, err := b.run(ctx, 3*time.Minute, b.DataDir, in, "core", "files", "import")
	if err != nil {
		return nil, nil, err
	}
	var res struct {
		Stored []string `json:"stored"`
		Kept   []string `json:"kept"`
	}
	if err := decodeLast(out, &res); err != nil {
		return nil, nil, err
	}
	return res.Stored, res.Kept, nil
}

// Running reports whether the core runs in this box.
func (b *BoxCore) Running() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.proc != nil
}

// Start makes this box the core: the role first, so that a box that
// restarts on the way starts the core again, then the core itself.
func (b *BoxCore) Start(ctx context.Context) error {
	if err := saveCoreRole(b.StateDir, RoleCore); err != nil {
		return fmt.Errorf("writing the box's role: %w", err)
	}
	if err := b.launch(); err != nil {
		if errors.Is(err, errBoxStopping) {
			// The container stops: the box is the core, and starts it
			// again with the agent.
			return err
		}
		if rerr := saveCoreRole(b.StateDir, RoleStandby); rerr != nil {
			b.logf("node: box: writing the box's role: %v", rerr)
		}
		return err
	}
	return nil
}

// Stop stands the box down: the role first, then the core, which
// releases the lease as it stops cleanly.
func (b *BoxCore) Stop(ctx context.Context) error {
	if err := saveCoreRole(b.StateDir, RoleStandby); err != nil {
		return fmt.Errorf("writing the box's role: %w", err)
	}
	return b.halt(ctx)
}

// errBoxStopping is launch's error while the agent stops.
var errBoxStopping = errors.New("the agent is stopping; the core starts with it again")

// launch starts the core unless it runs.
func (b *BoxCore) launch() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.proc != nil {
		return nil
	}
	if b.stopped {
		return errBoxStopping
	}
	env, err := b.env(b.DataDir)
	if err != nil {
		return err
	}
	defer clear(env)
	spawn := b.Spawn
	if spawn == nil {
		spawn = b.spawn
	}
	p, err := spawn(env, BoxCoreArgs...)
	if err != nil {
		return fmt.Errorf("starting the core: %w", err)
	}
	bp := &boxProc{p: p, done: make(chan struct{}), started: time.Now()}
	b.proc = bp
	b.logf("node: box: the core runs in this box")
	go b.watch(bp)
	return nil
}

// spawn runs the image's entrypoint, as the core container runs it, with
// the core's output in the container's log.
func (b *BoxCore) spawn(env []string, args ...string) (BoxProcess, error) {
	prog := b.Entrypoint
	if prog == "" {
		prog = b.Exe
	}
	cmd := exec.Command(prog, args...)
	cmd.Env, cmd.Stdout, cmd.Stderr, cmd.Dir = env, os.Stdout, os.Stderr, b.DataDir
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return execProc{cmd}, nil
}

type execProc struct{ cmd *exec.Cmd }

func (p execProc) Signal(s os.Signal) error { return p.cmd.Process.Signal(s) }
func (p execProc) Kill() error              { return p.cmd.Process.Kill() }
func (p execProc) Wait() error              { return p.cmd.Wait() }

// watch waits for a run of the core to end, and starts it again when it
// ended by itself while the box is the core.
func (b *BoxCore) watch(bp *boxProc) {
	bp.err = bp.p.Wait()
	close(bp.done)
	b.mu.Lock()
	if b.proc == bp {
		b.proc = nil
	}
	again := !bp.stopping && !b.stopped
	b.mu.Unlock()
	if !again {
		return
	}
	role, err := LoadCoreRole(b.StateDir)
	if err != nil || role != RoleCore {
		return
	}
	wait := b.restartWait()
	if time.Since(bp.started) < time.Minute {
		wait *= 6
	}
	b.logf("node: box: the core ended by itself (%v); starting it again in %s", bp.err, wait)
	time.AfterFunc(wait, func() {
		if role, err := LoadCoreRole(b.StateDir); err == nil && role == RoleCore {
			if err := b.launch(); err != nil {
				b.logf("node: box: starting the core again: %v", err)
			}
		}
	})
}

// halt stops the core, if it runs, as docker stop does: SIGTERM, then a
// kill once StopWait passed. A killed core may not have released the
// lease, which the move then sees.
func (b *BoxCore) halt(ctx context.Context) error {
	b.mu.Lock()
	bp := b.proc
	if bp != nil {
		bp.stopping = true
	}
	b.mu.Unlock()
	if bp == nil {
		return nil
	}
	b.logf("node: box: stopping the core")
	if err := bp.p.Signal(syscall.SIGTERM); err != nil {
		b.logf("node: box: signalling the core: %v", err)
	}
	t := time.NewTimer(b.stopWait())
	defer t.Stop()
	select {
	case <-bp.done:
		return nil
	case <-t.C:
	case <-ctx.Done():
	}
	_ = bp.p.Kill()
	<-bp.done
	return fmt.Errorf("the core did not stop within %s and was killed", b.stopWait())
}

// Supervise runs as long as the agent: it publishes the core's ports,
// starts the core when the box is the core, and stops it (keeping the
// role) when ctx ends, as a container stop would.
func (b *BoxCore) Supervise(ctx context.Context) {
	b.mu.Lock()
	b.ctx, b.stopped = ctx, false
	b.mu.Unlock()
	var wg sync.WaitGroup
	b.publish(ctx, &wg)
	switch role, err := LoadCoreRole(b.StateDir); {
	case err != nil:
		b.logf("node: box: %v; staying on standby", err)
	case role == RoleCore:
		b.logf("node: box: this box is the core: starting it")
		if err := b.launch(); err != nil {
			b.logf("node: box: %v", err)
		}
	}
	<-ctx.Done()
	b.mu.Lock()
	b.stopped = true
	b.mu.Unlock()
	if err := b.halt(context.Background()); err != nil {
		b.logf("node: box: %v", err)
	}
	wg.Wait()
}

// publish forwards each port from the container's addresses on the box
// network, where Docker delivers the published ports, to the core on the
// loopback, refusing FrIDE's address: FrIDE shares the box network but
// never reaches the core's dashboard or site that way.
func (b *BoxCore) publish(ctx context.Context, wg *sync.WaitGroup) {
	if len(b.Ports) == 0 {
		return
	}
	notFride := func(net.Addr) bool { return true }
	if b.Fride != "" {
		host, _, _ := net.SplitHostPort(b.Fride)
		from := fromHost(ctx, host)
		notFride = func(a net.Addr) bool { return !from(a) }
	}
	var lc net.ListenConfig
	for _, ip := range boxAddrs() {
		for _, port := range b.Ports {
			addr := net.JoinHostPort(ip, strconv.Itoa(port))
			ln, err := lc.Listen(ctx, "tcp", addr)
			if err != nil {
				b.logf("node: box: the core's port %d cannot be published on %s: %v", port, addr, err)
				continue
			}
			lo := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
			wg.Go(func() {
				forward(ctx, ln, func(ctx context.Context) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "tcp", lo)
				}, notFride, b.logf)
			})
		}
	}
}

// fromHost reports whether a connection comes from one of host's
// addresses, as the box network's DNS resolves it now.
func fromHost(ctx context.Context, host string) func(net.Addr) bool {
	return func(addr net.Addr) bool {
		ta, ok := addr.(*net.TCPAddr)
		if !ok {
			return false
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
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
}

// bwrapPath is bubblewrap in the fridolin-core image.
const bwrapPath = "/usr/bin/bwrap"

// BwrapProbe checks that bubblewrap can make the namespaces of a brain's
// run in this container, as the core checks it before its first run.
func BwrapProbe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bwrapPath, "--unshare-user", "--unshare-pid", "--unshare-ipc",
		"--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev", "--die-with-parent", "--", "/bin/true")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, lastLine(string(out)))
	}
	return nil
}

// boxSetup is a box's checks for core-host: the core's settings
// (box-core.yaml: the volumes, the ports, the security profile) and the
// brains' sandbox, which needs that profile.
func (a *Agent) boxSetup(ctx context.Context) ([]nodeproto.CoreCheckItem, bool, string) {
	bc, ok := a.CoreRunner.(*BoxCore)
	if !ok {
		return []nodeproto.CoreCheckItem{{ID: "box-core", Title: "The core's settings", Status: "fail", Required: true,
			Detail: "this box was made without box-core.yaml (its data volume, ports and the sandbox's security profile): box.sh --update (box.ps1 -Update) on it writes them"}}, false, ""
	}
	items := []nodeproto.CoreCheckItem{{ID: "box-core", Title: "The core's settings", Status: "ok", Required: true,
		Detail: "box-core.yaml: the data volume, the dashboard and site ports, the sandbox's security profile"}}
	probe := bc.Probe
	if probe == nil {
		probe = BwrapProbe
	}
	sb := nodeproto.CoreCheckItem{ID: "sandbox", Title: "The brains' sandbox (bubblewrap)", Status: "ok", Required: true,
		Detail: "bubblewrap makes a brain's namespaces in the box"}
	if err := probe(ctx); err != nil {
		sb.Status = "fail"
		sb.Detail = "bubblewrap cannot make a brain's namespaces in the box (" + err.Error() + "); on Linux with AppArmor (Ubuntu) set FRIDOLIN_BOX_APPARMOR in the box's .env (docs/NODES.md, \"The core in a box\")"
	}
	items = append(items, sb)
	return items, sb.Status == "ok", ""
}

// ForgetFiles removes the named secret files a move wrote into the box's
// home (fridolin core files forget, #266).
func (b *BoxCore) ForgetFiles(ctx context.Context, dir string, names []string) ([]string, error) {
	in, err := json.Marshal(map[string]any{"dir": dir, "names": names})
	if err != nil {
		return nil, err
	}
	out, err := b.run(ctx, 3*time.Minute, b.DataDir, in, "core", "files", "forget")
	if err != nil {
		return nil, err
	}
	var res struct {
		Removed []string `json:"removed"`
	}
	return res.Removed, decodeLast(out, &res)
}

// ForgetData empties the box's data volume (fridolin core forget-data,
// #266), which checks the lease itself; never while the core runs here.
func (b *BoxCore) ForgetData(ctx context.Context) ([]string, error) {
	if b.Running() {
		return nil, errors.New("the core runs in this box")
	}
	out, err := b.run(ctx, 30*time.Minute, b.DataDir, nil, "core", "forget-data", "--json")
	if err != nil {
		return nil, err
	}
	var res struct {
		Removed []string `json:"removed"`
	}
	return res.Removed, decodeLast(out, &res)
}
