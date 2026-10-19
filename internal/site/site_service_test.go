package site

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"io/fs"

	"net/http"

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

// TestSiteShellIsTheEmbeddedProcess checks what ensureShell unpacks: the
// site process from the checkout's site/, byte for byte, without the
// starter template and the builder plugin, and a stamp over the files'
// names and contents. The stamp names each file as from the checkout's
// root (site/server/...), as it did when the files were embedded from
// there, so the same files keep the same stamp and an upgrade does not
// install the site's dependencies again for nothing.
func TestSiteShellIsTheEmbeddedProcess(t *testing.T) {
	m := newTestSite(t)
	dir, err := m.ensureShell(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(repoDir, "site")
	h := sha256.New()
	var want []string
	// In the order fs.WalkDir visits them, which go:embed's own rules
	// decide the set of: no file whose name starts with . or _.
	for _, top := range []string{"package-lock.json", "package.json", "server", "web"} {
		err := filepath.WalkDir(filepath.Join(src, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_") {
				return err
			}
			rel, err := filepath.Rel(src, p)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			h.Write([]byte("site/" + filepath.ToSlash(rel) + "\x00"))
			h.Write(data)
			want = append(want, rel)
			if got, err := os.ReadFile(filepath.Join(dir, rel)); err != nil || !bytes.Equal(got, data) {
				t.Errorf("shell/%s is not the checkout's site/%s (%v)", rel, rel, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.Name() == "node_modules" {
				return fs.SkipDir
			}
			return err
		}
		if rel, _ := filepath.Rel(dir, p); rel != ".fridolin-shell" {
			got = append(got, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("the shell holds\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	stamp, err := os.ReadFile(filepath.Join(dir, ".fridolin-shell"))
	if want := hex.EncodeToString(h.Sum(nil)) + "\n"; err != nil || string(stamp) != want {
		t.Errorf("stamp = %q (%v), want %q", stamp, err, want)
	}
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
	again := NewSiteManager(SiteOptions{DataDir: m.dataDir})
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
		"git@github.com:-you/site.git":   "",
		"git@github.com:you/..":          "",
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
	if !fsx.Exists(filepath.Join(m.modulesDirOf(first), "welcome", "module.json")) {
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
	testutil.WaitFor(t, 60*time.Second, "the site to answer", func() bool { return siteHealthy(listen) })

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

// Run from a git hook or `git rebase -x`, the bridge inherits GIT_DIR and
// GIT_INDEX_FILE. The site's commits and pushes must still go to the
// site's own repository, never to the one those variables name.
func TestSiteGitIgnoresInheritedGitVariables(t *testing.T) {
	ctx := context.Background()
	decoy := t.TempDir()
	gitT(t, decoy, "init", "-q", "-b", "main")
	gitT(t, decoy, "commit", "-q", "--allow-empty", "-m", "decoy")
	before := gitT(t, decoy, "rev-parse", "HEAD")
	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_WORK_TREE", decoy)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoy, ".git", "index"))

	m := newTestSite(t)
	remote := newBareRepo(t)
	if err := m.ConnectRepo(ctx, SiteRepo{URL: remote, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := m.AddStarter(ctx); err != nil {
		t.Fatal(err)
	}
	if after := gitT(t, decoy, "rev-parse", "HEAD"); after != before {
		t.Errorf("the site committed into the repository GIT_DIR names: HEAD %s, was %s", after, before)
	}
	if n := gitT(t, decoy, "rev-list", "--all", "--count"); n != "1" {
		t.Errorf("the repository GIT_DIR names has %s commits, want 1", n)
	}
	if log := gitT(t, remote, "log", "--oneline", "main"); log == "" {
		t.Error("the starter did not reach the site's own remote")
	}
}

func TestGitEnv(t *testing.T) {
	got := gitEnv([]string{"HOME=/Users/you", "GIT_DIR=/x/.git", "GIT_INDEX_FILE=/x/.git/index", "GITHUB_TOKEN=t", "PATH=/bin"})
	want := []string{"HOME=/Users/you", "GITHUB_TOKEN=t", "PATH=/bin"}
	if !slices.Equal(got, want) {
		t.Errorf("gitEnv = %q, want %q", got, want)
	}
}
