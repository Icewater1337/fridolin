package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/testutil"
)

// testFridePin is the FrIDE commit the fixture's checkout pins.
const testFridePin = "a80b2268707fd4a580e9df6d25e5219d7cf406bd"

// frideSandboxFixture is a joined node's state dir, a checkout and the
// fakes' folder, with sbx running and signed in and no template yet.
func frideSandboxFixture(t *testing.T) (FrideSandboxOptions, string, *bytes.Buffer) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("Docker Sandboxes run on macOS and Linux")
	}
	repo, fake, dir := t.TempDir(), t.TempDir(), t.TempDir()
	for name, body := range map[string]string{
		frideSandboxDockerfile: "FROM x\n",
		frideImageDockerfile:   "ARG FRIDE_REPO\nARG FRIDE_COMMIT=" + testFridePin + "\n",
		frideCompanionConfig:   `{"version":1,"name":"box","workspaceRoot":"/workspaces","baseFolders":["/var/lib/fride/repos"],"companion":{"enabled":true}}`,
	} {
		writeTestFile(t, filepath.Join(repo, filepath.FromSlash(name)), body)
	}
	writeTestFile(t, filepath.Join(fake, "templates.json"), "[]")
	if err := SaveConfig(dir, Config{Core: "core:8766", Name: "mac-sbx", Service: FrideSandboxService}); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	exe := testExe(t)
	return FrideSandboxOptions{Dir: dir, Sbx: exe, Docker: exe, Repo: repo, Base: filepath.Join(t.TempDir(), "repos"),
		FrideRepo: "https://github.com/you/fride", GitName: "You", GitEmail: "you@example.com", Out: out,
		env: []string{"FAKE_SBX_DIR=" + fake, "FAKE_DOCKER_DIR=" + fake}, url: "http://127.0.0.1:1", wait: time.Second}, fake, out
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeFile is a file of the fake VM's FrIDE, "" when it is not there.
func fakeFile(fake, name string) string {
	b, _ := os.ReadFile(filepath.Join(fake, "fride", name))
	return string(b)
}

// createdSandbox is the fixture after create.
func createdSandbox(t *testing.T) (FrideSandboxOptions, string, *bytes.Buffer) {
	t.Helper()
	o, fake, out := frideSandboxFixture(t)
	if err := FrideSandboxCreate(context.Background(), o); err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	return o, fake, out
}

// create builds and loads the template, makes the sandbox with the base
// folder, its port, memory and egress, prepares FrIDE and builds it at the
// pinned commit; FrIDE does not run yet.
func TestFrideSandboxCreate(t *testing.T) {
	t.Parallel()
	o, fake, out := createdSandbox(t)
	st, err := LoadFrideSandbox(o.Dir)
	if err != nil || st == nil {
		t.Fatalf("state: %v %v", st, err)
	}
	base, _ := filepath.EvalSymlinks(o.Base)
	tag, _ := FrideSandboxTemplateTag(o.Repo)
	if st.Base != base || st.Template != tag || st.Commit != testFridePin || st.Repo != o.FrideRepo || st.Run || st.Built.IsZero() {
		t.Errorf("state %+v", st)
	}
	calls := fakeCalls(t, fake)
	for _, want := range [][]string{
		{"template", "load"},
		{"create", "--name", "fride-sandbox", "--memory", "6g", "-t", tag, "--pull", "never", "-p", "127.0.0.1:17850:17850", "claude", base},
		{"policy", "allow", "network", "--sandbox", "fride-sandbox", "localhost:17851"},
		{"policy", "deny", "network", "--sandbox", "fride-sandbox", "downloads.claude.ai"},
	} {
		if !called(calls, want...) {
			t.Errorf("no %v in %v", want, calls)
		}
	}
	egress := ""
	for _, c := range calls {
		if a := c.Args; len(a) == 6 && a[0] == "policy" && a[1] == "allow" && strings.Contains(a[5], ",") {
			egress = a[5]
		}
	}
	for _, h := range []string{"github.com:443", "proxy.golang.org:443", "sum.golang.org:443", "registry.npmjs.org:443", "api.anthropic.com:443"} {
		if !slices.Contains(strings.Split(egress, ","), h) {
			t.Errorf("egress %q lacks %s", egress, h)
		}
	}
	docker, _ := os.ReadFile(filepath.Join(fake, "docker.log"))
	if !strings.Contains(string(docker), "build -f "+filepath.Join(o.Repo, "container", "sandbox", "fride.Dockerfile")+" -t "+tag) {
		t.Errorf("docker: %s", docker)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(fakeFile(fake, "config.json")), &cfg); err != nil {
		t.Fatalf("FrIDE's config: %v", err)
	}
	if cfg["workspaceRoot"] != "~/fride-companion/workspaces" || !slices.Equal(anyStrings(cfg["baseFolders"]), []string{base}) || cfg["companion"] == nil {
		t.Errorf("FrIDE's config %v", cfg)
	}
	if fakeFile(fake, "git") != "You|you@example.com" || fakeFile(fake, "built") != testFridePin {
		t.Errorf("git %q, built %q", fakeFile(fake, "git"), fakeFile(fake, "built"))
	}
	if !strings.Contains(fakeFile(fake, "builds.log"), "https://github.com/you/fride "+testFridePin) || fakeFile(fake, "serves.log") != "" {
		t.Errorf("builds: %q, serves %q", fakeFile(fake, "builds.log"), fakeFile(fake, "serves.log"))
	}
	if !strings.Contains(out.String(), "fridolin node fride sandbox login") {
		t.Errorf("no next step in:\n%s", out)
	}
	if err := FrideSandboxCreate(context.Background(), o); err == nil || !strings.Contains(err.Error(), "made already") {
		t.Errorf("create again: %v", err)
	}
}

func anyStrings(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, s := range l {
		str, _ := s.(string)
		out = append(out, str)
	}
	return out
}

// supervising is an agent on o's state dir with the fake sbx, whose loop
// keeps the sandboxed FrIDE running until the test ends.
func supervising(t *testing.T, o FrideSandboxOptions) *syncLog {
	t.Helper()
	log := &syncLog{}
	a := &Agent{Dir: o.Dir, Logf: log.logf, FrideSandboxPoll: 20 * time.Millisecond, SandboxDriver: o.sbx()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.frideSandboxLoop(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return log
}

func lines(s string) int { return strings.Count(s, "\n") }

// The agent keeps FrIDE running while the state says run: it holds the
// serve script's exec, starts it again when FrIDE ends, and ends it, for
// good, on stop.
func TestFrideSandboxSupervise(t *testing.T) {
	t.Parallel()
	o, fake, _ := createdSandbox(t)
	log := supervising(t, o)
	time.Sleep(100 * time.Millisecond)
	if fakeFile(fake, "serves.log") != "" {
		t.Fatal("FrIDE started before start")
	}
	st, _ := LoadFrideSandbox(o.Dir)
	st.Run = true
	if err := saveFrideSandbox(o.Dir, *st); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "FrIDE to run", func() bool { return lines(fakeFile(fake, "serves.log")) == 1 })
	if got := fakeFile(fake, "serves.log"); got != "17850 17851\n" {
		t.Errorf("serve's arguments: %q", got)
	}
	writeTestFile(t, filepath.Join(fake, "fride", "crash"), "")
	waitFor(t, "FrIDE to run again", func() bool { return lines(fakeFile(fake, "serves.log")) == 2 })
	if !strings.Contains(log.String(), "Started FrIDE (42)") || !strings.Contains(log.String(), "exec ended") {
		t.Errorf("log:\n%s", log)
	}
	if err := FrideSandboxStop(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "FrIDE to end", func() bool { _, err := os.Stat(filepath.Join(fake, "fride", "running")); return err != nil })
	time.Sleep(200 * time.Millisecond)
	if n := lines(fakeFile(fake, "serves.log")); n != 2 {
		t.Errorf("FrIDE started %d times; after stop it stays stopped", n)
	}
	if st, _ := LoadFrideSandbox(o.Dir); st.Run {
		t.Error("stop left run set")
	}
}

// update builds FrIDE again at the checkout's new pin, keeps the sandbox,
// and the agent starts the new FrIDE; a changed template is only noted.
func TestFrideSandboxUpdate(t *testing.T) {
	t.Parallel()
	o, fake, out := createdSandbox(t)
	supervising(t, o)
	st, _ := LoadFrideSandbox(o.Dir)
	st.Run = true
	if err := saveFrideSandbox(o.Dir, *st); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "FrIDE to run", func() bool { return lines(fakeFile(fake, "serves.log")) == 1 })
	const newPin = "1111111111111111111111111111111111111111"
	writeTestFile(t, filepath.Join(o.Repo, filepath.FromSlash(frideImageDockerfile)), "ARG FRIDE_COMMIT="+newPin+"\n")
	writeTestFile(t, filepath.Join(o.Repo, filepath.FromSlash(frideSandboxDockerfile)), "FROM y\n")
	out.Reset()
	o.GitName, o.GitEmail = "New Name", ""
	if err := FrideSandboxUpdate(context.Background(), o); err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	if fakeFile(fake, "built") != newPin || fakeFile(fake, "git") != "New Name|" {
		t.Errorf("built %q, git %q", fakeFile(fake, "built"), fakeFile(fake, "git"))
	}
	if got, _ := LoadFrideSandbox(o.Dir); got.Commit != newPin || !got.Run || got.Template != st.Template {
		t.Errorf("state after update: %+v", got)
	}
	if !strings.Contains(out.String(), "the template changed") || called(fakeCalls(t, fake), "rm") {
		t.Errorf("update with a new template:\n%s", out)
	}
	waitFor(t, "the new FrIDE to run", func() bool { return lines(fakeFile(fake, "serves.log")) == 2 })
	if n := lines(fakeFile(fake, "stops.log")); n != 1 {
		t.Errorf("FrIDE was stopped %d times", n)
	}
}

// start sets run, waits for FrIDE and pairs the node with it, with a code
// from FrIDE in the sandbox, the webhook on 17851 and the base folder;
// started again, it keeps the pairing.
func TestFrideSandboxStartPairs(t *testing.T) {
	t.Parallel()
	o, fake, _ := createdSandbox(t)
	ctx := context.Background()
	if _, err := FrideSandboxStart(ctx, o); err == nil || !strings.Contains(err.Error(), "does this node's agent run") {
		t.Fatalf("start without FrIDE answering: %v", err)
	}
	ff := testutil.StartFakeFride(t, "fridolin", "fride")
	o.url = ff.URL
	writeTestFile(t, filepath.Join(fake, "fride", "code"), ff.NewCode())
	info, err := FrideSandboxStart(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Repos) != 2 {
		t.Errorf("FrIDE's repositories: %v", info.Repos)
	}
	st, _ := LoadFrideSandbox(o.Dir)
	cfg, err := LoadConfig(o.Dir)
	if err != nil || cfg.Fride == nil || cfg.Fride.URL != ff.URL || cfg.Fride.Webhook != "127.0.0.1:17851" || cfg.Fride.Base != st.Base || !st.Run {
		t.Fatalf("node.json's fride: %+v %v; state %+v", cfg.Fride, err, st)
	}
	if fi, err := os.Stat(filepath.Join(o.Dir, FrideTokenFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the token file: %v %v", fi, err)
	}
	if _, err := FrideSandboxStart(ctx, o); err != nil {
		t.Fatal(err)
	}
	pairs := 0
	for _, c := range fakeCalls(t, fake) {
		if len(c.Args) > 4 && strings.HasPrefix(c.Args[4], "# fride-sandbox: pair-code") {
			pairs++
		}
	}
	if pairs != 1 {
		t.Errorf("asked FrIDE for %d codes; paired, start asks for none", pairs)
	}
	cfg.Fride = &FrideConfig{URL: "http://127.0.0.1:17840", Webhook: "127.0.0.1:17841"}
	if err := SaveConfig(o.Dir, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := FrideSandboxStart(ctx, o); err == nil || !strings.Contains(err.Error(), "node of its own") {
		t.Errorf("start on a node paired with another FrIDE: %v", err)
	}
}
