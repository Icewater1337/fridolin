package app

import (
	"context"

	sitesvc "github.com/Icewater1337/fridolin/internal/site"

	"encoding/json"
	"fmt"

	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// newTestSite is a SiteManager in a temp data dir whose npm installs only
// copy the site package's own node_modules (no network in tests). A copy, not
// a link: the live site's sandbox allows the deploy's real path, not the
// checkout a link would lead to.
func newTestSite(t *testing.T) *sitesvc.SiteManager {
	t.Helper()
	return NewSiteManager(testSiteOptions(t))
}

func testSiteOptions(t *testing.T) sitesvc.SiteOptions {
	t.Helper()
	env := envOf(t)
	opts := sitesvc.SiteOptions{DataDir: t.TempDir(), GoogleClient: func() string { return "" }, Host: siteHostFor(env, func() string { return getenv(env, "HOME") })}
	opts.NPMInstall = func(ctx context.Context, dir string, prod bool) error {
		src := filepath.Join(repoDir, "site", "node_modules")
		if !fsx.Exists(src) {
			// What an install leaves even then: a deploy without it is built again.
			return os.MkdirAll(filepath.Join(dir, "node_modules"), 0o700)
		}
		if out, err := exec.CommandContext(ctx, "cp", "-R", src, filepath.Join(dir, "node_modules")).CombinedOutput(); err != nil {
			return fmt.Errorf("copying node_modules: %v %s", err, out)
		}
		return nil
	}
	return opts
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = gitEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newBareRepo is an empty "remote" for the site repository.
func newBareRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "site.git")
	gitT(t, t.TempDir(), "init", "-q", "--bare", "-b", "main", dir)
	return dir
}

func waitDeploy(t *testing.T, m *sitesvc.SiteManager) *sitesvc.DeploySnapshot {
	t.Helper()
	var j *sitesvc.DeploySnapshot
	testutil.WaitFor(t, 2*time.Minute, "the deploy to finish", func() bool {
		j = m.DeployStatus()
		return j != nil && j.State != "running"
	})
	if j.State != "done" {
		t.Fatalf("deploy %s: %s\n%s", j.State, j.Error, strings.Join(j.Log, "\n"))
	}
	return j
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// Saving or removing the site's own Google client reaches the running site
// without a manual restart; so does a change to Fridolin's shared client
// while the site uses that one.
func TestSiteGoogleClientReachesRunningSite(t *testing.T) {
	t.Parallel()
	requireSiteRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := testSiteOptions(t)
	shared := filepath.Join(opts.DataDir, "google", "oauth_client.json") // as GoogleAuth.clientPath
	opts.GoogleClient = func() string { return shared }
	m := NewSiteManager(opts)
	clientJSON := func(id string) string { return `{"web":{"client_id":"` + id + `","client_secret":"s"}}` }
	setShared := func(id string) {
		t.Helper()
		os.MkdirAll(filepath.Dir(shared), 0o700)
		if err := os.WriteFile(shared, []byte(clientJSON(id)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	setShared("shared-1")

	if err := m.ConnectRepo(ctx, sitesvc.SiteRepo{URL: newBareRepo(t)}); err != nil {
		t.Fatal(err)
	}
	if err := m.AddStarter(ctx); err != nil {
		t.Fatal(err)
	}
	up, _ := m.Fetch(ctx)
	listen := freePort(t)
	if _, err := m.UpdateSettings(sitesvc.SiteSettingsUpdate{Listen: &listen}); err != nil {
		t.Fatal(err)
	}
	m.Start(ctx)
	defer m.Stop()
	if err := m.Deploy(ctx, up.Head); err != nil {
		t.Fatal(err)
	}
	waitDeploy(t, m)
	on := true
	if _, err := m.UpdateSettings(sitesvc.SiteSettingsUpdate{Enabled: &on}); err != nil {
		t.Fatal(err)
	}

	b := &App{site: m}
	mux := http.NewServeMux()
	b.siteAPI(mux)
	call := func(method, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/site/google-client", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%s /api/site/google-client: %d %s", method, rec.Code, rec.Body)
		}
	}
	// signInClient is the client the site sends people to Google with.
	noRedirect := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	signInClient := func() string {
		resp, err := noRedirect.Get("http://" + listen + "/auth/google")
		if err != nil {
			return ""
		}
		resp.Body.Close()
		u, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			return ""
		}
		return u.Query().Get("client_id")
	}
	expect := func(what, id, file string) {
		t.Helper()
		testutil.WaitFor(t, 60*time.Second, what, func() bool { return signInClient() == id })
		var rt struct {
			GoogleClientFile string `json:"google_client_file"`
		}
		data, _ := os.ReadFile(filepath.Join(opts.DataDir, "site", "site.runtime.json"))
		if err := json.Unmarshal(data, &rt); err != nil || rt.GoogleClientFile != file {
			t.Errorf("%s: runtime.json names %q (%v), want %q", what, rt.GoogleClientFile, err, file)
		}
	}
	expect("the shared client", "shared-1", shared)

	call("PUT", clientJSON("site-1"))
	expect("the site's own client once saved", "site-1", filepath.Join(opts.DataDir, "site", "google_client.json"))

	// The site's own client stays ahead of a changed shared one.
	setShared("shared-2")
	if got := signInClient(); got != "site-1" {
		t.Errorf("after the shared client changed, the site signs in with %q", got)
	}

	call("DELETE", "")
	expect("the shared client once the site's own is removed", "shared-2", shared)

	// The site reads the shared client at each sign-in: no restart needed.
	setShared("shared-3")
	if got := signInClient(); got != "shared-3" {
		t.Errorf("after the shared client changed, the site signs in with %q", got)
	}
}

func TestSiteAPI(t *testing.T) {
	t.Parallel()
	b := &App{site: newTestSite(t)}
	mux := http.NewServeMux()
	b.siteAPI(mux)
	call := func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, st := call("GET", "/api/site", "")
	if code != 200 || st["settings"] == nil || st["google"].(map[string]any)["redirect_uri"] != "http://127.0.0.1:8780/auth/callback" {
		t.Fatalf("status %d %v", code, st)
	}
	if code, out := call("PUT", "/api/site/settings", `{"enabled": true}`); code != 400 || !strings.Contains(out["error"].(string), "admin") {
		t.Errorf("turning on without an admin: %d %v", code, out)
	}
	if code, _ := call("POST", "/api/site/users", `{"kind":"google","email":"owner@example.com","role":"admin"}`); code != 200 {
		t.Errorf("adding the admin: %d", code)
	}
	// Without a deploy the site serves its built-in Files and Wiki alone.
	if code, out := call("PUT", "/api/site/settings", `{"enabled": true}`); code != 200 || out["enabled"] != true {
		t.Errorf("turning on without a deploy: %d %v", code, out)
	}
	if code, _ := call("PUT", "/api/site/settings", `{"enabled": false}`); code != 200 {
		t.Errorf("turning off: %d", code)
	}
	if code, _ := call("PUT", "/api/site/settings", `{"domain": "example.com"}`); code != 200 {
		t.Errorf("setting the domain: %d", code)
	}
	_, st = call("GET", "/api/site", "")
	if st["google"].(map[string]any)["redirect_uri"] != "https://example.com/auth/callback" {
		t.Errorf("redirect uri = %v", st["google"])
	}
	if code, _ := call("PUT", "/api/site/google-client", `{"installed":{"client_id":"a","client_secret":"b"}}`); code != 400 {
		t.Errorf("a Desktop client was accepted for the site: %d", code)
	}
	if code, _ := call("PUT", "/api/site/google-client", `{"web":{"client_id":"a","client_secret":"b"}}`); code != 200 {
		t.Errorf("a web client was refused: %d", code)
	}
	_, st = call("GET", "/api/site", "")
	if st["google"].(map[string]any)["source"] != "site" {
		t.Errorf("google = %v", st["google"])
	}
	if code, out := call("GET", "/api/site/users", ""); code != 200 || len(out["users"].([]any)) != 1 {
		t.Errorf("users: %d %v", code, out)
	}
}

// The deploy key goes on the repository before Connect can clone it, so
// until a repository is connected gh adds it to the address typed for
// Connect, checked as Connect checks it; afterwards to the connected one.
func TestSiteDeployKeyWithGH(t *testing.T) {
	// Serial: puts a fake gh on the process's PATH; the site runs gh with
	// the process's environment.
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	bin := t.TempDir()
	argsLog := filepath.Join(bin, "args")
	fake := "#!/bin/sh\necho \"$@\" >> \"$(dirname \"$0\")/args\"\n[ -n \"$FRIDOLIN_FAKE_GH_FAIL\" ] && { echo \"$FRIDOLIN_FAKE_GH_FAIL\" >&2; exit 1; }\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FRIDOLIN_FAKE_GH_FAIL", "")

	opts := testSiteOptions(t)
	m := NewSiteManager(opts)
	b := &App{site: m}
	mux := http.NewServeMux()
	b.siteAPI(mux)
	call := func(body string) (int, map[string]any) {
		req := httptest.NewRequest("POST", "/api/site/deploy-key/github", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	ghCalls := func() []string {
		data, _ := os.ReadFile(argsLog)
		if len(data) == 0 {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}

	// A fresh site: nothing connected, so the address has to come with the request.
	for body, want := range map[string]string{
		"":                                      "SSH address first",
		`{"url": "  "}`:                         "SSH address first",
		`{"url": "--upload-pack=evil"}`:         "SSH address, e.g.",
		`{"url": "git@github.com:you/a b.git"}`: "SSH address, e.g.",
		`{"url": "git@example.com:you/site.git"}`: "not on GitHub",
		`{"url": "git@github.com:-you/site.git"}`: "not on GitHub",
		`{"url": "git@github.com:you/...git"}`:    "not on GitHub",
		`{"url": 7}`:                              "cannot unmarshal",
	} {
		if code, out := call(body); code != 400 || !strings.Contains(fmt.Sprint(out["error"]), want) {
			t.Errorf("%s: %d %v, want 400 %q", body, code, out, want)
		}
	}
	if got := ghCalls(); len(got) != 0 {
		t.Fatalf("gh ran for a refused address: %v", got)
	}

	code, out := call(`{"url": " git@github.com:you/my-site.git "}`)
	if code != 200 || out["repo"] != "you/my-site" {
		t.Fatalf("add before connecting: %d %v", code, out)
	}
	pub := filepath.Join(opts.DataDir, "site", "deploy_key.pub")
	if !fsx.Exists(pub) {
		t.Fatal("no deploy key was made")
	}
	want := "repo deploy-key add " + pub + " --repo you/my-site --allow-write --title Fridolin site"
	if got := ghCalls(); len(got) != 1 || got[0] != want {
		t.Errorf("gh ran with %v, want %s", got, want)
	}
	if u := m.Settings().Repo.URL; u != "" {
		t.Errorf("adding the key connected %q", u)
	}

	// gh's own failure comes back; a key GitHub already has is no failure.
	t.Setenv("FRIDOLIN_FAKE_GH_FAIL", "HTTP 404: Not Found")
	if code, out := call(`{"url": "git@github.com:you/my-site.git"}`); code != 400 || !strings.Contains(fmt.Sprint(out["error"]), "gh: HTTP 404") {
		t.Errorf("gh failing: %d %v", code, out)
	}
	t.Setenv("FRIDOLIN_FAKE_GH_FAIL", "key is already in use")
	if code, out := call(`{"url": "git@github.com:you/my-site.git"}`); code != 200 {
		t.Errorf("a key GitHub already has: %d %v", code, out)
	}
	t.Setenv("FRIDOLIN_FAKE_GH_FAIL", "")

	// Connected: the connected repository, as before; another address is refused.
	setRepo := func(repo sitesvc.SiteRepo) {
		t.Helper()
		settings := m.Settings()
		settings.Repo = repo
		data, err := json.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(opts.DataDir, "site", "settings.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		m = NewSiteManager(opts)
		b.site = m
	}
	setRepo(sitesvc.SiteRepo{URL: "git@github.com:you/my-site.git", Branch: "main"})
	if err := os.MkdirAll(filepath.Join(opts.DataDir, "site", "repo"), 0o700); err != nil {
		t.Fatal(err)
	}
	os.Remove(argsLog)
	for _, body := range []string{"", `{"url": "git@github.com:you/my-site.git"}`} {
		if code, out := call(body); code != 200 || out["repo"] != "you/my-site" {
			t.Errorf("connected, %q: %d %v", body, code, out)
		}
	}
	if code, out := call(`{"url": "git@github.com:someone/else.git"}`); code != 400 || !strings.Contains(fmt.Sprint(out["error"]), "another repository is connected") {
		t.Errorf("connected, another address: %d %v", code, out)
	}
	if got := ghCalls(); len(got) != 2 || got[0] != want || got[1] != want {
		t.Errorf("gh ran with %v, want %s twice", got, want)
	}

	setRepo(sitesvc.SiteRepo{URL: newBareRepo(t), Branch: "main"})
	if code, out := call(""); code != 400 || !strings.Contains(fmt.Sprint(out["error"]), "not on GitHub") {
		t.Errorf("connected to a repository elsewhere: %d %v", code, out)
	}
}

// TestSiteHostDefaultsToTheProcess: a site manager given no host runs its
// programs with the process's environment and home folder, as the binary's
// does; a bridge passes its own (siteHostFor).
func TestSiteHostDefaultsToTheProcess(t *testing.T) {
	t.Parallel()
	var h sitesvc.SiteHost
	siteHostDefaults(&h)
	if got := h.Home(); got != HomeDir() {
		t.Errorf("home %q, want the process's %q", got, HomeDir())
	}
	if got, want := len(h.Environ()), len(os.Environ()); got != want {
		t.Errorf("%d variables, want the process's %d", got, want)
	}
	if env := h.Env("/opt/node/bin/node"); !slices.Contains(env, "HOME="+HomeDir()) || !slices.Contains(env, "PATH="+siteEnvPath("/opt/node/bin/node")) {
		t.Errorf("a site process's environment %q lacks the process's HOME or node's PATH", env)
	}
	if h.Node == nil || h.Tool == nil || h.GitPath == nil || h.Profile == nil || h.GoogleClientType == nil {
		t.Errorf("a lookup was left out: %+v", h)
	}

	own := sitesvc.SiteHost{Home: func() string { return "/Users/you" }}
	siteHostDefaults(&own)
	if got := own.Home(); got != "/Users/you" {
		t.Errorf("a host's own home was replaced by %q", got)
	}
}
