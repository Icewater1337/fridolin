package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/ghapp"
	"github.com/Icewater1337/fridolin/internal/ghapp/ghapptest"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// memSecrets is a host secret store in memory, which the tests' bridges
// keep their GitHub app in (RunTestMain).
type memSecrets struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *memSecrets) Get(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.m[name]; ok {
		return v, nil
	}
	return "", hostsecrets.ErrNotFound
}

func (s *memSecrets) Set(name, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[name] = value
	return nil
}

func (s *memSecrets) Has(name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[name]
	return ok, nil
}

func (s *memSecrets) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, name)
	return nil
}

// useFakeGitHub points the bridge's GitHub App at a fake of GitHub.
func useFakeGitHub(t *testing.T, g *githubService) *ghapptest.Server {
	t.Helper()
	fake := ghapptest.New()
	t.Cleanup(fake.Close)
	g.client.API, g.web = fake.URL, fake.URL
	return fake
}

// connectFakeGitHub runs the manifest flow against the fake up to the
// install page, through the routes the browser takes.
func connectFakeGitHub(t *testing.T, h *harness, fake *ghapptest.Server) {
	t.Helper()
	w := h.Do("POST", "/api/github/connect", map[string]any{"pull_requests": true})
	if w.Code != http.StatusOK {
		t.Fatalf("connect: %d %s", w.Code, w.Body.String())
	}
	var start struct{ URL string }
	if err := json.Unmarshal(w.Body.Bytes(), &start); err != nil || !strings.HasPrefix(start.URL, "/github/connect?state=") {
		t.Fatalf("connect answered %s", w.Body.String())
	}
	state := strings.TrimPrefix(start.URL, "/github/connect?state=")

	// The hand-off page: no token needed, a form to GitHub only.
	page := h.Do("GET", start.URL, nil, func(r *http.Request) { r.Header.Del("X-Bridge-Token") })
	if page.Code != http.StatusOK {
		t.Fatalf("hand-off: %d %s", page.Code, page.Body.String())
	}
	csp := page.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "form-action "+fake.URL+";") || !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "script-src") {
		t.Errorf("hand-off CSP: %s", csp)
	}
	body := html.UnescapeString(page.Body.String())
	if !strings.Contains(body, `action="`+fake.URL+`/settings/apps/new?state=`+state+`"`) || strings.Contains(body, "<script") {
		t.Errorf("hand-off page:\n%s", body)
	}
	for _, want := range []string{`"redirect_url":"http://` + h.listen + `/github/created"`, `"setup_url":"http://` + h.listen + `/github/installed"`,
		`"pull_requests":"write"`, `"active":false`, `"public":false`} {
		if !strings.Contains(body, want) {
			t.Errorf("the manifest lacks %s:\n%s", want, body)
		}
	}

	// GitHub sends the browser back with the code; the bridge exchanges it
	// once and sends the browser on to install the app.
	back := "/github/created?code=" + ghapptest.Code + "&state=" + state
	noToken := func(r *http.Request) { r.Header.Del("X-Bridge-Token") }
	w = h.Do("GET", back, nil, noToken)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != fake.URL+"/apps/"+fake.Slug+"/installations/new" {
		t.Fatalf("created: %d %q %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	if w := h.Do("GET", back, nil, noToken); w.Code != http.StatusBadRequest {
		t.Errorf("the state was used twice: %d", w.Code)
	}
	if w := h.Do("GET", "/github/installed?installation_id=77&setup_action=install", nil, noToken); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/#/integrations" {
		t.Errorf("installed: %d %q", w.Code, w.Header().Get("Location"))
	}
}

// TestGitHubConnect: the dashboard connects GitHub through the manifest
// flow, shows the installations, proves a narrowed token works, and
// disconnects. The key and the client secret never reach config.json, a
// file in the data dir in clear, an API answer or the log.
func TestGitHubConnect(t *testing.T) {
	h := newHarness(t, "")
	fake := useFakeGitHub(t, h.b.github)
	connectFakeGitHub(t, h, fake)

	secrets := []string{"fake-client-secret", "PRIVATE KEY", fake.PEM()[40:100]}
	w := h.Do("GET", "/api/github", nil)
	var st httpapi.GitHubStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Connected || st.App == nil || st.App.Slug != fake.Slug || len(st.Installations) != 1 ||
		fmt.Sprint(st.Installations[0].Repos) != "[you/fride you/fridolin]" || st.Error != "" ||
		st.DeleteURL != fake.URL+"/settings/apps/"+fake.Slug+"/advanced" {
		t.Errorf("status: %s", w.Body.String())
	}
	if len(fake.Live()) != 0 {
		t.Errorf("the status left tokens: %v", fake.Live())
	}

	w = h.Do("POST", "/api/github/test", map[string]string{"repo": "you/fridolin"})
	var res httpapi.GitHubTestResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != http.StatusOK || res.Issue == nil || res.Issue.Number != 7 || !res.Revoked {
		t.Errorf("test: %d %s", w.Code, w.Body.String())
	}
	if m := fake.LastMinted(); fmt.Sprint(m.Repos, m.Permissions) != "[fridolin] map[issues:read]" {
		t.Errorf("the test asked for %+v", m)
	}
	if len(fake.Live()) != 0 {
		t.Errorf("the test left its token: %v", fake.Live())
	}

	var leaks []string
	_ = filepath.WalkDir(h.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, _ := os.ReadFile(path)
		for _, s := range secrets {
			if strings.Contains(string(data), s) {
				leaks = append(leaks, path)
			}
		}
		return nil
	})
	if logs := h.Do("GET", "/api/logs", nil).Body.String(); slices.ContainsFunc(secrets, func(s string) bool { return strings.Contains(logs, s) }) {
		leaks = append(leaks, "the log")
	}
	if len(leaks) > 0 {
		t.Errorf("the app's secrets are in %v", leaks)
	}

	w = h.Do("POST", "/api/github/disconnect", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"delete_url":"`+fake.URL+"/settings/apps/"+fake.Slug+`/advanced"`) {
		t.Errorf("disconnect: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("GET", "/api/github", nil); !strings.Contains(w.Body.String(), `"connected":false`) {
		t.Errorf("after disconnecting: %s", w.Body.String())
	}
	if w := h.Do("POST", "/api/github/test", map[string]string{"repo": "you/fridolin"}); w.Code != http.StatusConflict {
		t.Errorf("test without an app: %d", w.Code)
	}
}

// TestGitHubConnectRefuses: a state the bridge did not make opens no
// page, and the dashboard's address and an organization are checked.
func TestGitHubConnectRefuses(t *testing.T) {
	h := newHarness(t, "")
	useFakeGitHub(t, h.b.github)
	for _, path := range []string{"/github/connect", "/github/connect?state=nope", "/github/created?code=" + ghapptest.Code + "&state=nope"} {
		if w := h.Do("GET", path, nil); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	if w := h.Do("POST", "/api/github/connect", map[string]string{"org": "not an org!"}); w.Code != http.StatusBadRequest {
		t.Errorf("a bad organization: %d", w.Code)
	}
	if _, err := h.b.github.Start("javascript:alert(1)", "", false); err == nil {
		t.Error("a redirect to javascript: was made")
	}
	w := h.Do("POST", "/api/github/connect", map[string]string{"org": "acme"})
	var start struct{ URL string }
	_ = json.Unmarshal(w.Body.Bytes(), &start)
	if page := h.Do("GET", start.URL, nil); !strings.Contains(html.UnescapeString(page.Body.String()), "/organizations/acme/settings/apps/new?state=") {
		t.Errorf("an organization's app: %s", page.Body.String())
	}
}

// TestGitHubTokenForARun: a run whose message carries a grant (a scheduled
// agent's) gets a token for exactly the grant's repositories and
// permissions, as gh's login in GH_CONFIG_DIR, and the bridge revokes it
// and removes the login when the run ends.
func TestGitHubTokenForARun(t *testing.T) {
	tb, _ := serveTestBridge(t, "")
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A", Routing: manifest.Routing{HandlesDMs: true}})
	fake := useFakeGitHub(t, tb.github)
	creds, err := tb.github.client.Convert(context.Background(), ghapptest.Code)
	if err != nil {
		t.Fatal(err)
	}
	if err := tb.github.secrets.save(creds); err != nil {
		t.Fatal(err)
	}
	envLog := filepath.Join(t.TempDir(), "brain.env")
	t.Setenv("FRIDOLIN_FAKE_ENV_LOG", envLog)

	grant := &chat.GitHubGrant{Repos: []string{"you/fridolin"}, Permissions: map[string]string{"issues": "write"}}
	tb.handleInbound(context.Background(), brain.Inbound{Inbound: chat.Inbound{JID: apiTestDM, Sender: "15551234567", Text: "file the issue", Source: chat.SourceSchedule, GitHub: grant}})
	testutil.WaitFor(t, 10*time.Second, "the reply", func() bool { return len(tb.sentTo(apiTestDM)) > 0 })
	logged, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatal(err)
	}
	m := fake.LastMinted()
	env := map[string]string{}
	for _, kv := range strings.Split(strings.TrimSpace(string(logged)), "\n") {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	dir := env["GH_CONFIG_DIR"]
	if fmt.Sprint(m.Repos, m.Permissions) != "[fridolin] map[issues:write]" || filepath.Dir(dir) != runTokenDir(tb.cfgs.Get().DataDir, "a") {
		t.Errorf("the run got %+v, GH_CONFIG_DIR=%q", m, dir)
	}
	// The token is in gh's login, never in the environment.
	if strings.Contains(string(logged), m.Token) || env["GH_TOKEN"] != "" || strings.Contains(string(logged), "FRIDOLIN_GITHUB") {
		t.Errorf("the run's environment shows a GitHub token:\n%s", logged)
	}
	testutil.WaitFor(t, 5*time.Second, "the run's GitHub token to be revoked and its login removed", func() bool {
		_, err := os.Stat(dir)
		return len(fake.Live()) == 0 && errors.Is(err, fs.ErrNotExist)
	})

	// A message without a grant gets no token.
	tb.handleInbound(context.Background(), brain.Inbound{Inbound: chat.Inbound{JID: apiTestDM, Sender: "15551234567", Text: "hello", Source: "whatsapp"}})
	testutil.WaitFor(t, 10*time.Second, "the second reply", func() bool { return len(tb.sentTo(apiTestDM)) > 1 })
	if logged, _ := os.ReadFile(envLog); strings.Contains(string(logged), "GH_CONFIG_DIR=") {
		t.Error("a run without a grant got a GitHub token")
	}
}

func TestGitHubMintRefuses(t *testing.T) {
	g := &githubService{client: &ghapp.Client{}, secrets: githubSecrets{getenv: func(string) string { return "" }, store: &memSecrets{m: map[string]string{}}},
		log: slog.New(slog.DiscardHandler), now: time.Now}
	ctx := context.Background()
	for _, grant := range []chat.GitHubGrant{
		{},
		{Repos: []string{"you/a", "other/b"}},
		{Repos: []string{"nope"}},
		{Repos: []string{"you/a"}, Permissions: map[string]string{"issues": "all"}},
	} {
		if _, _, err := g.Mint(ctx, grant); err == nil {
			t.Errorf("%+v was minted", grant)
		}
	}
	if _, _, err := g.Mint(ctx, chat.GitHubGrant{Repos: []string{"you/a"}}); !errors.Is(err, errNoGitHub) {
		t.Errorf("without an app or a token: %v", err)
	}
	// The typed token is the second choice.
	g.secrets.getenv = func(k string) string {
		if k == "FRIDOLIN_GITHUB_TOKEN" {
			return "typed-token"
		}
		return ""
	}
	if tok, done, err := g.Mint(ctx, chat.GitHubGrant{Repos: []string{"you/a"}}); err != nil || tok != "typed-token" || done == nil {
		t.Errorf("typed token: %q %v", tok, err)
	}
}

// TestGitHubSecretsInTheContainer: in the core container the app's
// credentials go to secrets/github-app.age, encrypted to the core's
// post-quantum identity, and are read back and forgotten there.
func TestGitHubSecretsInTheContainer(t *testing.T) {
	id, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"FRIDOLIN_AGE_IDENTITY_PQ": id.String()}
	dir := filepath.Join(t.TempDir(), "secrets")
	s := githubSecrets{getenv: func(k string) string { return env[k] }, secretsDir: dir}
	if _, ok, err := s.load(); ok || err != nil {
		t.Fatalf("before: %v %v", ok, err)
	}
	fake := ghapptest.New()
	t.Cleanup(fake.Close)
	creds, err := (&ghapp.Client{API: fake.URL}).Convert(context.Background(), ghapptest.Code)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.save(creds); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "github-app.age"))
	if err != nil || strings.Contains(string(data), "PRIVATE KEY") || strings.Contains(string(data), "fake-client-secret") {
		t.Fatalf("the file holds the key in clear (%v)", err)
	}
	if st, _ := os.Stat(filepath.Join(dir, "github-app.age")); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	got, ok, err := s.load()
	if err != nil || !ok || got.PEM != creds.PEM || got.ClientSecret != creds.ClientSecret || got.ID != creds.ID {
		t.Fatalf("read back: %v %v", ok, err)
	}
	if err := s.forget(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.load(); ok {
		t.Error("still there after forget")
	}
	// Without the identity nothing is kept.
	env = map[string]string{}
	if err := s.save(creds); err == nil {
		t.Error("kept the key without an identity to encrypt it to")
	}
}

// TestWriteGitHubLogin: the run's gh login is hosts.yml with the token and
// a config.yml of version 1, which gh reads without migrating it, in a
// folder only the bridge's user may read.
func TestWriteGitHubLogin(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run-tokens", "a")
	gh, err := writeGitHubLogin(dir, "ghs_abc")
	if err != nil {
		t.Fatal(err)
	}
	hosts, _ := os.ReadFile(filepath.Join(gh, "hosts.yml"))
	cfg, _ := os.ReadFile(filepath.Join(gh, "config.yml"))
	if !strings.Contains(string(hosts), "\n    oauth_token: ghs_abc\n") || !strings.Contains(string(hosts), "            oauth_token: ghs_abc\n") || string(cfg) != "version: \"1\"\n" {
		t.Errorf("hosts.yml:\n%s\nconfig.yml:\n%s", hosts, cfg)
	}
	st, _ := os.Stat(gh)
	fst, _ := os.Stat(filepath.Join(gh, "hosts.yml"))
	if st.Mode().Perm() != 0o700 || fst.Mode().Perm() != 0o600 || !strings.HasPrefix(filepath.Base(gh), "run-") || !strings.HasSuffix(gh, ".gh") {
		t.Errorf("%s: modes %v %v", gh, st.Mode(), fst.Mode())
	}
}

// TestGitHubConnectLikeABrowser walks the whole manifest flow as a browser
// does, against a bridge served on loopback and a fake GitHub whose pages
// stand in for the owner's clicks: the dashboard's Connect, the hand-off
// page's form posted to GitHub, GitHub's redirect back with the code, the
// install page, and GitHub's return to the setup URL, every redirect
// followed. It ends on the dashboard's Integrations page with the app
// installed, and a token minted from it works.
func TestGitHubConnectLikeABrowser(t *testing.T) {
	h := newServedHarness(t, "")
	fake := useFakeGitHub(t, h.b.github)
	w := h.Do("POST", "/api/github/connect", map[string]any{})
	var start struct{ URL string }
	if err := json.Unmarshal(w.Body.Bytes(), &start); err != nil || w.Code != http.StatusOK {
		t.Fatalf("connect: %d %s", w.Code, w.Body.String())
	}

	// The browser: cookies, redirects followed, no dashboard token.
	var visited []string
	browser := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		visited = append(visited, r.URL.String())
		return nil
	}}
	resp, err := browser.Get("http://" + h.listen + start.URL)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	form := regexp.MustCompile(`<form method="post" action="([^"]+)">\s*<input type="hidden" name="manifest" value="([^"]+)">`).FindSubmatch(page)
	if form == nil {
		t.Fatalf("no form on the hand-off page:\n%s", page)
	}
	action, manifest := html.UnescapeString(string(form[1])), html.UnescapeString(string(form[2]))

	// Continue to GitHub: the form posts the manifest; GitHub makes the
	// app and sends the browser back, the bridge on to the install page,
	// GitHub back to the setup URL, and that to the dashboard.
	resp, err = browser.PostForm(action, url.Values{"manifest": {manifest}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/" || len(visited) != 4 ||
		!strings.HasPrefix(visited[0], "http://"+h.listen+"/github/created?code=") ||
		visited[1] != fake.URL+"/apps/"+fake.Slug+"/installations/new" ||
		!strings.HasPrefix(visited[2], "http://"+h.listen+"/github/installed?installation_id=77") ||
		visited[3] != "http://"+h.listen+"/#/integrations" {
		t.Fatalf("ended at %d %s after %q", resp.StatusCode, resp.Request.URL, visited)
	}
	m := fake.LastManifest()
	if m["public"] != false || fmt.Sprint(m["default_permissions"]) != "map[contents:read issues:write metadata:read]" {
		t.Errorf("GitHub was posted %v", m)
	}

	st := h.b.github.Status(context.Background())
	if !st.Connected || len(st.Installations) != 1 || fmt.Sprint(st.Installations[0].Repos) != "[you/fride you/fridolin]" {
		t.Fatalf("after the flow: %+v", st)
	}
	tok, done, err := h.b.github.Mint(context.Background(), chat.GitHubGrant{Repos: []string{"you/fride"}, Permissions: map[string]string{"issues": "read"}})
	if err != nil {
		t.Fatal(err)
	}
	if issue, err := h.b.github.client.LatestIssue(context.Background(), tok, "you/fride"); err != nil || issue == nil {
		t.Errorf("the token does not read an issue: %v", err)
	}
	if _, err := h.b.github.client.LatestIssue(context.Background(), tok, "you/fridolin"); !ghapp.IsStatus(err, http.StatusNotFound) {
		t.Errorf("the token reaches a repository it was not minted for: %v", err)
	}
	done()
	if len(fake.Live()) != 0 {
		t.Errorf("tokens left: %v", fake.Live())
	}
}
