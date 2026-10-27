package nodeagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
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
	if os.Getenv("FRIDOLIN_LIVE_SBX") != "1" {
		t.Skip("set FRIDOLIN_LIVE_SBX=1 to run against the real sbx")
	}
	sbx, err := exec.LookPath("sbx")
	if err != nil {
		t.Skip("no sbx")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("no docker")
	}
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Join(filepath.Dir(file), "..", "..")
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	var out strings.Builder
	if err := SandboxSetup(ctx, SandboxSetupOptions{Dir: dir, Sbx: sbx, Docker: docker, Repo: repo, Out: &out}); err != nil {
		t.Fatalf("setup: %v\n%s", err, out.String())
	}
	t.Log(out.String())

	// The linux fridolin, stamped as make stamps it.
	bin := filepath.Join(t.TempDir(), "fridolin")
	build := exec.Command("go", "build", "-trimpath", "-ldflags", "-X github.com/Icewater1337/fridolin/internal/version.version=v0.0.0-live", "-o", bin, "./cmd/fridolin")
	build.Dir, build.Env = repo, append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the linux fridolin: %v\n%s", err, b)
	}
	store := newMemSecrets()
	_ = store.Set(hostsecrets.ClaudeOAuthToken, "sk-not-a-real-token")
	a := &Agent{Dir: dir, SandboxSbx: sbx, CoreSecrets: store, Logf: t.Logf}
	if v, err := a.putSandboxBinFile(bin); err != nil || v != "v0.0.0-live" {
		t.Fatalf("the binary: %q %v", v, err)
	}
	info, err := a.sandboxProbe(ctx)
	if err != nil || info.Problem != "" || !info.Ready("claude") || info.Bin != "v0.0.0-live" {
		t.Fatalf("not ready: %+v %v", info, err)
	}
	if !a.sandboxCheck(ctx, true) || a.sandboxDeclared() == nil {
		t.Fatal("not declared")
	}

	folder, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, nodeproto.SandboxMarkerFile), []byte("rt-live\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, perr := a.sandboxRemove(context.Background(), nodeproto.SandboxRemoveParams{Brain: "livetest"}); perr != nil {
			t.Errorf("remove: %v", perr)
		}
	})
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
