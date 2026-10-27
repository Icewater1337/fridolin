package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/doctor"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// doctor --node's sandbox checks, from a look at sbx that tests make up:
// none without sbx; before setup, only that it is not set up; then each
// part of what sandbox.info says.
func TestNodeSandboxDoctorChecks(t *testing.T) {
	t.Parallel()
	run := func(s nodeagent.SandboxStatus) map[string]doctor.CheckReport {
		rep := doctor.Run(context.Background(), doctorEnvFor("", nil), sandboxChecks(func() nodeagent.SandboxStatus { return s }))
		out := map[string]doctor.CheckReport{}
		for _, c := range rep.Checks {
			out[c.ID] = c
		}
		return out
	}
	if got := run(nodeagent.SandboxStatus{}); len(got) != 0 {
		t.Errorf("without sbx: %v", got)
	}
	if got := run(nodeagent.SandboxStatus{Sbx: "/opt/homebrew/bin/sbx", Off: true}); len(got) != 0 {
		t.Errorf("turned off: %v", got)
	}
	got := run(nodeagent.SandboxStatus{Sbx: "/opt/homebrew/bin/sbx", Err: errors.New("cannot connect")})
	if c := got["node-sandbox-sbx"]; c.Status != doctor.Fail || len(got) != 1 {
		t.Errorf("sbx does not run: %v", got)
	}
	info := nodeproto.SandboxInfo{SandboxCap: nodeproto.SandboxCap{Version: nodeagent.TestedSbxVersion, Daemon: true, SignedIn: true, Settings: true,
		Problem: nodeagent.ErrSandboxNotSetUp.Error()}, FreeMemoryMB: 9000}
	got = run(nodeagent.SandboxStatus{Sbx: "/opt/homebrew/bin/sbx", Info: info})
	if c := got["node-sandbox-setup"]; c.Status != doctor.Warn || !strings.Contains(c.Detail, "not set up: run fridolin node sandbox setup") || len(got) != 2 {
		t.Errorf("before setup: %v", got)
	}
	info.Template, info.Bin, info.Problem = "fridolin-brain:0123abcd4567", "v1.2.3", ""
	info.Providers = []string{"claude", "codex"}
	got = run(nodeagent.SandboxStatus{Sbx: "/opt/homebrew/bin/sbx", Info: info, ClaudeToken: true})
	for id, c := range got {
		if c.Status != doctor.OK {
			t.Errorf("ready, %s: %+v", id, c)
		}
	}
	if len(got) != 10 {
		t.Errorf("ready: %d checks", len(got))
	}
	info.Version, info.Settings, info.Bin, info.Providers, info.FreeMemoryMB = "v0.51.0", false, "", []string{"claude"}, 500
	got = run(nodeagent.SandboxStatus{Sbx: "/opt/homebrew/bin/sbx", Info: info})
	for id, want := range map[string]string{
		"node-sandbox-sbx": doctor.Warn, "node-sandbox-settings": doctor.Fail, "node-sandbox-bin": doctor.Warn,
		"node-sandbox-claude": doctor.Warn, "node-sandbox-codex": doctor.Warn, "node-sandbox-memory": doctor.Warn,
	} {
		if got[id].Status != want {
			t.Errorf("%s: %+v, want %s", id, got[id], want)
		}
	}
}
