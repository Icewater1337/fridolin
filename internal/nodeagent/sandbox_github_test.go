package nodeagent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

const testGitHubToken = "test-github-token-for-the-run"

// gitHubRules are the sandbox's rules for GitHub's hosts in the fake.
func gitHubRules(fake, name string) []string {
	var hosts []string
	for _, r := range fakeRules(fake) {
		if r.Scope == "sandbox:"+name && slices.Contains(nodeproto.SandboxGitHubHosts, r.Resources[0]) {
			hosts = append(hosts, r.Resources[0])
		}
	}
	return hosts
}

// A run with a GitHub token: the token goes to sbx on stdin as the
// sandbox's github secret, GitHub's hosts open for the run, and both go
// when it ends. A host the brain's own egress has keeps its rule. The
// token is in no argument and no log line.
func TestSandboxRunGitHub(t *testing.T) {
	t.Parallel()
	a, fake, log := sandboxAgent(t)
	folder := brainFolder(t, "rt-1")
	if _, perr := ensure(a, nodeproto.SandboxEnsureParams{Brain: "work", Folder: folder, Marker: "rt-1", APIPort: 8765, Provider: "claude",
		Egress: []string{"api.github.com"}}); perr != nil {
		t.Fatal(perr)
	}
	p := nodeproto.SandboxRunParams{Brain: "work", Run: "r1", Folder: folder, Argv: []string{"fake-agent", "echo", "0"}, Token: "run-token-secret-1234",
		APIPort: 8765, Provider: "claude", GitHubToken: testGitHubToken}
	if _, perr := sbCall(t, a, nodeproto.MethodSandboxRun, p); perr != nil {
		t.Fatal(perr)
	}
	out, end := readRun(t, a, "r1")
	if end.ExitCode != 0 {
		t.Fatalf("end: %+v", end)
	}
	if !strings.Contains(out, "github: secret ") {
		t.Errorf("the run did not have GitHub's secret:\n%s", out)
	}
	for _, h := range nodeproto.SandboxGitHubHosts {
		if !strings.Contains(out, " "+h) {
			t.Errorf("the run could not reach %s:\n%s", h, out)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(fake, "github-seen")); string(b) != testGitHubToken {
		t.Errorf("sbx's github secret held %q", b)
	}
	calls := fakeCalls(t, fake)
	if !called(calls, "secret", "set", "github", "--sandbox", "fridolin-work") {
		t.Error("the token was not stored as the sandbox's github secret")
	}
	for _, c := range calls {
		if strings.Contains(strings.Join(c.Args, " "), testGitHubToken) {
			t.Errorf("the GitHub token in sbx's arguments: %v", c.Args)
		}
	}
	if strings.Contains(log.String(), testGitHubToken) || strings.Contains(out, testGitHubToken) {
		t.Errorf("the GitHub token in the log or the run's output:\n%s", log.String())
	}
	// After the run: no secret, only the brain's own rule.
	if _, err := os.Stat(filepath.Join(fake, "github-fridolin-work")); err == nil {
		t.Error("the github secret stays after the run")
	}
	if got := gitHubRules(fake, "fridolin-work"); !slices.Equal(got, []string{"api.github.com"}) {
		t.Errorf("GitHub rules after the run: %v", got)
	}
	if st, _ := a.loadSandboxState(); st.Sandboxes["work"].GitHub != nil {
		t.Errorf("state.json keeps GitHub: %+v", st.Sandboxes["work"].GitHub)
	}

	// A run without a token gets none.
	p.Run, p.GitHubToken = "r2", ""
	if _, perr := sbCall(t, a, nodeproto.MethodSandboxRun, p); perr != nil {
		t.Fatal(perr)
	}
	if out, _ := readRun(t, a, "r2"); !strings.Contains(out, "github: api.github.com\n") {
		t.Errorf("a run without a token:\n%s", out)
	}
}

// Two runs of a brain with GitHub at once: the first to end leaves it
// open for the other, and the last closes it.
func TestSandboxGitHubOverlap(t *testing.T) {
	t.Parallel()
	a, fake, _ := sandboxAgent(t)
	p := readyBrain(t, a, "claude", "fake-agent", "wait")
	p.GitHubToken = testGitHubToken
	if _, perr := sbCall(t, a, nodeproto.MethodSandboxRun, p); perr != nil {
		t.Fatal(perr)
	}
	waitStarted(t, a, "r1")
	q := p
	q.Run, q.Argv = "r2", []string{"fake-agent", "echo", "0"}
	if _, perr := sbCall(t, a, nodeproto.MethodSandboxRun, q); perr != nil {
		t.Fatal(perr)
	}
	if out, _ := readRun(t, a, "r2"); !strings.Contains(out, "github: secret github.com") {
		t.Errorf("the second run:\n%s", out)
	}
	if got := gitHubRules(fake, "fridolin-work"); len(got) != len(nodeproto.SandboxGitHubHosts) {
		t.Errorf("GitHub closed while r1 runs: %v", got)
	}
	if _, perr := sbCall(t, a, nodeproto.MethodSandboxStop, nodeproto.SandboxStopParams{Run: "r1", GraceS: 5}); perr != nil {
		t.Fatal(perr)
	}
	readRun(t, a, "r1")
	if got := gitHubRules(fake, "fridolin-work"); len(got) != 0 {
		t.Errorf("GitHub open after both runs: %v", got)
	}
	if _, err := os.Stat(filepath.Join(fake, "github-fridolin-work")); err == nil {
		t.Error("the github secret stays after both runs")
	}
}

// An agent that stopped during a run with GitHub: the next ensure, by a
// new agent on the same state dir, closes what state.json names.
func TestSandboxGitHubLeftOver(t *testing.T) {
	t.Parallel()
	a, fake, _ := sandboxAgent(t)
	p := readyBrain(t, a, "claude", "fake-agent", "echo", "0")
	if perr := a.githubOpen(context.Background(), a.sbx(), "work", testGitHubToken); perr != nil {
		t.Fatal(perr)
	}
	if got := gitHubRules(fake, "fridolin-work"); len(got) != len(nodeproto.SandboxGitHubHosts) {
		t.Fatalf("open: %v", got)
	}
	b := &Agent{Dir: a.Dir, Config: a.Config, CoreSecrets: a.CoreSecrets, Logf: a.Logf, SandboxDriver: a.SandboxDriver}
	if res, perr := ensure(b, nodeproto.SandboxEnsureParams{Brain: "work", Folder: p.Folder, Marker: "rt-1", APIPort: 8765, Provider: "claude"}); perr != nil || res.State != nodeproto.SandboxReady {
		t.Fatalf("ensure: %+v %v", res, perr)
	}
	if got := gitHubRules(fake, "fridolin-work"); len(got) != 0 {
		t.Errorf("GitHub left open: %v", got)
	}
	if st, _ := b.loadSandboxState(); st.Sandboxes["work"].GitHub != nil {
		t.Errorf("state.json keeps GitHub: %+v", st.Sandboxes["work"].GitHub)
	}
}

// A GitHub token sbx does not take refuses the run, opens nothing, and
// its error does not show the token.
func TestSandboxGitHubRefused(t *testing.T) {
	t.Parallel()
	a, fake, log := sandboxAgent(t)
	p := readyBrain(t, a, "claude", "fake-agent", "echo", "0")
	p.GitHubToken = testGitHubToken
	if err := os.WriteFile(filepath.Join(fake, "fail-secret-github"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, perr := sbCall(t, a, nodeproto.MethodSandboxRun, p)
	if perr == nil || perr.Code != nodeproto.CodeFailed || strings.Contains(perr.Message, testGitHubToken) {
		t.Fatalf("run: %v", perr)
	}
	if got := gitHubRules(fake, "fridolin-work"); len(got) != 0 || a.getRun("r1") != nil {
		t.Errorf("rules %v, run kept %v", got, a.getRun("r1") != nil)
	}
	if strings.Contains(log.String(), testGitHubToken) {
		t.Errorf("the token in the log:\n%s", log.String())
	}
	if slices.ContainsFunc(fakeCalls(t, fake), func(c fakeCall) bool { return slices.Contains(c.Args, "setsid") }) {
		t.Error("the run started")
	}
}
