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
	// No sbx: nothing declared, and nothing ran.
	b := &Agent{Dir: t.TempDir(), Logf: func(string, ...any) {}}
	if b.sandboxCheck(context.Background(), true) || b.declared().Sandbox != nil {
		t.Error("declared without sbx")
	}
}
