package nodeagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// TestFrideInstanceLive runs a FrIDE instance against the machine's real
// sbx and Docker, only with FRIDOLIN_LIVE_SBX=1 (never in CI), in a
// scratch state dir: it sets the host up (the template built from this
// checkout's pins, unless sbx has it; with FRIDOLIN_LIVE_TEMPLATE=IMAGE
// --no-build instead, the instance made from IMAGE, which sbx pulls: no
// Docker needed, as on Windows), starts an instance with a made-up
// Claude token and a made-up GitLab token (which sbx's secret command
// reads on the host), checks that FrIDE answers and is paired and that
// the instance's webhook reaches the agent from inside the VM, starts it
// again with more memory (a remake), stops and removes it, and removes
// the template when it built it. Its instance has a scratch name, and its
// ports are the first free pair from 17950 up.
func TestFrideInstanceLive(t *testing.T) {
	// Serial: it drives the machine's one sbx, its ports and a VM.
	if os.Getenv("FRIDOLIN_LIVE_SBX") != "1" {
		t.Skip("set FRIDOLIN_LIVE_SBX=1 to run against the real sbx")
	}
	sbx, err := exec.LookPath("sbx")
	if err != nil {
		t.Skip("no sbx")
	}
	tpl := os.Getenv("FRIDOLIN_LIVE_TEMPLATE")
	docker, err := exec.LookPath("docker")
	if err != nil && tpl == "" {
		t.Skip("no docker: set FRIDOLIN_LIVE_TEMPLATE to a published template")
	}
	git, _ := exec.LookPath("git")
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Join(filepath.Dir(file), "..", "..")
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{Core: "core.invalid:8766", Name: "live"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	d := sbxCLI{path: sbx}
	o := FrideHostSetupOptions{Dir: dir, Sbx: sbx, Docker: docker, Git: git, Repo: repo, Caches: filepath.Join(t.TempDir(), "caches"), Out: tlogWriter{t}}
	if runtime.GOOS == "windows" {
		o.Caches = ""
	}
	host := tpl
	if tpl != "" {
		o.NoBuild = true
	} else if host, err = liveTemplate(ctx, t, d, o); err != nil {
		t.Fatal(err)
	}
	if err := FrideHostSetup(ctx, o); err != nil {
		t.Fatalf("host setup: %v", err)
	}
	t.Logf("set up with %s", host)

	b := make([]byte, 3)
	_, _ = rand.Read(b)
	name := "live-" + hex.EncodeToString(b)
	store := hostsecrets.Files{Dir: t.TempDir()}
	if err := store.Set(hostsecrets.ClaudeOAuthToken, "sk-ant-"+"oat01-live-made-up"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := LoadConfig(dir)
	// On Windows sbx reads the git token with the test binary as fridolin.exe.
	a := &Agent{Dir: dir, Config: cfg, SandboxSbx: sbx, Executable: testExe(t), CoreSecrets: store, Logf: t.Logf, FrideInstancePoll: time.Second}
	a.inst.portBase = 17950
	lctx, stopLoop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	a.frideInstancesSupervise(lctx, &wg)
	t.Cleanup(func() {
		ctx := context.Background()
		if err := a.instanceOps().remove(ctx, name, true); err != nil && !IsNoFrideInstance(err) {
			t.Errorf("remove: %v", err)
		}
		stopLoop()
		wg.Wait()
	})

	start := time.Now()
	out, perr := a.frideInstanceStart(ctx, nodeproto.FrideInstanceStartParams{Name: name, MemoryMB: 3072, Template: tpl,
		Identities: []nodeproto.FrideIdentity{{ID: "live", Kind: "gitlab", Host: "gitlab.example.com", Token: "glpat-live-" + "made-up"}},
		Git:        &nodeproto.FrideGitUser{Name: "Live Test", Email: "you@example.com"}})
	if perr != nil {
		t.Fatalf("start: %v", perr)
	}
	t.Logf("started in %s: %+v", time.Since(start).Round(time.Second), out)
	st, _ := loadFrideInstance(dir, name)
	if out.State != nodeproto.FrideInstanceRunning || out.URL != frideInstanceURL(st.Port) {
		t.Errorf("start answered %+v", out)
	}
	// The webhook's way to the agent: an unsigned post is refused there.
	code, stderr, err := d.run(ctx, nil, nil, "exec", st.Sandbox, "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
		"-X", "POST", "http://127.0.0.1:"+strconv.Itoa(st.WebhookPort)+"/fride")
	if err != nil || string(code) != "401" {
		t.Errorf("the webhook's way to the agent: %q %v %s", code, err, stderr)
	}

	start = time.Now()
	out, perr = a.frideInstanceStart(ctx, nodeproto.FrideInstanceStartParams{Name: name, MemoryMB: 4096, Template: tpl})
	if perr != nil {
		t.Fatalf("start with more memory: %v", perr)
	}
	t.Logf("made anew in %s: %+v", time.Since(start).Round(time.Second), out)
	if out.MemoryMB != 4096 || out.State != nodeproto.FrideInstanceRunning {
		t.Errorf("after the remake: %+v", out)
	}

	if _, perr := a.frideInstanceCall(ctx, nodeproto.MethodFrideInstanceStop, []byte(`{"name":"`+name+`"}`)); perr != nil {
		t.Fatalf("stop: %v", perr)
	}
	if status, _ := sbxSandboxStatus(ctx, d, st.Sandbox); status == "running" {
		t.Errorf("after stop the sandbox is %s", status)
	}
	if waitAnswers(ctx, frideInstanceURL(st.Port), 1) == nil {
		t.Error("FrIDE answers after stop")
	}
	if _, perr := a.frideInstanceCall(ctx, nodeproto.MethodFrideInstanceRemove, []byte(`{"name":"`+name+`"}`)); perr != nil {
		t.Fatalf("remove: %v", perr)
	}
	if status, _ := sbxSandboxStatus(ctx, d, st.Sandbox); status != "" {
		t.Errorf("after remove sbx has %s: %s", st.Sandbox, status)
	}
}

// liveTemplate is the template the live test sets up with; when sbx does
// not have it yet, the test removes it again at its end.
func liveTemplate(ctx context.Context, t *testing.T, d sbxDriver, o FrideHostSetupOptions) (string, error) {
	t.Helper()
	origin, err := exec.CommandContext(ctx, o.Git, "-C", o.Repo, "remote", "get-url", "origin").Output()
	if err != nil {
		return "", err
	}
	frideRepo, err := frideRepoFrom(strings.TrimSpace(string(origin)))
	if err != nil {
		return "", err
	}
	tag, err := FrideTemplateTag(o.Repo, frideRepo)
	if err != nil {
		return "", err
	}
	if !sbxHasTemplate(ctx, d, tag) {
		t.Cleanup(func() {
			if _, stderr, err := d.run(context.Background(), nil, nil, "template", "rm", "--force", tag); err != nil {
				t.Logf("template rm: %v", sbxError(err, stderr))
			}
		})
	}
	return tag, nil
}

// tlogWriter writes to the test's log.
type tlogWriter struct{ t *testing.T }

func (w tlogWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
