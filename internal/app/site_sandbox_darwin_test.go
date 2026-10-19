//go:build darwin

package app

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/sandbox"
)

// TestSiteSandboxEnforcesDeclarations runs system programs under the live
// site's profile, as module code would run them: reads and writes reach
// what a module declares, by its access mode, and nothing else; no
// declaration opens Fridolin's data folder or the usual secrets; the site's
// own files are readable, not writable; a declared folder stays put; only
// declared programs run, and none reaches the bridge's own port. No node
// needed: this is the profile, under the real sandbox-exec.
func TestSiteSandboxEnforcesDeclarations(t *testing.T) {
	requireSandboxExec(t)
	home := HomeDir()
	data := filepath.Join(home, config.DataDirName)
	site := filepath.Join(data, "site")
	files := t.TempDir()
	mod := filepath.Join(site, "deploys", "abc", "modules", "m")
	write := func(p, body string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	readable := write(filepath.Join(files, "read", "note.txt"), "note")
	readDir := filepath.Join(files, "docs")
	inReadDir := write(filepath.Join(readDir, "a.txt"), "a")
	db := write(filepath.Join(files, "rw", "m.db"), "db")
	rwDir := filepath.Join(files, "notes")
	inRwDir := write(filepath.Join(rwDir, "old.txt"), "old")
	undeclared := write(filepath.Join(files, "rw", "other.txt"), "other")
	sshKey := write(filepath.Join(home, ".ssh", "id_test"), "key")
	token := write(filepath.Join(home, ".example-token"), "tok")
	edge := write(filepath.Join(home, "Library", "Application Support", "Microsoft Edge Beta", "Default", "Cookies"), "cookies")
	secrets := write(filepath.Join(site, "secrets", "m.json"), `{"K":"v"}`)
	users := write(filepath.Join(site, "users.json"), `{"users":[]}`)
	shellFile := write(filepath.Join(site, "shell", "server", "main.ts"), "// shell")
	deployFile := write(filepath.Join(mod, "server.ts"), "// module")
	config := write(filepath.Join(files, "elsewhere", "config.json"), "{}")
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	bridge, other := httptest.NewServer(ok), httptest.NewServer(ok)
	defer bridge.Close()
	defer other.Close()
	_, bridgePort, _ := net.SplitHostPort(bridge.Listener.Addr().String())

	in := sandbox.SitePolicy{
		Home: home, DataDir: data,
		Shell: filepath.Join(site, "shell"), Deploy: filepath.Join(site, "deploys", "abc"),
		Tmp: filepath.Join(site, "tmp"), Reads: []string{users},
		SiteLimits: sandbox.SiteLimits{Deny: []string{config}, BridgePort: bridgePort},
		Modules: []sandbox.SiteModule{{ID: "m", Dir: mod, Files: []sandbox.FileCapability{
			{Name: "note", Path: readable, Access: "read"},
			{Name: "docs", Path: readDir, Access: "read", Folder: true},
			{Name: "db", Path: db, Access: "write"},
			{Name: "notes", Path: rwDir, Access: "write", Folder: true},
			// Declared, but out of every module's reach.
			{Name: "key", Path: sshKey, Access: "read"},
			{Name: "token", Path: token, Access: "read"},
			{Name: "edge", Path: edge, Access: "read"},
			{Name: "secrets", Path: secrets, Access: "read"},
			{Name: "users", Path: users, Access: "write"},
			{Name: "config", Path: config, Access: "read"},
		}, Commands: []string{"/bin/cat", "/bin/mv", "/bin/ln", "/bin/rm", "/usr/bin/curl"}}},
		Path: "/usr/bin:/bin",
	}
	profile, err := siteSandboxProfile(in)
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(sandbox.ExecPath, append([]string{"-p", profile}, args...)...)
		cmd.Dir = files
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	sh := func(script string) (string, error) { return run("/bin/sh", "-c", script) }

	for _, p := range []string{readable, inReadDir, db, inRwDir, users, shellFile, deployFile} {
		if out, err := run("/bin/cat", p); err != nil {
			t.Errorf("reading %s was denied: %v %s", p, err, out)
		}
	}
	for _, p := range []string{undeclared, sshKey, token, edge, secrets, config} {
		if out, err := run("/bin/cat", p); err == nil {
			t.Errorf("read %s: %s", p, out)
		}
	}
	// Writes: the declared file (and SQLite's journal next to it), in the
	// declared folder; not a read-only declaration, the site's own files,
	// or anything else.
	for _, p := range []string{db, db + "-journal", filepath.Join(rwDir, "new.txt"), filepath.Join(files, "tmp-not-declared")} {
		_, err := sh("echo x > " + shellQuote(p))
		if allowed := !strings.HasSuffix(p, "tmp-not-declared"); allowed != (err == nil) {
			t.Errorf("writing %s: err %v, want allowed=%v", p, err, allowed)
		}
	}
	for _, p := range []string{readable, inReadDir, users, shellFile, deployFile, filepath.Join(home, "new.txt")} {
		if _, err := sh("echo x > " + shellQuote(p)); err == nil {
			t.Errorf("wrote %s", p)
		}
	}
	// The declared folder cannot be moved aside or swapped for a symlink,
	// nor the declared file replaced by one.
	if out, err := run("/bin/mv", rwDir, rwDir+".moved"); err == nil {
		t.Errorf("moved the declared folder: %s", out)
	}
	if out, err := sh("/bin/rm " + shellQuote(db) + " && /bin/ln -s " + shellQuote(undeclared) + " " + shellQuote(db)); err == nil {
		t.Errorf("replaced the declared file with a symlink: %s", out)
	}
	// Programs: the declared ones run, others do not.
	if out, err := run("/bin/date"); err == nil {
		t.Errorf("ran an undeclared program: %s", out)
	}
	// The network is open, but for the bridge's port.
	if out, err := run("/usr/bin/curl", "-sf", "-m", "5", other.URL); err != nil || out != "ok" {
		t.Errorf("another local server: %v %s", err, out)
	}
	if out, err := run("/usr/bin/curl", "-sf", "-m", "5", bridge.URL); err == nil {
		t.Errorf("reached the bridge's port: %s", out)
	}
}
