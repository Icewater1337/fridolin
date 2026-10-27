package nodeagent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// testClaudeToken is the Claude token in the tests' host store.
const testClaudeToken = "sk-test-claude-token-0000"

// syncLog is what the agent logs, for the tests to search.
type syncLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *syncLog) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(&l.b, format+"\n", args...)
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// sandboxAgent is an agent whose sbx is the fake (fakeSbx) in its own
// folder, with the Claude token in its host store and a template set
// up. It never runs the machine's sbx.
func sandboxAgent(t *testing.T) (*Agent, string, *syncLog) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("sandbox is offered on macOS and Linux")
	}
	fake := t.TempDir()
	st := newMemSecrets()
	if err := st.Set(hostsecrets.ClaudeOAuthToken, testClaudeToken); err != nil {
		t.Fatal(err)
	}
	log := &syncLog{}
	a := &Agent{Dir: t.TempDir(), Config: Config{Core: "x", Name: "mac"}, CoreSecrets: st, Logf: log.logf,
		SandboxDriver: sbxCLI{path: testExe(t), env: []string{"FAKE_SBX_DIR=" + fake}}}
	if err := a.saveSandboxState(sandboxState{Template: "fridolin-brain:v1"}); err != nil {
		t.Fatal(err)
	}
	return a, fake, log
}

// fakeCalls are the sbx calls the fake recorded.
type fakeCall struct {
	Args         []string `json:"args"`
	TokenEnv     bool     `json:"token_env"`
	TelemetryOff bool     `json:"telemetry_off"`
}

func fakeCalls(t *testing.T, fake string) []fakeCall {
	t.Helper()
	f, err := os.Open(filepath.Join(fake, "calls.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []fakeCall
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 4<<20)
	for sc.Scan() {
		var c fakeCall
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

// called reports whether a call began with prefix.
func called(calls []fakeCall, prefix ...string) bool {
	return slices.ContainsFunc(calls, func(c fakeCall) bool { return len(c.Args) >= len(prefix) && slices.Equal(c.Args[:len(prefix)], prefix) })
}

func sandboxInfo(t *testing.T, a *Agent) nodeproto.SandboxInfo {
	t.Helper()
	res, perr := a.sandboxCall(context.Background(), nodeproto.MethodSandboxInfo, nil)
	if perr != nil {
		t.Fatal(perr)
	}
	return res.(nodeproto.SandboxInfo)
}

func TestSandboxInfoAndDeclaration(t *testing.T) {
	t.Parallel()
	a, fake, _ := sandboxAgent(t)
	info := sandboxInfo(t, a)
	if info.Version != "v0.50.0" || !info.Daemon || !info.SignedIn || !info.Settings || info.Template != "fridolin-brain:v1" || info.Problem != "" {
		t.Fatalf("info: %+v", info)
	}
	if !slices.Equal(info.Providers, []string{"claude"}) {
		t.Errorf("providers %v, want claude only", info.Providers)
	}
	if info.BinPath != "/opt/fridolin/bin/fridolin" || info.ToolsPath != "/opt/fridolin/tools" || info.Sandboxes == nil {
		t.Errorf("paths %q %q, sandboxes %v", info.BinPath, info.ToolsPath, info.Sandboxes)
	}
	for _, c := range fakeCalls(t, fake) {
		if !c.TelemetryOff || c.TokenEnv {
			t.Errorf("%v: telemetry off %v, token in env %v", c.Args, c.TelemetryOff, c.TokenEnv)
		}
	}
	// Codex's login in sbx: both providers.
	if err := os.WriteFile(filepath.Join(fake, "codex"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if info := sandboxInfo(t, a); !slices.Equal(info.Providers, []string{"claude", "codex"}) {
		t.Errorf("providers %v with the openai login", info.Providers)
	}
	if !a.sandboxCheck(context.Background(), true) || a.declared().Sandbox == nil || !a.declared().Sandbox.Ready("codex") {
		t.Fatalf("declared: %+v", a.declared().Sandbox)
	}
	// Turned off here: not declared.
	if err := SetSandboxOff(a.Dir, true); err != nil {
		t.Fatal(err)
	}
	if !a.sandboxCheck(context.Background(), true) || a.declared().Sandbox != nil {
		t.Errorf("declared while off: %+v", a.declared().Sandbox)
	}
	if err := SetSandboxOff(a.Dir, false); err != nil {
		t.Fatal(err)
	}
	if !a.sandboxCheck(context.Background(), true) || a.declared().Sandbox == nil {
		t.Error("not declared once on again")
	}
	// No sbx, as for every agent nobody told where sbx is: nothing
	// declared, and nothing ran.
	b := &Agent{Dir: t.TempDir(), Logf: func(string, ...any) {}}
	if b.sandboxCheck(context.Background(), true) || b.declared().Sandbox != nil {
		t.Error("declared without sbx")
	}
}

// brainFolder is a brain's folder holding the core's runtime marker.
func brainFolder(t *testing.T, marker string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if marker != "" {
		if err := os.WriteFile(filepath.Join(dir, nodeproto.SandboxMarkerFile), []byte(marker+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func ensure(a *Agent, p nodeproto.SandboxEnsureParams) (nodeproto.SandboxEnsureResult, *nodeproto.Error) {
	raw, _ := json.Marshal(p)
	res, perr := a.sandboxCall(context.Background(), nodeproto.MethodSandboxEnsure, raw)
	if perr != nil {
		return nodeproto.SandboxEnsureResult{}, perr
	}
	return res.(nodeproto.SandboxEnsureResult), nil
}

func TestSandboxEnsure(t *testing.T) {
	t.Parallel()
	a, fake, _ := sandboxAgent(t)
	folder := brainFolder(t, "rt-1")
	p := nodeproto.SandboxEnsureParams{Brain: "work", Folder: folder, Marker: "rt-1", APIPort: 8765, Provider: "claude",
		Mounts: []nodeproto.SandboxMount{{Path: folder}, {Path: "/opt/shared", ReadOnly: true}}, Egress: []string{"api.github.com"}}
	res, perr := ensure(a, p)
	if perr != nil || res.Name != "fridolin-work" || res.State != nodeproto.SandboxCreated {
		t.Fatalf("first ensure: %+v %v", res, perr)
	}
	calls := fakeCalls(t, fake)
	want := []string{"create", "--name", "fridolin-work", "--memory", "4096m", "-t", "fridolin-brain:v1", "--pull", "never", "claude", folder, "/opt/shared:ro"}
	if !called(calls, want...) {
		t.Errorf("no %v in %v", want, calls)
	}
	for _, rule := range [][]string{
		{"policy", "allow", "network", "--sandbox", "fridolin-work", "localhost:8765"},
		{"policy", "allow", "network", "--sandbox", "fridolin-work", "api.github.com"},
		{"policy", "deny", "network", "--sandbox", "fridolin-work", "downloads.claude.ai"},
	} {
		if !called(calls, rule...) {
			t.Errorf("no rule %v", rule)
		}
	}
	// The same again: ready, nothing made.
	if res, perr = ensure(a, p); perr != nil || res.State != nodeproto.SandboxReady {
		t.Fatalf("second ensure: %+v %v", res, perr)
	}
	if called(fakeCalls(t, fake)[len(calls):], "create") {
		t.Error("ensure made the sandbox again although nothing changed")
	}
	// Wider egress: made again.
	p.Egress = append(p.Egress, "*.wikipedia.org")
	if res, perr = ensure(a, p); perr != nil || res.State != nodeproto.SandboxRecreated {
		t.Fatalf("ensure with more egress: %+v %v", res, perr)
	}
	if !called(fakeCalls(t, fake), "rm", "-f", "fridolin-work") {
		t.Error("the old sandbox was not removed")
	}
	if info := sandboxInfo(t, a); len(info.Sandboxes) != 1 || info.Sandboxes[0].Brain != "work" || info.Sandboxes[0].MemoryMB != 4096 {
		t.Errorf("info's sandboxes: %+v", info.Sandboxes)
	}
}

// readyBrain ensures brain's sandbox and returns run params for it.
func readyBrain(t *testing.T, a *Agent, provider string, argv ...string) nodeproto.SandboxRunParams {
	t.Helper()
	folder := brainFolder(t, "rt-1")
	if _, perr := ensure(a, nodeproto.SandboxEnsureParams{Brain: "work", Folder: folder, Marker: "rt-1", APIPort: 8765, Provider: provider}); perr != nil {
		t.Fatal(perr)
	}
	return nodeproto.SandboxRunParams{Brain: "work", Run: "r1", Folder: folder, Argv: argv, Env: map[string]string{"BRIDGE_CHAT": "dm"},
		Stdin: "hello there", Token: "run-token-secret-1234", APIPort: 8765, Provider: provider}
}

func sbCall(t *testing.T, a *Agent, method string, params any) (any, *nodeproto.Error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return a.sandboxCall(context.Background(), method, raw)
}

// readRun reads a run to its end, as the core does.
func readRun(t *testing.T, a *Agent, run string) (string, nodeproto.SandboxReadResult) {
	t.Helper()
	var out strings.Builder
	var off int64
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); {
		res, perr := sbCall(t, a, nodeproto.MethodSandboxRead, nodeproto.SandboxReadParams{Run: run, Offset: off, WaitMS: 5000})
		if perr != nil {
			t.Fatal(perr)
		}
		r := res.(nodeproto.SandboxReadResult)
		out.WriteString(r.Data)
		off = r.Next
		if r.Done {
			return out.String(), r
		}
	}
	t.Fatalf("the run did not end; %d bytes read", out.Len())
	return "", nodeproto.SandboxReadResult{}
}

func TestSandboxRun(t *testing.T) {
	t.Parallel()
	a, fake, log := sandboxAgent(t)
	p := readyBrain(t, a, "claude", "fake-agent", "echo", "3")
	if res, perr := sbCall(t, a, nodeproto.MethodSandboxRun, p); perr != nil || res.(nodeproto.SandboxRunResult).Run != "r1" {
		t.Fatalf("run: %v %v", res, perr)
	}
	if _, perr := sbCall(t, a, nodeproto.MethodSandboxRun, p); perr == nil {
		t.Error("the same run id started twice")
	}
	out, end := readRun(t, a, "r1")
	if end.ExitCode != 3 || end.Error != "" || !strings.Contains(end.StderrTail, "a warning") {
		t.Errorf("end: %+v", end)
	}
	for _, want := range []string{"prompt: hello there", "argv: fake-agent echo 3", "token: true", "grüße",
		"BRIDGE_CHAT=dm", "FRIDOLIN_API=http://host.docker.internal:8765", "FRIDOLIN_BRAIN_TOKEN_FILE=/tmp/fridolin-run-r1.token",
		"FRIDOLIN_BIN=/opt/fridolin/bin/fridolin", "FRIDOLIN_TOOLS_DIR=/opt/fridolin/tools"} {
		if !strings.Contains(out, want) {
			t.Errorf("the run's output lacks %q:\n%s", want, out)
		}
	}
	// The run token reached sandboxd through the 0600 file, and the VM
	// had only the placeholder.
	if b, _ := os.ReadFile(filepath.Join(fake, "secret-value")); string(b) != p.Token {
		t.Errorf("sandboxd read %q from the token file", b)
	}
	calls := fakeCalls(t, fake)
	for _, c := range calls {
		joined := strings.Join(c.Args, " ")
		if strings.Contains(joined, p.Token) || strings.Contains(joined, testClaudeToken) {
			t.Errorf("a secret in sbx's arguments: %v", c.Args)
		}
		if c.TokenEnv != (c.Args[0] == "exec" && slices.Contains(c.Args, "setsid")) {
			t.Errorf("the Claude token in the environment of %v: %v", c.Args, c.TokenEnv)
		}
	}
	if s := log.String(); strings.Contains(s, p.Token) || strings.Contains(s, testClaudeToken) || strings.Contains(s, "sbx-r") {
		t.Errorf("a secret in the log:\n%s", s)
	}
	// Cleaned up: the secret, both token files, the pid file.
	if !called(calls, "secret", "rm", "--placeholder") {
		t.Error("the run's secret was not removed")
	}
	if ents, _ := os.ReadDir(filepath.Join(a.Dir, sandboxDir, sandboxRunsDir)); len(ents) != 0 {
		t.Errorf("token files stay on the host: %v", ents)
	}
	if ents, _ := os.ReadDir(filepath.Join(fake, "vm")); len(ents) != 0 {
		t.Errorf("files stay in the VM: %v", ents)
	}
	// A read past the output is refused; a finished run is forgotten later.
	if _, perr := sbCall(t, a, nodeproto.MethodSandboxRead, nodeproto.SandboxReadParams{Run: "r1", Offset: 1 << 20}); perr == nil {
		t.Error("a read past the output")
	}
	a.pruneRuns(time.Now().Add(nodeproto.SandboxRunForget + time.Second))
	if _, perr := sbCall(t, a, nodeproto.MethodSandboxRead, nodeproto.SandboxReadParams{Run: "r1"}); perr == nil || perr.Code != nodeproto.CodeNotFound {
		t.Errorf("a forgotten run: %v", perr)
	}
}

// waitStarted reads until the run printed "started".
func waitStarted(t *testing.T, a *Agent, run string) int64 {
	t.Helper()
	var off int64
	var seen string
	for range 100 {
		res, perr := sbCall(t, a, nodeproto.MethodSandboxRead, nodeproto.SandboxReadParams{Run: run, Offset: off, WaitMS: 5000})
		if perr != nil {
			t.Fatal(perr)
		}
		r := res.(nodeproto.SandboxReadResult)
		seen += r.Data
		off = r.Next
		if strings.Contains(seen, "started") || r.Done {
			return off
		}
	}
	t.Fatal("the run did not start")
	return 0
}

func TestSandboxStop(t *testing.T) {
	t.Parallel()
	a, fake, _ := sandboxAgent(t)
	p := readyBrain(t, a, "claude", "fake-agent", "wait")
	if _, perr := sbCall(t, a, nodeproto.MethodSandboxRun, p); perr != nil {
		t.Fatal(perr)
	}
	waitStarted(t, a, "r1")
	// A read with nothing new waits, then answers empty.
	start := time.Now()
	res, perr := sbCall(t, a, nodeproto.MethodSandboxRead, nodeproto.SandboxReadParams{Run: "r1", Offset: int64(len("started\n")), WaitMS: 200})
	if perr != nil || res.(nodeproto.SandboxReadResult).Data != "" || res.(nodeproto.SandboxReadResult).Done || time.Since(start) < 150*time.Millisecond {
		t.Fatalf("an idle read: %+v %v after %v", res, perr, time.Since(start))
	}
	if _, perr := sbCall(t, a, nodeproto.MethodSandboxStop, nodeproto.SandboxStopParams{Run: "r1", GraceS: 5}); perr != nil {
		t.Fatal(perr)
	}
	if _, end := readRun(t, a, "r1"); end.ExitCode != 143 {
		t.Errorf("stopped: %+v", end)
	}
	want := []string{"exec", "fridolin-work", "sh", "-c", `kill -TERM -"$(cat "$0")"`, "/tmp/fridolin-run-r1.pid"}
	if !called(fakeCalls(t, fake), want...) {
		t.Errorf("no TERM to the run's process group inside the VM")
	}
	// One that ignores TERM gets KILL after the grace.
	p.Run, p.Argv = "r2", []string{"fake-agent", "wait", "ignore-term"}
	if _, perr := sbCall(t, a, nodeproto.MethodSandboxRun, p); perr != nil {
		t.Fatal(perr)
	}
	waitStarted(t, a, "r2")
	if _, perr := sbCall(t, a, nodeproto.MethodSandboxStop, nodeproto.SandboxStopParams{Run: "r2", GraceS: 1}); perr != nil {
		t.Fatal(perr)
	}
	if _, end := readRun(t, a, "r2"); end.ExitCode != 137 {
		t.Errorf("killed: %+v", end)
	}
	if _, perr := sbCall(t, a, nodeproto.MethodSandboxStop, nodeproto.SandboxStopParams{Run: "nope"}); perr == nil || perr.Code != nodeproto.CodeNotFound {
		t.Errorf("stopping an unknown run: %v", perr)
	}
}

// A run that writes past the buffer is stopped and says why; its output
// up to the buffer is still there.
func TestSandboxRunOverflow(t *testing.T) {
	t.Parallel()
	a, _, _ := sandboxAgent(t)
	p := readyBrain(t, a, "claude", "fake-agent", "flood")
	if _, perr := sbCall(t, a, nodeproto.MethodSandboxRun, p); perr != nil {
		t.Fatal(perr)
	}
	out, end := readRun(t, a, "r1")
	if len(out) != nodeproto.SandboxRunBuffer || end.Error == "" || end.ExitCode != 143 {
		t.Errorf("overflow: %d bytes, %+v", len(out), end)
	}
}

// Codex runs get sbx's provider settings as -c options and no Claude
// token.
func TestSandboxRunCodex(t *testing.T) {
	t.Parallel()
	a, fake, _ := sandboxAgent(t)
	if err := os.WriteFile(filepath.Join(fake, "codex"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p := readyBrain(t, a, "codex", "fake-agent", "echo", "0")
	if _, perr := sbCall(t, a, nodeproto.MethodSandboxRun, p); perr != nil {
		t.Fatal(perr)
	}
	out, end := readRun(t, a, "r1")
	if end.ExitCode != 0 || !strings.Contains(out, "token: false") {
		t.Errorf("codex run: %+v\n%s", end, out)
	}
	if !strings.Contains(out, `-c model_provider="sandboxd"`) || !strings.Contains(out, "model_providers.sandboxd.base_url") {
		t.Errorf("no provider settings in codex's argv:\n%s", out)
	}
	if got := withCodexConfig([]string{"codex", "exec", "--json", "-"}); got[1] != "exec" || got[2] != "-c" || got[len(got)-1] != "-" {
		t.Errorf("withCodexConfig: %v", got)
	}
	for _, c := range fakeCalls(t, fake) {
		if c.TokenEnv {
			t.Errorf("the Claude token went to %v", c.Args)
		}
	}
}

func TestSandboxEnsureRefuses(t *testing.T) {
	t.Parallel()
	a, fake, _ := sandboxAgent(t)
	p := nodeproto.SandboxEnsureParams{Brain: "work", Folder: brainFolder(t, "rt-old"), Marker: "rt-new", APIPort: 8765, Provider: "claude"}
	// Another core's folder (a stale copy after a move): unavailable.
	if _, perr := ensure(a, p); perr == nil || perr.Code != nodeproto.CodeUnavailable || !strings.Contains(perr.Message, "marker") {
		t.Fatalf("marker mismatch: %v", perr)
	}
	p.Folder = brainFolder(t, "")
	if _, perr := ensure(a, p); perr == nil || perr.Code != nodeproto.CodeUnavailable {
		t.Fatalf("no marker: %v", perr)
	}
	if called(fakeCalls(t, fake), "create") {
		t.Error("a sandbox was made for a folder that is not the core's")
	}
	// Codex without sbx's login: unavailable.
	p.Folder, p.Marker, p.Provider = brainFolder(t, "rt-2"), "rt-2", "codex"
	if _, perr := ensure(a, p); perr == nil || perr.Code != nodeproto.CodeUnavailable {
		t.Fatalf("codex without its login: %v", perr)
	}
	// A rule that cannot be set: failed, and the half-made sandbox goes.
	p.Provider = "claude"
	if err := os.WriteFile(filepath.Join(fake, "fail-policy"), []byte("policy store is locked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, perr := ensure(a, p); perr == nil || perr.Code != nodeproto.CodeFailed {
		t.Fatalf("a rule fails: %v", perr)
	}
	if len(fakeSandboxes(fake)) != 0 {
		t.Errorf("a half-made sandbox stays: %v", fakeSandboxes(fake))
	}
	// sbx cannot create it: unavailable (the VM does not start).
	if err := os.Remove(filepath.Join(fake, "fail-policy")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fake, "fail-create"), []byte("no space"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, perr := ensure(a, p); perr == nil || perr.Code != nodeproto.CodeUnavailable {
		t.Fatalf("create fails: %v", perr)
	}
	// Not signed in: sbx ls says so, and ensure is unavailable.
	if err := os.WriteFile(filepath.Join(fake, "fail-ls"), []byte("You are not signed in. Run sbx login."), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, perr := ensure(a, p); perr == nil || perr.Code != nodeproto.CodeUnavailable || !strings.Contains(perr.Message, "signed in") {
		t.Fatalf("not signed in: %v", perr)
	}
	if info := sandboxInfo(t, a); info.SignedIn || !info.Daemon {
		t.Errorf("not signed in: %+v", info)
	}
}
