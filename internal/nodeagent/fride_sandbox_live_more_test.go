package nodeagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// liveSandboxName is a scratch sandbox's name, never fride-sandbox's.
func liveSandboxName(t *testing.T) string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return FrideSandboxName + "-live" + hex.EncodeToString(b)
}

// liveExec runs the bash script with args in o's sandbox (as user, ""
// for the VM's), stdin on its stdin, and returns what it printed.
func liveExec(ctx context.Context, t *testing.T, o FrideSandboxOptions, user, stdin, script string, args ...string) map[string]string {
	t.Helper()
	argv := []string{"exec", "-i"}
	if user != "" {
		argv = append(argv, "-u", user)
	}
	argv = append(argv, o.sandbox(), "bash", "-c", script, "live")
	out, stderr, err := o.sbx().run(ctx, strings.NewReader(stdin), nil, append(argv, args...)...)
	if err != nil {
		t.Fatalf("in the sandbox: %v (%s)", err, firstLine(stderr))
	}
	return parseKeyValues(out)
}

// liveSetup is what the live tests of the sandboxed FrIDE need: sbx and
// Docker, a fridolin checkout, port 17850 free; nil when one is missing.
func liveSetup(t *testing.T) *FrideSandboxOptions {
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
	if ln, err := net.Listen("tcp", "127.0.0.1:17850"); err != nil {
		t.Skipf("127.0.0.1:17850 is taken: %v", err)
	} else {
		ln.Close()
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
	dir, scratch := t.TempDir(), t.TempDir()
	if err := SaveConfig(dir, Config{Core: "core.invalid:8766", Name: "live-sbx", Service: FrideSandboxService}); err != nil {
		t.Fatal(err)
	}
	return &FrideSandboxOptions{Dir: dir, Sbx: sbx, Docker: docker, Repo: repo, FrideRepo: frideRepo,
		Base: filepath.Join(scratch, "live-repos"), Caches: filepath.Join(scratch, "live-caches"),
		GitName: "Live Test", GitEmail: "you@example.com", Out: tlogWriter{t}, name: liveSandboxName(t)}
}

// hostOut is what a program on the host prints, trimmed.
func hostOut(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	return strings.TrimSpace(string(out))
}

// TestFrideSandboxLiveGitHubCachesHistory runs the sandboxed FrIDE's
// GitHub, per-host tokens, Docker disk, shared caches, history and
// template change against the machine's real sbx, Docker and gh, only
// with FRIDOLIN_LIVE_SBX=1 (never in CI), in a scratch state dir, base
// folder, caches folder and sandbox; it removes its sandbox and
// templates. The host's gh must be logged in.
func TestFrideSandboxLiveGitHubCachesHistory(t *testing.T) {
	// Serial: it drives the machine's one sbx, port 17850 and a VM.
	po := liveSetup(t)
	o := *po
	ghTok, err := exec.Command("gh", "auth", "token", "--hostname", githubHost).Output()
	if err != nil || len(strings.TrimSpace(string(ghTok))) == 0 {
		t.Skip("gh is not logged in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()
	tags := map[string]bool{}
	tag, _ := FrideSandboxTemplateTag(o.Repo)
	tags[tag] = true
	t.Cleanup(func() {
		ctx := context.Background()
		o.SkipHistory = true
		if err := FrideSandboxRemove(ctx, o); err != nil {
			t.Errorf("rm: %v", err)
		}
		for tag := range tags {
			if _, stderr, err := o.sbx().run(ctx, nil, nil, "template", "rm", "--force", tag); err != nil {
				t.Logf("template rm %s: %v", tag, sbxError(err, stderr))
			}
		}
	})
	start := time.Now()
	if err := FrideSandboxCreate(ctx, o); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Logf("created %s in %s", o.sandbox(), time.Since(start).Round(time.Second))
	if err := FrideSandboxGitHub(ctx, o, string(ghTok)); err != nil {
		t.Fatalf("github: %v", err)
	}
	liveGitHub(ctx, t, o, string(ghTok))
	liveOtherHosts(ctx, t, o)
	kv := liveExec(ctx, t, o, "", "", `echo "docker=$(df -B1 --output=size /var/lib/docker | tail -1 | tr -d ' ')"`)
	if n, _ := strconv.ParseInt(kv["docker"], 10, 64); n < 39<<30 {
		t.Errorf("/var/lib/docker is %s bytes, not 40g", kv["docker"])
	} else {
		t.Logf("/var/lib/docker: %.1f GiB", float64(n)/(1<<30))
	}
	liveCachesAndHistory(ctx, t, o, tags)
}

// liveGitHubScript checks GitHub in the VM: gh's login, ls-remote of
// $1, and a push --dry-run to it, which needs the right to push.
const liveGitHubScript = `export GIT_TERMINAL_PROMPT=0
echo "login=$(gh api user --jq .login 2>&1 | head -1)"
echo "head=$(git ls-remote "$1" HEAD 2>&1 | cut -f1 | head -1)"
r=$(mktemp -d) && cd "$r" && git init -q && git -c user.name=t -c user.email=t@example.com commit -q --allow-empty -m live
if git push --dry-run "$1" HEAD:refs/heads/fridolin-live-dry-run >/dev/null 2>&1; then echo push=ok; else echo push=no; fi
cd / && rm -rf "$r"
`

// liveScanScript, as root, looks for the token on stdin in the VM's
// files (not the host folders $1 and $2, or /proc, /sys, /dev) and in
// every process's environment and arguments; it prints file names only.
const liveScanScript = `t=$(cat)
echo "files=$(grep -rlsF -f <(printf '%s\n' "$t") --exclude-dir=proc --exclude-dir=sys --exclude-dir=dev --exclude-dir="$1" --exclude-dir="$2" / | head -3 | tr '\n' ' ')"
echo "procs=$(grep -lsF -f <(printf '%s\n' "$t") /proc/[0-9]*/environ /proc/[0-9]*/cmdline | head -3 | tr '\n' ' ')"
`

// liveGitHub checks gh, ls-remote and push in the VM, and that the
// token is nowhere in it.
func liveGitHub(ctx context.Context, t *testing.T, o FrideSandboxOptions, tok string) {
	repoURL := o.FrideRepo
	kv := liveExec(ctx, t, o, "", "", liveGitHubScript, repoURL)
	login := hostOut(t, "gh", "api", "user", "--jq", ".login")
	head := strings.Fields(hostOut(t, "git", "ls-remote", repoURL, "HEAD"))[0]
	if kv["login"] != login || kv["head"] != head || kv["push"] != "ok" {
		t.Errorf("GitHub in the VM: login %q (host %q), HEAD %q (host %q), push --dry-run %q", kv["login"], login, kv["head"], head, kv["push"])
	} else {
		t.Logf("GitHub in the VM: gh api user is %s, ls-remote HEAD %s, push --dry-run ok", login, head[:12])
	}
	start := time.Now()
	kv = liveExec(ctx, t, o, "root", strings.TrimSpace(tok)+"\n", liveScanScript, filepath.Base(o.Base), filepath.Base(o.Caches))
	if strings.TrimSpace(kv["files"]) != "" || strings.TrimSpace(kv["procs"]) != "" {
		t.Errorf("the token is in the VM: files %q, processes %q", kv["files"], kv["procs"])
	} else {
		t.Logf("the token is in no file, environment or argument in the VM (scanned in %s)", time.Since(start).Round(time.Second))
	}
}

// liveEchoScript checks the swap on a host other than GitHub's: Basic
// auth with the placeholder $1 and with a wrong password, and what a
// Bearer header with it arrives as.
const liveEchoScript = `u=https://postman-echo.com
echo "basic=$(curl -s -m 20 -o /dev/null -w '%{http_code}' -u "postman:$1" $u/basic-auth)"
echo "wrong=$(curl -s -m 20 -o /dev/null -w '%{http_code}' -u postman:wrong $u/basic-auth)"
echo "bearer=$(curl -s -m 20 -H "Authorization: Bearer $1" $u/headers | jq -r .headers.authorization)"
`

// liveGitServerScript is git ls-remote of the host's git server on port
// $2 over http, with git's helper handing it the placeholder $1.
const liveGitServerScript = `export GIT_TERMINAL_PROMPT=0
url="http://host.docker.internal:$2"
h="!f() { test \"\$1\" = get || exit 0; echo username=oauth2; echo password=$1; }; f"
echo "head=$(git -c credential.helper= -c "credential.$url.helper=$h" ls-remote "$url/repo.git" HEAD 2>&1 | cut -f1 | head -1)"
echo "without=$(git -c credential.helper= ls-remote "$url/repo.git" HEAD 2>&1 | tail -1)"
`

// liveOtherHosts checks setHostToken for hosts other than GitHub: the
// Basic and Bearer swap on postman-echo.com (whose test password is
// "password", no secret), and git's Basic auth against a git server on
// the host, which takes only its made-up token, as a self-hosted GitLab
// would; then removeHostToken.
func liveOtherHosts(ctx context.Context, t *testing.T, o FrideSandboxOptions) {
	ph, err := setHostToken(ctx, o.sbx(), o.Dir, o.sandbox(), "postman-echo.com", "password")
	if err != nil {
		t.Fatalf("setHostToken postman-echo.com: %v", err)
	}
	kv := liveExec(ctx, t, o, "", "", liveEchoScript, ph)
	switch {
	case kv["wrong"] == "000":
		t.Logf("postman-echo.com is not reachable; its check is skipped")
	case kv["basic"] != "200" || kv["wrong"] != "401" || kv["bearer"] != "Bearer password":
		t.Errorf("postman-echo.com: Basic with the placeholder %s, a wrong one %s, Bearer arrived as %q", kv["basic"], kv["wrong"], kv["bearer"])
	default:
		t.Logf("postman-echo.com: Basic with the placeholder 200, a wrong password 401, Bearer arrived with the token")
	}
	srv := startGitServer(t, "glpat-live-made-up-0123456789") // personal-data:ignore (made up)
	host := "localhost:" + strconv.Itoa(srv.port)
	ph, err = setHostToken(ctx, o.sbx(), o.Dir, o.sandbox(), host, "glpat-live-made-up-0123456789") // personal-data:ignore (made up)
	if err != nil {
		t.Fatalf("setHostToken %s: %v", host, err)
	}
	kv = liveExec(ctx, t, o, "", "", liveGitServerScript, ph, strconv.Itoa(srv.port))
	if kv["head"] != srv.head || srv.authorized.Load() == 0 {
		t.Errorf("git on the host's server: HEAD %q, want %q (without the helper: %q); %d requests with the token, %d without",
			kv["head"], srv.head, kv["without"], srv.authorized.Load(), srv.refused.Load())
	} else {
		t.Logf("git ls-remote with the placeholder reached the git server with the token (HEAD %s; %d requests with it, %d refused)",
			srv.head[:12], srv.authorized.Load(), srv.refused.Load())
	}
	for _, h := range []string{"postman-echo.com", host} {
		if err := removeHostToken(ctx, o.sbx(), o.Dir, o.sandbox(), h); err != nil {
			t.Errorf("removeHostToken %s: %v", h, err)
		}
	}
	out, _, _ := o.sbx().run(ctx, nil, nil, "secret", "ls", "--sandbox", o.sandbox(), "--json")
	if strings.Contains(string(out), ph) {
		t.Error("the secret stayed after removeHostToken")
	}
}

// gitServer is a git server over http on the host's loopback that takes
// only Basic auth with its token: what a self-hosted GitLab asks.
type gitServer struct {
	port                int
	head                string
	authorized, refused atomic.Int32
}

// startGitServer serves a repository with one commit as /repo.git
// through git http-backend.
func startGitServer(t *testing.T, token string) *gitServer {
	t.Helper()
	root := t.TempDir()
	bare, work := filepath.Join(root, "repo.git"), filepath.Join(t.TempDir(), "work")
	for _, args := range [][]string{
		{"init", "-q", "--bare", bare},
		{"init", "-q", work},
		{"-C", work, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "live"},
		{"-C", work, "push", "-q", bare, "HEAD:refs/heads/main"},
		{"-C", bare, "symbolic-ref", "HEAD", "refs/heads/main"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	s := &gitServer{head: hostOut(t, "git", "-C", bare, "rev-parse", "HEAD")}
	backend := &cgi.Handler{Path: filepath.Join(hostOut(t, "git", "--exec-path"), "git-http-backend"),
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.port = ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pw, ok := r.BasicAuth(); !ok || pw != token {
			s.refused.Add(1)
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "token needed", http.StatusUnauthorized)
			return
		}
		s.authorized.Add(1)
		backend.ServeHTTP(w, r)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	return s
}

// liveModScript downloads a Go module ($1 says GOPROXY) and prints where
// it is; liveMarkScript leaves a session, a FrIDE file and a workspace
// file; liveSeeScript says whether they are there, whose they are, and
// whether the VM's user can append to the session.
const (
	liveModScript = `t0=$(date +%s)
echo "dir=$(GOPROXY=$1 go mod download -json golang.org/x/text@v0.14.0 2>&1 | jq -r '.Dir // .Error')"
echo "secs=$(( $(date +%s) - t0 ))"
`
	liveMarkScript = `h=~/fride-companion/home
mkdir -p ~/.claude/projects/-live ~/fride-companion/workspaces/live-task
echo '{"live":1}' >~/.claude/projects/-live/s1.jsonl
echo marked >"$h"/live-marker
echo notes >~/fride-companion/workspaces/live-task/notes.txt
`
	liveSeeScript = `s=~/.claude/projects/-live/s1.jsonl
echo "session=$(cat $s 2>/dev/null)"
echo "owner=$(stat -c %U $s 2>/dev/null)"
if echo '{"live":2}' >>$s 2>/dev/null; then echo append=ok; else echo append=no; fi
h=~/fride-companion/home
echo "marker=$(cat "$h"/live-marker 2>/dev/null)"
echo "notes=$(cat ~/fride-companion/workspaces/live-task/notes.txt 2>/dev/null)"
echo "login=$(gh api user --jq .login 2>&1 | head -1)"
`
)

// liveCachesAndHistory checks that a module lands in the shared cache,
// that rm and create keep the history and the cache, and that update
// with a new template keeps the workspaces too.
func liveCachesAndHistory(ctx context.Context, t *testing.T, o FrideSandboxOptions, tags map[string]bool) {
	caches, _ := filepath.EvalSymlinks(o.Caches)
	kv := liveExec(ctx, t, o, "", "", liveModScript, "https://proxy.golang.org")
	if !strings.HasPrefix(kv["dir"], filepath.Join(caches, "gomod")+"/") {
		t.Fatalf("the module is not in the shared cache: %q", kv["dir"])
	}
	if _, err := os.Stat(kv["dir"]); err != nil {
		t.Errorf("the host does not see the module: %v", err)
	}
	t.Logf("golang.org/x/text downloaded into the shared cache in %ss: %s", kv["secs"], kv["dir"])
	liveExec(ctx, t, o, "", "", liveMarkScript)
	if err := FrideSandboxLogin(ctx, o, "sk-ant-"+"oat01-live-made-up"); err != nil {
		t.Fatal(err)
	}
	if err := FrideSandboxRemove(ctx, o); err != nil {
		t.Fatalf("rm: %v", err)
	}
	start := time.Now()
	if err := FrideSandboxCreate(ctx, o); err != nil {
		t.Fatalf("create again: %v", err)
	}
	t.Logf("created again in %s (FrIDE built with the shared caches)", time.Since(start).Round(time.Second))
	login := hostOut(t, "gh", "api", "user", "--jq", ".login")
	kv = liveExec(ctx, t, o, "", "", liveSeeScript)
	if kv["session"] != `{"live":1}` || kv["owner"] != "agent" || kv["append"] != "ok" || kv["marker"] != "marked" || kv["login"] != login {
		t.Errorf("after rm and create: %v", kv)
	} else {
		t.Logf("after rm and create: the session and FrIDE's file are back, the VM's user's, and GitHub works again; the workspace went with rm (%q)", kv["notes"])
	}
	kv = liveExec(ctx, t, o, "", "", liveModScript, "off")
	if !strings.HasPrefix(kv["dir"], filepath.Join(caches, "gomod")+"/") {
		t.Errorf("the new sandbox does not find the module offline: %q", kv["dir"])
	} else {
		t.Log("the new sandbox found golang.org/x/text in the shared cache with GOPROXY=off")
	}
	liveRecreate(ctx, t, o, tags)
}

// liveRecreate changes the template (a comment in a copy of the
// checkout's Dockerfile) and updates: the workspace, the session,
// FrIDE's file, the login and GitHub are there in the new sandbox.
func liveRecreate(ctx context.Context, t *testing.T, o FrideSandboxOptions, tags map[string]bool) {
	repo := t.TempDir()
	for _, f := range []string{frideSandboxDockerfile, frideImageDockerfile, frideCompanionConfig} {
		b, err := os.ReadFile(filepath.Join(o.Repo, filepath.FromSlash(f)))
		if err != nil {
			t.Fatal(err)
		}
		if f == frideSandboxDockerfile {
			b = append(b, "# The live test's template change.\n"...)
		}
		writeTestFile(t, filepath.Join(repo, filepath.FromSlash(f)), string(b))
	}
	o.Repo = repo
	tag, _ := FrideSandboxTemplateTag(repo)
	tags[tag] = true
	liveExec(ctx, t, o, "", "", liveMarkScript)
	start := time.Now()
	if err := FrideSandboxUpdate(ctx, o); err != nil {
		t.Fatalf("update with a new template: %v", err)
	}
	t.Logf("made the sandbox anew from %s in %s", tag, time.Since(start).Round(time.Second))
	login := hostOut(t, "gh", "api", "user", "--jq", ".login")
	kv := liveExec(ctx, t, o, "", "", liveSeeScript)
	if kv["notes"] != "notes" || kv["session"] == "" || kv["owner"] != "agent" || kv["marker"] != "marked" || kv["login"] != login {
		t.Errorf("after update with a new template: %v", kv)
	} else {
		t.Log("after update with a new template: the workspace, the session, FrIDE's file and GitHub are there")
	}
	r, err := FrideSandboxStatus(ctx, o)
	if st, _ := LoadFrideSandbox(o.Dir); err != nil || st.Template != tag || r.Inside["login"] != "yes" || r.Inside["git"] != "yes" || o.carrying() {
		t.Errorf("status after update: %+v %v (state %+v)", r.Inside, err, st)
	}
}
