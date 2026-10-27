package nodeagent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// settingsAgent is an agent whose sbx is the fake (fakeSbx) in its own
// folder. It never runs the machine's sbx.
func settingsAgent(t *testing.T) (*Agent, sbxDriver, string, *syncLog) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("Docker Sandboxes are used on macOS and Linux")
	}
	fake := t.TempDir()
	log := &syncLog{}
	d := sbxCLI{path: testExe(t), env: []string{"FAKE_SBX_DIR=" + fake}}
	a := &Agent{Dir: t.TempDir(), Config: Config{Core: "x", Name: "mac"}, Logf: log.logf, SandboxDriver: d}
	return a, d, fake, log
}

// The agent sets the owned settings again when they drifted, and
// initializes a missing network policy; another policy it leaves to
// create, since replacing it stops every sandbox.
func TestSandboxSettingsEnforced(t *testing.T) {
	t.Parallel()
	a, d, fake, log := settingsAgent(t)
	if err := os.WriteFile(filepath.Join(fake, "settings.json"), []byte(`{"browser.enabled":true,"ssh.agentForwardingEnabled":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fake, "policy-unset"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, why := a.sandboxSettings(context.Background(), d); !ok {
		t.Fatalf("settings not made to hold: %s", why)
	}
	calls := fakeCalls(t, fake)
	for _, want := range [][]string{
		{"settings", "set", "browser.enabled", "false"},
		{"settings", "set", "ssh.agentForwardingEnabled", "false"},
		{"policy", "init", "deny-all"},
	} {
		if !called(calls, want...) {
			t.Errorf("no %v in %v", want, calls)
		}
	}
	if !strings.Contains(log.String(), "after sbx's next daemon restart") {
		t.Errorf("log: %s", log)
	}
	// A balanced policy: not fridolin's, and the agent does not reset it.
	if err := os.WriteFile(filepath.Join(fake, "policy"), []byte(fakeBalanced), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, why := a.sandboxSettings(context.Background(), d); ok || why == "" {
		t.Errorf("balanced policy: ok %v, %q", ok, why)
	}
	if called(fakeCalls(t, fake), "policy", "reset") {
		t.Error("the agent reset the policy")
	}
}
