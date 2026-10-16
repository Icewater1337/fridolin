//go:build darwin

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests run generated profiles under the real sandbox-exec, so they
// check what the sandbox enforces rather than what the profile text says.

func requireSandboxExec(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(sandboxExecPath); err != nil {
		t.Skipf("%s not available: %v", sandboxExecPath, err)
	}
	// Inside another sandbox (go test run from a sandboxed tool) sandbox-exec
	// cannot apply any profile and fails with "Operation not permitted", the
	// same words as a denied access: a test that only expects denials would
	// pass without testing anything.
	if out, err := exec.Command(sandboxExecPath, "-p", "(version 1)(allow default)", "/usr/bin/true").CombinedOutput(); err != nil {
		t.Skipf("%s cannot apply a profile here: %v: %s", sandboxExecPath, err, out)
	}
}

// TestSandboxedRunAppliesProfileInline runs a brain through invoke with the
// sandbox on: the CLI starts under sandbox-exec, cannot read another brain's
// folder, and still starts when TMPDIR is unwritable (which used to break
// the profile file and, before that, the sandbox itself).
func TestSandboxedRunAppliesProfileInline(t *testing.T) {
	requireSandboxExec(t)
	mgr, d, home := queueTestManager(t, 0, `, "sandbox": {"enabled": true}`)
	secret := filepath.Join(home, "b", "secret.txt")
	if err := os.WriteFile(secret, []byte("brain b only"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WCB_FAKE_READ", secret)

	tmp := t.TempDir()
	if err := os.Chmod(tmp, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(tmp, 0o700) })
	t.Setenv("TMPDIR", tmp)

	if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "the run", func() bool { return d.count() == 1 })
	res := d.all()[0].res
	if res.Err != nil {
		t.Fatalf("sandboxed run failed: %v", res.Err)
	}
	if !strings.Contains(res.Text, "read=denied") {
		t.Errorf("brain a's run = %q, want it denied brain b's file", res.Text)
	}
}

// TestSandboxIgnoresPlantedSymlinks: a brain may write its own folder, the
// media dir and its calendar token, so it can swap any of them (or an
// allow_paths folder inside a writable denied dir) for a symlink to a secret.
// Allow rules must not follow such a symlink on the next run.
//
// It covers runs of the brain set as it was resolved. Resolving the brains
// again (a config reload or a restart) runs canonicalDir on every folder,
// which follows a symlink planted in place of a brain's own folder. The
// profile cannot tell that from a configured folder, so only brain
// resolution can refuse it.
func TestSandboxIgnoresPlantedSymlinks(t *testing.T) {
	requireSandboxExec(t)
	homes := []struct {
		name string
		make func(t *testing.T) string
	}{
		{"canonical home", func(t *testing.T) string { return canonicalDir(t.TempDir()) }},
		{"home under /var", func(t *testing.T) string { return filepath.Join(t.TempDir(), "home") }},
	}
	plants := []struct {
		name, link, target, read string // relative to HOME
	}{
		{"own folder", "assistant/a", ".ssh", "assistant/a/id_ed25519"},
		{"media dir", ".fridolin/media", ".ssh", ".fridolin/media/id_ed25519"},
		{"own calendar token", "creds/google_calendar_token_a.json", "creds/google_calendar_token_b.json", "creds/google_calendar_token_a.json"},
		{"allow_paths folder", "Documents/share", ".ssh", "Documents/share/id_ed25519"},
	}
	for _, h := range homes {
		for _, pl := range plants {
			t.Run(h.name+"/"+pl.name, func(t *testing.T) {
				home := h.make(t)
				t.Setenv("HOME", home)
				sp, _ := sandboxFixture(t, home, "b", "clients")
				link := filepath.Join(home, pl.link)
				if err := os.Rename(link, link+".moved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(home, pl.target), link); err != nil {
					t.Fatal(err)
				}
				if allowed, out := sandboxTry(t, sp, home, false, filepath.Join(home, pl.read)); allowed {
					t.Errorf("brain a read the secret through its planted symlink: %q", out)
				}
			})
		}
	}
}

// sandboxLayout is one way to lay out a fake home for the enforcement test.
type sandboxLayout struct {
	name string
	// home creates and returns the HOME to configure.
	home func(t *testing.T) string
	// otherBrain and denyDir name brain b's folder and the deny_paths entry.
	otherBrain, denyDir string
}

// sandboxCheck is one access a brain tries under its profile.
type sandboxCheck struct {
	name  string
	write bool   // write a new file at path instead of reading it
	path  string // relative to HOME
	allow bool
}

// TestSandboxEnforcesProfile builds the effective config, the brain set and
// the profile through the real code, then runs /bin/cat and /bin/sh under
// sandbox-exec as brain a. Every check runs through the configured spelling
// of the path and through the real one, because the sandbox matches real
// paths and an injected brain can use either.
func TestSandboxEnforcesProfile(t *testing.T) {
	requireSandboxExec(t)
	layouts := []sandboxLayout{
		{name: "canonical home", home: func(t *testing.T) string { return canonicalDir(t.TempDir()) },
			otherBrain: "b", denyDir: "clients"},
		// No-break spaces, emoji joiners, quotes and backslashes in folder
		// names. Go's %q quoting broke the first two silently.
		{name: "unusual characters", home: func(t *testing.T) string { return canonicalDir(t.TempDir()) },
			otherBrain: "My\u00a0Brain \"q\" \\ \U0001F468\u200d\U0001F469\u200d\U0001F467",
			denyDir:    "Screenshot 10.00\u202fAM"},
		// t.TempDir() is under /var, a symlink to /private/var, so HOME and
		// the data dir are reached through a symlink. Before the profile
		// resolved its paths, a brain read the data dir via /private/var.
		{name: "home under /var", home: func(t *testing.T) string { return filepath.Join(t.TempDir(), "home") },
			otherBrain: "b", denyDir: "clients"},
		{name: "symlinked home and folders", home: symlinkedHome, otherBrain: "b", denyDir: "clients"},
	}
	checks := []sandboxCheck{
		{"own folder read", false, "assistant/a/notes.md", true},
		{"own folder write", true, "assistant/a/new.md", true},
		{"other brain read", false, "assistant/{b}/diary.md", false},
		{"other brain write", true, "assistant/{b}/planted.md", false},
		{"data dir token", false, ".fridolin/dashboard.token", false},
		{"data dir database", false, ".fridolin/bridge.db", false},
		{"data dir write", true, ".fridolin/planted", false},
		{"media read", false, ".fridolin/media/photo.jpg", true},
		{"media write", true, ".fridolin/media/new.jpg", true},
		{"own calendar token", false, "creds/google_calendar_token_a.json", true},
		{"other brain's calendar token", false, "creds/google_calendar_token_b.json", false},
		{"credentials dir write", true, "creds/planted.json", false},
		{"ssh key", false, ".ssh/id_ed25519", false},
		{"deny_paths entry", false, "{deny}/contract.txt", false},
		{"default-denied Documents", false, "Documents/private.txt", false},
		{"allow_paths entry", false, "Documents/share/ok.txt", true},
	}
	for _, l := range layouts {
		t.Run(l.name, func(t *testing.T) {
			home := l.home(t)
			t.Setenv("HOME", home)
			sp, ownDir := sandboxFixture(t, home, l.otherBrain, l.denyDir)
			expand := strings.NewReplacer("{b}", l.otherBrain, "{deny}", l.denyDir)
			for _, c := range checks {
				configured := filepath.Join(home, expand.Replace(c.path))
				spellings := [][2]string{{"configured", configured}}
				if real := resolvedForTest(t, configured); real != configured {
					spellings = append(spellings, [2]string{"real", real})
				}
				for _, spelling := range spellings {
					label, p := spelling[0], spelling[1]
					if c.write {
						p += "." + label // a fresh file per spelling
					}
					t.Run(c.name+"/"+label, func(t *testing.T) {
						allowed, out := sandboxTry(t, sp, ownDir, c.write, p)
						if allowed != c.allow {
							t.Errorf("%s: allowed=%v, want %v (output %q)", p, allowed, c.allow, out)
						}
					})
				}
			}
		})
	}
}

// symlinkedHome makes HOME a symlink, and the data dir, the credentials
// dir, the deny path, ~/.ssh and the brains' parent folder symlinks out of
// it, the way a home on another volume or a cloud-synced folder looks.
func symlinkedHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	realHome := filepath.Join(root, "home-real")
	if err := os.Mkdir(realHome, 0o700); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{filepath.Join(root, "home"): realHome}
	for _, d := range []string{".fridolin", "creds", "clients", ".ssh", "assistant"} {
		links[filepath.Join(realHome, d)] = filepath.Join(root, "elsewhere", strings.TrimPrefix(d, "."))
	}
	for link, target := range links {
		if err := os.MkdirAll(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, "home")
}

// sandboxFixture writes a config with brains a and b, a credentials dir,
// a deny path and an allow path, loads it the way the bridge does, and
// creates one file for every check. It returns the profiler and a's folder.
func sandboxFixture(t *testing.T, home, otherBrain, denyDir string) (*SandboxProfiler, string) {
	t.Helper()
	cfgJSON, err := json.Marshal(map[string]any{
		"brain_mode": true,
		"data_dir":   "~/.fridolin",
		"sandbox": map[string]any{
			"enabled":         true,
			"credentials_dir": "~/creds",
			"deny_paths":      []string{"~/" + denyDir},
			"allow_paths":     []string{"~/Documents/share"},
		},
		"brains": []map[string]any{
			{"name": "a", "work_dir": "~/assistant/a", "handles_dms": true},
			{"name": "b", "work_dir": "~/assistant/" + otherBrain, "groups": []string{"120363000000000000@g.us"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(writeConfig(t, string(cfgJSON)))
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestManager(t, cfg)
	t.Cleanup(mgr.Stop)

	for _, f := range []string{
		"assistant/a/notes.md",
		"assistant/" + otherBrain + "/diary.md",
		".fridolin/dashboard.token",
		".fridolin/media/photo.jpg",
		"creds/google_calendar_token_a.json",
		"creds/google_calendar_token_b.json",
		".ssh/id_ed25519",
		denyDir + "/contract.txt",
		"Documents/private.txt",
		"Documents/share/ok.txt",
	} {
		p := filepath.Join(home, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("content of "+f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(cfg.DBPath); err != nil {
		t.Fatalf("the store did not create %s: %v", cfg.DBPath, err)
	}
	return mgr.profiler, mgr.Get("a").Dir()
}

// sandboxTry reads (cat) or writes (sh) path as brain a under its profile,
// from a's folder like a real run. Anything other than success or the
// sandbox's "Operation not permitted" fails the test, so a missing file can
// never pass as denied, and neither can sandbox-exec failing to apply the
// profile (its own errors start with "sandbox-exec:").
func sandboxTry(t *testing.T, sp *SandboxProfiler, dir string, write bool, path string) (bool, string) {
	t.Helper()
	prog, args := "/bin/cat", []string{path}
	if write {
		prog, args = "/bin/sh", []string{"-c", `printf planted > "$1"`, "sh", path}
	}
	bin, argv, err := sp.Command("a", prog, args)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, argv...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	switch {
	case err == nil:
		return true, string(out)
	case strings.Contains(string(out), "Operation not permitted") && !strings.HasPrefix(string(out), "sandbox-exec:"):
		return false, string(out)
	}
	t.Fatalf("%s %q: %v: %s", prog, args, err, out)
	return false, ""
}

// resolvedForTest is the real path of p, resolving symlinks in its existing
// part (a file a check writes does not exist yet).
func resolvedForTest(t *testing.T, p string) string {
	t.Helper()
	dir, base := filepath.Split(p)
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(real, base)
}
