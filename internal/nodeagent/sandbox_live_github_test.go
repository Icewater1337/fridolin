package nodeagent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// liveSandbox sets up the machine's real sbx and Docker as `fridolin node
// sandbox setup` does, in a state dir of the test's, and puts a linux
// fridolin built here in place. Only with FRIDOLIN_LIVE_SBX=1 (never in
// CI). It sets sbx's global settings as fridolin owns them.
func liveSandbox(ctx context.Context, t *testing.T) *Agent {
	t.Helper()
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
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
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
	return a
}

// liveFolder is a brain folder that holds marker, and removes brain's
// sandbox at the end.
func liveFolder(t *testing.T, a *Agent, brain, marker string) string {
	t.Helper()
	folder, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, nodeproto.SandboxMarkerFile), []byte(marker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, perr := a.sandboxRemove(context.Background(), nodeproto.SandboxRemoveParams{Brain: brain}); perr != nil {
			t.Errorf("remove: %v", perr)
		}
	})
	return folder
}

// hostGitHub is the host's own gh login: its token, never printed, and
// its user.
func hostGitHub(t *testing.T) (token, login string) {
	t.Helper()
	tok, err := exec.Command("gh", "auth", "token").Output()
	if err != nil || len(bytes.TrimSpace(tok)) == 0 {
		t.Skip("gh is not logged in on this machine")
	}
	user, err := exec.Command("gh", "api", "user", "--jq", ".login").Output()
	if err != nil {
		t.Skip("gh api user does not answer here")
	}
	return string(bytes.TrimSpace(tok)), string(bytes.TrimSpace(user))
}

// liveAPI is the core's API on the host's loopback, which answers whether
// a call carried the run token: what sbx's proxy put in for the VM's
// placeholder.
func liveAPI(t *testing.T, runToken string) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+runToken {
			_, _ = w.Write([]byte("arrived"))
			return
		}
		_, _ = w.Write([]byte("missing"))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// liveModule is fridolin's module path, from go.mod: its repository is
// a private one on GitHub.
func liveModule(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if mod, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(mod)
		}
	}
	t.Fatal("go.mod names no module")
	return ""
}

// liveRun starts script with sh as run p; read it with readRun.
func liveRun(ctx context.Context, t *testing.T, a *Agent, p nodeproto.SandboxRunParams, script string) {
	t.Helper()
	p.Argv = []string{"sh", "-c", script}
	if _, perr := a.sandboxRunStart(ctx, p); perr != nil {
		t.Fatalf("run %s: %v", p.Run, perr)
	}
}

// liveScan is what a scan of the VM as root reads: every process's
// environment and command line, and the files where a credential could
// land.
const liveScan = `for p in /proc/[0-9]*; do cat $p/environ $p/cmdline 2>/dev/null; echo; done
find /home /root /etc /run /tmp /var/tmp /opt -type f -size -20M 2>/dev/null | while read -r f; do cat "$f" 2>/dev/null; echo; done`

// TestSandboxLiveGitHub runs against the machine's real sbx with the
// owner's own gh login (FRIDOLIN_LIVE_SBX=1; the token goes to sbx on
// stdin and is never printed). A run with the token reaches GitHub: gh's
// API and git over HTTPS on a private repository. A scan of the VM finds
// no token, the run token reaches the host's API through sbx's proxy with
// no prompt, and GitHub is closed after the run. A session written in the
// VM survives a rebuild of the sandbox.
func TestSandboxLiveGitHub(t *testing.T) {
	// Serial: it drives the machine's one sbx, and starts a VM.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	a := liveSandbox(ctx, t)
	token, login := hostGitHub(t)
	const runToken = "run-token-live-github"
	port := liveAPI(t, runToken)
	folder := liveFolder(t, a, "livegh", "rt-live")
	name := nodeproto.SandboxName("livegh")
	p := nodeproto.SandboxEnsureParams{Brain: "livegh", Folder: folder, Marker: "rt-live", APIPort: port, Provider: "claude", MemoryMB: 2048}
	if _, perr := a.sandboxEnsure(ctx, p); perr != nil {
		t.Fatalf("ensure: %v", perr)
	}
	rp := nodeproto.SandboxRunParams{Brain: "livegh", Run: "gh1", Folder: folder, Token: runToken, APIPort: port, Provider: "claude", TimeoutS: 300, GitHubToken: token}
	liveRun(ctx, t, a, rp, `echo "gh token: ${GH_TOKEN:+set}"
echo "login: $(gh api user --jq .login 2>&1 | head -1)"
echo "remote: $(git ls-remote "https://`+liveModule(t)+`" HEAD 2>&1 | head -1)"
echo "api: $(curl -s -m 20 -H "Authorization: Bearer $(cat "$FRIDOLIN_BRAIN_TOKEN_FILE")" "$FRIDOLIN_API/check")"
mkdir -p ~/.claude/projects/live && echo '{"live":1}' > ~/.claude/projects/live/session.jsonl
echo started
for i in $(seq 240); do [ -f scan-done ] && break; sleep 0.5; done`)
	waitStarted(t, a, "gh1")
	scan, _, err := a.sbx().run(ctx, strings.NewReader(liveScan), nil, "exec", "-i", "-u", "root", name, "sh")
	if err != nil && len(scan) == 0 {
		t.Fatalf("the scan: %v", err)
	}
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	t.Logf("scanned %d bytes of the VM", len(scan))
	if bytes.Contains(scan, []byte(token)) || bytes.Contains(scan, []byte(basic)) {
		t.Error("the GitHub token is in the VM")
	}
	if !bytes.Contains(scan, []byte("sbxproxymanaged")) {
		t.Error("the scan did not see sbx's placeholder: it read too little")
	}
	if err := os.WriteFile(filepath.Join(folder, "scan-done"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out, end := readRun(t, a, "gh1")
	if end.ExitCode != 0 || strings.Contains(out, token) {
		t.Fatalf("run: exit %d, stderr %q", end.ExitCode, end.StderrTail)
	}
	for _, want := range []string{"gh token: set", "login: " + login + "\n", "api: arrived"} {
		if !strings.Contains(out, want) {
			t.Errorf("the run's output lacks %q:\n%s", want, out)
		}
	}
	if !regexp.MustCompile(`remote: [0-9a-f]{40}\tHEAD`).MatchString(out) {
		t.Errorf("git ls-remote did not answer:\n%s", out)
	}
	if secret, hosts := liveGitHubOpen(ctx, t, a, name); secret || len(hosts) > 0 {
		t.Errorf("after the run: github secret %v, rules %v", secret, hosts)
	}

	// A rebuild keeps the session; the next run has no GitHub.
	p.MemoryMB = 2560
	if res, perr := a.sandboxEnsure(ctx, p); perr != nil || res.(nodeproto.SandboxEnsureResult).State != nodeproto.SandboxRecreated {
		t.Fatalf("rebuild: %+v %v", res, perr)
	}
	rp.Run, rp.GitHubToken = "gh2", ""
	liveRun(ctx, t, a, rp, `cat ~/.claude/projects/live/session.jsonl
gh api user --jq .login >/dev/null 2>&1 && echo "github: open" || echo "github: closed"`)
	out, _ = readRun(t, a, "gh2")
	if !strings.Contains(out, `{"live":1}`) || !strings.Contains(out, "github: closed") {
		t.Errorf("after the rebuild:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(a.sandboxHistoryDir("livegh", "claude"), "live", "session.jsonl")); err != nil {
		t.Errorf("the session is not on the host: %v", err)
	}
}

// liveGitHubOpen reports what of GitHub sbx keeps for sandbox name: its
// github secret, and its rules for GitHub's hosts.
func liveGitHubOpen(ctx context.Context, t *testing.T, a *Agent, name string) (secret bool, hosts []string) {
	t.Helper()
	d := a.sbx()
	out, _, err := d.run(ctx, nil, nil, "secret", "ls", "--sandbox", name, "--json")
	if err != nil {
		t.Fatalf("sbx secret ls: %v", err)
	}
	secret = bytes.Contains(out, []byte(`"github"`))
	out, _, err = d.run(ctx, nil, nil, "policy", "ls", name, "--json")
	var pol struct {
		Rules []fakeRule `json:"rules"`
	}
	if err != nil || json.Unmarshal(out, &pol) != nil {
		t.Fatalf("sbx policy ls: %v", err)
	}
	for _, r := range pol.Rules {
		for _, h := range r.Resources {
			if r.Scope == "sandbox:"+name && slices.Contains(nodeproto.SandboxGitHubHosts, strings.TrimSuffix(h, ":443")) {
				hosts = append(hosts, h)
			}
		}
	}
	return secret, hosts
}
