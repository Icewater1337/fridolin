package nodeagent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// TestSandboxLive runs against the machine's real sbx and Docker, only
// with FRIDOLIN_LIVE_SBX=1 (never in CI): setup builds and loads the
// template, a linux fridolin built here is put in place, and a scratch
// brain's sandbox runs `claude --version` and fridolin's own `version`
// through the mount, streamed back through sandbox.read. It sets sbx's
// global settings as fridolin owns them, and removes its sandbox.
func TestSandboxLive(t *testing.T) {
	// Serial: it drives the machine's one sbx, and starts a VM.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	a := liveSandbox(ctx, t)
	folder := liveFolder(t, a, "livetest", "rt-live")
	p := nodeproto.SandboxEnsureParams{Brain: "livetest", Folder: folder, Marker: "rt-live", APIPort: 8799, Provider: "claude", MemoryMB: 2048}
	if res, perr := a.sandboxEnsure(ctx, p); perr != nil {
		t.Fatalf("ensure: %v", perr)
	} else {
		t.Logf("ensure: %+v", res)
	}
	for i, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{"claude", "--version"}, "(Claude Code)"},
		{[]string{sandboxBinPath, "version"}, "v0.0.0-live"},
	} {
		run := "live" + string(rune('a'+i))
		rp := nodeproto.SandboxRunParams{Brain: "livetest", Run: run, Folder: folder, Argv: tc.argv, Token: "run-token-live", APIPort: 8799, Provider: "claude", TimeoutS: 120}
		if _, perr := a.sandboxRunStart(ctx, rp); perr != nil {
			t.Fatalf("run %v: %v", tc.argv, perr)
		}
		got, res := readRun(t, a, run)
		t.Logf("%v: exit %d, %q, stderr %q", tc.argv, res.ExitCode, got, res.StderrTail)
		if res.ExitCode != 0 || !strings.Contains(got, tc.want) {
			t.Errorf("%v: exit %d, %q", tc.argv, res.ExitCode, got)
		}
	}
}
