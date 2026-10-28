package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// testTemplate is the template of the fixtures' host setup.
const testTemplate = "fridolin-fride-sbx:0123456789ab"

// testClaudeToken is the made-up Claude token in the fixtures' secret
// store.
const testClaudeToken = "sk-ant-oat01-madeUp_claude-123" // personal-data:ignore (a made-up token)

// instFix is a joined node with fride_host set up: an agent on its state
// dir with the fake sbx in a folder of its own, a secret store with a
// Claude token, and a fake FrIDE for each instance a test names.
type instFix struct {
	t    *testing.T
	a    *Agent
	fake string
	log  *syncLog
	mu   sync.Mutex
	fr   map[string]*testutil.FakeFride
}

// freePortBase is a port with room for a few pairs above it, which
// nothing listened on a moment ago: away from the ephemeral ports, so
// that the tests' own connections do not take it.
func freePortBase(t *testing.T) int {
	t.Helper()
	for range 50 {
		p := 20000 + 2*rand.IntN(10000)
		if portFree(p) && portFree(p+1) {
			return p
		}
	}
	t.Fatal("no free pair of ports")
	return 0
}

func newInstFix(t *testing.T) *instFix {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("Docker Sandboxes run on macOS and Linux")
	}
	fake, dir, secrets := t.TempDir(), t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(fake, "fride-fake"), "")
	writeTestFile(t, filepath.Join(fake, "templates.json"), `["`+testTemplate+`"]`)
	if err := SaveConfig(dir, Config{Core: "core:8766", Name: "mac"}); err != nil {
		t.Fatal(err)
	}
	if err := saveFrideHost(dir, FrideHostState{Template: testTemplate, FrideRepo: "https://github.com/you/fride", FrideCommit: testFridePin,
		Companion: json.RawMessage(`{"version":1,"name":"fridolin companion (instance)","companion":{"enabled":true}}`), DockerDisk: "40g",
		Caches: filepath.Join(t.TempDir(), "caches")}); err != nil {
		t.Fatal(err)
	}
	store := hostsecrets.Files{Dir: secrets}
	if err := store.Set(hostsecrets.ClaudeOAuthToken, testClaudeToken); err != nil {
		t.Fatal(err)
	}
	f := &instFix{t: t, fake: fake, log: &syncLog{}, fr: map[string]*testutil.FakeFride{}}
	cfg, _ := LoadConfig(dir)
	f.a = &Agent{Dir: dir, Config: cfg, Logf: f.log.logf, CoreSecrets: store, FrideInstancePoll: 20 * time.Millisecond,
		SandboxDriver: sbxCLI{path: testExe(t), env: []string{"FAKE_SBX_DIR=" + fake}}}
	f.a.inst.url = f.url
	f.a.inst.wait = 2 * time.Second
	f.a.inst.portBase = freePortBase(t)
	f.a.inst.memory = func() (uint64, uint64, error) { return 16 << 30, 10 << 30, nil }
	return f
}

// fride is the fake FrIDE of the instance name, started with repos
// unless it is, with a pairing code in its sandbox.
func (f *instFix) fride(name string, repos ...string) *testutil.FakeFride {
	f.mu.Lock()
	defer f.mu.Unlock()
	ff := f.fr[name]
	if ff == nil {
		ff = testutil.StartFakeFride(f.t, repos...)
		ff.SetBaseFolders(filepath.Join(f.fake, "vmhome", FrideInstanceSandbox(name), "fride-companion", "repos"))
		f.fr[name] = ff
	}
	writeTestFile(f.t, filepath.Join(fakeVM(f.fake, FrideInstanceSandbox(name)), "code"), ff.NewCode())
	return ff
}

func (f *instFix) url(name string, _ int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ff := f.fr[name]; ff != nil {
		return ff.URL
	}
	return "http://127.0.0.1:1"
}

// vm is a file of the instance's FrIDE in the fake, "" when it is not
// there.
func (f *instFix) vm(name, file string) string {
	b, _ := os.ReadFile(filepath.Join(fakeVM(f.fake, FrideInstanceSandbox(name)), file))
	return string(b)
}

// home is a path under the instance's VM's home in the fake.
func (f *instFix) home(name, p string) string {
	return filepath.Join(f.fake, "vmhome", FrideInstanceSandbox(name), filepath.FromSlash(p))
}

// start is fride.instance.start through the agent.
func (f *instFix) start(p nodeproto.FrideInstanceStartParams) nodeproto.FrideInstance {
	f.t.Helper()
	out, perr := f.a.frideInstanceStart(context.Background(), p)
	if perr != nil {
		f.t.Fatalf("start %s: %v\n%s", p.Name, perr, f.log)
	}
	return out
}

// supervise runs the agent's supervisor until the test ends.
func (f *instFix) supervise() {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	f.a.frideInstancesSupervise(ctx, &wg)
	f.t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
}

// approve makes the agent's device approved for caps.
func (f *instFix) approve(caps nodeproto.Capabilities) {
	f.a.mu.Lock()
	f.a.state = nodeproto.State{Device: nodeproto.DeviceApproved, Commands: nodeproto.DeviceApproved, Approved: caps}
	f.a.mu.Unlock()
}

const (
	testGitHubToken = "ghs_madeUpInstallationToken1" // personal-data:ignore (a made-up token)
	testGitLabToken = "glpat-madeUpWorkToken-2"      // personal-data:ignore (a made-up token)
)

func workParams() nodeproto.FrideInstanceStartParams {
	return nodeproto.FrideInstanceStartParams{Name: "work", MemoryMB: 4096,
		Repos: []string{"https://gitlab.example.com/group/app.git"},
		Identities: []nodeproto.FrideIdentity{
			{ID: "gitlab", Kind: "gitlab", Host: "gitlab.example.com", Name: "You", Email: "you@example.com", Token: testGitLabToken},
			{ID: "github", Kind: "github", Host: "github.com", Name: "You", Email: "you@example.com", Token: testGitHubToken},
		},
		Egress: []string{"registry.example.com"},
		Git:    &nodeproto.FrideGitUser{Name: "You", Email: "you@example.com"}}
}

// A new instance gets its sandbox (memory, template, port, caches, Docker
// disk), its network rules, its git hosts' tokens as placeholders, the
// Claude login, the git identity, its repositories cloned in the VM, and
// FrIDE paired with the node and given the identities with placeholders;
// no token ever reaches sbx's arguments or the VM.
func TestFrideInstanceStartNew(t *testing.T) {
	t.Parallel()
	f := newInstFix(t)
	ff := f.fride("work")
	out := f.start(workParams())
	st, err := loadFrideInstance(f.a.Dir, "work")
	if err != nil {
		t.Fatal(err)
	}
	if out.Name != "work" || out.State != nodeproto.FrideInstanceRunning || out.MemoryMB != 4096 || out.URL != ff.URL || out.Problem != "" {
		t.Errorf("start answered %+v", out)
	}
	if !slices.Contains(out.Repos, "app") {
		t.Errorf("FrIDE's repositories: %v", out.Repos)
	}
	if st.Sandbox != "fride-work" || st.Port != f.a.inst.portBase || st.WebhookPort != st.Port+1 || !st.Run || st.Template != testTemplate {
		t.Errorf("state %+v", st)
	}
	calls := fakeCalls(t, f.fake)
	host, _ := LoadFrideHost(f.a.Dir)
	caches, _ := filepath.EvalSymlinks(filepath.Join(host.Caches, "work"))
	want := []string{"create", "--name", "fride-work", "--memory", "4096m", "-t", testTemplate, "--pull", "never",
		"-p", portMap(st.Port), "--deny-network", "downloads.claude.ai", "--deny-network", "bridge.claudeusercontent.com",
		"-e", "GOMODCACHE=" + caches + "/gomod", "-e", "GOCACHE=" + caches + "/gobuild", "-e", "npm_config_cache=" + caches + "/npm",
		"-e", "GOFLAGS=-modcacherw", "claude", caches}
	if !called(calls, want...) {
		t.Errorf("no %v in %v", want, calls)
	}
	if b, _ := os.ReadFile(filepath.Join(f.fake, "disk.log")); string(b) != "40g\n" {
		t.Errorf("Docker disk %q", b)
	}
	rules := fakeSandboxRules(f.fake, "fride-work")
	for _, r := range []string{"api.anthropic.com:443", "proxy.golang.org:443", "registry.npmjs.org:443", "registry-1.docker.io:443",
		"github.com:443", "codeload.github.com:443", "gitlab.example.com:443", "registry.example.com:443", "localhost:" + itoa(st.WebhookPort)} {
		if !slices.Contains(rules, r) {
			t.Errorf("the sandbox may not reach %s: %v", r, rules)
		}
	}
	if slices.Contains(rules, "api.openai.com:443") {
		t.Error("OpenAI without a Codex login")
	}
	// The tokens: on the host, in sbx's secrets, never in the VM.
	ids := ff.GitIdentities()
	gl, gh := ids["gitlab"], ids["github"]
	if !strings.HasPrefix(gl.Token, "fridolin-git-") || !strings.HasPrefix(gh.Token, "fridolin-git-") || gl.Token == gh.Token || gl.Host != "gitlab.example.com" {
		t.Fatalf("FrIDE's identities: %+v", ids)
	}
	if got := fakeCustom(f.fake, gl.Token); got != "gitlab.example.com="+testGitLabToken {
		t.Errorf("sbx's secret for GitLab: %q", got)
	}
	if got := fakeCustom(f.fake, gh.Token); got != "github.com,api.github.com="+testGitHubToken {
		t.Errorf("sbx's secret for GitHub: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(f.fake, "github-secret")); string(got) != testGitHubToken {
		t.Errorf("sbx's github service: %q", got)
	}
	creds := f.vm("work", "credentials")
	if !strings.Contains(creds, "https://gitlab.example.com oauth2 "+gl.Token) || !strings.Contains(creds, "https://github.com x-access-token "+gh.Token) {
		t.Errorf("git's helpers in the VM: %q", creds)
	}
	tokFile, _, _ := hostTokenFiles(filepath.Join(frideInstanceDir(f.a.Dir, "work"), frideInstanceTokens), "fride-work", "gitlab.example.com")
	if fi, err := os.Stat(tokFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the token file: %v %v", fi, err)
	}
	noTokenIn(t, f.fake, testGitLabToken, testGitHubToken)
	if fi, err := os.Stat(instanceTokenFileOf(f.a.Dir, "work")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the pairing token: %v %v", fi, err)
	}
	if got := f.vm("work", "token"); got != testClaudeToken+"\n" {
		t.Errorf("the Claude login in the VM: %q", got)
	}
	if strings.Contains(string(mustRead(t, filepath.Join(f.fake, "calls.log"))), "madeUp_claude") {
		t.Error("the Claude token is in sbx's arguments")
	}
	if f.vm("work", "git") != "You|you@example.com" || f.vm("work", "clones.log") != "https://gitlab.example.com/group/app.git\n" {
		t.Errorf("git %q, clones %q", f.vm("work", "git"), f.vm("work", "clones.log"))
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(f.vm("work", "config.json")), &cfg); err != nil || cfg["name"] != "fridolin companion (instance)" {
		t.Errorf("FrIDE's config: %v %v", cfg, err)
	}
}

// Started again with the same memory, an instance is only brought up to
// date: no new sandbox, no new pairing; rules, tokens and identities
// follow the params.
func TestFrideInstanceStartIdempotent(t *testing.T) {
	t.Parallel()
	f := newInstFix(t)
	ff := f.fride("work")
	f.start(workParams())
	p := workParams()
	p.Egress = []string{"registry2.example.com:5000"}
	p.Identities = p.Identities[:1]
	p.Identities[0].Token = testGitLabToken + "x"
	out := f.start(p)
	if out.State != nodeproto.FrideInstanceRunning || out.Problem != "" {
		t.Errorf("again: %+v", out)
	}
	creates, pairs := 0, 0
	for _, c := range fakeCalls(t, f.fake) {
		if c.Args[0] == "create" {
			creates++
		}
	}
	for _, c := range ff.Calls() {
		if c == "POST /api/companion/pair" {
			pairs++
		}
	}
	if creates != 1 || pairs != 1 {
		t.Errorf("%d creates, %d pairings", creates, pairs)
	}
	rules := fakeSandboxRules(f.fake, "fride-work")
	if !slices.Contains(rules, "registry2.example.com:5000") || slices.Contains(rules, "registry.example.com:443") || slices.Contains(rules, "github.com:443") {
		t.Errorf("rules after the second start: %v", rules)
	}
	ids := ff.GitIdentities()
	if _, ok := ids["github"]; ok || len(ids) != 1 {
		t.Errorf("FrIDE's identities: %+v", ids)
	}
	if got := fakeCustom(f.fake, ids["gitlab"].Token); got != "gitlab.example.com="+testGitLabToken+"x" {
		t.Errorf("the new token: %q", got)
	}
	if _, err := os.Stat(filepath.Join(f.fake, "github-secret")); err == nil {
		t.Error("GitHub's token stayed with sbx")
	}
	if strings.Contains(f.vm("work", "credentials"), "github.com") {
		t.Errorf("git's helper for GitHub stayed: %q", f.vm("work", "credentials"))
	}
}

// Another memory makes the sandbox anew, at the new size, with what was
// on the old VM's disk: workspaces, repositories, sessions, FrIDE's
// state, the login.
func TestFrideInstanceMemoryRemake(t *testing.T) {
	t.Parallel()
	f := newInstFix(t)
	f.fride("work")
	f.start(workParams())
	for p, body := range map[string]string{
		".claude/projects/p1/a.jsonl":        "session",
		fh + "/tasks.json":                   "[]",
		"fride-companion/workspaces/w1/x.go": "package x",
		"fride-companion/repos/app/README":   "app",
		".claude/settings.json":              `{"env":{}}`,
	} {
		writeTestFile(t, f.home("work", p), body)
	}
	p := workParams()
	p.MemoryMB = 8192
	out := f.start(p)
	if out.MemoryMB != 8192 || out.State != nodeproto.FrideInstanceRunning {
		t.Errorf("after the remake: %+v", out)
	}
	calls := fakeCalls(t, f.fake)
	if !called(calls, "rm", "-f", "fride-work") || !called(calls, "create", "--name", "fride-work", "--memory", "8192m") {
		t.Errorf("no remake in %v", calls)
	}
	for p, body := range map[string]string{
		".claude/projects/p1/a.jsonl":        "session",
		"fride-companion/workspaces/w1/x.go": "package x",
		"fride-companion/repos/app/README":   "app",
	} {
		if got, _ := os.ReadFile(f.home("work", p)); string(got) != body {
			t.Errorf("%s after the remake: %q", p, got)
		}
	}
	if _, err := os.Stat(filepath.Join(frideInstanceDir(f.a.Dir, "work"), frideInstanceCarryDir)); err == nil {
		t.Error("the carry stayed")
	}
	if st, _ := loadFrideInstance(f.a.Dir, "work"); st.MemoryMB != 8192 || !st.Run {
		t.Errorf("state %+v", st)
	}
	// The new sandbox has its rules and tokens again.
	if rules := fakeSandboxRules(f.fake, "fride-work"); !slices.Contains(rules, "gitlab.example.com:443") {
		t.Errorf("rules after the remake: %v", rules)
	}
}

// stop clears run and stops the VM; remove saves the history, removes
// the sandbox and the instance's state, pairing and tokens, and keeps the
// history, which the next sandbox of that name gets.
func TestFrideInstanceStopRemove(t *testing.T) {
	t.Parallel()
	f := newInstFix(t)
	f.fride("work")
	f.start(workParams())
	ctx := context.Background()
	res, perr := f.a.frideInstanceCall(ctx, nodeproto.MethodFrideInstanceStop, json.RawMessage(`{"name":"work"}`))
	if perr != nil {
		t.Fatal(perr)
	}
	if in := res.(nodeproto.FrideInstance); in.State != nodeproto.FrideInstanceStopped {
		t.Errorf("stop answered %+v", in)
	}
	if st, _ := loadFrideInstance(f.a.Dir, "work"); st.Run {
		t.Error("stop left run set")
	}
	if !called(fakeCalls(t, f.fake), "stop", "fride-work") || f.vm("work", "stops.log") != "stop\n" {
		t.Error("stop did not end FrIDE and stop the VM")
	}
	res, _ = f.a.frideInstanceCall(ctx, nodeproto.MethodFrideInstanceStatus, nil)
	if l := res.(nodeproto.FrideInstances).Instances; len(l) != 1 || l[0].State != nodeproto.FrideInstanceStopped || l[0].URL != "" {
		t.Errorf("status after stop: %+v", l)
	}
	writeTestFile(t, f.home("work", ".claude/projects/p1/a.jsonl"), "session")
	writeTestFile(t, f.home("work", "fride-companion/workspaces/w1/x.go"), "package x")
	if _, perr := f.a.frideInstanceCall(ctx, nodeproto.MethodFrideInstanceRemove, json.RawMessage(`{"name":"work"}`)); perr != nil {
		t.Fatal(perr)
	}
	hist := filepath.Join(frideInstanceDir(f.a.Dir, "work"), frideInstanceHistoryDir)
	if got, _ := os.ReadFile(filepath.Join(hist, ".claude", "projects", "p1", "a.jsonl")); string(got) != "session" {
		t.Errorf("the history: %q", got)
	}
	if _, err := os.Stat(filepath.Join(hist, "fride-companion", "workspaces")); err == nil {
		t.Error("remove saved the workspaces")
	}
	if !called(fakeCalls(t, f.fake), "rm", "-f", "fride-work") {
		t.Error("the sandbox stayed")
	}
	for _, p := range []string{frideInstanceFile, frideInstanceTokenFile, frideInstanceTokens} {
		if _, err := os.Stat(filepath.Join(frideInstanceDir(f.a.Dir, "work"), p)); err == nil {
			t.Errorf("%s stayed", p)
		}
	}
	res, _ = f.a.frideInstanceCall(ctx, nodeproto.MethodFrideInstanceStatus, nil)
	if l := res.(nodeproto.FrideInstances).Instances; len(l) != 0 {
		t.Errorf("status after remove: %+v", l)
	}
	if _, perr := f.a.frideInstanceCall(ctx, nodeproto.MethodFrideInstanceStop, json.RawMessage(`{"name":"work"}`)); perr == nil || perr.Code != nodeproto.CodeNotFound {
		t.Errorf("stop of a removed instance: %v", perr)
	}
	// Made again, it gets the history back.
	f.fride("work")
	f.start(workParams())
	if got, _ := os.ReadFile(f.home("work", ".claude/projects/p1/a.jsonl")); string(got) != "session" {
		t.Errorf("the history in the new sandbox: %q", got)
	}
}

// A start refuses what an instance cannot be, before it touches sbx.
func TestFrideInstanceStartRefuses(t *testing.T) {
	t.Parallel()
	f := newInstFix(t)
	ctx := context.Background()
	for name, mutate := range map[string]func(*nodeproto.FrideInstanceStartParams){
		"too little memory":         func(p *nodeproto.FrideInstanceStartParams) { p.MemoryMB = 512 },
		"native":                    func(p *nodeproto.FrideInstanceStartParams) { p.Name = "native" },
		"two identities on a host":  func(p *nodeproto.FrideInstanceStartParams) { p.Identities[1].Host = "gitlab.example.com" },
		"an egress host with a URL": func(p *nodeproto.FrideInstanceStartParams) { p.Egress = []string{"https:x"} },
	} {
		p := workParams()
		mutate(&p)
		if _, perr := f.a.frideInstanceStart(ctx, p); perr == nil || perr.Code != nodeproto.CodeBadRequest {
			t.Errorf("%s: %v", name, perr)
		}
	}
	if calls := fakeCalls(t, f.fake); len(calls) != 0 {
		t.Errorf("sbx was asked: %v", calls)
	}
	writeTestFile(t, filepath.Join(f.fake, "templates.json"), `[]`)
	if _, perr := f.a.frideInstanceStart(ctx, workParams()); perr == nil || perr.Code != nodeproto.CodeUnavailable || !strings.Contains(perr.Message, "no template") {
		t.Errorf("without the template: %v", perr)
	}
	if err := os.Remove(filepath.Join(f.a.Dir, frideHostFile)); err != nil {
		t.Fatal(err)
	}
	if _, perr := f.a.frideInstanceStart(ctx, workParams()); perr == nil || !strings.Contains(perr.Message, "fride host setup") {
		t.Errorf("without setup: %v", perr)
	}
}

// A start whose FrIDE does not answer fails the instance: the VM stops,
// run is cleared, and the instance reports why until the next start.
func TestFrideInstanceStartFails(t *testing.T) {
	t.Parallel()
	f := newInstFix(t)
	_, perr := f.a.frideInstanceStart(context.Background(), workParams())
	if perr == nil || !strings.Contains(perr.Message, "does not answer") {
		t.Fatalf("start without FrIDE: %v", perr)
	}
	st, _ := loadFrideInstance(f.a.Dir, "work")
	if st == nil || st.Run || st.Problem == "" {
		t.Fatalf("state %+v", st)
	}
	if !called(fakeCalls(t, f.fake), "stop", "fride-work") {
		t.Error("the VM runs on")
	}
	res, _ := f.a.frideInstanceCall(context.Background(), nodeproto.MethodFrideInstanceStatus, nil)
	if l := res.(nodeproto.FrideInstances).Instances; len(l) != 1 || l[0].State != nodeproto.FrideInstanceFailed || l[0].Problem == "" {
		t.Errorf("status: %+v", l)
	}
}

// The agent keeps a running instance's FrIDE running: it holds the
// serve script's exec with the instance's ports, starts it again when it
// ends, and lets go of it once run is cleared.
func TestFrideInstanceSupervise(t *testing.T) {
	t.Parallel()
	f := newInstFix(t)
	f.fride("work")
	f.supervise()
	f.start(workParams())
	st, _ := loadFrideInstance(f.a.Dir, "work")
	waitFor(t, "FrIDE to run", func() bool { return lines(f.vm("work", "serves.log")) == 1 })
	if got := f.vm("work", "serves.log"); got != itoa(st.Port)+" "+itoa(st.WebhookPort)+"\n" {
		t.Errorf("serve's arguments: %q", got)
	}
	writeTestFile(t, filepath.Join(fakeVM(f.fake, "fride-work"), "crash"), "")
	waitFor(t, "FrIDE to run again", func() bool { return lines(f.vm("work", "serves.log")) == 2 })
	if !strings.Contains(f.log.String(), "Started FrIDE (42)") || !strings.Contains(f.log.String(), "exec ended") {
		t.Errorf("log:\n%s", f.log)
	}
	if _, perr := f.a.frideInstanceCall(context.Background(), nodeproto.MethodFrideInstanceStop, json.RawMessage(`{"name":"work"}`)); perr != nil {
		t.Fatal(perr)
	}
	waitFor(t, "FrIDE to end", func() bool {
		_, err := os.Stat(filepath.Join(fakeVM(f.fake, "fride-work"), "running"))
		return err != nil
	})
	time.Sleep(200 * time.Millisecond)
	if n := lines(f.vm("work", "serves.log")); n != 2 {
		t.Errorf("FrIDE started %d times; after stop it stays stopped", n)
	}
}

func lines(s string) int { return strings.Count(s, "\n") }

// The fride.* methods go to the FrIDE their params name: "" the node's
// own, refused without one; a name that instance's, while it runs; news
// from an instance's webhook reaches the core as that instance's,
// whatever its body says.
func TestFrideInstanceRouting(t *testing.T) {
	t.Parallel()
	f := newInstFix(t)
	ff := f.fride("work", "app")
	f.supervise()
	f.start(workParams())
	f.a.frideHostRefresh(context.Background(), true)
	f.approve(nodeproto.Capabilities{FrideHost: &nodeproto.FrideHostCap{}})
	ctx := context.Background()
	call := func(method, params string) (nodeproto.Response, json.RawMessage) {
		resp := f.a.handle(ctx, nodeproto.Request{Method: method, Params: json.RawMessage(params)})
		return resp, resp.Result
	}
	resp, raw := call(nodeproto.MethodFrideTask, `{"instance":"work","message":"fix the build","ref":"r1"}`)
	if resp.Error != nil {
		t.Fatalf("fride.task to work: %v", resp.Error)
	}
	var th nodeproto.FrideThread
	if err := json.Unmarshal(raw, &th); err != nil || th.ThreadID == 0 {
		t.Errorf("thread %s %v", raw, err)
	}
	if !slices.Contains(ff.Calls(), "POST /api/companion/tasks") {
		t.Errorf("work's FrIDE got %v", ff.Calls())
	}
	for params, code := range map[string]string{
		`{"message":"x"}`:                     nodeproto.CodeDenied,
		`{"instance":"home","message":"x"}`:   nodeproto.CodeNotFound,
		`{"instance":"Work","message":"x"}`:   nodeproto.CodeBadRequest,
		`{"instance":"native","message":"x"}`: nodeproto.CodeBadRequest,
	} {
		if resp, _ := call(nodeproto.MethodFrideTask, params); resp.Error == nil || resp.Error.Code != code {
			t.Errorf("%s: %v, want %s", params, resp.Error, code)
		}
	}
	// The webhook: news is work's, also when its body names another.
	f.a.fride.init()
	ff.Emit(map[string]any{"event": "question", "thread_id": th.ThreadID, "task_id": "t1", "text": "which branch?", "instance": "fridolin"})
	select {
	case ev := <-f.a.fride.events:
		if ev.Instance != "work" || ev.ThreadID != th.ThreadID || ev.Event != "question" {
			t.Errorf("news %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no news from the instance")
	}
	if code := ff.EmitUnsigned(map[string]any{"event": "done", "thread_id": th.ThreadID}); code != 401 {
		t.Errorf("unsigned news: %d", code)
	}
	// A stopped instance is not reached.
	if _, perr := f.a.frideInstanceCall(ctx, nodeproto.MethodFrideInstanceStop, json.RawMessage(`{"name":"work"}`)); perr != nil {
		t.Fatal(perr)
	}
	if resp, _ := call(nodeproto.MethodFrideStatus, `{"instance":"work","thread_id":1}`); resp.Error == nil || resp.Error.Code != nodeproto.CodeUnavailable {
		t.Errorf("a stopped instance: %v", resp.Error)
	}
	// A bundle staged on the node needs no FrIDE.
	if resp, _ := call(nodeproto.MethodFrideWrite, `{"instance":"work","offset":0,"data":"aGk="}`); resp.Error != nil {
		t.Errorf("staging a bundle for a stopped instance: %v", resp.Error)
	}
	// Without fride_host approved, an instance is not reached at all.
	f.approve(nodeproto.Capabilities{Fride: &nodeproto.FrideCap{}})
	if resp, _ := call(nodeproto.MethodFrideTask, `{"instance":"work","message":"x"}`); resp.Error == nil || resp.Error.Code != nodeproto.CodeDenied {
		t.Errorf("fride_host not approved: %v", resp.Error)
	}
}

// fride.repo_clone and repo_new with an instance work in its VM's base
// folder, and FrIDE reads it again.
func TestFrideInstanceRepos(t *testing.T) {
	t.Parallel()
	f := newInstFix(t)
	f.fride("work")
	f.start(workParams())
	ctx := context.Background()
	out, perr := f.a.frideCall(ctx, nodeproto.MethodFrideRepoClone, json.RawMessage(`{"instance":"work","name":"lib","url":"https://gitlab.example.com/group/lib.git"}`))
	if perr != nil {
		t.Fatal(perr)
	}
	r := out.(nodeproto.FrideRepo)
	if r.Path != fakeVMHome+"/fride-companion/repos/lib" || !r.Rescanned || !slices.Contains(r.Repos, "lib") {
		t.Errorf("clone: %+v", r)
	}
	if _, perr := f.a.frideCall(ctx, nodeproto.MethodFrideRepoClone, json.RawMessage(`{"instance":"work","name":"lib","url":"https://gitlab.example.com/group/lib.git"}`)); perr == nil || perr.Code != nodeproto.CodeBadRequest {
		t.Errorf("clone again: %v", perr)
	}
	ssh := json.RawMessage(`{"instance":"work","name":"x","url":"git@gitlab.example.com:group/x.git"}`) // personal-data:ignore (an scp-like git URL)
	if _, perr := f.a.frideCall(ctx, nodeproto.MethodFrideRepoClone, ssh); perr == nil || perr.Code != nodeproto.CodeBadRequest {
		t.Errorf("an ssh URL: %v", perr)
	}
	out, perr = f.a.frideCall(ctx, nodeproto.MethodFrideRepoNew, json.RawMessage(`{"instance":"work","name":"tool","github":true}`))
	if perr != nil {
		t.Fatal(perr)
	}
	if r := out.(nodeproto.FrideRepo); r.GitHubURL != "https://github.com/you/tool" || !slices.Contains(r.Repos, "tool") {
		t.Errorf("new: %+v", r)
	}
}

// The node declares fride_host once it is set up: sbx's version and
// readiness, the machine's memory, what it keeps (6144 MB, or node.json's
// reserve_mb), and the instances; a box never does.
func TestFrideHostDeclared(t *testing.T) {
	t.Parallel()
	f := newInstFix(t)
	ctx := context.Background()
	f.fride("work")
	f.start(workParams())
	if !f.a.frideHostRefresh(ctx, true) {
		t.Error("the first read changed nothing")
	}
	c := f.a.declared().FrideHost
	if c == nil {
		t.Fatal("no fride_host")
	}
	if c.Version != "v0.50.0" || !c.Ready || c.Problem != "" || c.TotalMemoryMB != 16384 || c.AvailableMemoryMB != 10240 || c.ReserveMB != DefaultFrideReserveMB {
		t.Errorf("fride_host %+v", c)
	}
	if len(c.Instances) != 1 || c.Instances[0].Name != "work" || c.Instances[0].State != nodeproto.FrideInstanceRunning || c.Instances[0].MemoryMB != 4096 {
		t.Errorf("instances %+v", c.Instances)
	}
	if err := c.Check(); err != nil {
		t.Error(err)
	}
	if f.a.frideHostRefresh(ctx, false) {
		t.Error("read again within a minute")
	}
	// host setup --reserve-mb writes node.json under the running agent,
	// whose next declaration takes it.
	if err := SaveConfig(f.a.Dir, Config{Core: "core:8766", Name: "mac", FrideHost: &FrideHostConfig{ReserveMB: 2048}}); err != nil {
		t.Fatal(err)
	}
	if !f.a.frideHostRefresh(ctx, true) || f.a.frideHostDeclared().ReserveMB != 2048 {
		t.Error("node.json's reserve_mb")
	}
	writeTestFile(t, filepath.Join(f.fake, "templates.json"), `[]`)
	f.a.frideHostRefresh(ctx, true)
	if c := f.a.frideHostDeclared(); c.Ready || !strings.Contains(c.Problem, "template") {
		t.Errorf("without the template: %+v", c)
	}
	f.a.Config.Box = &BoxConfig{}
	f.a.frideHostRefresh(ctx, true)
	if f.a.frideHostDeclared() != nil {
		t.Error("a box declares fride_host")
	}
	f.a.Config.Box = nil
	if err := os.Remove(filepath.Join(f.a.Dir, frideHostFile)); err != nil {
		t.Fatal(err)
	}
	f.a.frideHostRefresh(ctx, true)
	if f.a.frideHostDeclared() != nil {
		t.Error("fride_host without setup")
	}
}

func TestParseVMStat(t *testing.T) {
	t.Parallel()
	out := []byte(`Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                              100.
Pages active:                            299816.
Pages inactive:                          200.
Pages speculative:                         30.
Pages throttled:                              0.
Pages wired down:                        152939.
Pages purgeable:                           4.
"Translation faults":               27318000279.
`)
	if got, ok := parseVMStat(out); !ok || got != 334*16384 {
		t.Errorf("%d %v", got, ok)
	}
	if _, ok := parseVMStat([]byte("Pages free: 1.\n")); ok {
		t.Error("no page size")
	}
}

// The network rules: the baseline, the webhook, the identities' hosts
// (all of GitHub's for GitHub), OpenAI with a Codex login, the egress;
// an egress that is not a host is refused.
func TestFrideInstanceRules(t *testing.T) {
	t.Parallel()
	rules, err := frideInstanceRules(17851, []nodeproto.FrideIdentity{{ID: "gl", Host: "GitLab.example.com:8443"}}, []string{"*.example.org", "Reg.example.com"}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{"localhost:17851", "gitlab.example.com:8443", "*.example.org:443", "reg.example.com:443", "api.openai.com:443", "api.anthropic.com:443"} {
		if !slices.Contains(rules, r) {
			t.Errorf("no %s in %v", r, rules)
		}
	}
	if slices.Contains(rules, "github.com:443") {
		t.Error("GitHub without a GitHub identity")
	}
	for _, bad := range []string{"https://x.example.com", "x.example.com/path", "x.example.com:99999", "a b"} {
		if _, err := frideInstanceRules(1, nil, []string{bad}, false); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
}

// fakeSandboxRules are the hosts the fake sbx allows sandbox.
func fakeSandboxRules(fake, sandbox string) []string {
	var out []string
	for _, r := range fakeRules(fake) {
		if r.Scope == "sandbox:"+sandbox && r.Decision == "allow" {
			out = append(out, r.Resources...)
		}
	}
	return out
}

// noTokenIn fails when any of tokens is in sbx's arguments or a file of
// the fake VM.
func noTokenIn(t *testing.T, fake string, tokens ...string) {
	t.Helper()
	calls := string(mustRead(t, filepath.Join(fake, "calls.log")))
	for _, tok := range tokens {
		if strings.Contains(calls, tok) {
			t.Errorf("a token is in sbx's arguments")
		}
	}
	for _, root := range []string{filepath.Join(fake, "vmhome"), filepath.Join(fake, "fride")} {
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			b, _ := os.ReadFile(p)
			for _, tok := range tokens {
				if bytes.Contains(b, []byte(tok)) {
					t.Errorf("a token is in the VM's %s", p)
				}
			}
			return nil
		})
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func itoa(n int) string { return strconv.Itoa(n) }

func portMap(port int) string { return "127.0.0.1:" + itoa(port) + ":" + itoa(port) }

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// What did not work in a start that did is the instance's problem while
// it runs: no Claude login, a repository that did not clone.
func TestFrideInstanceNotes(t *testing.T) {
	t.Parallel()
	f := newInstFix(t)
	f.a.CoreSecrets = hostsecrets.Files{Dir: t.TempDir()}
	f.fride("work")
	writeTestFile(t, filepath.Join(fakeVM(f.fake, "fride-work"), "fail-clone"), "fatal: repository not found\n")
	out := f.start(workParams())
	if out.State != nodeproto.FrideInstanceRunning || !strings.Contains(out.Problem, "no Claude login") || !strings.Contains(out.Problem, "repository not found") {
		t.Errorf("start answered %+v", out)
	}
	f.a.frideHostRefresh(context.Background(), true)
	if c := f.a.frideHostDeclared(); c == nil || len(c.Instances) != 1 || c.Instances[0].Problem != out.Problem {
		t.Errorf("declared %+v", c)
	}
}

// On a node that built no template, a new instance's sandbox is made
// from the template the start names, which sbx pulls, with the
// template's own FrIDE config; a start that names none makes no new
// sandbox, but brings a made one up to date. A new template from the
// core makes a stopped instance's sandbox anew, never a running one's.
func TestFrideInstanceTemplateFromCore(t *testing.T) {
	t.Parallel()
	f := newInstFix(t)
	if err := saveFrideHost(f.a.Dir, FrideHostState{FromCore: true, DockerDisk: "40g"}); err != nil {
		t.Fatal(err)
	}
	const v1, v2 = "ghcr.io/you/fridolin-fride-sandbox:t-0123456789ab", "ghcr.io/you/fridolin-fride-sandbox:t-ba9876543210"
	p := nodeproto.FrideInstanceStartParams{Name: "work", MemoryMB: 4096}
	if _, perr := f.a.frideInstanceStart(context.Background(), p); perr == nil || !strings.Contains(perr.Message, "the core sends none") {
		t.Fatalf("a start without a template: %v", perr)
	}
	f.fride("work")
	p.Template = v1
	f.start(p)
	st, _ := loadFrideInstance(f.a.Dir, "work")
	if st.Template != v1 || st.Caches != "" {
		t.Errorf("state %+v", st)
	}
	if calls := fakeCalls(t, f.fake); !called(calls, "create", "--name", "fride-work", "--memory", "4096m", "-t", v1, "--pull", "missing") {
		t.Errorf("no create from %s in %v", v1, calls)
	}
	if cfg := f.vm("work", "config.json"); !strings.Contains(cfg, `"from":"the template"`) {
		t.Errorf("FrIDE's config: %s", cfg)
	}

	// A running instance keeps its sandbox; a start without a template
	// still brings it up to date.
	p.Template = v2
	f.start(p)
	p.Template = ""
	f.start(p)
	if n := strings.Count(string(mustRead(t, filepath.Join(f.fake, "disk.log"))), "\n"); n != 1 {
		t.Errorf("%d sandboxes made", n)
	}
	// Stopped, it is made anew from the new one.
	if _, err := f.a.instanceOps().stop(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	f.fride("work")
	p.Template = v2
	f.start(p)
	if st, _ := loadFrideInstance(f.a.Dir, "work"); st.Template != v2 {
		t.Errorf("after the new template: %+v", st)
	}
	if calls := fakeCalls(t, f.fake); !called(calls, "create", "--name", "fride-work", "--memory", "4096m", "-t", v2, "--pull", "missing") {
		t.Errorf("no create from %s in %v", v2, calls)
	}
	if !strings.Contains(f.log.String(), "making the FrIDE instance work anew (a new template, "+v2+")") {
		t.Errorf("log:\n%s", f.log)
	}
}

// The node's own template wins over the one the start names.
func TestFrideInstanceOwnTemplateWins(t *testing.T) {
	t.Parallel()
	f := newInstFix(t)
	f.fride("work")
	p := workParams()
	p.Template = "ghcr.io/you/fridolin-fride-sandbox:v1"
	f.start(p)
	if calls := fakeCalls(t, f.fake); !called(calls, "create", "--name", "fride-work", "--memory", "4096m", "-t", testTemplate, "--pull", "never") {
		t.Errorf("not made from the node's own template: %v", calls)
	}
}
