package nodeagent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
)

// fakeProc is a core process: Signal ends it as a clean stop does, die
// ends it as a crash does.
type fakeProc struct {
	once   sync.Once
	done   chan struct{}
	signal chan os.Signal
}

func newFakeProc() *fakeProc {
	return &fakeProc{done: make(chan struct{}), signal: make(chan os.Signal, 4)}
}
func (p *fakeProc) die() { p.once.Do(func() { close(p.done) }) }
func (p *fakeProc) Signal(s os.Signal) error {
	p.signal <- s
	p.die()
	return nil
}
func (p *fakeProc) Kill() error { p.die(); return nil }
func (p *fakeProc) Wait() error { <-p.done; return nil }

type spawner struct {
	mu    sync.Mutex
	procs []*fakeProc
	envs  [][]string
	args  [][]string
}

func (s *spawner) spawn(env []string, args ...string) (BoxProcess, error) {
	p := newFakeProc()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.procs, s.envs, s.args = append(s.procs, p), append(s.envs, slices.Clone(env)), append(s.args, args)
	return p, nil
}

func (s *spawner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.procs)
}

func (s *spawner) last() *fakeProc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.procs[len(s.procs)-1]
}

func testBoxCore(t *testing.T, sp *spawner) (*BoxCore, hostsecrets.Store) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	key, err := CoreSecretsKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	st := BoxSecretStore(dir, key)
	return &BoxCore{Exe: "/opt/fridolin/fridolin", StateDir: dir, DataDir: filepath.Join(t.TempDir(), "data"), Secrets: st,
		Environ: []string{"PATH=/usr/bin", "FRIDOLIN_HOME=/elsewhere", "B2_VAULT_KEY=from-the-container"},
		Spawn:   sp.spawn, Logf: t.Logf, StopWait: time.Second, RestartWait: 10 * time.Millisecond}, st
}

// The box's role outlives the agent: a box that is the core starts it
// again when the agent starts (a container restart, a box update), and a
// container stop stops it without standing the box down.
func TestBoxCoreRoleOutlivesTheAgent(t *testing.T) {
	sp := &spawner{}
	b, st := testBoxCore(t, sp)
	if err := st.Set(hostsecrets.B2VaultKey, "test-b2-value"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Supervise(ctx); close(done) }()
	waitFor(t, "the supervisor", func() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.ctx != nil })
	if sp.count() != 0 {
		t.Fatal("a box on standby started a core")
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r, _ := LoadCoreRole(b.StateDir); r != RoleCore || !b.Running() {
		t.Fatalf("after Start: role %s, running %v", r, b.Running())
	}
	env, args := sp.envs[0], sp.args[0]
	if !slices.Equal(args, BoxCoreArgs) {
		t.Errorf("the core's arguments: %q", args)
	}
	for _, want := range []string{"FRIDOLIN_HOME=" + b.DataDir, "FRIDOLIN_CONFIG=" + filepath.Join(b.DataDir, "config.json"), "B2_VAULT_KEY=test-b2-value", "PATH=/usr/bin"} {
		if !slices.Contains(env, want) {
			t.Errorf("the core's environment lacks %s", strings.SplitN(want, "=", 2)[0])
		}
	}
	for _, not := range []string{"FRIDOLIN_HOME=/elsewhere", "B2_VAULT_KEY=from-the-container"} {
		if slices.Contains(env, not) {
			t.Errorf("the core's environment keeps the container's %s", strings.SplitN(not, "=", 2)[0])
		}
	}

	// A core that ends by itself starts again while the box is the core.
	sp.last().die()
	waitFor(t, "the core to start again", func() bool { return sp.count() == 2 && b.Running() })

	// The agent stops (docker stop): the core stops with SIGTERM, the
	// role stays.
	cancel()
	<-done
	if b.Running() {
		t.Error("the core runs after the agent stopped")
	}
	if s := <-sp.procs[1].signal; s.String() != "terminated" {
		t.Errorf("the core was stopped with %v", s)
	}
	if r, _ := LoadCoreRole(b.StateDir); r != RoleCore {
		t.Errorf("a container stop stood the box down: %s", r)
	}

	// The agent starts again: the core with it.
	b2 := &BoxCore{Exe: b.Exe, StateDir: b.StateDir, DataDir: b.DataDir, Secrets: st, Environ: b.Environ,
		Spawn: sp.spawn, Logf: t.Logf, StopWait: time.Second, RestartWait: 10 * time.Millisecond}
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { b2.Supervise(ctx2); close(done2) }()
	waitFor(t, "the core to start with the agent", func() bool { return sp.count() == 3 && b2.Running() })

	// A move stands it down: role first, then the core, which stays off.
	if err := b2.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r, _ := LoadCoreRole(b.StateDir); r != RoleStandby || b2.Running() {
		t.Errorf("after Stop: role %s, running %v", r, b2.Running())
	}
	time.Sleep(50 * time.Millisecond)
	if sp.count() != 3 {
		t.Error("a stood-down core started again")
	}
	cancel2()
	<-done2
}

// A core that does not stop is killed after StopWait, and Stop says so:
// it may not have released the lease, which the move then sees.
func TestBoxCoreKillsACoreThatDoesNotStop(t *testing.T) {
	stuck := &stuckProc{done: make(chan struct{})}
	b, _ := testBoxCore(t, nil)
	b.Spawn = func([]string, ...string) (BoxProcess, error) { return stuck, nil }
	b.StopWait = 50 * time.Millisecond
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := b.Stop(context.Background()); err == nil || !strings.Contains(err.Error(), "killed") || !stuck.killed {
		t.Fatalf("stopping a stuck core: %v (killed %v)", err, stuck.killed)
	}
	if b.Running() {
		t.Error("the killed core still runs after Stop")
	}
}

// Once Stop returns, the core is gone: Running says so, and a Start
// right after it starts the core again (a move's source after a failed
// stop, #311).
func TestBoxCoreStartsRightAfterAStop(t *testing.T) {
	sp := &spawner{}
	b, _ := testBoxCore(t, sp)
	ctx := context.Background()
	for i := 1; i <= 100; i++ {
		if err := b.Start(ctx); err != nil {
			t.Fatal(err)
		}
		if n := sp.count(); n != i || !b.Running() {
			t.Fatalf("start %d: %d cores started, running %v", i, n, b.Running())
		}
		if err := b.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		if b.Running() {
			t.Fatalf("stop %d: the core runs after Stop", i)
		}
	}
}

// A core that ended by itself waits to start again. A Stop drops that
// start, and so does the agent's stop: nothing of the box starts or logs
// once Supervise returned (#311).
func TestBoxCoreStopDropsAPendingStart(t *testing.T) {
	sp := &spawner{}
	b, _ := testBoxCore(t, sp)
	var mu sync.Mutex
	var logs []string
	b.Logf = func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		mu.Lock()
		logs = append(logs, line)
		mu.Unlock()
		t.Log(line)
	}
	// pending reports whether the n-th start of a core that ended by
	// itself is pending.
	pending := func(n int) func() bool {
		return func() bool {
			mu.Lock()
			defer mu.Unlock()
			seen := 0
			for _, l := range logs {
				if strings.Contains(l, "starting it again") {
					seen++
				}
			}
			return seen == n
		}
	}
	// A core that ran less than a minute starts again after 6×20ms.
	b.RestartWait = 20 * time.Millisecond
	past := 300 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Supervise(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitFor(t, "the supervisor", func() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.ctx != nil })

	// A move stands the box down while the start is pending.
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	sp.last().die()
	waitFor(t, "the start to be pending", pending(1))
	if err := b.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(past)
	if n := sp.count(); n != 1 || b.Running() {
		t.Fatalf("a stood-down box started the core: %d starts, running %v", n, b.Running())
	}

	// The agent stops (docker stop) while the start is pending.
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	sp.last().die()
	waitFor(t, "the start to be pending", pending(2))
	cancel()
	<-done
	mu.Lock()
	n := len(logs)
	mu.Unlock()
	time.Sleep(past)
	if c := sp.count(); c != 2 || b.Running() {
		t.Errorf("the core started after the agent stopped: %d starts, running %v", c, b.Running())
	}
	mu.Lock()
	late := slices.Clone(logs[n:])
	mu.Unlock()
	if len(late) > 0 {
		t.Errorf("the box logged after the agent stopped: %q", late)
	}
	if r, _ := LoadCoreRole(b.StateDir); r != RoleCore {
		t.Errorf("a container stop stood the box down: %s", r)
	}
}

type stuckProc struct {
	done   chan struct{}
	killed bool
}

func (p *stuckProc) Signal(os.Signal) error { return nil }
func (p *stuckProc) Kill() error            { p.killed = true; close(p.done); return nil }
func (p *stuckProc) Wait() error            { <-p.done; return nil }

// Every step runs a fixed `fridolin core` command on the box's data
// volume with the store's secrets, and the rehearsal only ever on a
// scratch dir in the node's volume, which it removes.
func TestBoxCoreCommands(t *testing.T) {
	b, st := testBoxCore(t, &spawner{})
	if err := st.Set(hostsecrets.ExportKey, "test-export-value"); err != nil {
		t.Fatal(err)
	}
	var calls []string
	var homes []string
	b.Exec = func(_ context.Context, env []string, stdin []byte, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		for _, kv := range env {
			if h, ok := strings.CutPrefix(kv, "FRIDOLIN_HOME="); ok {
				homes = append(homes, h)
			}
		}
		if !slices.Contains(env, "FRIDOLIN_EXPORT_KEY=test-export-value") {
			return nil, errors.New("no export key")
		}
		switch args[1] {
		case "status":
			return []byte(`{"device":"abc"}`), nil
		case "export":
			return []byte(`{"key":"core/exports/1.tar.gz.age","device":"abc","move":"` + args[3] + `"}`), nil
		case "files":
			if args[2] == "import" && !strings.Contains(string(stdin), `"dir":"~/.credentials"`) {
				return nil, errors.New("no files on stdin")
			}
			return []byte(`{"stored":["a.json"],"kept":[]}`), nil
		case "forget-data":
			return []byte(`{"removed":["bridge.db"]}`), nil
		case "import":
			if slices.Contains(args, "--rehearsal") {
				if err := os.MkdirAll(filepath.Join(b.StateDir, coreRehearsalDir, "core"), 0o700); err != nil {
					return nil, err
				}
			}
		}
		return nil, nil
	}
	ctx := context.Background()
	if s, err := b.Status(ctx); err != nil || s.Device != "abc" {
		t.Fatalf("status: %+v, %v", s, err)
	}
	if i, err := b.Export(ctx, "m-20261024-1200-0001"); err != nil || i.Move != "m-20261024-1200-0001" {
		t.Fatalf("export: %+v, %v", i, err)
	}
	if _, err := b.Export(ctx, "../x"); err == nil {
		t.Error("an export with a bad move id ran")
	}
	for _, f := range []func(context.Context) error{b.Import, b.Promote, b.Release} {
		if err := f(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if stored, _, err := b.ImportFiles(ctx, "~/.credentials", map[string][]byte{"a.json": []byte("{}")}); err != nil || len(stored) != 1 {
		t.Fatalf("files import: %q, %v", stored, err)
	}
	res, err := b.Rehearse(ctx, "m-20261024-1200-0001")
	if err != nil || !res.OK {
		t.Fatalf("rehearsal: %+v, %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(b.StateDir, coreRehearsalDir)); !os.IsNotExist(err) {
		t.Error("the rehearsal's scratch dir stayed")
	}
	want := []string{"core status --json", "core export --move m-20261024-1200-0001 --json", "core import --latest --force", "core promote", "core release", "core files import",
		"core import --latest --rehearsal --data-dir " + filepath.Join(b.StateDir, coreRehearsalDir) + " --config " + filepath.Join(b.StateDir, coreRehearsalDir, "config.json")}
	if !slices.Equal(calls, want) {
		t.Errorf("the commands:\n%q\nwant\n%q", calls, want)
	}
	for i, h := range homes {
		if want := b.DataDir; i == len(homes)-1 {
			if h != filepath.Join(b.StateDir, coreRehearsalDir) {
				t.Errorf("the rehearsal ran on %s", h)
			}
		} else if h != want {
			t.Errorf("%s ran on %s", calls[i], h)
		}
	}

	// What a move brought, forgotten through the same commands (#266).
	var _ CoreForgetter = b
	if _, err := b.ForgetFiles(ctx, "~/.credentials", []string{"a.json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ForgetData(ctx); err != nil {
		t.Fatal(err)
	}
	if got := calls[len(calls)-2:]; !slices.Equal(got, []string{"core files forget", "core forget-data --json"}) {
		t.Errorf("forgetting ran %q", got)
	}

	// No import, and no forgetting the data, while the core runs in the box.
	if err := b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Import(ctx); err == nil {
		t.Error("an import ran under a running core")
	}
	if _, err := b.ForgetData(ctx); err == nil {
		t.Error("the data dir was forgotten under a running core")
	}
	_ = b.Stop(ctx)
}

// The published ports reach the core on the loopback from anywhere but
// FrIDE's address on the box network.
func TestBoxCorePortsRefuseFride(t *testing.T) {
	addrs := boxAddrs()
	if len(addrs) == 0 {
		t.Skip("no address but the loopback here")
	}
	own := addrs[0]
	// The core: on the loopback, at a port the box publishes.
	core, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	go func() {
		for {
			c, err := core.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("core\n"))
			c.Close()
		}
	}()
	port := core.Addr().(*net.TCPAddr).Port
	try := func(fride string) string {
		t.Helper()
		b, _ := testBoxCore(t, &spawner{})
		b.Ports, b.Fride = []int{port}, fride
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		b.publish(ctx, &wg)
		defer func() { cancel(); wg.Wait() }()
		c, err := net.DialTimeout("tcp", net.JoinHostPort(own, strconv.Itoa(port)), 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		got, _ := io.ReadAll(c)
		return string(got)
	}
	// Another machine's FrIDE: this connection is forwarded.
	if got := try("192.0.2.1:17840"); got != "core\n" {
		t.Errorf("a connection from the host got %q", got)
	}
	// FrIDE is this address: refused.
	if got := try(net.JoinHostPort(own, "17840")); got != "" {
		t.Errorf("FrIDE reached the core: %q", got)
	}
}

// A files import that failed halfway still answers with what it wrote, so
// the move records it (#452).
func TestBoxCoreImportFilesPartial(t *testing.T) {
	b, st := testBoxCore(t, &spawner{})
	if err := st.Set(hostsecrets.ExportKey, "test-export-value"); err != nil {
		t.Fatal(err)
	}
	b.Exec = func(context.Context, []string, []byte, ...string) ([]byte, error) {
		return []byte(`{"stored":["a.json"],"kept":[]}`), errors.New("exit status 1")
	}
	stored, _, err := b.ImportFiles(context.Background(), "~/.credentials", map[string][]byte{"a.json": []byte("{}")})
	if err == nil || !slices.Equal(stored, []string{"a.json"}) {
		t.Fatalf("stored %q, err %v", stored, err)
	}
}
