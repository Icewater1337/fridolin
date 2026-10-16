package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestSandboxFailsClosed: with the sandbox on, a run whose sandbox cannot be
// set up is refused before anything starts, the tmux viewer included. It used
// to fall back to an unsandboxed spawn that still skipped every permission
// prompt.
func TestSandboxFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		breakIt func(sp *SandboxProfiler, home string)
	}{
		{"sandbox-exec missing", func(sp *SandboxProfiler, home string) {
			sp.execPath = filepath.Join(home, "no-sandbox-exec")
		}},
		{"path that cannot be quoted", func(sp *SandboxProfiler, home string) {
			sp.extraDeny = append(sp.extraDeny, filepath.Join(home, "two\nlines"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr, d, home := queueTestManager(t, 0, `, "viewer": true`)
			argsLog := filepath.Join(home, "args.log")
			t.Setenv("WCB_FAKE_ARGS_LOG", argsLog)
			tmuxCalls := fakeTmux(t)
			sp := mgr.profiler
			sp.mu.Lock()
			sp.enabled = true
			tc.breakIt(sp, home)
			sp.mu.Unlock()

			if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 10*time.Second, "the run", func() bool { return d.count() == 1 })

			res := d.all()[0].res
			if !errors.Is(res.Err, errSandboxUnavailable) {
				t.Fatalf("run result = %+v, want errSandboxUnavailable", res)
			}
			if strings.Contains(res.Err.Error(), home) {
				t.Errorf("the chat error names a local path: %v", res.Err)
			}
			if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
				t.Error("the CLI was started without the sandbox")
			}
			if _, err := os.Stat(tmuxCalls); !os.IsNotExist(err) {
				t.Error("a refused run started the tmux viewer")
			}
			if last := mgr.Get("a").Snapshot().Last; last == nil || last.Outcome != "error" {
				t.Errorf("last run = %+v, want outcome error", last)
			}

			// Control: with the sandbox off the same brain does start the
			// viewer, so the check above would have seen it.
			sp.mu.Lock()
			sp.enabled = false
			sp.mu.Unlock()
			if _, _, _, err := mgr.Submit(dm("again")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 10*time.Second, "the second run", func() bool { return d.count() == 2 })
			if _, err := os.Stat(tmuxCalls); err != nil {
				t.Errorf("the unsandboxed run did not start the viewer: %v", err)
			}
		})
	}
}

// fakeTmux makes the activity viewer use a tmux that only logs its calls to
// the returned file and then fails, so the viewer never gets as far as
// opening a Terminal window.
func fakeTmux(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls.log")
	t.Setenv("WCB_FAKE_TMUX_LOG", calls)
	script := filepath.Join(dir, "tmux")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\" >> \"$WCB_FAKE_TMUX_LOG\"\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	tmuxOnce.Do(func() {}) // keep findTmux from looking up the real one
	old := tmuxPath
	tmuxPath = script
	t.Cleanup(func() { tmuxPath = old })
	return calls
}

// TestSandboxConfigNeedsMacOS: the sandbox only exists on macOS, and brain
// runs refuse to start without it, so enabling it elsewhere is rejected when
// the config loads instead of failing every message.
func TestSandboxConfigNeedsMacOS(t *testing.T) {
	cases := []struct {
		goos    string
		enabled bool
		wantErr bool
	}{
		{"darwin", true, false},
		{"darwin", false, false},
		{"linux", true, true},
		{"linux", false, false},
		{"freebsd", true, true},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s enabled=%v", tc.goos, tc.enabled), func(t *testing.T) {
			err := SandboxConfig{Enabled: tc.enabled}.validate(tc.goos)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate = %v, want error %v", err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "sandbox.enabled") {
				t.Errorf("error %q does not name the setting", err)
			}
		})
	}
}

// TestSandboxCommandPassesProfileInline: the profile travels as an argument,
// never through a file in the shared TMPDIR that another brain could swap.
func TestSandboxCommandPassesProfileInline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sp := NewSandboxProfiler(map[string]string{"a": filepath.Join(home, "a"), "b": filepath.Join(home, "b")},
		&Config{DataDir: filepath.Join(home, "data")})
	sp.execPath = os.Args[0] // any file that exists

	bin, args, err := sp.Command("a", "/opt/claude", []string{"--verbose", "-p", "hi"})
	if runtime.GOOS != "darwin" {
		if err == nil {
			t.Fatalf("Command succeeded on %s, which has no sandbox-exec", runtime.GOOS)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string{"-p", mustProfile(t, sp, "a"), "/opt/claude"}, "--verbose", "-p", "hi")
	if bin != sp.execPath || !slices.Equal(args, want) {
		t.Errorf("Command = %s %q, want %s %q", bin, args, sp.execPath, want)
	}
}

// mustProfile is sp.Profile(brainID) for profiles that must build.
func mustProfile(t *testing.T, sp *SandboxProfiler, brainID string) string {
	t.Helper()
	profile, err := sp.Profile(brainID)
	if err != nil {
		t.Fatalf("Profile(%q): %v", brainID, err)
	}
	return profile
}

// TestSBPLString: paths go into the profile as raw UTF-8 with only backslash
// and double quote escaped. Go's %q wrote \u00a0 and \u200d escapes the
// profile reader does not decode, so those rules never matched.
func TestSBPLString(t *testing.T) {
	cases := []struct {
		name, in, want string
		wantErr        bool
	}{
		{"plain", "/Users/you/a", `"/Users/you/a"`, false},
		{"space", "/Users/you/My Brain", `"/Users/you/My Brain"`, false},
		{"no-break space", "/Users/you/My\u00a0Brain", "\"/Users/you/My\u00a0Brain\"", false},
		{"narrow no-break space", "/x/10.00\u202fAM.png", "\"/x/10.00\u202fAM.png\"", false},
		{"emoji with joiners", "/x/\U0001F468\u200d\U0001F469\u200d\U0001F467", "\"/x/\U0001F468\u200d\U0001F469\u200d\U0001F467\"", false},
		{"tab", "/x/a\tb", "\"/x/a\tb\"", false},
		{"double quote", `/x/say "hi"`, `"/x/say \"hi\""`, false},
		{"backslash", `/x/a\b`, `"/x/a\\b"`, false},
		{"NUL", "/x/a\x00b", "", true},
		{"newline", "/x/a\nb", "", true},
		{"carriage return", "/x/a\rb", "", true},
		{"invalid UTF-8", "/x/a\xffb", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sbplString(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("sbplString(%q) error = %v, want error %v", tc.in, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("sbplString(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

// TestSandboxProfileRefusesUnquotablePaths: a path that cannot be written
// into the profile fails the whole profile instead of dropping its rule.
func TestSandboxProfileRefusesUnquotablePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := map[string]func(cfg *Config, dirs map[string]string){
		"other brain's folder": func(cfg *Config, dirs map[string]string) { dirs["b"] = home + "/b\nc" },
		"deny path":            func(cfg *Config, dirs map[string]string) { cfg.Sandbox.DenyPaths = []string{home + "/x\x00"} },
		"allow path":           func(cfg *Config, dirs map[string]string) { cfg.Sandbox.AllowPaths = []string{home + "/x\r"} },
		"data dir":             func(cfg *Config, dirs map[string]string) { cfg.DataDir = home + "/data\n" },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := &Config{DataDir: filepath.Join(home, "data")}
			dirs := map[string]string{"a": filepath.Join(home, "a"), "b": filepath.Join(home, "b")}
			breakIt(cfg, dirs)
			if profile, err := NewSandboxProfiler(dirs, cfg).Profile("a"); err == nil {
				t.Errorf("Profile succeeded:\n%s", profile)
			}
		})
	}
}

// TestResolvePath: the sandbox matches real paths, so every profile path is
// resolved through symlinks, including the parts that do not exist yet.
func TestResolvePath(t *testing.T) {
	root := canonicalDir(t.TempDir())
	for _, d := range []string{"real/sub", "other"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		"link":       filepath.Join(root, "real"),
		"deep":       filepath.Join(root, "real", "sub"),
		"rel":        "real",
		"real/up":    "../other",
		"chain":      "link",
		"dangling":   filepath.Join(root, "nowhere", "sub"),
		"loop1":      "loop2",
		"loop2":      "loop1",
		"other/back": "../link/sub",
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct{ name, in, want string }{
		{"real path", "real/sub", "real/sub"},
		{"missing tail", "real/missing/deeper", "real/missing/deeper"},
		{"symlinked parent", "link/sub", "real/sub"},
		{"missing tail under a symlink", "link/missing/deeper", "real/missing/deeper"},
		{"the symlink itself", "link", "real"},
		{"relative symlink", "rel/sub", "real/sub"},
		{"relative symlink with ..", "real/up", "other"},
		{"symlink to a symlink", "chain/sub", "real/sub"},
		{"dangling symlink", "dangling/f", "nowhere/sub/f"},
		{".. after a symlink is physical", "deep/../x", "real/x"},
		{".. inside the missing part", "real/missing/../sub", "real/sub"},
		{"symlink inside a symlink target", "other/back", "real/sub"},
		{"symlink loop is left alone", "loop1/x", "loop1/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Not filepath.Join: it would apply ".." before resolvePath sees it.
			if got, want := resolvePath(root+"/"+tc.in), filepath.Join(root, tc.want); got != want {
				t.Errorf("resolvePath(%s) = %s, want %s", tc.in, got, want)
			}
		})
	}
	t.Run("temp dir spelling", func(t *testing.T) {
		// On macOS t.TempDir() is under /var, a symlink to /private/var.
		dir := t.TempDir()
		if got, want := resolvePath(dir), canonicalDir(dir); got != want {
			t.Errorf("resolvePath(%s) = %s, want %s", dir, got, want)
		}
	})
}

// TestSandboxProfileCoversRealPaths: with HOME, the data dir, the
// credentials dir and a deny path all reached through symlinks, the profile
// carries each rule for the configured and for the real path.
func TestSandboxProfileCoversRealPaths(t *testing.T) {
	root := canonicalDir(t.TempDir())
	realHome := filepath.Join(root, "home-real")
	home := filepath.Join(root, "home")
	for _, d := range []string{"home-real", "elsewhere/data", "elsewhere/creds", "elsewhere/clients", "elsewhere/ssh"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		home:                                 realHome,
		filepath.Join(realHome, ".fridolin"): filepath.Join(root, "elsewhere", "data"),
		filepath.Join(realHome, "creds"):     filepath.Join(root, "elsewhere", "creds"),
		filepath.Join(realHome, "clients"):   filepath.Join(root, "elsewhere", "clients"),
		filepath.Join(realHome, ".ssh"):      filepath.Join(root, "elsewhere", "ssh"),
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	cfg := &Config{
		DataDir: filepath.Join(home, ".fridolin"),
		Sandbox: SandboxConfig{
			CredentialsDir: filepath.Join(home, "creds"),
			DenyPaths:      []string{filepath.Join(home, "clients")},
		},
	}
	profile := mustProfile(t, NewSandboxProfiler(map[string]string{"a": filepath.Join(realHome, "a")}, cfg), "a")
	for _, want := range []string{
		`(deny file-read* (subpath "` + filepath.Join(home, ".fridolin") + `"))`,
		`(deny file-read* (subpath "` + filepath.Join(root, "elsewhere", "data") + `"))`,
		`(allow file-read* (subpath "` + filepath.Join(root, "elsewhere", "data", "media") + `"))`,
		`(deny file-read* (subpath "` + filepath.Join(root, "elsewhere", "creds") + `"))`,
		`(allow file-read* (literal "` + filepath.Join(root, "elsewhere", "creds", "google_calendar_token_a.json") + `"))`,
		`(deny file-read* (subpath "` + filepath.Join(root, "elsewhere", "clients") + `"))`,
		`(deny file-read* (subpath "` + filepath.Join(home, ".ssh") + `"))`,
		`(deny file-read* (subpath "` + filepath.Join(root, "elsewhere", "ssh") + `"))`,
		`(deny file-read* (subpath "` + filepath.Join(realHome, ".aws") + `"))`, // not there yet
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile lacks %s", want)
		}
	}
}

// TestSandboxAllowsDoNotFollowSymlinks: a brain can swap its own folder,
// the media dir, its calendar token or a writable allow_paths folder for a
// symlink. Allow rules resolve only HOME and the data and credentials dirs,
// never such a symlink, or the brain would be allowed into its target.
func TestSandboxAllowsDoNotFollowSymlinks(t *testing.T) {
	root := canonicalDir(t.TempDir())
	home := filepath.Join(root, "home")
	secret := filepath.Join(root, "secret")
	for _, d := range []string{"home-real/.fridolin", "home-real/Documents", "secret"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		home:                                  filepath.Join(root, "home-real"),
		filepath.Join(root, "home-real", "a"): secret,
		filepath.Join(root, "home-real", ".fridolin", "media"):                        secret,
		filepath.Join(root, "home-real", ".fridolin", "google_calendar_token_a.json"): filepath.Join(secret, "token"),
		filepath.Join(root, "home-real", "Documents", "share"):                        secret,
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	cfg := &Config{
		DataDir: filepath.Join(home, ".fridolin"),
		Sandbox: SandboxConfig{AllowPaths: []string{filepath.Join(home, "Documents", "share")}},
	}
	profile := mustProfile(t, NewSandboxProfiler(map[string]string{"a": filepath.Join(home, "a")}, cfg), "a")
	for _, line := range strings.Split(profile, "\n") {
		if strings.HasPrefix(line, "(allow file-") && strings.Contains(line, secret) {
			t.Errorf("allow rule follows a symlink into its target: %s", line)
		}
	}
	// The trusted roots still resolve, so the rules match real paths.
	for _, want := range []string{
		`(allow file-read* (subpath "` + filepath.Join(root, "home-real", ".fridolin", "media") + `"))`,
		`(allow file-read* (subpath "` + filepath.Join(root, "home-real", "a") + `"))`,
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile lacks %s", want)
		}
	}
}

// TestSandboxProfileNeedsAbsolutePaths: an empty or relative HOME, or an
// empty path, would turn rules into ones that match nothing (or, made
// absolute, the bridge's working directory), so the profile fails instead.
func TestSandboxProfileNeedsAbsolutePaths(t *testing.T) {
	cases := []struct {
		name, home string
		allow      []string
	}{
		{"empty HOME", "", nil},
		{"relative HOME", "home", nil},
		{"empty allow path", "/Users/you", []string{""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", tc.home)
			cfg := &Config{DataDir: "/Users/you/.fridolin", Sandbox: SandboxConfig{AllowPaths: tc.allow}}
			if profile, err := NewSandboxProfiler(map[string]string{"a": "/Users/you/a"}, cfg).Profile("a"); err == nil {
				t.Errorf("Profile succeeded:\n%s", profile)
			}
		})
	}
}
