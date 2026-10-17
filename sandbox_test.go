package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// TestSandboxConfigRejectsUnusablePaths: deny and allow paths (and the
// credentials dir) must be absolute after ~ expansion. A relative entry used
// to load fine and protect nothing.
func TestSandboxConfigRejectsUnusablePaths(t *testing.T) {
	cases := []struct {
		name    string
		sandbox string
		wantErr string
	}{
		{"home-relative deny", `{"deny_paths": ["~/clients"]}`, ""},
		{"absolute deny", `{"deny_paths": ["/Volumes/work/clients"]}`, ""},
		{"relative deny", `{"deny_paths": ["clients"]}`, "sandbox.deny_paths"},
		{"empty deny", `{"deny_paths": [""]}`, "sandbox.deny_paths"},
		{"other user's home", `{"deny_paths": ["~you/clients"]}`, "sandbox.deny_paths"},
		{"deny with a line break", `{"deny_paths": ["/x/a\nb"]}`, "sandbox.deny_paths"},
		{"relative allow", `{"allow_paths": ["Documents/share"]}`, "sandbox.allow_paths"},
		{"home-relative allow", `{"allow_paths": ["~/Documents/share"]}`, ""},
		{"relative credentials dir", `{"credentials_dir": "creds"}`, "sandbox.credentials_dir"},
		{"home-relative credentials dir", `{"credentials_dir": "~/creds"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			_, err := LoadConfig(writeConfig(t, `{"sandbox": `+tc.sandbox+`}`))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("LoadConfig: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("LoadConfig error = %v, want one naming %s", err, tc.wantErr)
			}
		})
	}
}

// TestSandboxProfilePinsParents: the sandbox matches paths, so a brain that
// renamed a denied path, or any folder above it, read it under the new name.
// Every denied path is denied for writes too, and every folder above one is
// pinned up to HOME (or / outside HOME): fully when it exists, against
// rename and delete only when it does not, so the CLI can still create
// ~/.claude on its first run.
func TestSandboxProfilePinsParents(t *testing.T) {
	root := canonicalDir(t.TempDir())
	home := filepath.Join(root, "home")
	outside := canonicalDir(t.TempDir()) // a sibling of root, not inside it
	for _, d := range []string{"home/assistant/a", "home/assistant/b", "home/.config"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(outside, "clients"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	cfg := &Config{
		DataDir: filepath.Join(home, ".fridolin"),
		Sandbox: SandboxConfig{DenyPaths: []string{filepath.Join(outside, "clients")}},
	}
	dirs := map[string]string{"a": filepath.Join(home, "assistant", "a"), "b": filepath.Join(home, "assistant", "b")}
	profile := mustProfile(t, NewSandboxProfiler(dirs, cfg), "a")
	lines := strings.Split(profile, "\n")

	cases := []struct {
		name, rule string
		want       bool
	}{
		{"home-relative literal denied for writes", `(deny file-write* (literal "` + home + `/.git-credentials"))`, true},
		{"home-relative subpath denied for writes", `(deny file-write* (subpath "` + home + `/.ssh"))`, true},
		{"transcripts cannot be moved out", `(deny file-write-unlink (subpath "` + home + `/.claude/projects"))`, true},
		{"transcripts stay writable for the CLI", `(deny file-write* (subpath "` + home + `/.claude/projects"))`, false},
		{"home pinned", `(deny file-write* (literal "` + home + `"))`, true},
		{"nothing above home pinned", `(literal "` + root + `"))`, false},
		{"other brain's parent pinned", `(deny file-write* (literal "` + home + `/assistant"))`, true},
		{"existing ~/.config pinned fully", `(deny file-write* (literal "` + home + `/.config"))`, true},
		{"missing ~/.claude still creatable", `(deny file-write-unlink (literal "` + home + `/.claude"))`, true},
		{"missing ~/.claude not pinned fully", `(deny file-write* (literal "` + home + `/.claude"))`, false},
		{"missing sessions folder pinned", `(deny file-write-unlink (literal "` + home + `/.claude/sessions"))`, true},
		// The CLI deletes its sessions/<pid>.json on exit.
		{"session files stay removable", `(deny file-write-unlink (subpath "` + home + `/.claude/sessions"))`, false},
		{"deny path outside home pinned up to /", `(deny file-write* (literal "` + outside + `"))`, true},
		{"filesystem root pinned", `(deny file-write* (literal "/"))`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := slices.Contains(lines, tc.rule) || (!tc.want && strings.Contains(profile, tc.rule)); got != tc.want {
				t.Errorf("profile has %s: %v, want %v", tc.rule, got, tc.want)
			}
		})
	}
	t.Run("own folder stays last", func(t *testing.T) {
		own := `(allow file-write* (subpath "` + dirs["a"] + `"))`
		if i := slices.Index(lines, own); i < 0 || slices.ContainsFunc(lines[i:], func(l string) bool { return strings.HasPrefix(l, "(deny") }) {
			t.Errorf("a deny follows the own-folder allow, or it is missing:\n%s", profile)
		}
	})
}

// TestSandboxDeniesEverySpelling: a deny covers the path as configured and
// its real path; a write deny also covers its entry (folder resolved, own
// name kept). The entry matters for a symlink such as a dotfiles-linked
// file: renaming or deleting it acts on the link, which a rule for the
// target alone does not cover. Read rules leave the link alone, because
// following it reads it, and that would block what is allowed behind it.
func TestSandboxDeniesEverySpelling(t *testing.T) {
	root := canonicalDir(t.TempDir())
	home := filepath.Join(root, "home")
	for _, d := range []string{"home", "dotfiles/config", "vault/gh"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		filepath.Join(home, ".config"):                  filepath.Join(root, "dotfiles", "config"),
		filepath.Join(root, "dotfiles", "config", "gh"): filepath.Join(root, "vault", "gh"),
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	profile := mustProfile(t, NewSandboxProfiler(map[string]string{"a": filepath.Join(home, "a")}, &Config{}), "a")
	configured := filepath.Join(home, ".config", "gh")
	link := filepath.Join(root, "dotfiles", "config", "gh")
	target := filepath.Join(root, "vault", "gh")
	for _, c := range []struct {
		op, path string
		want     bool
	}{
		{"file-read*", configured, true},
		{"file-read*", link, false},
		{"file-read*", target, true},
		{"file-write*", configured, true},
		{"file-write*", link, true},
		{"file-write*", target, true},
	} {
		rule := `(deny ` + c.op + ` (subpath "` + c.path + `"))`
		if got := strings.Contains(profile, rule); got != c.want {
			t.Errorf("profile has %s: %v, want %v", rule, got, c.want)
		}
	}
	// The folders above the link and above its target are pinned too.
	for _, dir := range []string{filepath.Join(root, "dotfiles", "config"), filepath.Join(root, "vault")} {
		if rule := `(deny file-write* (literal "` + dir + `"))`; !strings.Contains(profile, rule) {
			t.Errorf("profile lacks %s", rule)
		}
	}
}

// TestSandboxProfileDeniesWritesToCodeOutsideIt: files that are loaded or
// run outside the sandbox (Claude Code settings, hooks and memory, launchd
// agents, shell startup files) are denied for writing, never for reading,
// since the CLI and your shells must still read them.
func TestSandboxProfileDeniesWritesToCodeOutsideIt(t *testing.T) {
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	profile := mustProfile(t, NewSandboxProfiler(map[string]string{"a": filepath.Join(home, "a")}, &Config{}), "a")
	paths := map[string]string{}
	for _, sub := range defaultWriteDenySubpaths {
		paths[sub] = "subpath"
	}
	for _, lit := range defaultWriteDenyLiterals {
		paths[lit] = "literal"
	}
	for _, rel := range []string{".claude/settings.json", ".claude/CLAUDE.md", ".claude/hooks", "Library/LaunchAgents", ".zshrc", ".bashrc", ".profile", ".config/fish"} {
		if paths[rel] == "" {
			t.Errorf("%s is not in the write denies", rel)
		}
	}
	for rel, filter := range paths {
		t.Run(rel, func(t *testing.T) {
			p := `(` + filter + ` "` + filepath.Join(home, rel) + `"))`
			if !strings.Contains(profile, "(deny file-write* "+p) {
				t.Errorf("profile lacks (deny file-write* %s", p)
			}
			if strings.Contains(profile, "(deny file-read* "+p) {
				t.Errorf("profile denies reading %s", rel)
			}
		})
	}
	// The CLI writes these on every run.
	for _, rel := range []string{".claude.json", ".claude/todos", ".claude/statsig", ".claude/shell-snapshots"} {
		if strings.Contains(profile, `"`+filepath.Join(home, rel)+`"`) {
			t.Errorf("profile has a rule for %s, which the CLI writes", rel)
		}
	}
}

// TestSandboxProfileDeniesMemoryAboveBrains: Claude Code loads CLAUDE.md,
// CLAUDE.local.md, .mcp.json and .claude/ from every folder above its
// working directory, and your own sessions load them from your home folder.
// Brains must not write any of them, nor the auto-memory every new session
// of a project loads, but each brain keeps its own folder's.
func TestSandboxProfileDeniesMemoryAboveBrains(t *testing.T) {
	root := canonicalDir(t.TempDir())
	home := filepath.Join(root, "home")
	outside := canonicalDir(t.TempDir()) // a sibling of root, not inside it
	t.Setenv("HOME", home)
	dirs := map[string]string{
		"a": home + "/assistant/a",
		"b": home + "/assistant/b",
		"n": home + "/assistant/a/n", // inside a
		"d": outside + "/brains/d",   // outside HOME
	}
	sp := NewSandboxProfiler(dirs, &Config{})
	lit := func(p string) string { return `(deny file-write* (literal "` + p + `"))` }
	sub := func(p string) string { return `(deny file-write* (subpath "` + p + `"))` }
	cases := []struct {
		brain, name, rule string
		want              bool
	}{
		{"a", "your home folder's CLAUDE.md", lit(home + "/CLAUDE.md"), true},
		{"a", "your home folder's CLAUDE.local.md", lit(home + "/CLAUDE.local.md"), true},
		{"a", "your home folder's .mcp.json", lit(home + "/.mcp.json"), true},
		{"a", "the CLI's own ~/.claude", sub(home + "/.claude"), false},
		{"a", "the brains folder's CLAUDE.md", lit(home + "/assistant/CLAUDE.md"), true},
		{"a", "the brains folder's CLAUDE.local.md", lit(home + "/assistant/CLAUDE.local.md"), true},
		{"a", "the brains folder's .claude", sub(home + "/assistant/.claude"), true},
		{"a", "its own CLAUDE.md, though a brain lives inside", lit(home + "/assistant/a/CLAUDE.md"), false},
		{"a", "its own .claude", sub(home + "/assistant/a/.claude"), false},
		{"n", "the CLAUDE.md of the brain it lives in", lit(home + "/assistant/a/CLAUDE.md"), true},
		{"a", "above a brain outside HOME", lit(outside + "/brains/CLAUDE.md"), true},
		{"a", "up to / outside HOME", lit("/CLAUDE.md"), true},
		{"a", "nothing above HOME", lit(root + "/CLAUDE.md"), false},
		{"a", "auto-memory in every project", `(deny file-write* (regex "^` + sbplRegex(home+"/.claude/projects") + `/[^/]+/memory(/.*)?$"))`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := strings.Split(mustProfile(t, sp, tc.brain), "\n")
			if got := slices.Contains(lines, tc.rule); got != tc.want {
				t.Errorf("profile of %s has %s: %v, want %v", tc.brain, tc.rule, got, tc.want)
			}
		})
	}
	t.Run("pattern quotes HOME", func(t *testing.T) {
		odd := filepath.Join(root, `h.o+m(e)[1]{2}$^|*`)
		t.Setenv("HOME", odd)
		// Each metacharacter escaped, and each backslash again for the string.
		rule := `(deny file-write* (regex "^` + sbplRegex(root) +
			`/h\\.o\\+m\\(e\\)\\[1\\]\\{2\\}\\$\\^\\|\\*/\\.claude/projects/[^/]+/memory(/.*)?$"))`
		if lines := strings.Split(mustProfile(t, sp, "a"), "\n"); !slices.Contains(lines, rule) {
			t.Errorf("profile lacks %s", rule)
		}
	})
}

// outsideProgramsFixture lays out, under home, what the bridge runs outside
// the sandbox: a natively installed Claude CLI (a symlink into versions/),
// rclone, the Plaud CLI as an npm package in nvm with its node, tmux, and
// the bridge binary inside its checkout. PATH finds only the Claude CLI,
// and also names ~/bin2, which does not exist yet. It returns a profiler
// for brain a that runs all of them.
func outsideProgramsFixture(t *testing.T, home string) *SandboxProfiler {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("PATH", filepath.Join(home, ".local", "bin")+":"+filepath.Join(home, "bin2")+":/usr/bin:/bin")
	nvm := ".nvm/versions/node/v22.0.0/"
	for _, f := range []string{
		".local/share/claude/versions/1.0.0",
		"bin/rclone",
		"bin/tmux",
		nvm + "bin/node",
		nvm + "lib/node_modules/@plaud-ai/cli/dist/index.js",
		nvm + "lib/node_modules/@plaud-ai/cli/node_modules/dep/index.js",
		"fridolin/fridolin",
		"fridolin/tools/calendar_tool.py",
		"a/notes.md",
	} {
		p := filepath.Join(home, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\necho ran "+filepath.Base(f)+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		".local/bin/claude": "../share/claude/versions/1.0.0",
		nvm + "bin/plaud":   "../lib/node_modules/@plaud-ai/cli/dist/index.js",
	} {
		p := filepath.Join(home, link)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	old := tmuxPath
	tmuxOnce.Do(func() {}) // keep findTmux from looking up the real one
	tmuxPath = filepath.Join(home, "bin", "tmux")
	t.Cleanup(func() { tmuxPath = old })

	cfg := &Config{ClaudePath: "claude", Viewer: true, Backup: BackupConfig{RclonePath: filepath.Join(home, "bin", "rclone")}}
	sp := NewSandboxProfiler(map[string]string{"a": filepath.Join(home, "a")}, cfg)
	sp.bridgeExe = filepath.Join(home, "fridolin", "fridolin")
	return sp
}

// TestSandboxProfileProtectsOutsidePrograms: launchd runs the bridge outside
// any sandbox, and the bridge runs the Claude CLI, rclone, the Plaud CLI
// and tmux unsandboxed. A brain that replaced one of them would run code
// outside its sandbox, so they are never writable: the bridge binary and its
// checkout, and every helper under HOME, as found and as resolved, with the
// whole npm package and the node of a node script. Nor can a brain plant a
// program the bridge would find next: a name it runs by name in a PATH
// folder under HOME, rclone off PATH, a newer node version in nvm.
func TestSandboxProfileProtectsOutsidePrograms(t *testing.T) {
	home := canonicalDir(t.TempDir())
	sp := outsideProgramsFixture(t, home)
	nvm := filepath.Join(home, ".nvm/versions/node/v22.0.0")
	cases := []struct{ name, rule string }{
		{"bridge binary", `(literal "` + home + `/fridolin/fridolin")`},
		{"bridge checkout", `(subpath "` + home + `/fridolin")`},
		{"claude on PATH", `(literal "` + home + `/.local/bin/claude")`},
		{"claude it resolves to", `(literal "` + home + `/.local/share/claude/versions/1.0.0")`},
		{"configured rclone", `(literal "` + home + `/bin/rclone")`},
		{"plaud found in nvm", `(literal "` + nvm + `/bin/plaud")`},
		{"plaud's npm package", `(subpath "` + nvm + `/lib/node_modules/@plaud-ai/cli")`},
		{"the node that runs plaud", `(literal "` + nvm + `/bin/node")`},
		{"tmux for the viewer", `(literal "` + home + `/bin/tmux")`},
		{"a name it runs, in a PATH folder", `(literal "` + home + `/.local/bin/osascript")`},
		{"a name it runs, in a PATH folder not there yet", `(literal "` + home + `/bin2/touch")`},
		{"a name it runs, in the Plaud CLI's own folder", `(literal "` + nvm + `/bin/open")`},
		{"rclone off PATH", `(literal "` + home + `/.local/bin/rclone")`},
		{"every node version in nvm", `(subpath "` + home + `/.nvm/versions/node")`},
	}
	profile := mustProfile(t, sp, "a")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(profile, "(deny file-write* "+tc.rule+")") {
				t.Errorf("profile lacks (deny file-write* %s)", tc.rule)
			}
			if strings.Contains(profile, "(deny file-read* "+tc.rule+")") {
				t.Errorf("profile denies reading %s", tc.rule)
			}
		})
	}

	t.Run("nvm stays writable with plaud.cli_path", func(t *testing.T) {
		sp.mu.Lock()
		sp.plaudCLIPath = nvm + "/bin/plaud"
		sp.mu.Unlock()
		t.Cleanup(func() {
			sp.mu.Lock()
			sp.plaudCLIPath = ""
			sp.mu.Unlock()
		})
		profile := mustProfile(t, sp, "a")
		if rule := `(deny file-write* (subpath "` + home + `/.nvm/versions/node"))`; strings.Contains(profile, rule) {
			t.Errorf("profile has %s", rule)
		}
		for _, f := range []string{"/bin/plaud", "/bin/node", "/bin/open"} {
			if rule := `(deny file-write* (literal "` + nvm + f + `"))`; !strings.Contains(profile, rule) {
				t.Errorf("profile lacks %s", rule)
			}
		}
	})

	t.Run("helpers outside HOME are left to a later change", func(t *testing.T) {
		sp.mu.Lock()
		sp.claudePath, sp.viewer = "/bin/sh", false
		sp.mu.Unlock()
		profile := mustProfile(t, sp, "a")
		for _, p := range []string{"/bin/sh", "/usr/bin/touch", "/opt/homebrew/bin/plaud"} {
			if strings.Contains(profile, `"`+p+`"`) {
				t.Errorf("profile has a rule for %s", p)
			}
		}
	})
}

// sbplRegex is p quoted for a regex, as written in the profile.
func sbplRegex(p string) string {
	return strings.ReplaceAll(regexp.QuoteMeta(p), `\`, `\\`)
}

// TestNodePackage finds the npm package folder of an installed file.
func TestNodePackage(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/n/lib/node_modules/@plaud-ai/cli/dist/index.js", "/n/lib/node_modules/@plaud-ai/cli"},
		{"/n/lib/node_modules/@anthropic-ai/claude-code/cli.js", "/n/lib/node_modules/@anthropic-ai/claude-code"},
		{"/n/lib/node_modules/rimraf/bin.js", "/n/lib/node_modules/rimraf"},
		{"/n/lib/node_modules/a/node_modules/b/bin.js", "/n/lib/node_modules/a/node_modules/b"},
		{"/n/lib/node_modules/@scope", ""},
		{"/n/lib/node_modules/rimraf", ""},
		{"/Users/you/.local/share/claude/versions/2.1.0", ""},
	}
	for _, tc := range cases {
		if got := nodePackage(tc.in); got != tc.want {
			t.Errorf("nodePackage(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSandboxProfileDeniesBridgeSecrets: the deny set follows the bridge's
// secrets wherever they are. db_path can put the WhatsApp session store
// outside the data dir, where every brain could read it; an install that
// moved to ~/.fridolin can leave the old ~/.whatsapp-claude-bridge behind
// with older copies of the same keys and tokens.
func TestSandboxProfileDeniesBridgeSecrets(t *testing.T) {
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	tmp := canonicalDir(os.TempDir())
	both := func(filter, p string) []string {
		return []string{`(deny file-read* (` + filter + ` "` + p + `"))`, `(deny file-write* (` + filter + ` "` + p + `"))`}
	}
	dbRules := func(db string) []string {
		var out []string
		for _, s := range []string{"", "-wal", "-shm", "-journal"} {
			out = append(out, both("literal", db+s)...)
		}
		return out
	}
	cases := []struct {
		name          string
		dataDir, db   string
		want, notWant []string
	}{
		{"db_path outside the data dir", home + "/.fridolin", home + "/wa-store/bridge.db",
			append(dbRules(home+"/wa-store/bridge.db"), both("subpath", home+"/wa-store")...), nil},
		{"db_path in the data dir", home + "/.fridolin", home + "/.fridolin/bridge.db",
			dbRules(home + "/.fridolin/bridge.db"), nil},
		{"db_path in HOME keeps HOME open", home + "/.fridolin", home + "/bridge.db",
			dbRules(home + "/bridge.db"), both("subpath", home)},
		{"db_path in the temp dir keeps it open", home + "/.fridolin", tmp + "/bridge.db",
			dbRules(tmp + "/bridge.db"), both("subpath", tmp)},
		{"custom data dir still denies both default names", home + "/fd", home + "/fd/bridge.db",
			append(append(both("subpath", home+"/fd"), both("subpath", home+"/.fridolin")...), both("subpath", home+"/.whatsapp-claude-bridge")...), nil},
		{"legacy data dir active", home + "/.whatsapp-claude-bridge", home + "/.whatsapp-claude-bridge/bridge.db",
			append(both("subpath", home+"/.fridolin"), `(allow file-read* (subpath "`+home+`/.whatsapp-claude-bridge/media"))`), nil},
		{"bridge logs", home + "/.fridolin", home + "/.fridolin/bridge.db",
			append(both("subpath", home+"/Library/Logs/fridolin"), both("subpath", home+"/Library/Logs/whatsapp-claude-bridge")...), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{DataDir: tc.dataDir, DBPath: tc.db}
			lines := strings.Split(mustProfile(t, NewSandboxProfiler(map[string]string{"a": home + "/a"}, cfg), "a"), "\n")
			for _, rule := range tc.want {
				if !slices.Contains(lines, rule) {
					t.Errorf("profile lacks %s", rule)
				}
			}
			for _, rule := range tc.notWant {
				if slices.Contains(lines, rule) {
					t.Errorf("profile has %s", rule)
				}
			}
			// No media_dir can open the DB again: its denies follow the allow.
			media := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "(allow file-read* (subpath") })
			db := slices.Index(lines, `(deny file-read* (literal "`+tc.db+`"))`)
			if media < 0 || db < media {
				t.Errorf("the DB deny (line %d) does not follow the media allow (line %d)", db, media)
			}
		})
	}
	// db_path and DB_PATH are only ~-expanded, so they can be relative; the
	// bridge then opens the DB in its working directory. Its folder is
	// denied like any other, relative to that directory, not to "/".
	for _, db := range []string{"wa/bridge.db", "bridge.db"} {
		t.Run("relative db_path "+db, func(t *testing.T) {
			wd := canonicalDir(t.TempDir())
			t.Chdir(wd)
			cfg := &Config{DataDir: home + "/.fridolin", DBPath: db}
			lines := strings.Split(mustProfile(t, NewSandboxProfiler(map[string]string{"a": home + "/a"}, cfg), "a"), "\n")
			abs := filepath.Join(wd, db)
			for _, rule := range append(dbRules(abs), both("subpath", filepath.Dir(abs))...) {
				if !slices.Contains(lines, rule) {
					t.Errorf("profile lacks %s", rule)
				}
			}
		})
	}
}

// TestSandboxProfileDeniesTheConfigFile: --config or CONFIG_FILE can put
// config.json outside the data dir, and with it the .env loaded next to it.
// Brains could read both, and rewrite the config without the dashboard
// token. Inside the data dir the data dir deny covers them already.
func TestSandboxProfileDeniesTheConfigFile(t *testing.T) {
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, cfgFile string
		want          bool
	}{
		{"outside the data dir", home + "/etc/fridolin/config.json", true},
		{"in the data dir", home + "/.fridolin/config.json", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp := NewSandboxProfiler(map[string]string{"a": home + "/a"}, &Config{DataDir: home + "/.fridolin"})
			sp.SetConfigFile(tc.cfgFile)
			lines := strings.Split(mustProfile(t, sp, "a"), "\n")
			dir := filepath.Dir(tc.cfgFile)
			for _, f := range []string{tc.cfgFile, tc.cfgFile + ".bak", dir + "/.env"} {
				for _, op := range []string{"file-read*", "file-write*"} {
					rule := `(deny ` + op + ` (literal "` + f + `"))`
					if got := slices.Contains(lines, rule); got != tc.want {
						t.Errorf("profile has %s: %v, want %v", rule, got, tc.want)
					}
				}
			}
			// The temp file writeFileAtomic saves through, .config.json.*.tmp.
			for _, op := range []string{"file-read*", "file-write*"} {
				rule := `(deny ` + op + ` (regex "^` + sbplRegex(dir) + `/\\.config\\.json\\.[^/]*\\.tmp$"))`
				if got := slices.Contains(lines, rule); got != tc.want {
					t.Errorf("profile has %s: %v, want %v", rule, got, tc.want)
				}
			}
			// The .env in the working directory, which loadDotEnv falls back to.
			if rule := `(deny file-read* (literal "` + filepath.Join(wd, ".env") + `"))`; !slices.Contains(lines, rule) {
				t.Errorf("profile lacks %s", rule)
			}
		})
	}
}

// TestBridgeTellsTheSandboxItsConfigFile: the bridge hands the profiler the
// config file it loaded, so every brain's profile denies it.
func TestBridgeTellsTheSandboxItsConfigFile(t *testing.T) {
	tb := newTestBridge(t, "")
	rule := `(deny file-read* (literal "` + tb.cfgs.Path() + `"))`
	if profile := mustProfile(t, tb.brainMgr.profiler, "a"); !strings.Contains(profile, rule) {
		t.Errorf("profile lacks %s", rule)
	}
}
