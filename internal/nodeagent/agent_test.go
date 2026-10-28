package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// TestMain lets the test binary stand in for an allowlisted program:
// "helper-echo a b" prints "a b", "helper-sleep" sleeps.
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_AGENT") != "" {
		os.Exit(fakeAgent())
	}
	if dir := os.Getenv("FAKE_DOCKER_DIR"); dir != "" && len(os.Args) > 1 && (os.Args[1] == "build" || os.Args[1] == "save" || os.Args[1] == "image") {
		os.Exit(fakeDocker(dir))
	}
	// sbx runs the test binary as fridolin.exe to read a token on
	// Windows (the live test).
	if len(os.Args) == 6 && slices.Equal(os.Args[1:5], sbxSecretArgs) {
		if err := PrintSbxSecret(os.Stdout, os.Args[5]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if dir := os.Getenv("FAKE_SBX_DIR"); dir != "" {
		if code, ok := fakeFrideSbx(dir, os.Args[1:]); ok {
			os.Exit(code)
		}
		os.Exit(fakeSbx(dir))
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "helper-echo":
			fmt.Print(strings.Join(os.Args[2:], " "))
			os.Exit(0)
		case "helper-fail":
			fmt.Fprint(os.Stderr, "failing")
			os.Exit(3)
		case "helper-sleep":
			time.Sleep(time.Minute)
			os.Exit(0)
		case "helper-loud":
			fmt.Print(strings.Repeat("x", 4096))
			os.Exit(0)
		}
	}
	// The machine's JDK and Android SDK stay out of the tests' nodes,
	// unless a test gives one a toolchain (android_test.go).
	findAndroidToolchain = func() (platform.AndroidToolchain, error) {
		return platform.AndroidToolchain{}, errors.New("no toolchain in the tests")
	}
	os.Exit(m.Run())
}

func testExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// core is a nodes.Service on a loopback port.
type core struct {
	svc  *nodes.Service
	addr string
}

func startCore(t *testing.T, dir string) *core {
	t.Helper()
	svc, err := nodes.New(nodes.Options{Dir: dir, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Serve(ln); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	return &core{svc: svc, addr: svc.Addr()}
}

func (c *core) code(t *testing.T) string {
	t.Helper()
	code, _, err := c.svc.NewJoinCode()
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// running is an agent's Run in the background.
type running struct {
	agent  *Agent
	cancel context.CancelFunc
	done   chan error
}

func runAgent(t *testing.T, a *Agent) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{agent: a, cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(10 * time.Second):
			t.Error("the agent did not stop")
		}
	})
	return r
}

func joinNode(t *testing.T, c *core, caps nodeproto.Capabilities) *Agent {
	t.Helper()
	dir := t.TempDir()
	j, err := Join(context.Background(), JoinOptions{Dir: dir, Core: c.addr, Code: c.code(t), Name: "test node", Capabilities: caps, Version: "test", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if j.State.Device != nodeproto.DevicePending {
		t.Fatalf("a node that just joined is %s, want pending", j.State.Device)
	}
	a, err := Load(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID() != j.ID {
		t.Fatalf("the loaded key is %s, joined as %s", a.ID(), j.ID)
	}
	a.Logf, a.MinBackoff, a.MaxBackoff, a.HeartbeatEvery = t.Logf, 10*time.Millisecond, 100*time.Millisecond, 50*time.Millisecond
	return a
}

func TestJoinApproveServeRevoke(t *testing.T) {
	c := startCore(t, t.TempDir())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello from the node"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	exe := testExe(t)
	caps := nodeproto.Capabilities{
		Exec:   &nodeproto.ExecCap{Allow: []nodeproto.ExecRule{{Name: exe, Args: []string{"helper-echo", "helper-fail", "helper-sleep", "helper-loud", "[a-z]+"}}}, TimeoutSeconds: 2, MaxOutputBytes: 100},
		FS:     &nodeproto.FSCap{Roots: []nodeproto.FSRoot{{Path: root, Write: true}}},
		Host:   true,
		Notify: true,
	}
	a := joinNode(t, c, caps)
	var notified []string
	var mu sync.Mutex
	a.Notifier = func(_ context.Context, title, msg string) (nodeproto.NotifyResult, error) {
		mu.Lock()
		defer mu.Unlock()
		notified = append(notified, title+": "+msg)
		return nodeproto.NotifyResult{Shown: true, Method: "test"}, nil
	}
	r := runAgent(t, a)
	waitFor(t, "the agent to connect", func() bool { return c.svc.Online(a.ID()) })
	ctx := context.Background()

	// Pending: nothing runs.
	if _, err := c.svc.HostInfo(ctx, a.ID()); !isCode(err, nodeproto.CodeDenied) {
		t.Fatalf("host.info on a pending device: %v", err)
	}
	if _, err := c.svc.ApproveCommands(a.ID()); !errors.Is(err, nodes.ErrNotApproved) {
		t.Fatalf("approving commands before the device: %v", err)
	}
	if _, err := c.svc.ApproveDevice(a.ID()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the device approval to reach the agent", func() bool { return a.State().Device == nodeproto.DeviceApproved })
	if st := a.State(); st.Commands != nodeproto.CommandsPending || !st.Approved.Empty() {
		t.Fatalf("an approved device whose commands are not: %+v", st)
	}
	if _, err := c.svc.HostInfo(ctx, a.ID()); !isCode(err, nodeproto.CodeDenied) {
		t.Fatalf("host.info before the commands are approved: %v", err)
	}
	if _, err := c.svc.ApproveCommands(a.ID()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the commands approval to reach the agent", func() bool { return a.State().Commands == nodeproto.CommandsApproved })

	res, err := c.svc.Exec(ctx, a.ID(), nodeproto.ExecParams{Argv: []string{exe, "helper-echo", "hi", "there"}})
	if err != nil || res.ExitCode != 0 || res.Stdout != "hi there" {
		t.Fatalf("exec: %+v, %v", res, err)
	}
	res, err = c.svc.Exec(ctx, a.ID(), nodeproto.ExecParams{Argv: []string{exe, "helper-fail"}})
	if err != nil || res.ExitCode != 3 || res.Stderr != "failing" {
		t.Fatalf("exec of a failing command: %+v, %v", res, err)
	}
	res, err = c.svc.Exec(ctx, a.ID(), nodeproto.ExecParams{Argv: []string{exe, "helper-loud"}})
	if err != nil || len(res.Stdout) != 100 || !res.Truncated {
		t.Fatalf("exec past the output cap: %d bytes, truncated %v, %v", len(res.Stdout), res.Truncated, err)
	}
	res, err = c.svc.Exec(ctx, a.ID(), nodeproto.ExecParams{Argv: []string{exe, "helper-sleep"}, TimeoutSeconds: 1})
	if err != nil || !res.TimedOut {
		t.Fatalf("exec past its timeout: %+v, %v", res, err)
	}
	if _, err := c.svc.Exec(ctx, a.ID(), nodeproto.ExecParams{Argv: []string{exe, "HELPER", "--evil"}}); !isCode(err, nodeproto.CodeDenied) {
		t.Fatalf("exec with arguments the rule does not allow: %v", err)
	}
	if _, err := c.svc.Exec(ctx, a.ID(), nodeproto.ExecParams{Argv: []string{"hostname"}}); !isCode(err, nodeproto.CodeDenied) {
		t.Fatalf("exec of a program no rule names: %v", err)
	}

	info, err := c.svc.HostInfo(ctx, a.ID())
	if err != nil || info.OS != runtime.GOOS || info.CPUs < 1 {
		t.Fatalf("host.info: %+v, %v", info, err)
	}
	if n, err := c.svc.Notify(ctx, a.ID(), nodeproto.NotifyParams{Title: "T", Message: "hello"}); err != nil || !n.Shown {
		t.Fatalf("notify: %+v, %v", n, err)
	}
	mu.Lock()
	if len(notified) != 1 || notified[0] != "T: hello" {
		t.Errorf("notified %q", notified)
	}
	mu.Unlock()

	list, err := c.svc.List(ctx, a.ID(), root)
	if err != nil || len(list.Entries) != 1 || list.Entries[0].Name != "hello.txt" {
		t.Fatalf("fs.list: %+v, %v", list, err)
	}
	rd, err := c.svc.ReadFile(ctx, a.ID(), nodeproto.FSReadParams{Path: filepath.Join(root, "hello.txt")})
	if err != nil || string(rd.Data) != "hello from the node" {
		t.Fatalf("fs.read: %+v, %v", rd, err)
	}
	if _, err := c.svc.WriteFile(ctx, a.ID(), nodeproto.FSWriteParams{Path: filepath.Join(root, "sub", "new.txt"), Data: []byte("written"), Mkdirs: true}); err != nil {
		t.Fatalf("fs.write: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "sub", "new.txt")); err != nil || string(data) != "written" {
		t.Fatalf("what fs.write wrote: %q, %v", data, err)
	}
	for _, p := range []string{filepath.Join(outside, "secret.txt"), filepath.Join(root, "..", filepath.Base(outside), "secret.txt")} {
		if _, err := c.svc.ReadFile(ctx, a.ID(), nodeproto.FSReadParams{Path: p}); !isCode(err, nodeproto.CodeOutsideRoot) {
			t.Errorf("fs.read of %s: %v", p, err)
		}
	}
	if _, err := c.svc.ReadFile(ctx, a.ID(), nodeproto.FSReadParams{Path: "hello.txt"}); !isCode(err, nodeproto.CodeBadRequest) {
		t.Errorf("fs.read of a relative path: %v", err)
	}
	// A symlink inside the root that leads out of it.
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err == nil {
		if _, err := c.svc.ReadFile(ctx, a.ID(), nodeproto.FSReadParams{Path: filepath.Join(root, "escape", "secret.txt")}); !isCode(err, nodeproto.CodeOutsideRoot) {
			t.Errorf("fs.read through a symlink out of the root: %v", err)
		}
		if _, err := c.svc.WriteFile(ctx, a.ID(), nodeproto.FSWriteParams{Path: filepath.Join(root, "escape", "x.txt"), Data: []byte("x")}); !isCode(err, nodeproto.CodeOutsideRoot) {
			t.Errorf("fs.write through a symlink out of the root: %v", err)
		}
	} else {
		t.Logf("no symlink here (%v); the escape through one is not tried", err)
	}

	// Revoked: disconnected at once, and refused from then on.
	if _, err := c.svc.Revoke(a.ID()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-r.done:
		ce, ok := errors.AsType[*CloseError](err)
		if !ok || ce.Reason != nodeproto.CloseRevoked {
			t.Fatalf("the agent stopped with %v, want revoked", err)
		}
		r.done <- err
	case <-time.After(10 * time.Second):
		t.Fatal("the revoked agent is still running")
	}
	if c.svc.Online(a.ID()) {
		t.Error("the revoked node is online")
	}
	err = a.session(context.Background())
	if ce, ok := errors.AsType[*CloseError](err); !ok || ce.Reason != nodeproto.CloseRevoked {
		t.Errorf("reconnecting after the revocation: %v", err)
	}
	if _, err := c.svc.HostInfo(ctx, a.ID()); !isCode(err, nodeproto.CodeDenied) {
		t.Errorf("a call on a revoked node: %v", err)
	}
}

func isCode(err error, code string) bool {
	pe, ok := errors.AsType[*nodeproto.Error](err)
	return ok && pe.Code == code
}

func TestDeclarationThatGrowsWaitsForApproval(t *testing.T) {
	c := startCore(t, t.TempDir())
	exe := testExe(t)
	echo := nodeproto.ExecRule{Name: exe, Args: []string{"helper-echo", "[a-z]+"}}
	a := joinNode(t, c, nodeproto.Capabilities{Exec: &nodeproto.ExecCap{Allow: []nodeproto.ExecRule{echo}}})
	r := runAgent(t, a)
	waitFor(t, "the agent to connect", func() bool { return c.svc.Online(a.ID()) })
	if _, err := c.svc.ApproveDevice(a.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.svc.ApproveCommands(a.ID()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "approved commands", func() bool { return a.State().Commands == nodeproto.CommandsApproved })
	r.cancel()
	<-r.done
	r.done <- nil

	// The owner adds host and a second rule to node.json.
	b, err := Load(a.Dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	b.Logf, b.HeartbeatEvery = t.Logf, 50*time.Millisecond
	b.Config.Capabilities.Host = true
	b.Config.Capabilities.Exec.Allow = append(b.Config.Capabilities.Exec.Allow, nodeproto.ExecRule{Name: exe, Args: []string{"helper-fail"}})
	runAgent(t, b)
	waitFor(t, "the grown declaration", func() bool { return b.Connected() && b.State().Commands == nodeproto.CommandsPending })
	ctx := context.Background()
	if res, err := c.svc.Exec(ctx, a.ID(), nodeproto.ExecParams{Argv: []string{exe, "helper-echo", "still"}}); err != nil || res.Stdout != "still" {
		t.Fatalf("what was approved before: %+v, %v", res, err)
	}
	if _, err := c.svc.Exec(ctx, a.ID(), nodeproto.ExecParams{Argv: []string{exe, "helper-fail"}}); !isCode(err, nodeproto.CodeDenied) {
		t.Fatalf("the new rule before approval: %v", err)
	}
	if _, err := c.svc.HostInfo(ctx, a.ID()); !isCode(err, nodeproto.CodeDenied) {
		t.Fatalf("host before approval: %v", err)
	}
	if _, err := c.svc.ApproveCommands(a.ID()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the new approval", func() bool { return b.State().Commands == nodeproto.CommandsApproved })
	if res, err := c.svc.Exec(ctx, a.ID(), nodeproto.ExecParams{Argv: []string{exe, "helper-fail"}}); err != nil || res.ExitCode != 3 {
		t.Fatalf("the new rule once approved: %+v, %v", res, err)
	}
}

func TestTheNodeRefusesWhatItDoesNotDeclare(t *testing.T) {
	// A core that approves more than the node declares (or a forged
	// state) does not widen what the node serves.
	root := t.TempDir()
	a := &Agent{Dir: t.TempDir(), Config: Config{Core: "x", Name: "n", Capabilities: nodeproto.Capabilities{
		Exec: &nodeproto.ExecCap{Allow: []nodeproto.ExecRule{{Name: "hostname", Args: []string{}}}},
		FS:   &nodeproto.FSCap{Roots: []nodeproto.FSRoot{{Path: root}}},
	}}, Logf: t.Logf}
	a.setState(nodeproto.State{Device: nodeproto.DeviceApproved, Commands: nodeproto.CommandsApproved, Approved: nodeproto.Capabilities{
		Exec: &nodeproto.ExecCap{Allow: []nodeproto.ExecRule{{Name: "hostname", Args: []string{}}, {Name: "rm"}}},
		FS:   &nodeproto.FSCap{Roots: []nodeproto.FSRoot{{Path: root, Write: true}, {Path: filepath.Dir(root)}}},
		Host: true,
	}})
	call := func(method, params string) *nodeproto.Error {
		return a.handle(context.Background(), nodeproto.Request{Method: method, Params: []byte(params)}).Error
	}
	for _, c := range []struct{ method, params, code string }{
		{nodeproto.MethodExec, `{"argv":["rm","-rf","x"]}`, nodeproto.CodeDenied},
		{nodeproto.MethodExec, `{"argv":["sh","-c","id"]}`, nodeproto.CodeDenied},
		{nodeproto.MethodExec, `{"argv":["hostname","-s"]}`, nodeproto.CodeDenied},
		{nodeproto.MethodHostInfo, `{}`, nodeproto.CodeDenied},
		{nodeproto.MethodFSWrite, `{"path":` + quote(filepath.Join(root, "x")) + `,"data":"eA=="}`, nodeproto.CodeDenied},
		{nodeproto.MethodFSList, `{"path":` + quote(filepath.Dir(root)) + `}`, nodeproto.CodeOutsideRoot},
		{"shell", `{}`, nodeproto.CodeUnsupported},
		{nodeproto.MethodFSList, `{"path":3}`, nodeproto.CodeBadRequest},
	} {
		if e := call(c.method, c.params); e == nil || e.Code != c.code {
			t.Errorf("%s %s: %v, want %s", c.method, c.params, e, c.code)
		}
	}
	a.setState(nodeproto.State{Device: nodeproto.DevicePending})
	if e := call(nodeproto.MethodFSList, `{"path":`+quote(root)+`}`); e == nil || e.Code != nodeproto.CodeDenied {
		t.Errorf("a pending device lists: %v", e)
	}
}

func quote(s string) string { return fmt.Sprintf("%q", s) }

func TestUnknownDeviceAndBadCode(t *testing.T) {
	c := startCore(t, t.TempDir())
	dir := t.TempDir()
	_, err := Join(context.Background(), JoinOptions{Dir: dir, Core: c.addr, Code: "fj-wrong", Name: "n", Capabilities: DefaultCapabilities(runtime.GOOS)})
	if ce, ok := errors.AsType[*CloseError](err); !ok || ce.Reason != nodeproto.CloseUnknownDevice {
		t.Fatalf("joining with a wrong code: %v", err)
	}
	// The code works once.
	code := c.code(t)
	if _, err := Join(context.Background(), JoinOptions{Dir: dir, Core: c.addr, Code: code, Name: "n", Capabilities: DefaultCapabilities(runtime.GOOS)}); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	_, err = Join(context.Background(), JoinOptions{Dir: other, Core: c.addr, Code: code, Name: "m", Capabilities: DefaultCapabilities(runtime.GOOS)})
	if ce, ok := errors.AsType[*CloseError](err); !ok || ce.Reason != nodeproto.CloseUnknownDevice {
		t.Fatalf("a used code: %v", err)
	}
	// An agent the core never saw stops instead of retrying.
	a, err := Load(other, "test")
	if err != nil {
		t.Fatal(err)
	}
	a.Logf = t.Logf
	if err := a.Run(context.Background()); err == nil {
		t.Fatal("an unknown device keeps running")
	} else if ce, ok := errors.AsType[*CloseError](err); !ok || !ce.Final() {
		t.Fatalf("an unknown device: %v", err)
	}
}

func TestReconnectsWithBackoff(t *testing.T) {
	dir := t.TempDir()
	c := startCore(t, dir)
	a := joinNode(t, c, DefaultCapabilities(runtime.GOOS))
	// The core goes away, and comes back on another port with the same
	// registry: the agent keeps trying until it is back.
	c.svc.Stop()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	a.Config.Core = addr
	runAgent(t, a)
	time.Sleep(150 * time.Millisecond)
	if a.Connected() {
		t.Fatal("connected to nothing")
	}
	svc, err := nodes.New(nodes.Options{Dir: dir, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	if err := svc.ListenAndServe(addr, nil); err != nil {
		t.Skipf("the port went to someone else: %v", err)
	}
	waitFor(t, "the agent to reconnect", func() bool { return svc.Online(a.ID()) && a.Connected() })
	views, err := svc.Views()
	if err != nil || len(views) != 1 || views[0].LastSeen == nil {
		t.Fatalf("views: %+v, %v", views, err)
	}
	waitFor(t, "a heartbeat", func() bool {
		n, err := svc.Registry().Get(a.ID())
		return err == nil && n.Resources != nil && n.Resources.CPUs > 0
	})
}

func TestConfigCheck(t *testing.T) {
	ok := Config{Core: "core:8766", Name: "n", Capabilities: DefaultCapabilities(runtime.GOOS)}
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]Config{
		"no core":       {Name: "n"},
		"no name":       {Core: "c:1"},
		"a shell":       {Core: "c:1", Name: "n", Capabilities: nodeproto.Capabilities{Exec: &nodeproto.ExecCap{Allow: []nodeproto.ExecRule{{Name: "pwsh"}}}}},
		"relative root": {Core: "c:1", Name: "n", Capabilities: nodeproto.Capabilities{FS: &nodeproto.FSCap{Roots: []nodeproto.FSRoot{{Path: "docs"}}}}},
	} {
		if err := c.Check(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	dir := t.TempDir()
	if _, err := LoadConfig(dir); err == nil {
		t.Error("a missing node.json loads")
	}
	if err := SaveConfig(dir, ok); err != nil {
		t.Fatal(err)
	}
	if back, err := LoadConfig(dir); err != nil || back.Core != ok.Core {
		t.Fatalf("%+v, %v", back, err)
	}
	if _, err := LoadKey(dir); err == nil {
		t.Error("a missing key loads")
	}
	k, err := NewKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if back, err := LoadKey(dir); err != nil || !back.Equal(k) {
		t.Fatalf("the key read back differs: %v", err)
	}
}

func TestCoreURL(t *testing.T) {
	for in, want := range map[string]string{
		"fridolin-core:8766":       "ws://fridolin-core:8766/v1/node",
		" 127.0.0.1:1 ":            "ws://127.0.0.1:1/v1/node",
		"wss://core.example:443":   "wss://core.example:443/v1/node",
		"ws://core:1/custom/path":  "ws://core:1/custom/path",
		"ws://[::1]:8766":          "ws://[::1]:8766/v1/node",
		"fridolin-core.tail:8766":  "ws://fridolin-core.tail:8766/v1/node",
		"wss://core.example:8766/": "wss://core.example:8766/",
	} {
		if got := coreURL(in); got != want {
			t.Errorf("coreURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCappedBuffer(t *testing.T) {
	b := &cappedBuffer{max: 5}
	for _, s := range []string{"ab", "cd", "ef", "gh"} {
		if n, err := b.Write([]byte(s)); n != len(s) || err != nil {
			t.Fatal(n, err)
		}
	}
	if b.buf.String() != "abcde" || !b.truncated {
		t.Errorf("%q truncated %v", b.buf.String(), b.truncated)
	}
}

func TestServiceConfig(t *testing.T) {
	dir := filepath.FromSlash("/state/fridolin-node")
	for _, goos := range []string{"darwin", "linux", "windows"} {
		c := ServiceConfig(goos, "/opt/fridolin", dir)
		if got := strings.Join(c.Arguments, " "); got != "node run --state-dir "+dir {
			t.Errorf("%s: arguments %q", goos, got)
		}
		if c.Executable != "/opt/fridolin" {
			t.Errorf("%s: executable %q", goos, c.Executable)
		}
		switch goos {
		case "darwin":
			if c.Name != "ai.fridolin.node" || c.Name == "ai.fridolin.bridge" {
				t.Errorf("darwin label %q", c.Name)
			}
			if c.Option["UserService"] != true || c.Option["KeepAlive"] != true || c.Option["LogDirectory"] != dir {
				t.Errorf("darwin options %v", c.Option)
			}
		case "linux":
			if c.Name != "fridolin-node" || c.Option["UserService"] != true || c.Option["Restart"] != "always" {
				t.Errorf("linux %q %v", c.Name, c.Option)
			}
			// A user manager never reaches multi-user.target (issue #171).
			script, _ := c.Option["SystemdScript"].(string)
			if !strings.Contains(script, "\nWantedBy=default.target\n") || strings.Contains(script, "multi-user") {
				t.Errorf("linux systemd template:\n%s", script)
			}
		case "windows":
			if c.Name != "fridolin-node" || c.Option["StartType"] != "automatic" || c.Option["OnFailure"] != "restart" || c.Option["UserService"] != nil {
				t.Errorf("windows %q %v", c.Name, c.Option)
			}
		}
	}
}

func TestRepairBootTarget(t *testing.T) {
	home := t.TempDir()
	var calls []string
	systemctl := func(args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}
	// No unit: nothing to do.
	if moved, err := RepairBootTarget(home, systemctl); moved || err != nil || calls != nil {
		t.Fatalf("no unit: %v %v %q", moved, err, calls)
	}
	unit := LinuxUnitPath(home)
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		t.Fatal(err)
	}
	old := "[Service]\nExecStart=/opt/fridolin node run\nRestart=always\n\n[Install]\nWantedBy=multi-user.target\n"
	if err := os.WriteFile(unit, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	moved, err := RepairBootTarget(home, systemctl)
	if !moved || err != nil {
		t.Fatalf("old unit: %v %v", moved, err)
	}
	want := []string{"--user disable fridolin-node.service", "--user daemon-reload", "--user enable fridolin-node.service"}
	if !slices.Equal(calls, want) {
		t.Errorf("systemctl %q, want %q", calls, want)
	}
	data, _ := os.ReadFile(unit)
	if got := string(data); got != strings.Replace(old, "multi-user.target", "default.target", 1) {
		t.Errorf("unit after the repair:\n%s", got)
	}
	// Once moved, it stays as it is.
	calls = nil
	if moved, err := RepairBootTarget(home, systemctl); moved || err != nil || calls != nil {
		t.Errorf("second run: %v %v %q", moved, err, calls)
	}
	// A failing disable leaves the unit as it was.
	if err := os.WriteFile(unit, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RepairBootTarget(home, func(...string) error { return errors.New("no user bus") }); err == nil || !strings.Contains(err.Error(), "no user bus") {
		t.Errorf("failing systemctl: %v", err)
	}
	if data, _ := os.ReadFile(unit); string(data) != old {
		t.Errorf("unit changed though disable failed:\n%s", data)
	}
}
