package main

import (
	"bytes"
	"context"
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
)

// newTestSite is a SiteManager in a temp data dir whose npm installs only
// copy the site package's own node_modules (no network in tests). A copy, not
// a link: the live site's sandbox allows the deploy's real path, not the
// checkout a link would lead to.
func newTestSite(t *testing.T) *SiteManager {
	t.Helper()
	m := NewSiteManager(t.TempDir(), func() string { return "" })
	m.npmInstall = func(ctx context.Context, dir string, prod bool) error {
		src := filepath.Join(repoDir, "site", "node_modules")
		if !fileExists(src) {
			return nil
		}
		if out, err := exec.CommandContext(ctx, "cp", "-R", src, filepath.Join(dir, "node_modules")).CombinedOutput(); err != nil {
			return fmt.Errorf("copying node_modules: %v %s", err, out)
		}
		return nil
	}
	return m
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
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

func waitDeploy(t *testing.T, m *SiteManager) *siteJob {
	t.Helper()
	var j *siteJob
	waitFor(t, 2*time.Minute, "the deploy to finish", func() bool {
		j = m.DeployStatus()
		return j != nil && j.State != "running"
	})
	if j.State != "done" {
		t.Fatalf("deploy %s: %s\n%s", j.State, j.Error, strings.Join(j.Log, "\n"))
	}
	return j
}

func TestSiteAccounts(t *testing.T) {
	m := newTestSite(t)
	admin, err := m.AddUser(SiteUserInput{Kind: "google", Email: "  OWNER@example.com ", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if admin.Email != "owner@example.com" {
		t.Errorf("email = %q, want it trimmed and lower-cased", admin.Email)
	}
	if _, err := m.AddUser(SiteUserInput{Kind: "google", Email: "owner@example.com", Role: "member"}); err == nil {
		t.Error("a second account for the same Google address was accepted")
	}
	if _, err := m.AddUser(SiteUserInput{Kind: "password", Username: "sam", Password: "long enough pw", Role: "admin"}); err == nil {
		t.Error("a password admin was accepted")
	}
	if _, err := m.AddUser(SiteUserInput{Kind: "password", Username: "sam", Password: "short", Role: "member"}); err == nil {
		t.Error("a short password was accepted")
	}
	if _, err := m.AddUser(SiteUserInput{Kind: "password", Username: "sam", Password: "long enough pw", Role: "boss"}); err == nil {
		t.Error("an unknown role was accepted")
	}
	sam, err := m.AddUser(SiteUserInput{Kind: "password", Username: "sam", Name: "Sam", Password: "long enough pw", Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	if sam.PasswordHash != "" {
		t.Error("AddUser returned the password hash")
	}
	if _, err := m.AddUser(SiteUserInput{Kind: "password", Username: "SAM", Password: "long enough pw", Role: "member"}); err == nil {
		t.Error("a username differing only in case was accepted")
	}

	// The file holds the hash, the API does not, and only the owner may read it.
	data, _ := os.ReadFile(m.dir("users.json"))
	if !strings.Contains(string(data), "pbkdf2-sha256$600000$") {
		t.Errorf("users.json has no password hash:\n%s", data)
	}
	if got := permOf(t, m.dir("users.json")); got != 0o600 {
		t.Errorf("users.json mode = %o, want 600", got)
	}
	for _, u := range m.Users() {
		if u.PasswordHash != "" {
			t.Error("Users() returned a password hash")
		}
	}

	if _, err := m.UpdateUser(admin.ID, SiteUserInput{Role: "member"}); err == nil {
		t.Error("the only admin was demoted")
	}
	if _, err := m.UpdateUser(sam.ID, SiteUserInput{Password: "x"}); err == nil {
		t.Error("a short new password was accepted")
	}
	if _, err := m.UpdateUser(sam.ID, SiteUserInput{Password: "another long one"}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetRoles([]string{"guest"}); err == nil {
		t.Error("removing a role someone has was accepted")
	}
	if err := m.SetRoles([]string{"member", "Guest", "member"}); err != nil {
		t.Fatal(err)
	}
	if got := m.Settings().Roles; strings.Join(got, ",") != "admin,member,guest" {
		t.Errorf("roles = %v", got)
	}
	if err := m.RemoveUser(sam.ID); err != nil {
		t.Fatal(err)
	}

	// Reloaded from disk.
	again := NewSiteManager(m.dataDir, nil)
	if len(again.Users()) != 1 || again.Users()[0].Email != "owner@example.com" {
		t.Errorf("reloaded users = %+v", again.Users())
	}
}

// An account moved over from another app keeps its bcrypt hash, and with it
// the password its owner had there (site/test/site.test.ts signs in with it).
func TestSiteAccountWithBroughtHash(t *testing.T) {
	m := newTestSite(t)
	const salted = "F48X9LFXjtA2Te5nMw.Y/emwxyBEOJIqP85ZEYE.5adtF22W1jdn2"
	add := func(username, hash, password string) (SiteUser, error) {
		return m.AddUser(SiteUserInput{Kind: "password", Username: username, PasswordHash: hash, Password: password, Role: "member"})
	}
	for _, h := range []string{"$2b$10$" + salted, "$2y$12$" + salted, "$2a$14$" + salted} {
		name := "u" + h[2:3] + h[4:6]
		u, err := add(name, h, "")
		if err != nil {
			t.Fatalf("%s: %v", h[:7], err)
		}
		if u.PasswordHash != "" {
			t.Error("AddUser returned the hash")
		}
		i := slices.IndexFunc(m.users, func(x SiteUser) bool { return x.ID == u.ID })
		if m.users[i].PasswordHash != h {
			t.Errorf("%s stored as %q, want it as it came", name, m.users[i].PasswordHash)
		}
	}
	for _, c := range []struct{ hash, password, want string }{
		{"$2b$10$" + salted, "long enough pw", "not both"},
		{"$2b$09$" + salted, "", "cost 9"},
		{"$2b$15$" + salted, "", "cost 15"},
		{"$2x$10$" + salted, "", "bcrypt"},
		{"$2b$10$" + salted[1:], "", "bcrypt"},
		{"$2b$10$" + salted + "\n", "", "bcrypt"},
		{"pbkdf2-sha256$1$c2FsdA==$aGFzaA==", "", "bcrypt"},
	} {
		if _, err := add("other", c.hash, c.password); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v, want it to mention %q", c.hash, err, c.want)
		}
	}
	if _, err := add("other", "", "short"); err == nil {
		t.Error("a short password was accepted")
	}
}

// The site process (Node) must accept the hashes Fridolin writes.
func TestSitePasswordHashMatchesNode(t *testing.T) {
	node, err := findNode()
	if err != nil {
		t.Skip("node not installed")
	}
	h, err := hashSitePassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	script := `
const { pbkdf2Sync, timingSafeEqual } = require('node:crypto')
const [, iter, salt, hash] = process.argv[1].split('$')
const want = Buffer.from(hash, 'base64')
const got = pbkdf2Sync(process.argv[2], Buffer.from(salt, 'base64'), Number(iter), want.length, 'sha256')
process.stdout.write(timingSafeEqual(got, want) ? 'ok' : 'no')`
	for pw, want := range map[string]string{"correct horse battery": "ok", "wrong": "no"} {
		out, err := exec.Command(node, "-e", script, h, pw).Output()
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != want {
			t.Errorf("node says %q for %q, want %q", out, pw, want)
		}
	}
}

func TestSiteSettingsValidation(t *testing.T) {
	m := newTestSite(t)
	str := func(s string) *string { return &s }
	for _, bad := range []SiteSettingsUpdate{
		{Domain: str("not a domain")},
		{Domain: str("localhost")},
		{Listen: str("8780")},
		{Listen: str("127.0.0.1:0")},
		{Title: str("")},
	} {
		if _, err := m.UpdateSettings(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	tunnel := true
	if _, err := m.UpdateSettings(SiteSettingsUpdate{Tunnel: &tunnel}); err == nil {
		t.Error("the tunnel was turned on without a token")
	}
	s, err := m.UpdateSettings(SiteSettingsUpdate{Domain: str("https://Example.com/"), Title: str("Team site")})
	if err != nil {
		t.Fatal(err)
	}
	if s.Domain != "example.com" || s.Title != "Team site" {
		t.Errorf("settings = %+v", s)
	}
	if err := m.SetTunnelToken("short"); err == nil {
		t.Error("a short tunnel token was accepted")
	}
}

func TestSiteRepoURLs(t *testing.T) {
	for u, want := range map[string]bool{
		"git@github.com:you/site.git":      true,
		"ssh://git@example.com/x/site.git": true,
		"https://github.com/you/site":      true,
		"/srv/git/site.git":                true,
		"--upload-pack=evil":               false,
		"ftp://example.com/site":           false,
		"site":                             false,
	} {
		if got := validRepoURL(u); got != want {
			t.Errorf("validRepoURL(%q) = %v, want %v", u, got, want)
		}
	}
	for u, want := range map[string]string{
		"git@github.com:you/site.git":    "you/site",
		"https://github.com/you/site":    "you/site",
		"ssh://git@github.com/you/s.git": "you/s",
		"git@example.com:you/site.git":   "",
	} {
		if got := githubRepo(u); got != want {
			t.Errorf("githubRepo(%q) = %q, want %q", u, got, want)
		}
	}
}

func TestSiteReviewManifest(t *testing.T) {
	old := map[string]any{"route": "/a", "access": map[string]any{"roles": []any{"admin"}}, "app": map[string]any{"dist": "d"}}
	same := reviewManifest(old, old)
	if len(same) != 0 {
		t.Errorf("no change reported %v", same)
	}
	wider := map[string]any{
		"route": "/a", "access": map[string]any{"roles": []any{"*"}},
		"app":          map[string]any{"dist": "d", "public": []any{"x.json"}},
		"server":       map[string]any{"entry": "server.ts"},
		"capabilities": map[string]any{"files": map[string]any{"db": map[string]any{"path": "~/x.db", "access": "write"}}},
	}
	var fields []string
	for _, it := range reviewManifest(old, wider) {
		fields = append(fields, it.Field)
	}
	if got := strings.Join(fields, ","); got != "access,server,capabilities.files,app.public" {
		t.Errorf("review fields = %s", got)
	}
}

func TestSiteRepoStarterDeployAndReview(t *testing.T) {
	ctx := context.Background()
	m := newTestSite(t)
	remote := newBareRepo(t)
	if err := m.ConnectRepo(ctx, SiteRepo{URL: "--upload-pack=x"}); err == nil {
		t.Fatal("an option-like URL was accepted")
	}
	if err := m.ConnectRepo(ctx, SiteRepo{URL: remote, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	up, err := m.Fetch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if up.Head != "" || len(up.Commits) != 0 {
		t.Fatalf("an empty repository has updates: %+v", up)
	}
	if err := m.AddStarter(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.AddStarter(ctx); err == nil {
		t.Error("the starter was added twice")
	}
	up, err = m.Fetch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(up.Commits) != 1 || len(up.Changes) != 1 || up.Changes[0].ID != "welcome" || up.Changes[0].Status != "added" {
		t.Fatalf("after the starter: %+v", up)
	}
	first := up.Head

	if err := m.Deploy(ctx, "abc"); err == nil {
		t.Error("a short commit id was accepted")
	}
	if err := m.Deploy(ctx, first); err != nil {
		t.Fatal(err)
	}
	waitDeploy(t, m)
	if m.Settings().Approved != first {
		t.Fatalf("approved = %q, want %s", m.Settings().Approved, first)
	}
	if !fileExists(filepath.Join(m.modulesDirOf(first), "welcome", "module.json")) {
		t.Fatal("the deployed checkout has no welcome module")
	}

	// Someone pushes a change that widens access and adds server code, and
	// a commit on another branch.
	work := filepath.Join(t.TempDir(), "work")
	gitT(t, filepath.Dir(work), "clone", "-q", remote, work)
	mf := filepath.Join(work, "modules", "welcome", "module.json")
	data, _ := os.ReadFile(mf)
	data = bytes.Replace(data, []byte(`"app":`), []byte(`"server": { "entry": "server.ts" }, "app":`), 1)
	os.WriteFile(mf, data, 0o644)
	os.WriteFile(filepath.Join(work, "modules", "welcome", "server.ts"), []byte("export default () => null\n"), 0o644)
	gitT(t, work, "add", "-A")
	gitT(t, work, "commit", "-q", "-m", "Give welcome a server")
	gitT(t, work, "push", "-q", "origin", "main")
	gitT(t, work, "checkout", "-q", "-b", "other")
	os.WriteFile(filepath.Join(work, "README.md"), []byte("other\n"), 0o644)
	gitT(t, work, "commit", "-q", "-am", "Elsewhere")
	gitT(t, work, "push", "-q", "origin", "other")
	other := gitT(t, work, "rev-parse", "HEAD")

	up, err = m.Fetch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(up.Commits) != 1 || up.Commits[0].Subject != "Give welcome a server" {
		t.Fatalf("updates = %+v", up.Commits)
	}
	if len(up.Changes) != 1 || up.Changes[0].Status != "changed" || len(up.Changes[0].Review) != 1 || up.Changes[0].Review[0].Field != "server" {
		t.Fatalf("changes = %+v", up.Changes)
	}
	if err := m.Deploy(ctx, other); err == nil || !strings.Contains(err.Error(), "not on the main branch") {
		t.Errorf("a commit from another branch was accepted: %v", err)
	}
	second := up.Head
	if err := m.Deploy(ctx, second); err != nil {
		t.Fatal(err)
	}
	waitDeploy(t, m)

	// Rolling back to the first deploy reuses its build.
	if err := m.Deploy(ctx, first); err != nil {
		t.Fatal(err)
	}
	j := waitDeploy(t, m)
	if !strings.Contains(strings.Join(j.Log, "\n"), "Reusing the build") {
		t.Errorf("the rollback rebuilt: %v", j.Log)
	}
	if m.Settings().Approved != first || len(m.Settings().History) != 2 {
		t.Errorf("after the rollback: approved %s, history %+v", m.Settings().Approved, m.Settings().History)
	}
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

// The whole thing: starter repository → deploy → the Node site process
// serving it → sign in → the welcome tile.
func TestSiteProcessEndToEnd(t *testing.T) {
	requireSiteRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newTestSite(t)
	if err := m.ConnectRepo(ctx, SiteRepo{URL: newBareRepo(t)}); err != nil {
		t.Fatal(err)
	}
	if err := m.AddStarter(ctx); err != nil {
		t.Fatal(err)
	}
	up, _ := m.Fetch(ctx)
	if _, err := m.AddUser(SiteUserInput{Kind: "google", Email: "owner@example.com", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddUser(SiteUserInput{Kind: "password", Username: "sam", Password: "long enough pw", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	listen := freePort(t)
	on := true
	if _, err := m.UpdateSettings(SiteSettingsUpdate{Listen: &listen}); err != nil {
		t.Fatal(err)
	}
	m.Start(ctx)
	defer m.Stop()
	if err := m.Deploy(ctx, up.Head); err != nil {
		t.Fatal(err)
	}
	waitDeploy(t, m)
	if _, err := m.UpdateSettings(SiteSettingsUpdate{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 60*time.Second, "the site to answer", func() bool { return siteHealthy(listen) })

	body, _ := json.Marshal(map[string]string{"username": "sam", "password": "long enough pw"})
	resp, err := http.Post("http://"+listen+"/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("sign-in: %d\n%s", resp.StatusCode, strings.Join(m.site.status().Log, "\n"))
	}
	req, _ := http.NewRequest("GET", "http://"+listen+"/api/modules", nil)
	for _, c := range resp.Cookies() {
		req.AddCookie(c)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Modules []struct{ ID string } `json:"modules"`
	}
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list.Modules) != 1 || list.Modules[0].ID != "welcome" {
		t.Fatalf("tiles = %+v", list.Modules)
	}

	// The site never sees the bridge's environment.
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-not-for-the-site")
	cmd, err := m.siteCommand(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "ANTHROPIC") {
			t.Errorf("the site process gets %s", kv)
		}
	}

	off := false
	if _, err := m.UpdateSettings(SiteSettingsUpdate{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if siteHealthy(listen) {
		t.Error("the site still answers after being turned off")
	}
}

// Saving or removing the site's own Google client reaches the running site
// without a manual restart; so does a change to Fridolin's shared client
// while the site uses that one.
func TestSiteGoogleClientReachesRunningSite(t *testing.T) {
	requireSiteRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newTestSite(t)
	shared := filepath.Join(m.dataDir, "google", "oauth_client.json") // as GoogleAuth.clientPath
	m.googleClient = func() string { return shared }
	clientJSON := func(id string) string { return `{"web":{"client_id":"` + id + `","client_secret":"s"}}` }
	setShared := func(id string) {
		t.Helper()
		os.MkdirAll(filepath.Dir(shared), 0o700)
		if err := os.WriteFile(shared, []byte(clientJSON(id)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	setShared("shared-1")

	if err := m.ConnectRepo(ctx, SiteRepo{URL: newBareRepo(t)}); err != nil {
		t.Fatal(err)
	}
	if err := m.AddStarter(ctx); err != nil {
		t.Fatal(err)
	}
	up, _ := m.Fetch(ctx)
	listen := freePort(t)
	if _, err := m.UpdateSettings(SiteSettingsUpdate{Listen: &listen}); err != nil {
		t.Fatal(err)
	}
	m.Start(ctx)
	defer m.Stop()
	if err := m.Deploy(ctx, up.Head); err != nil {
		t.Fatal(err)
	}
	waitDeploy(t, m)
	on := true
	if _, err := m.UpdateSettings(SiteSettingsUpdate{Enabled: &on}); err != nil {
		t.Fatal(err)
	}

	b := &Bridge{site: m}
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
		waitFor(t, 60*time.Second, what, func() bool { return signInClient() == id })
		var rt siteRuntime
		data, _ := os.ReadFile(m.dir("site.runtime.json"))
		if err := json.Unmarshal(data, &rt); err != nil || rt.GoogleClientFile != file {
			t.Errorf("%s: runtime.json names %q (%v), want %q", what, rt.GoogleClientFile, err, file)
		}
	}
	expect("the shared client", "shared-1", shared)

	call("PUT", clientJSON("site-1"))
	expect("the site's own client once saved", "site-1", m.dir("google_client.json"))

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
	b := &Bridge{site: newTestSite(t)}
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
	if code, out := call("PUT", "/api/site/settings", `{"enabled": true}`); code != 400 || !strings.Contains(out["error"].(string), "deploy") {
		t.Errorf("turning on without a deploy: %d %v", code, out)
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
