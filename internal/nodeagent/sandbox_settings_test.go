package nodeagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Before `fridolin node sandbox setup` recorded a template, the node does
// not declare sandbox, so the owner is not asked to approve it, and sbx
// is not even asked.
func TestSandboxNotDeclaredBeforeSetup(t *testing.T) {
	t.Parallel()
	a, fake, log := sandboxAgent(t)
	if err := a.saveSandboxState(sandboxState{}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.sandboxProbe(context.Background()); !errors.Is(err, ErrSandboxNotSetUp) {
		t.Fatalf("probe before setup: %v", err)
	}
	if a.sandboxCheck(context.Background(), true) || a.declared().Sandbox != nil {
		t.Fatalf("declared before setup: %+v", a.declared().Sandbox)
	}
	if calls := fakeCalls(t, fake); len(calls) != 0 {
		t.Errorf("sbx ran before setup: %v", calls)
	}
	if !strings.Contains(log.String(), "not set up: run fridolin node sandbox setup") {
		t.Errorf("log: %s", log)
	}
	if _, perr := a.sandboxCall(context.Background(), "sandbox.info", nil); perr == nil || !strings.Contains(perr.Message, "not set up") {
		t.Errorf("sandbox.info before setup: %v", perr)
	}
	// Set up: declared.
	if err := a.saveSandboxState(sandboxState{Template: "fridolin-brain:v1"}); err != nil {
		t.Fatal(err)
	}
	if !a.sandboxCheck(context.Background(), true) || a.declared().Sandbox == nil || a.declared().Sandbox.Problem != "" {
		t.Fatalf("not declared after setup: %+v", a.declared().Sandbox)
	}
}

// The agent sets the owned settings again when they drifted, and
// initializes a missing network policy; another policy it leaves to
// setup, since replacing it stops every sandbox.
func TestSandboxSettingsEnforced(t *testing.T) {
	t.Parallel()
	a, fake, log := sandboxAgent(t)
	if err := os.WriteFile(filepath.Join(fake, "settings.json"), []byte(`{"browser.enabled":true,"ssh.agentForwardingEnabled":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fake, "policy-unset"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info := sandboxInfo(t, a)
	if !info.Settings || info.Problem != "" {
		t.Fatalf("settings not made to hold: %+v", info)
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
	info = sandboxInfo(t, a)
	if info.Settings || !strings.Contains(info.Problem, "run fridolin node sandbox setup") {
		t.Errorf("balanced policy: %+v", info)
	}
	if called(fakeCalls(t, fake), "policy", "reset") {
		t.Error("the agent reset the policy")
	}
}
