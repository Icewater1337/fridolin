package nodeagent

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestFrideSandboxLive runs the sandboxed FrIDE against the machine's
// real sbx and Docker, only with FRIDOLIN_LIVE_SBX=1 (never in CI): in a
// scratch state dir and base folder, it creates the sandbox (the template
// and FrIDE built from this checkout's pins), stores a made-up Claude
// token, has an agent keep FrIDE running, pairs with it on 17850, checks
// the webhook's way to the host on 17851, updates, stops, and removes the
// sandbox and its template. Its sandbox has a scratch name, never
// fride-sandbox's; it skips while 17850 or 17851 is taken.
func TestFrideSandboxLive(t *testing.T) {
	// Serial: it drives the machine's one sbx, its ports and a VM.
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
	origin, err := exec.Command("git", "-C", repo, "remote", "get-url", "origin").Output()
	if err != nil {
		t.Fatal(err)
	}
	frideRepo, err := frideRepoFrom(string(origin))
	if err != nil {
		t.Fatal(err)
	}
	hook, err := net.Listen("tcp", "127.0.0.1:17851")
	if err != nil {
		t.Skipf("127.0.0.1:17851 is taken: %v", err)
	}
	if ln, err := net.Listen("tcp", "127.0.0.1:17850"); err != nil {
		t.Skipf("127.0.0.1:17850 is taken: %v", err)
	} else {
		ln.Close()
	}
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{Core: "core.invalid:8766", Name: "live-sbx", Service: FrideSandboxService}); err != nil {
		t.Fatal(err)
	}
	o := FrideSandboxOptions{Dir: dir, Sbx: sbx, Docker: docker, Repo: repo, Base: filepath.Join(t.TempDir(), "repos"),
		FrideRepo: frideRepo, GitName: "Live Test", GitEmail: "you@example.com", Out: tlogWriter{t}, name: liveSandboxName(t)}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	if status, err := o.sandboxStatus(ctx); err != nil || status != "" {
		t.Fatalf("a sandbox %s is there already (%q %v): this test makes and removes its own", o.sandbox(), status, err)
	}
	tag, _ := FrideSandboxTemplateTag(repo)
	t.Cleanup(func() {
		ctx := context.Background()
		o.SkipHistory = true
		if err := FrideSandboxRemove(ctx, o); err != nil {
			t.Errorf("rm: %v", err)
		}
		if _, stderr, err := o.sbx().run(ctx, nil, nil, "template", "rm", "--force", tag); err != nil {
			t.Logf("template rm: %v", sbxError(err, stderr))
		}
	})
	start := time.Now()
	if err := FrideSandboxCreate(ctx, o); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Logf("created in %s", time.Since(start).Round(time.Second))
	frideSandboxLiveRun(ctx, t, o, hook)
}

// frideSandboxLiveRun is the live test once the sandbox is made.
func frideSandboxLiveRun(ctx context.Context, t *testing.T, o FrideSandboxOptions, hook net.Listener) {
	hits := &hookHits{}
	srv := &http.Server{Handler: hits, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(hook) }()
	defer srv.Close()
	if err := FrideSandboxLogin(ctx, o, "sk-ant-"+"oat01-live-made-up"); err != nil {
		t.Fatalf("login: %v", err)
	}
	a := &Agent{Dir: o.Dir, SandboxSbx: o.Sbx, Logf: t.Logf, FrideSandboxPoll: time.Second}
	lctx, stopLoop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.frideSandboxLoop(lctx)
	}()
	defer func() { stopLoop(); <-done }()

	start := time.Now()
	info, err := FrideSandboxStart(ctx, o)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Logf("FrIDE answered and paired in %s: %+v", time.Since(start).Round(time.Second), info)
	r, err := FrideSandboxStatus(ctx, o)
	if err != nil || !r.Answers || !r.Paired || r.Inside["login"] != "yes" || r.Inside["running"] != "yes" || r.Inside["git"] != "yes" {
		t.Errorf("status: %+v %v", r, err)
	}
	out, stderr, err := o.sbx().run(ctx, nil, nil, "exec", o.sandbox(), "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
		"-X", "POST", "http://127.0.0.1:17851/fride")
	if err != nil || string(out) != "204" || hits.n.Load() == 0 {
		t.Errorf("the webhook's way to the host: %q %v %s (hits %d)", out, err, stderr, hits.n.Load())
	}

	start = time.Now()
	if err := FrideSandboxUpdate(ctx, o); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := waitAnswers(ctx, o.frideURL(), 2*time.Minute); err != nil {
		t.Fatalf("after update: %v", err)
	}
	t.Logf("updated and answering again in %s", time.Since(start).Round(time.Second))

	if err := FrideSandboxStop(ctx, o); err != nil {
		t.Fatalf("stop: %v", err)
	}
	time.Sleep(3 * time.Second)
	if waitAnswers(ctx, o.frideURL(), 1) == nil {
		t.Error("FrIDE answers after stop")
	}
	stopLoop()
	<-done
	// The login stays on the VM's disk across a stop of the VM.
	if _, stderr, err := o.sbx().run(ctx, nil, nil, "stop", o.sandbox()); err != nil {
		t.Fatalf("sbx stop: %v", sbxError(err, stderr))
	}
	out, _, err = o.sbx().run(ctx, nil, nil, append([]string{"exec", o.sandbox()}, frideSandboxScript(frideSandboxStatus)...)...)
	if kv := parseKeyValues(out); err != nil || kv["login"] != "yes" || kv["git"] != "yes" {
		t.Errorf("after the VM stopped: %v %v", kv, err)
	}
}

// tlogWriter writes to the test's log.
type tlogWriter struct{ t *testing.T }

func (w tlogWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// hookHits counts the requests the webhook's listener got.
type hookHits struct{ n atomic.Int32 }

func (h *hookHits) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	h.n.Add(1)
	w.WriteHeader(http.StatusNoContent)
}
