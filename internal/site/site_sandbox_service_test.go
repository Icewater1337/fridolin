package site

import (
	"bytes"
	"context"
	"encoding/json"

	"io"
	"net/http"

	"os"

	"path/filepath"

	"runtime"

	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"

	"github.com/Icewater1337/fridolin/internal/testutil"
)

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
	testutil.WaitFor(t, 60*time.Second, "the site to answer", func() bool { return siteHealthy(listen) })

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
	payload, err := m.secretsFor(readModuleCaps(m.modulesDirOf(commit), homeDir()))
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
