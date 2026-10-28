package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// testFridePin is the FrIDE commit the fixtures' checkout pins.
const testFridePin = "a80b2268707fd4a580e9df6d25e5219d7cf406bd"

// The FrIDE template takes FrIDE's commit, Go, Claude Code and ponytail
// from the fridolin-fride image, and this checkout pins FrIDE's commit.
func TestFrideTemplateDockerfilePins(t *testing.T) {
	t.Parallel()
	repo := filepath.Join("..", "..")
	anyValue := regexp.MustCompile(`^\S+$`)
	arg := func(file, name string) string {
		t.Helper()
		v, err := dockerfileArg(filepath.Join(repo, filepath.FromSlash(file)), name, anyValue)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, name := range []string{"FRIDE_COMMIT", "GO_IMAGE", "CLAUDE_CODE_VERSION", "PONYTAIL_REPO", "PONYTAIL_COMMIT"} {
		if got, want := arg(frideTemplateDockerfile, name), arg(frideImageDockerfile, name); got != want {
			t.Errorf("%s: %s in %s, %s in %s", name, got, frideTemplateDockerfile, want, frideImageDockerfile)
		}
	}
	if _, err := FridePin(repo); err != nil {
		t.Error(err)
	}
	if _, err := companionConfig(repo); err != nil {
		t.Error(err)
	}
	a, _ := FrideTemplateTag(repo, "https://github.com/you/fride")
	b, _ := FrideTemplateTag(repo, "https://github.com/someone/fride")
	if a == b || !strings.HasPrefix(a, frideTemplateRepo+":") {
		t.Errorf("tags %q %q", a, b)
	}
}

// The scripts are sh.
func TestFrideInstanceScripts(t *testing.T) {
	t.Parallel()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	for _, s := range frideInstanceScripts {
		if out, err := exec.Command(sh, "-n", "-c", s).CombinedOutput(); err != nil || !strings.HasPrefix(s, frideInstanceScriptPrefix) {
			t.Errorf("%v %s\n%s", err, out, s)
		}
		// The VM's sh is dash, whose kill takes no "--" ("Illegal number").
		if strings.Contains(s, "kill -TERM --") || strings.Contains(s, "kill -KILL --") {
			t.Errorf("a kill with -- in:\n%s", s)
		}
	}
}

func TestFrideRepoFrom(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"git@github.com:you/fridolin.git":          "https://github.com/you/fride",
		"https://github.com/you/fridolin":          "https://github.com/you/fride",
		"https://x-token@github.com/you/fridolin/": "https://github.com/you/fride", // personal-data:ignore (a made-up user in a URL)
		"ssh://git@github.com/you/fridolin.git":    "https://github.com/you/fride",
		"https://gitlab.com/you/fridolin":          "",
		"/srv/git/fridolin":                        "",
	} {
		got, err := frideRepoFrom(in)
		if got != want || (err == nil) != (want != "") {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
}

// setupFixture is a joined node's state dir, a checkout and the fakes'
// folder, with sbx running and signed in and no template yet.
func setupFixture(t *testing.T) (FrideHostSetupOptions, string, *bytes.Buffer) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("Docker Sandboxes run on macOS and Linux")
	}
	repo, fake, dir := t.TempDir(), t.TempDir(), t.TempDir()
	for name, body := range map[string]string{
		frideTemplateDockerfile: "FROM x\nARG FRIDE_COMMIT=" + testFridePin + "\n",
		frideCompanionConfig:    `{"version":1,"name":"box","workspaceRoot":"/workspaces","baseFolders":["/var/lib/fride/repos"],"companion":{"enabled":true}}`,
	} {
		writeTestFile(t, filepath.Join(repo, filepath.FromSlash(name)), body)
	}
	writeTestFile(t, filepath.Join(fake, "templates.json"), "[]")
	writeTestFile(t, filepath.Join(fake, "fride-fake"), "")
	if err := SaveConfig(dir, Config{Core: "core:8766", Name: "mac"}); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	exe := testExe(t)
	return FrideHostSetupOptions{Dir: dir, Sbx: exe, Docker: exe, Repo: repo, FrideRepo: "https://github.com/you/fride",
		Caches: filepath.Join(t.TempDir(), "caches"), Out: out,
		env: []string{"FAKE_SBX_DIR=" + fake, "FAKE_DOCKER_DIR=" + fake}}, fake, out
}

// Setup makes sbx's settings fridolin's, builds the template with FrIDE
// from its URL and loads it into sbx, and records it all; run again with
// the template there, it builds nothing.
func TestFrideHostSetup(t *testing.T) {
	t.Parallel()
	o, fake, out := setupFixture(t)
	writeTestFile(t, filepath.Join(fake, "settings.json"), `{"browser.enabled":true,"ssh.agentForwardingEnabled":false}`)
	o.ReserveMB = 4096
	ctx := context.Background()
	if err := FrideHostSetup(ctx, o); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	tag, _ := FrideTemplateTag(o.Repo, o.FrideRepo)
	h, err := LoadFrideHost(o.Dir)
	if err != nil || h == nil {
		t.Fatalf("fride-host.json: %v %v", h, err)
	}
	caches, _ := filepath.EvalSymlinks(o.Caches)
	if h.Template != tag || h.FrideCommit != testFridePin || h.FrideRepo != o.FrideRepo || h.DockerDisk != "40g" || h.Caches != caches || h.SetUp.IsZero() {
		t.Errorf("fride-host.json %+v", h)
	}
	var cfg map[string]any
	if json.Unmarshal(h.Companion, &cfg) != nil || cfg["name"] != "fridolin companion (instance)" {
		t.Errorf("the companion config: %s", h.Companion)
	}
	docker, _ := os.ReadFile(filepath.Join(fake, "docker.log"))
	if !strings.Contains(string(docker), "build -f "+filepath.Join(o.Repo, "container", "sandbox", "fride.Dockerfile")+" --build-arg FRIDE_REPO=https://github.com/you/fride -t "+tag) {
		t.Errorf("docker: %s", docker)
	}
	calls := fakeCalls(t, fake)
	if !called(calls, "template", "load") || !called(calls, "settings", "set", "browser.enabled", "false") {
		t.Errorf("calls %v", calls)
	}
	if cfg, _ := LoadConfig(o.Dir); cfg.reserveMB() != 4096 {
		t.Errorf("reserve %d", cfg.reserveMB())
	}
	if !strings.Contains(out.String(), "approve it once") {
		t.Errorf("no next step in:\n%s", out)
	}
	out.Reset()
	if err := FrideHostSetup(ctx, o); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "has the template") {
		t.Errorf("setup again:\n%s", out)
	}
	r, err := FrideHostStatus(ctx, o.Dir, "")
	if err != nil || r.Host == nil || r.Declared == nil || r.Declared.Ready || !strings.Contains(r.Declared.Problem, "sbx is not installed") {
		t.Errorf("status without sbx: %+v %v", r.Declared, err)
	}
}

// Setup refuses a node in a box and an sbx that is signed out.
func TestFrideHostSetupRefuses(t *testing.T) {
	t.Parallel()
	o, fake, _ := setupFixture(t)
	writeTestFile(t, filepath.Join(fake, "signed-out"), "")
	if err := FrideHostSetup(context.Background(), o); err == nil || !strings.Contains(err.Error(), "sbx login") {
		t.Errorf("signed out: %v", err)
	}
	cfg, _ := LoadConfig(o.Dir)
	cfg.Box = &BoxConfig{}
	if err := SaveConfig(o.Dir, cfg); err != nil {
		t.Skipf("a box config does not save here: %v", err)
	}
	if err := FrideHostSetup(context.Background(), o); err == nil || !strings.Contains(err.Error(), "box") {
		t.Errorf("a box: %v", err)
	}
	if h, _ := LoadFrideHost(o.Dir); h != nil {
		t.Error("a refused setup recorded one")
	}
}

// Setup with NoBuild needs no checkout, Docker or Git: it makes sbx's
// settings fridolin's and records that the template comes from the
// core; the node then declares fride_host ready, needing the core's
// template. A node set up before keeps reading its fride-host.json.
func TestFrideHostSetupNoBuild(t *testing.T) {
	t.Parallel()
	o, fake, out := setupFixture(t)
	o.NoBuild, o.Repo, o.Docker, o.FrideRepo = true, filepath.Join(t.TempDir(), "no-checkout"), "", ""
	ctx := context.Background()
	if err := FrideHostSetup(ctx, o); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	h, err := LoadFrideHost(o.Dir)
	if err != nil || h == nil || !h.FromCore || h.Template != "" || h.companion() != nil || h.DockerDisk != "40g" {
		t.Fatalf("fride-host.json %+v %v", h, err)
	}
	if _, err := os.Stat(filepath.Join(fake, "docker.log")); err == nil {
		t.Error("NoBuild ran Docker")
	}
	calls := fakeCalls(t, fake)
	if called(calls, "template", "load") || !called(calls, "settings", "ls") || !called(calls, "policy", "ls") {
		t.Errorf("calls %v", calls)
	}
	if !strings.Contains(out.String(), "the template the core publishes (fridolin-fride-sandbox") {
		t.Errorf("setup said:\n%s", out)
	}
	cfg, _ := LoadConfig(o.Dir)
	a := &Agent{Dir: o.Dir, Config: cfg, Logf: func(string, ...any) {}, SandboxDriver: sbxCLI{path: o.Sbx, env: o.env}}
	a.inst.memory = func() (uint64, uint64, error) { return 16 << 30, 10 << 30, nil }
	c := a.frideHostRead(ctx)
	if c == nil || !c.Ready || !c.NeedsTemplate || c.Problem != "" {
		t.Errorf("declared %+v", c)
	}

	// The fride-host.json of a setup that built its template, as an older
	// node wrote it, reads as it did.
	writeTestFile(t, filepath.Join(o.Dir, frideHostFile), `{"template":"`+testTemplate+`","fride_repo":"https://github.com/you/fride","fride_commit":"`+testFridePin+`","companion":{"version":1},"docker_disk":"40g"}`)
	h, err = LoadFrideHost(o.Dir)
	if err != nil || h.FromCore || h.Template != testTemplate || string(h.companion()) != `{"version":1}` {
		t.Errorf("an older fride-host.json: %+v %v", h, err)
	}
	if tpl, pull, err := h.templateFor("ghcr.io/you/fridolin-fride-sandbox:v1"); tpl != testTemplate || pull != sbxPullNever || err != nil {
		t.Errorf("its own template: %q %q %v", tpl, pull, err)
	}
	for _, bad := range []string{`{"docker_disk":"40g"}`, `{"template":"x","docker_disk":"40g"}`} {
		writeTestFile(t, filepath.Join(o.Dir, frideHostFile), bad)
		if _, err := LoadFrideHost(o.Dir); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}
