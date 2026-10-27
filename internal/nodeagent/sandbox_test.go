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

func TestSandboxEnsureRefuses(t *testing.T) {
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
