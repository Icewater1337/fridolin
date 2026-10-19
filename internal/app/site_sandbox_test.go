package app

import (
	"net/http"
	"net/http/httptest"

	"os/exec"
	"path/filepath"

	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/sandbox"
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
		if out, err := exec.Command(sandbox.ExecPath, "-p", "(version 1)(allow default)", "/usr/bin/true").CombinedOutput(); err != nil {
			t.Skipf("%s cannot apply a profile here: %v: %s", sandbox.ExecPath, err, out)
		}
	}
}

func TestSiteAPISecrets(t *testing.T) {
	b := &App{site: newTestSite(t)}
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

// TestSandboxProfilerSitePaths: the site's sandbox keeps from module code
// what the brains' keeps of the bridge itself: its data dir, DB, config,
// credentials and sandbox.deny_paths; and keeps the bridge's binary unwritable.
func TestSandboxProfilerSitePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := &config.File{DataDir: filepath.Join(home, "data"), DBPath: filepath.Join(home, "db", "bridge.db")}
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
