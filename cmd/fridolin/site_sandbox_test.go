package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/textx"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// requireSiteRuntime skips a test that runs the live site without what it
// needs: node, the site package's node_modules, and on macOS a sandbox-exec
// that can apply a profile (not inside another sandbox), since the live site
// runs only under one there.
func requireSiteRuntime(t *testing.T) {
	t.Helper()
	if _, err := findNode(); err != nil {
		t.Skip("node not installed")
	}
	if !fsx.Exists(filepath.Join(repoDir, "site", "node_modules")) {
		t.Skip("run npm install in site/ first")
	}
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command(sandboxExecPath, "-p", "(version 1)(allow default)", "/usr/bin/true").CombinedOutput(); err != nil {
			t.Skipf("%s cannot apply a profile here: %v: %s", sandboxExecPath, err, out)
		}
	}
}

// writeModule writes modules/<id> in a checkout of the site repository.
func writeSiteModule(t *testing.T, work, id string, manifest map[string]any, server string) {
	t.Helper()
	dir := filepath.Join(work, "modules", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mf := map[string]any{"version": 1, "id": id, "title": id, "route": "/" + id, "access": map[string]any{"roles": []string{"*"}}, "server": map[string]any{"entry": "server.ts"}}
	for k, v := range manifest {
		mf[k] = v
	}
	data, _ := json.MarshalIndent(mf, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "module.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server.ts"), []byte(server), 0o644); err != nil {
		t.Fatal(err)
	}
}

// probeServer is module server code that answers GET /probe with what each
// attempt returned, or "error <code>".
func probeServer(attempts map[string]string) string {
	var b strings.Builder
	b.WriteString(`import express from 'express'
import { readFileSync, writeFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
const attempt = (f) => { try { return String(f()).trim() } catch (e) { return 'error ' + (e.code ?? e.message) } }
export default (ctx) => {
  const r = express.Router()
  r.get('/probe', (_req, res) => res.json({
`)
	for k, v := range attempts {
		b.WriteString("    " + k + ": attempt(() => " + v + "),\n")
	}
	b.WriteString("  }))\n  return r\n}\n")
	return b.String()
}

func jsString(s string) string { b, _ := json.Marshal(s); return string(b) }

// TestSiteModuleCapabilities runs the live site with two modules and asks
// their server code what it can reach. On macOS, under the sandbox: what
// keeper's module.json declares, and nothing else, not even the files that
// hold the modules' secrets. On Linux, which has no sandbox: everything. On
// both, a secret reaches only the module that declares it.
func TestSiteModuleCapabilities(t *testing.T) {
	requireSiteRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newTestSite(t)
	remote := newBareRepo(t)
	if err := m.ConnectRepo(ctx, SiteRepo{URL: remote}); err != nil {
		t.Fatal(err)
	}
	if err := m.AddStarter(ctx); err != nil {
		t.Fatal(err)
	}

	files := t.TempDir()
	declared := filepath.Join(files, "declared", "note.txt")
	undeclared := filepath.Join(files, "undeclared", "private.txt")
	// Its folder does not exist yet: Fridolin creates it for the module.
	written := filepath.Join(files, "data", "out.txt")
	for p, body := range map[string]string{declared: "declared note", undeclared: "not for modules"} {
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	work := filepath.Join(t.TempDir(), "work")
	gitT(t, filepath.Dir(work), "clone", "-q", remote, work)
	writeSiteModule(t, work, "keeper", map[string]any{
		"capabilities": map[string]any{
			"files": map[string]any{
				"note": map[string]any{"path": declared, "access": "read"},
				"out":  map[string]any{"path": written, "access": "write"},
			},
			"commands": []string{"/bin/echo"},
			"secrets":  []string{"API_KEY"},
		},
	}, probeServer(map[string]string{
		"declared":          "readFileSync(ctx.file('note'), 'utf8')",
		"write":             "(writeFileSync(ctx.file('out'), 'written'), readFileSync(ctx.file('out'), 'utf8'))",
		"undeclared":        "readFileSync(" + jsString(undeclared) + ", 'utf8')",
		"undeclaredWrite":   "(writeFileSync(" + jsString(filepath.Join(filepath.Dir(undeclared), "new.txt")) + ", 'x'), 'written')",
		"secretsFile":       "readFileSync(" + jsString(m.secretsFile("keeper")) + ", 'utf8')",
		"command":           "execFileSync('/bin/echo', ['ran'])",
		"undeclaredCommand": "execFileSync('/bin/cat', [" + jsString(declared) + "], { stdio: 'pipe' })",
		"secret":            "ctx.secret('API_KEY')",
	}))
	writeSiteModule(t, work, "snoop", nil, probeServer(map[string]string{
		"secret": "ctx.secret('API_KEY')",
	}))
	gitT(t, work, "add", "-A")
	gitT(t, work, "commit", "-q", "-m", "Add keeper and snoop")
	gitT(t, work, "push", "-q", "origin", "main")

	if err := m.SetSecret("keeper", "API_KEY", "k-123"); err != nil {
		t.Fatal(err)
	}
	// Saved for snoop, which does not declare it: it never reaches the site.
	if err := m.SetSecret("snoop", "API_KEY", "snoop-own"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddUser(SiteUserInput{Kind: "password", Username: "sam", Password: "long enough pw", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	up, err := m.Fetch(ctx)
	if err != nil {
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
	waitFor(t, 60*time.Second, "the site to answer", func() bool { return siteHealthy(listen) })

	body, _ := json.Marshal(map[string]string{"username": "sam", "password": "long enough pw"})
	resp, err := http.Post("http://"+listen+"/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	probe := func(id string) map[string]string {
		t.Helper()
		req, _ := http.NewRequest("GET", "http://"+listen+"/"+id+"/api/probe", nil)
		for _, c := range resp.Cookies() {
			req.AddCookie(c)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var out map[string]string
		if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
			t.Fatalf("%s: %d %v\n%s", id, r.StatusCode, err, strings.Join(m.site.status().Log, "\n"))
		}
		return out
	}
	keeper := probe("keeper")
	want := map[string]string{
		"declared":          "declared note",
		"write":             "written",
		"undeclared":        "error EPERM",
		"undeclaredWrite":   "error EPERM",
		"secretsFile":       "error EPERM",
		"command":           "ran",
		"undeclaredCommand": "error EPERM",
		"secret":            "k-123",
	}
	if runtime.GOOS != "darwin" {
		// No sandbox: module code reaches what the bridge's user can.
		want["undeclared"] = "not for modules"
		want["undeclaredWrite"] = "written"
		want["undeclaredCommand"] = "declared note"
		delete(want, "secretsFile")
		if strings.HasPrefix(keeper["secretsFile"], "error") {
			t.Errorf("without a sandbox the secrets file should be readable: %s", keeper["secretsFile"])
		}
	}
	for k, v := range want {
		if keeper[k] != v {
			t.Errorf("keeper %s = %q, want %q", k, keeper[k], v)
		}
	}
	if data, _ := os.ReadFile(written); string(data) != "written" {
		t.Errorf("the declared file holds %q", data)
	}
	if s := probe("snoop")["secret"]; !strings.Contains(s, "does not declare") {
		t.Errorf("snoop got keeper's secret, or its own undeclared one: %q", s)
	}
	if st := m.SandboxStatus(); st.On != (runtime.GOOS == "darwin") {
		t.Errorf("sandbox status = %+v on %s", st, runtime.GOOS)
	}
}

func TestSiteSecrets(t *testing.T) {
	m := newTestSite(t)
	for _, c := range []struct{ module, name, value string }{
		{"Keeper", "API_KEY", "x"},
		{"../x", "API_KEY", "x"},
		{"keeper", "API KEY", "x"},
		{"keeper", "_KEY", "x"},
		{"keeper", "API_KEY", "  "},
		{"keeper", "API_KEY", strings.Repeat("x", siteSecretMaxLen+1)},
	} {
		if err := m.SetSecret(c.module, c.name, c.value); err == nil {
			t.Errorf("SetSecret(%q, %q, %d bytes) was accepted", c.module, c.name, len(c.value))
		}
	}
	if err := m.SetSecret("keeper", "API_KEY", " k-123\n"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetSecret("orphan", "TOKEN", "o-1"); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(m.dir("secrets")); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("secrets dir: %v %v", st.Mode(), err)
	}
	if st, err := os.Stat(m.secretsFile("keeper")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("secrets file: %v %v", st.Mode(), err)
	}

	// A live commit whose keeper declares two secrets and snoop none.
	commit := strings.Repeat("ab", 20)
	m.settings.Approved = commit
	for id, secrets := range map[string][]string{"keeper": {"API_KEY", "OTHER"}, "snoop": nil} {
		dir := filepath.Join(m.modulesDirOf(commit), id)
		os.MkdirAll(dir, 0o700)
		data, _ := json.Marshal(map[string]any{"id": id, "title": strings.ToUpper(id), "capabilities": map[string]any{"secrets": secrets}})
		os.WriteFile(filepath.Join(dir, "module.json"), data, 0o600)
	}
	if err := m.SetSecret("snoop", "API_KEY", "s-1"); err != nil {
		t.Fatal(err)
	}

	list, err := m.Secrets()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(list)
	for _, v := range []string{"k-123", "o-1", "s-1"} {
		if bytes.Contains(got, []byte(v)) {
			t.Errorf("the list shows a value: %s", got)
		}
	}
	want := `[{"module":"keeper","title":"KEEPER","live":true,"declared":["API_KEY","OTHER"],"set":["API_KEY"]},` +
		`{"module":"orphan","title":"orphan","live":false,"declared":[],"set":["TOKEN"]},` +
		`{"module":"snoop","title":"snoop","live":false,"declared":[],"set":["API_KEY"]}]`
	if string(got) != want {
		t.Errorf("secrets list\n got %s\nwant %s", got, want)
	}

	// The site gets keeper's declared value, trimmed, and nothing of the rest.
	payload, err := m.secretsFor(readModuleCaps(m.modulesDirOf(commit), config.HomeDir()))
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != `{"keeper":{"API_KEY":"k-123"}}` {
		t.Errorf("payload = %s", payload)
	}
	r, err := pipeWith(payload)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := io.ReadAll(r); !bytes.Equal(data, payload) {
		t.Errorf("pipe gave %s", data)
	}
	r.Close()

	if err := m.RemoveSecret("orphan", "TOKEN"); err != nil {
		t.Fatal(err)
	}
	if fsx.Exists(m.secretsFile("orphan")) {
		t.Error("an empty secrets file stays")
	}
	if err := m.RemoveSecret("orphan", "TOKEN"); err == nil {
		t.Error("removing a missing secret succeeded")
	}
}

func TestSiteAPISecrets(t *testing.T) {
	b := &Bridge{site: newTestSite(t)}
	mux := http.NewServeMux()
	b.siteAPI(mux)
	call := func(method, path, body string) (int, string) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, out := call("PUT", "/api/site/secrets/keeper/API_KEY", `{"value": "k-123"}`); code != 200 {
		t.Fatalf("set: %d %s", code, out)
	}
	if code, _ := call("PUT", "/api/site/secrets/keeper/bad%20name", `{"value": "x"}`); code != 400 {
		t.Errorf("a bad name: %d", code)
	}
	code, out := call("GET", "/api/site/secrets", "")
	if code != 200 || !strings.Contains(out, `"set":["API_KEY"]`) || strings.Contains(out, "k-123") {
		t.Errorf("list: %d %s", code, out)
	}
	if code, out := call("GET", "/api/site", ""); code != 200 || !strings.Contains(out, `"sandbox":{"on":`) {
		t.Errorf("status has no sandbox: %d %s", code, out)
	}
	if code, out := call("DELETE", "/api/site/secrets/keeper/API_KEY", ""); code != 200 {
		t.Errorf("remove: %d %s", code, out)
	}
	if _, out := call("GET", "/api/site/secrets", ""); strings.Contains(out, "API_KEY") {
		t.Errorf("after removing: %s", out)
	}
}

// TestSiteProfileClosesBrowserProfiles: no declaration opens a browser's
// profile to module code, the browsers whose release channels the brains'
// profile denies by the start of the folder's name included, and a
// missing declared folder there is left missing. Other programs' folders
// in Application Support stay open to a declaration.
func TestSiteProfileClosesBrowserProfiles(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, config.DataDirName)
	appSupport := filepath.Join(home, "Library", "Application Support")
	q := func(p string) string { s, _ := textx.SBPLString(p); return s }
	// The prefix rule, as the profile writes it; its strings escape a
	// backslash, as the regex's \. needs.
	prefixRule := strings.ReplaceAll("^"+regexp.QuoteMeta(appSupport)+browserProfilePattern(), `\`, `\\`)
	for _, c := range []struct {
		folder string
		closed bool
		deny   string // the rule that keeps it closed
	}{
		{"Microsoft Edge", true, `(deny file-write* (regex "` + prefixRule + `"))`},
		{"Microsoft Edge Beta", true, `(deny file-write* (regex "` + prefixRule + `"))`},
		{"Vivaldi Snapshot", true, `(deny file-write* (regex "` + prefixRule + `"))`},
		{"com.operasoftware.OperaGX", true, `(deny file-write* (regex "` + prefixRule + `"))`},
		{"Google/Chrome", true, `(deny file-write* (subpath ` + q(filepath.Join(appSupport, "Google")) + `))`},
		{"BraveSoftware/Brave-Browser", true, `(deny file-write* (subpath ` + q(filepath.Join(appSupport, "BraveSoftware")) + `))`},
		{"SomeTool", false, ""},
	} {
		t.Run(c.folder, func(t *testing.T) {
			dir := filepath.Join(appSupport, c.folder, "Default")
			in := siteProfileInput{
				Home: home, DataDir: data,
				Shell: filepath.Join(data, "site", "shell"), Deploy: filepath.Join(data, "site", "deploys", "abc"),
				Tmp: filepath.Join(data, "site", "tmp"),
				Modules: []siteModuleCaps{{ID: "m", Dir: filepath.Join(data, "site", "deploys", "abc", "modules", "m"), Files: []siteFileCap{
					{Name: "profile", Path: dir, Access: "write", Folder: true},
				}}},
			}
			if got := in.Unwritable(dir); got != c.closed {
				t.Errorf("unwritable = %v, want %v", got, c.closed)
			}
			prepareDeclaredFolders(in.Modules, in.Unwritable)
			if pathExists(dir) == c.closed {
				t.Errorf("the declared folder exists = %v, want %v", pathExists(dir), !c.closed)
			}
			profile, err := siteSandboxProfile(in)
			if err != nil {
				t.Fatal(err)
			}
			allow := strings.LastIndex(profile, `(allow file-read* file-write* (subpath `+q(dir)+`))`)
			if allow < 0 {
				t.Fatalf("the declaration is not in the profile:\n%s", profile)
			}
			if !c.closed {
				return
			}
			if deny := strings.LastIndex(profile, c.deny); deny < allow {
				t.Errorf("%s does not come after the declaration (%d, %d)", c.deny, deny, allow)
			}
			if read := strings.Replace(c.deny, "file-write*", "file-read*", 1); strings.LastIndex(profile, read) < allow {
				t.Errorf("%s does not come after the declaration", read)
			}
		})
	}
}

// TestSandboxProfilerSitePaths: the site's sandbox keeps from module code
// what the brains' keeps of the bridge itself: its data dir, DB, config,
// credentials and sandbox.deny_paths; and keeps the bridge's binary unwritable.
func TestSandboxProfilerSitePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := &config.Config{DataDir: filepath.Join(home, "data"), DBPath: filepath.Join(home, "db", "bridge.db")}
	cfg.Sandbox.CredentialsDir = filepath.Join(home, "creds")
	cfg.Sandbox.DenyPaths = []string{filepath.Join(home, "private")}
	sp := NewSandboxProfiler(nil, cfg)
	sp.SetConfigFile(filepath.Join(home, "etc", "config.json"))
	sp.bridgeExe = filepath.Join(home, "bin", "fridolin")
	deny, noWrite := sp.sitePaths()
	for _, p := range []string{cfg.DataDir, cfg.DBPath, cfg.DBPath + "-wal", filepath.Join(home, "etc", "config.json"), filepath.Join(home, "etc", ".env"), cfg.Sandbox.CredentialsDir, filepath.Join(home, "private")} {
		if !slices.Contains(deny, p) {
			t.Errorf("deny misses %s: %v", p, deny)
		}
	}
	if !slices.Contains(noWrite, sp.bridgeExe) {
		t.Errorf("noWrite = %v", noWrite)
	}
}
