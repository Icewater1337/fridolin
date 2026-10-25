package site

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// writeTree writes files (by slash path) below dir.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, body := range files {
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

const financeManifest = `{"version": 1, "id": "finance", "title": "Finance", "route": "/finance", "access": {"roles": ["*"]}, "server": {"entry": "server.ts"}}`

// A site repository as people keep one: no package.json at its root, the
// modules under dashboard/, whose package.json lists what they import.
var subfolderRepo = map[string]string{
	"README.md":                             "the site\n",
	"dashboard/package.json":                `{"type": "module", "dependencies": {"express": "^5.2.1"}}`,
	"dashboard/package-lock.json":           `{"lockfileVersion": 3, "packages": {}}`,
	"dashboard/lib/search.ts":               "export const search = 'found'\n",
	"dashboard/modules/finance/module.json": financeManifest,
	"dashboard/modules/finance/server.ts": `import express from 'express'
import { search } from '../../lib/search.ts'
export default () => {
  const r = express.Router()
  r.get('/ping', (_req, res) => { res.json({ search }) })
  return r
}
`,
	// A module with a package.json of its own, and no lock file.
	"dashboard/modules/hands/module.json":  `{"version": 1, "id": "hands", "title": "Hands", "route": "/hands", "access": {"roles": ["*"]}, "server": {"entry": "server.ts"}}`,
	"dashboard/modules/hands/package.json": `{"type": "module"}`,
	"dashboard/modules/hands/server.ts":    "import express from 'express'\nexport default () => express.Router().get('/ping', (_req, res) => { res.json({ id: 'hands' }) })\n",
}

func TestPackageDirs(t *testing.T) {
	top := t.TempDir()
	writeTree(t, top, subfolderRepo)
	writeTree(t, top, map[string]string{
		// Not a module: no manifest.
		"dashboard/modules/notes/package.json": "{}",
		"dashboard/modules/notes/x.ts":         "",
	})
	dash := filepath.Join(top, "dashboard")
	want := []string{dash, filepath.Join(dash, "modules", "hands")}
	if got := packageDirs(top, dash); !slices.Equal(got, want) {
		t.Errorf("subdir dashboard: %v, want %v", got, want)
	}
	// The repository's root as the site's folder: no modules/ there, no package.json.
	if got := packageDirs(top, top); len(got) != 0 {
		t.Errorf("the root: %v", got)
	}

	// Modules at the root, the package.json above them at the top.
	flat := t.TempDir()
	writeTree(t, flat, map[string]string{"package.json": "{}", "modules/finance/module.json": financeManifest})
	if got := packageDirs(flat, flat); !slices.Equal(got, []string{flat}) {
		t.Errorf("flat: %v", got)
	}
	// Nothing above the checkout counts, though Node would look there.
	inner := filepath.Join(flat, "deploy")
	writeTree(t, inner, map[string]string{"modules/finance/module.json": financeManifest})
	if got := packageDirs(inner, inner); len(got) != 0 {
		t.Errorf("above the checkout: %v", got)
	}
}

// pushTree commits files to the site repository's branch.
func pushTree(t *testing.T, remote string, files map[string]string, msg string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	gitT(t, filepath.Dir(work), "clone", "-q", remote, work)
	writeTree(t, work, files)
	gitT(t, work, "add", "-A")
	gitT(t, work, "commit", "-q", "-m", msg)
	gitT(t, work, "push", "-q", "origin", "HEAD:main")
}

// A deploy installs into every folder with a module's nearest package.json,
// reuses an install whose lock file it has seen, and the live site's start
// installs again what a moved data dir left behind.
func TestSiteDeployInstallsEachModulesPackage(t *testing.T) {
	ctx := t.Context()
	opts := testSiteOptions(t)
	var installs []string
	opts.NPMInstall = func(ctx context.Context, dir string, prod bool) error {
		installs = append(installs, dir)
		return os.MkdirAll(filepath.Join(dir, "node_modules", "express"), 0o700)
	}
	m := NewSiteManager(opts)
	remote := newBareRepo(t)
	pushTree(t, remote, subfolderRepo, "The dashboard")
	if err := m.ConnectRepo(ctx, SiteRepo{URL: remote, Subdir: "dashboard"}); err != nil {
		t.Fatal(err)
	}
	up, err := m.Fetch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Deploy(ctx, up.Head); err != nil {
		t.Fatal(err)
	}
	j := waitDeploy(t, m)
	deploy := m.deployDir(up.Head)
	dash := filepath.Join(deploy, "dashboard")
	hands := filepath.Join(dash, "modules", "hands")
	if !slices.Equal(installs, []string{dash, hands}) {
		t.Fatalf("installed in %v, want the dashboard and hands", installs)
	}
	if log := strings.Join(j.Log, "\n"); !strings.Contains(log, "Installing dependencies (npm) in dashboard\n") || !strings.Contains(log, "in dashboard/modules/hands") {
		t.Errorf("the log does not name the folders: %v", j.Log)
	}

	// The live site's sandbox may run what both installed.
	in := m.sandboxInput("/usr/bin/node", deploy, dash, "", nil)
	for _, d := range []string{filepath.Join(dash, "node_modules"), filepath.Join(hands, "node_modules")} {
		if !slices.Contains(in.Deps, d) {
			t.Errorf("the sandbox's deps %v leave out %s", in.Deps, d)
		}
	}

	// The next commit has the same lock file: the dashboard's install comes
	// from the cache; hands, without a lock file, is installed again.
	pushTree(t, remote, map[string]string{"dashboard/lib/search.ts": "export const search = 'found again'\n"}, "Search again")
	up2, err := m.Fetch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	installs = nil
	if err := m.Deploy(ctx, up2.Head); err != nil {
		t.Fatal(err)
	}
	waitDeploy(t, m)
	dash2 := filepath.Join(m.deployDir(up2.Head), "dashboard")
	if !slices.Equal(installs, []string{filepath.Join(dash2, "modules", "hands")}) {
		t.Errorf("the second deploy installed in %v", installs)
	}
	if !fsx.Exists(filepath.Join(dash2, "node_modules", "express")) {
		t.Error("the cached install was not copied into the second deploy")
	}

	// A data dir moved without node_modules: starting the site installs them.
	for _, d := range []string{dash2, filepath.Join(dash2, "modules", "hands")} {
		if err := os.RemoveAll(filepath.Join(d, "node_modules")); err != nil {
			t.Fatal(err)
		}
	}
	installs = nil
	m.ensureLiveDeps(ctx)
	if !fsx.Exists(filepath.Join(dash2, "node_modules", "express")) || !fsx.Exists(filepath.Join(dash2, "modules", "hands", "node_modules")) {
		t.Error("the live deploy's dependencies were not installed again")
	}
	if !slices.Equal(installs, []string{filepath.Join(dash2, "modules", "hands")}) {
		t.Errorf("the start installed in %v; the dashboard's comes from the cache", installs)
	}
}

// The whole thing with the package.json in a subfolder: the deploy installs
// express beside it, and the site process serves the module that imports it.
func TestSiteSubfolderPackageEndToEnd(t *testing.T) {
	requireSiteRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newTestSite(t)
	remote := newBareRepo(t)
	pushTree(t, remote, subfolderRepo, "The dashboard")
	if err := m.ConnectRepo(ctx, SiteRepo{URL: remote, Subdir: "dashboard"}); err != nil {
		t.Fatal(err)
	}
	up, err := m.Fetch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddUser(SiteUserInput{Kind: "password", Username: "sam", Password: "long enough pw", Role: "member"}); err != nil {
		t.Fatal(err)
	}
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
	get := func(path string) string {
		t.Helper()
		req, _ := http.NewRequest("GET", "http://"+listen+path, nil)
		for _, c := range resp.Cookies() {
			req.AddCookie(c)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return string(b)
	}
	if got := get("/finance/api/ping"); !strings.Contains(got, `"found"`) {
		t.Errorf("finance answered %s\n%s", got, strings.Join(m.site.status().Log, "\n"))
	}
	if got := get("/hands/api/ping"); !strings.Contains(got, `"hands"`) {
		t.Errorf("hands answered %s", got)
	}

	// As after a core move (docs/CORE.md): the export leaves node_modules
	// out, and the site starts on the imported deploy without a new one.
	m.Stop()
	if err := os.RemoveAll(filepath.Join(m.deployDir(up.Head), "dashboard", "node_modules")); err != nil {
		t.Fatal(err)
	}
	m.Start(ctx)
	testutil.WaitFor(t, 60*time.Second, "the site to answer again", func() bool { return siteHealthy(listen) })
	if got := get("/finance/api/ping"); !strings.Contains(got, `"found"`) {
		t.Errorf("after the move, finance answered %s\n%s", got, strings.Join(m.site.status().Log, "\n"))
	}
}

// A builder's workspace gets the same installs, as the brain, and a module
// that brings a package.json later gets its own before a check or preview.
func TestSiteWorkspaceInstallsEachModulesPackage(t *testing.T) {
	ctx := t.Context()
	m, _ := newTestBuilderSite(t)
	var installs []string
	m.workspaceInstall = func(ctx context.Context, brainID, dir string) error {
		installs = append(installs, dir)
		return os.MkdirAll(filepath.Join(dir, "node_modules"), 0o700)
	}
	remote := newBareRepo(t)
	pushTree(t, remote, subfolderRepo, "The dashboard")
	if err := m.ConnectRepo(ctx, SiteRepo{URL: remote, Subdir: "dashboard"}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetBuilder(ctx, "web", true); err != nil {
		t.Fatal(err)
	}
	dash := filepath.Join(m.workspace("web"), "dashboard")
	if want := []string{dash, filepath.Join(dash, "modules", "hands")}; !slices.Equal(installs, want) {
		t.Fatalf("the workspace installed in %v, want %v", installs, want)
	}

	installs = nil
	writeTree(t, dash, map[string]string{
		"modules/charts/module.json":  `{"version": 1, "id": "charts", "title": "Charts", "route": "/charts", "access": {"roles": ["*"]}, "server": {"entry": "server.ts"}}`,
		"modules/charts/package.json": `{"type": "module"}`,
	})
	if err := m.ensureWorkspaceDeps(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(dash, "modules", "charts")}; !slices.Equal(installs, want) {
		t.Errorf("then installed in %v, want %v", installs, want)
	}
}
