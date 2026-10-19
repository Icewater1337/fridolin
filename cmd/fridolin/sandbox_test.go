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

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/sandbox"
	"github.com/Icewater1337/fridolin/internal/testutil"

	"github.com/Icewater1337/fridolin/internal/fsx"
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
			sp.ExtraDeny = append(sp.ExtraDeny, filepath.Join(home, "two\nlines"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			viewerCalls := make(chan struct{}, 4)
			mgr, d, home := queueTestManager(t, 0, `, "viewer": true`, func(opts *brainManagerOptions) {
				opts.ViewerStart = func(string, string, string, []string) (*platform.Viewer, error) {
					viewerCalls <- struct{}{}
					return nil, errors.New("no tmux in tests")
				}
			})
			argsLog := filepath.Join(home, "args.log")
			t.Setenv("FRIDOLIN_FAKE_ARGS_LOG", argsLog)

			sp := mgr.profiler
			sp.mu.Lock()
			sp.enabled = true
			tc.breakIt(sp, home)
			sp.mu.Unlock()

			if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
				t.Fatal(err)
			}
			testutil.WaitFor(t, 10*time.Second, "the run", func() bool { return d.count() == 1 })

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
			if len(viewerCalls) != 0 {
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
			testutil.WaitFor(t, 10*time.Second, "the second run", func() bool { return d.count() == 2 })
			// The viewer starts in the background.
			testutil.WaitFor(t, 10*time.Second, "the viewer to start", func() bool { return len(viewerCalls) == 1 })
		})
	}
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
			err := config.SandboxConfig{Enabled: tc.enabled}.Validate(tc.goos)
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
	sp := NewSandboxProfiler(map[string][]string{"a": {filepath.Join(home, "a")}, "b": {filepath.Join(home, "b")}},
		&config.Config{DataDir: filepath.Join(home, "data")})
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

// TestSandboxProfileRefusesUnquotablePaths: a path that cannot be written
// into the profile fails the whole profile instead of dropping its rule.
func TestSandboxProfileRefusesUnquotablePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := map[string]func(cfg *config.Config, dirs map[string][]string){
		"other brain's folder": func(cfg *config.Config, dirs map[string][]string) { dirs["b"] = []string{home + "/b\nc"} },
		"other brain's folder, second spelling": func(cfg *config.Config, dirs map[string][]string) {
			dirs["b"] = []string{home + "/b", home + "/b\nc"}
		},
		"own folder, second spelling": func(cfg *config.Config, dirs map[string][]string) {
			dirs["a"] = []string{home + "/a", home + "/a\nc"}
		},
		"deny path":  func(cfg *config.Config, dirs map[string][]string) { cfg.Sandbox.DenyPaths = []string{home + "/x\x00"} },
		"allow path": func(cfg *config.Config, dirs map[string][]string) { cfg.Sandbox.AllowPaths = []string{home + "/x\r"} },
		"data dir":   func(cfg *config.Config, dirs map[string][]string) { cfg.DataDir = home + "/data\n" },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := &config.Config{DataDir: filepath.Join(home, "data")}
			dirs := map[string][]string{"a": {filepath.Join(home, "a")}, "b": {filepath.Join(home, "b")}}
			breakIt(cfg, dirs)
			if profile, err := NewSandboxProfiler(dirs, cfg).Profile("a"); err == nil {
				t.Errorf("Profile succeeded:\n%s", profile)
			}
		})
	}
}

// TestSandboxProfileCoversRealPaths: with HOME, the data dir, the
// credentials dir and a deny path all reached through symlinks, the profile
// carries each rule for the configured and for the real path.
func TestSandboxProfileCoversRealPaths(t *testing.T) {
	root := fsx.Canonical(t.TempDir())
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
	cfg := &config.Config{
		DataDir: filepath.Join(home, ".fridolin"),
		Sandbox: config.SandboxConfig{
			CredentialsDir: filepath.Join(home, "creds"),
			DenyPaths:      []string{filepath.Join(home, "clients")},
		},
	}
	profile := mustProfile(t, NewSandboxProfiler(map[string][]string{"a": {filepath.Join(realHome, "a")}}, cfg), "a")
	for _, want := range []string{
		`(deny file-read* (subpath "` + filepath.Join(home, ".fridolin") + `"))`,
		`(deny file-read* (subpath "` + filepath.Join(root, "elsewhere", "data") + `"))`,
		`(allow file-read* (subpath "` + filepath.Join(root, "elsewhere", "data", "media", "a") + `"))`,
		`(deny file-read* (subpath "` + filepath.Join(root, "elsewhere", "creds") + `"))`,
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
// the media dir or a writable allow_paths folder for a symlink, and one
// could swap its media inbox when that was not pinned. Allow rules resolve
// only HOME and the data, credentials and media dirs, never such a
// symlink, or the brain would be allowed into its target.
func TestSandboxAllowsDoNotFollowSymlinks(t *testing.T) {
	root := fsx.Canonical(t.TempDir())
	home := filepath.Join(root, "home")
	secret := filepath.Join(root, "secret")
	for _, d := range []string{"home-real/.fridolin/media", "home-real/Documents", "secret"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		home:                                  filepath.Join(root, "home-real"),
		filepath.Join(root, "home-real", "a"): secret,
		filepath.Join(root, "home-real", ".fridolin", "media", "a"): secret,
		filepath.Join(root, "home-real", "Documents", "share"):      secret,
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	cfg := &config.Config{
		DataDir: filepath.Join(home, ".fridolin"),
		Sandbox: config.SandboxConfig{AllowPaths: []string{filepath.Join(home, "Documents", "share")}},
	}
	profile := mustProfile(t, NewSandboxProfiler(map[string][]string{"a": {filepath.Join(home, "a")}}, cfg), "a")
	for _, line := range strings.Split(profile, "\n") {
		if strings.HasPrefix(line, "(allow file-") && strings.Contains(line, secret) {
			t.Errorf("allow rule follows a symlink into its target: %s", line)
		}
	}
	// The trusted roots still resolve, so the rules match real paths.
	for _, want := range []string{
		`(allow file-read* (subpath "` + filepath.Join(root, "home-real", ".fridolin", "media", "a") + `"))`,
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
			cfg := &config.Config{DataDir: "/Users/you/.fridolin", Sandbox: config.SandboxConfig{AllowPaths: tc.allow}}
			if profile, err := NewSandboxProfiler(map[string][]string{"a": {"/Users/you/a"}}, cfg).Profile("a"); err == nil {
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
			_, err := loadConfig(writeConfig(t, `{"sandbox": `+tc.sandbox+`}`))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Load: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("Load error = %v, want one naming %s", err, tc.wantErr)
			}
		})
	}
}

// TestSandboxDeniesEverySpelling: a deny covers the path as configured and
// its real path; a write deny also covers its entry (folder resolved, own
// name kept). The entry matters for a symlink such as a dotfiles-linked
// file: renaming or deleting it acts on the link, which a rule for the
// target alone does not cover. Read rules leave the link alone, because
// following it reads it, and that would block what is allowed behind it.
func TestSandboxDeniesEverySpelling(t *testing.T) {
	root := fsx.Canonical(t.TempDir())
	home := filepath.Join(root, "home")
	for _, d := range []string{"home", "dotfiles/config", "vault/gh", "vault/git"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		filepath.Join(home, ".config"):                   filepath.Join(root, "dotfiles", "config"),
		filepath.Join(root, "dotfiles", "config", "gh"):  filepath.Join(root, "vault", "gh"),
		filepath.Join(root, "dotfiles", "config", "git"): filepath.Join(root, "vault", "git"),
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	profile := mustProfile(t, NewSandboxProfiler(map[string][]string{"a": {filepath.Join(home, "a")}}, &config.Config{}), "a")
	for _, c := range []struct {
		op, path string
		want     bool
	}{
		// ~/.config/gh, denied for reading.
		{"file-read*", home + "/.config/gh", true},
		{"file-read*", root + "/dotfiles/config/gh", false},
		{"file-read*", root + "/vault/gh", true},
		// ~/.config/git, denied for writing.
		{"file-write*", home + "/.config/git", true},
		{"file-write*", root + "/dotfiles/config/git", true},
		{"file-write*", root + "/vault/git", true},
	} {
		rule := `(deny ` + c.op + ` (subpath "` + c.path + `"))`
		if got := strings.Contains(profile, rule); got != c.want {
			t.Errorf("profile has %s: %v, want %v", rule, got, c.want)
		}
	}
}

// TestSandboxProfileCoversBrainFoldersBothWays: the bridge runs a brain in
// its folder's real path, and the profile must cover the folder as
// configured as well. With ~/assistant a symlink to another volume, a
// profile with only the real path let a brain read the instructions in
// HOME above its folder, and kept the other brain's folder open by the way
// it is configured. One with only the folder as configured locked the
// brain out of its own files, as the sandbox matches real paths and a
// default deny on the way (~/Documents, say) won, and lost the denies on
// the CLAUDE.md and .claude above the real folders of every brain.
func TestSandboxProfileCoversBrainFoldersBothWays(t *testing.T) {
	root := fsx.Canonical(t.TempDir())
	home := filepath.Join(root, "home")
	volume := filepath.Join(root, "volume", "assistant") // ~/assistant
	other := filepath.Join(root, "other", "ext")         // ~/ext
	for _, d := range []string{home, volume, other} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{home + "/assistant": volume, home + "/ext": other} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	dirs := brainDirsJSON(t,
		brainFolder(t, "~/assistant/a", manifest.Manifest{ID: "a", Routing: manifest.Routing{HandlesDMs: true}}),
		brainFolder(t, "~/ext/b", manifest.Manifest{ID: "b"}),
	)
	rule := func(verb, op, filter, p string) string {
		return `(` + verb + ` ` + op + ` (` + filter + ` "` + p + `"))`
	}
	allow := func(op, p string) string { return rule("allow", op, "subpath", p) }
	deny := func(op, filter, p string) string { return rule("deny", op, filter, p) }
	cases := []struct {
		name    string
		rules   []string
		inherit bool // in the profile with inherit_claude_user_config too
	}{
		{"its own folder as configured", []string{allow("file-read*", home+"/assistant/a"), allow("file-write*", home+"/assistant/a")}, true},
		{"its own folder by its real path", []string{allow("file-read*", volume+"/a"), allow("file-write*", volume+"/a")}, true},
		{"its own folder pinned both ways", []string{deny("file-write*", "literal", home+"/assistant/a"), deny("file-write*", "literal", volume+"/a")}, true},
		{"the other brain's folder both ways", []string{
			deny("file-read*", "subpath", home+"/ext/b"), deny("file-write*", "subpath", home+"/ext/b"),
			deny("file-read*", "subpath", other+"/b"), deny("file-write*", "subpath", other+"/b")}, true},
		{"your instructions in HOME above its folder as configured", []string{deny("file-read*", "literal", home+"/CLAUDE.md")}, false},
		{"your instructions above its real folder", []string{
			deny("file-read*", "literal", root+"/volume/CLAUDE.md"), deny("file-read*", "subpath", root+"/volume/.claude")}, false},
	}
	for _, inherit := range []bool{false, true} {
		cfg, err := loadConfig(writeConfig(t, fmt.Sprintf(`{"data_dir": %q, "inherit_claude_user_config": %v%s}`, home+"/data", inherit, dirs)))
		if err != nil {
			t.Fatal(err)
		}
		mgr := newTestManager(t, cfg)
		t.Cleanup(mgr.Stop)
		if dir := mgr.Get("a").Dir(); dir != filepath.Join(volume, "a") {
			t.Fatalf("brain a runs in %s, want its real path", dir)
		}
		lines := strings.Split(mustProfile(t, mgr.profiler, "a"), "\n")
		t.Run(fmt.Sprintf("inherit_claude_user_config=%v", inherit), func(t *testing.T) {
			for _, tc := range cases {
				want := !inherit || tc.inherit
				t.Run(tc.name, func(t *testing.T) {
					for _, r := range tc.rules {
						if got := slices.Contains(lines, r); got != want {
							t.Errorf("profile has %s: %v, want %v", r, got, want)
						}
					}
				})
			}
		})
	}
}

// TestSandboxProfileCoversTheFolderABrainRunsIn: a brain runs in the real
// path its folder had when the brains were loaded. Once ~/assistant points
// elsewhere, the folder as configured leads somewhere else until the next
// reload, while the brain still reads and writes the old one. Its profile
// must still pin that folder and deny the instructions above it, and the
// other brains' profiles must still deny it, which the folder as
// configured no longer gives.
func TestSandboxProfileCoversTheFolderABrainRunsIn(t *testing.T) {
	root := fsx.Canonical(t.TempDir())
	home := filepath.Join(root, "home")
	volume := filepath.Join(root, "volume", "assistant") // ~/assistant when loaded
	moved := filepath.Join(root, "moved", "assistant")   // ~/assistant after
	for _, d := range []string{home, volume, filepath.Join(moved, "a")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(home, "assistant")
	if err := os.Symlink(volume, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	dirs := brainDirsJSON(t,
		brainFolder(t, "~/assistant/a", manifest.Manifest{ID: "a", Routing: manifest.Routing{HandlesDMs: true}}),
		brainFolder(t, "~/b", manifest.Manifest{ID: "b"}),
	)
	cfg, err := loadConfig(writeConfig(t, fmt.Sprintf(`{"data_dir": %q%s}`, home+"/data", dirs)))
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestManager(t, cfg)
	t.Cleanup(mgr.Stop)
	ran := mgr.Get("a").Dir()
	if ran != filepath.Join(volume, "a") {
		t.Fatalf("brain a runs in %s, want its real path", ran)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, link); err != nil {
		t.Fatal(err)
	}

	own := strings.Split(mustProfile(t, mgr.profiler, "a"), "\n")
	other := strings.Split(mustProfile(t, mgr.profiler, "b"), "\n")
	rule := func(op, filter, p string) string { return `(deny ` + op + ` (` + filter + ` "` + p + `"))` }
	for _, c := range []struct {
		name    string
		profile []string
		rules   []string
	}{
		{"its folder pinned", own, []string{rule("file-write*", "literal", ran)}},
		{"your instructions above its folder", own, []string{rule("file-read*", "literal", filepath.Join(volume, "CLAUDE.md"))}},
		{"its folder denied to the other brain", other, []string{rule("file-read*", "subpath", ran), rule("file-write*", "subpath", ran)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, r := range c.rules {
				if !slices.Contains(c.profile, r) {
					t.Errorf("profile lacks %s", r)
				}
			}
		})
	}
}

// TestSandboxProfileDeniesInstructionsAboveBrains: Claude Code reads the
// CLAUDE.md, CLAUDE.local.md and .claude/ in every folder above its working
// directory whatever its flags say, so a brain in HOME read ~/CLAUDE.md and
// ~/.claude/CLAUDE.md. Brains may not read them, up to /, along the
// folder's path as configured and its real path, nor ~/.claude/CLAUDE.md
// and ~/.claude/rules for a brain outside HOME (writing them is denied
// with everything else, see TestSandboxProfileWritesOnlyItsOwnPlaces). A
// brain's own folder is its own. inherit_claude_user_config drops all of it
// again.
func TestSandboxProfileDeniesInstructionsAboveBrains(t *testing.T) {
	root := fsx.Canonical(t.TempDir())
	home := filepath.Join(root, "home")
	outside := fsx.Canonical(t.TempDir()) // a sibling of root, not inside it
	vol := fsx.Canonical(t.TempDir())     // where ~/linked really is
	for _, d := range []string{home + "/assistant/a", home + "/assistant/b", outside + "/brains/d", vol + "/brains/l"} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(vol+"/brains", home+"/linked"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	dirs := map[string][]string{
		"a": {home + "/assistant/a"},
		"b": {home + "/assistant/b"},
		"d": {outside + "/brains/d"}, // outside HOME
		"l": {home + "/linked/l"},    // really vol/brains/l
	}
	isolated := NewSandboxProfiler(dirs, &config.Config{})
	inherit := NewSandboxProfiler(dirs, &config.Config{InheritClaudeUserConfig: true})
	rule := func(op, filter, p string) string { return `(deny ` + op + ` (` + filter + ` "` + p + `"))` }
	cases := []struct {
		brain, name string
		sp          *SandboxProfiler
		rules       []string
		want        bool
	}{
		{"a", "the brains folder's CLAUDE.md", isolated, []string{rule("file-read*", "literal", home+"/assistant/CLAUDE.md")}, true},
		{"a", "the brains folder's CLAUDE.local.md", isolated, []string{rule("file-read*", "literal", home+"/assistant/CLAUDE.local.md")}, true},
		{"a", "the brains folder's .claude", isolated, []string{rule("file-read*", "subpath", home+"/assistant/.claude")}, true},
		{"a", "your home folder's CLAUDE.md and CLAUDE.local.md", isolated, []string{
			rule("file-read*", "literal", home+"/CLAUDE.md"), rule("file-read*", "literal", home+"/CLAUDE.local.md")}, true},
		{"a", "~/.claude/CLAUDE.md and ~/.claude/rules", isolated, []string{
			rule("file-read*", "literal", home+"/.claude/CLAUDE.md"), rule("file-read*", "subpath", home+"/.claude/rules")}, true},
		{"a", "and the rest of ~/.claude", isolated, []string{rule("file-read*", "subpath", home+"/.claude")}, true},
		{"a", "above HOME", isolated, []string{
			rule("file-read*", "literal", root+"/CLAUDE.md"), rule("file-read*", "subpath", root+"/.claude")}, true},
		{"a", "up to /", isolated, []string{
			rule("file-read*", "literal", "/CLAUDE.md"), rule("file-read*", "literal", "/CLAUDE.local.md"), rule("file-read*", "subpath", "/.claude")}, true},
		{"a", "its own CLAUDE.md", isolated, []string{rule("file-read*", "literal", home+"/assistant/a/CLAUDE.md")}, false},
		{"a", "its own .claude", isolated, []string{rule("file-read*", "subpath", home+"/assistant/a/.claude")}, false},
		{"a", "not what is above another brain only", isolated, []string{rule("file-read*", "literal", outside+"/brains/CLAUDE.md")}, false},
		{"d", "above a brain outside HOME", isolated, []string{
			rule("file-read*", "literal", outside+"/brains/CLAUDE.md"), rule("file-read*", "subpath", outside+"/brains/.claude")}, true},
		{"d", "~/.claude/CLAUDE.md for a brain outside HOME too", isolated, []string{
			rule("file-read*", "literal", home+"/.claude/CLAUDE.md"), rule("file-read*", "subpath", home+"/.claude/rules")}, true},
		{"d", "not ~/CLAUDE.md, which is not above it", isolated, []string{rule("file-read*", "literal", home+"/CLAUDE.md")}, false},
		{"l", "above its path as configured", isolated, []string{
			rule("file-read*", "literal", home+"/linked/CLAUDE.md"), rule("file-read*", "subpath", home+"/linked/.claude")}, true},
		{"l", "above its real path", isolated, []string{
			rule("file-read*", "literal", vol+"/brains/CLAUDE.md"), rule("file-read*", "literal", vol+"/CLAUDE.md"), rule("file-read*", "subpath", vol+"/.claude")}, true},
		{"a", "inherit: none of your instructions", inherit, []string{
			rule("file-read*", "literal", home+"/assistant/CLAUDE.md"), rule("file-read*", "subpath", home+"/assistant/.claude"),
			rule("file-read*", "literal", home+"/CLAUDE.md"), rule("file-read*", "literal", home+"/.claude/CLAUDE.md"),
			rule("file-read*", "subpath", home+"/.claude/rules"), rule("file-read*", "literal", "/CLAUDE.md")}, false},
		{"a", "inherit: nothing above HOME, not even a pin", inherit, []string{
			rule("file-write*", "literal", root+"/CLAUDE.md"), rule("file-write*", "literal", root)}, false},
		{"a", "inherit: never writable", inherit, []string{rule("file-write*", "literal", home+"/.claude/CLAUDE.md")}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := strings.Split(mustProfile(t, tc.sp, tc.brain), "\n")
			for _, r := range tc.rules {
				if got := slices.Contains(lines, r); got != tc.want {
					t.Errorf("profile of %s has %s: %v, want %v", tc.brain, r, got, tc.want)
				}
			}
		})
	}
}

// sbplRegex is p quoted for a regex, as written in the profile.
func sbplRegex(p string) string {
	return strings.ReplaceAll(regexp.QuoteMeta(p), `\`, `\\`)
}

// TestSandboxProfileWritesEachDenyOnce: the same path can come from several
// places, and the profile goes inline with every run. A deny is written once until an allow follows. After an
// allow it must be written again: the last matching rule wins, so leaving
// it out there would let the allow open the path.
func TestSandboxProfileWritesEachDenyOnce(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
	t.Run("no deny twice between allows", func(t *testing.T) {
		sp, home, _ := precedenceFixture(t, false)
		// ~/.gitconfig is a protected path and part of the git config.
		profile := mustProfile(t, sp, "a")
		if !strings.Contains(profile, `(deny file-write* (literal "`+home+`/.gitconfig"))`) {
			t.Fatal("the fixture's profile does not protect ~/.gitconfig")
		}
		seen := map[string]bool{}
		for _, line := range strings.Split(profile, "\n") {
			switch {
			case strings.HasPrefix(line, "(allow"):
				clear(seen)
			case !strings.HasPrefix(line, "(deny"):
			case seen[line]:
				t.Errorf("written twice: %s", line)
			default:
				seen[line] = true
			}
		}
	})
	t.Run("a deny again after an allow", func(t *testing.T) {
		t.Setenv("HOME", home)
		team, b := filepath.Join(home, "team"), filepath.Join(home, "team", "b")
		cfg := &config.Config{Sandbox: config.SandboxConfig{DenyPaths: []string{b}, AllowPaths: []string{team}}}
		lines := strings.Split(mustProfile(t, NewSandboxProfiler(map[string][]string{"a": {filepath.Join(home, "a")}, "b": {b}}, cfg), "a"), "\n")
		allow := slices.Index(lines, `(allow file-read* (subpath "`+team+`"))`)
		// Writing b is denied with everything else before the allow.
		for _, rule := range []string{`(deny file-read* (subpath "` + b + `"))`} {
			first, last := slices.Index(lines, rule), -1
			for i, l := range lines {
				if l == rule {
					last = i
				}
			}
			if allow < 0 || first < 0 || first > allow || last < allow {
				t.Errorf("%s at lines %d and %d, the allow_paths allow at %d; want it before and after", rule, first, last, allow)
			}
		}
	})
}

// browserProfileRules are the rules that deny the browser profiles in home.
func browserProfileRules(home string, both func(filter, p string) []string) []string {
	var out []string
	for _, b := range []string{"Google", "Firefox", "BraveSoftware", "Arc", "Chromium"} {
		out = append(out, both("subpath", home+"/Library/Application Support/"+b)...)
	}
	// The profile's strings escape a backslash, as the regex's \. needs.
	prefixes := strings.ReplaceAll("^"+regexp.QuoteMeta(home+"/Library/Application Support")+sandbox.BrowserProfilePattern(), `\`, `\\`)
	out = append(out, both("regex", prefixes)...)
	return append(out, both("subpath", home+"/Library/Safari")...)
}

// Every browser's profile folders are denied, the release channels kept
// beside the stable one included. The prefix rule is a regex, matched here
// with Go's; TestSandboxDeniesBrowserProfiles checks the real sandbox.
func TestBrowserProfilesDenyEveryChannel(t *testing.T) {
	const home = "/Users/you"
	prefixes := regexp.MustCompile("^" + regexp.QuoteMeta(home+"/Library/Application Support") + sandbox.BrowserProfilePattern())
	denied := func(p string) bool {
		for _, sub := range sandbox.HomeReadDenies() {
			if d := home + "/" + sub; p == d || strings.HasPrefix(p, d+"/") {
				return true
			}
		}
		return prefixes.MatchString(p)
	}
	for _, c := range []struct {
		folder string
		want   bool
	}{
		{"Google/Chrome", true},
		{"Google/Chrome Beta", true},
		{"Google/Chrome Canary", true},
		{"BraveSoftware/Brave-Browser", true},
		{"BraveSoftware/Brave-Browser-Nightly", true},
		{"Microsoft Edge", true},
		{"Microsoft Edge Beta", true},
		{"Microsoft Edge Dev", true},
		{"Microsoft Edge Canary", true},
		{"Vivaldi", true},
		{"Vivaldi Snapshot", true},
		{"com.operasoftware.Opera", true},
		{"com.operasoftware.OperaGX", true},
		{"com.operasoftware.OperaNext", true},
		{"Arc", true},
		{"Chromium", true},
		{"Firefox", true},
		// Other programs' folders stay open: the CLI and tools may need them.
		{"Claude", false},
		{"Code", false},
		{"com.operasoftwareX", false},
	} {
		t.Run(c.folder, func(t *testing.T) {
			for _, p := range []string{c.folder, c.folder + "/Default/Local Storage/leveldb/000003.log"} {
				if got := denied(home + "/Library/Application Support/" + p); got != c.want {
					t.Errorf("%s denied = %v, want %v", p, got, c.want)
				}
			}
		})
	}
}

// TestSandboxProfileDeniesBridgeSecrets: the deny set follows the bridge's
// secrets wherever they are. db_path can put the WhatsApp session store
// outside the data dir, where every brain could read it; an install that
// moved to ~/.fridolin can leave the old ~/.whatsapp-claude-bridge behind
// with older copies of the same keys and tokens.
func TestSandboxProfileDeniesBridgeSecrets(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	tmp := fsx.Canonical(os.TempDir())
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
			append(both("subpath", home+"/.fridolin"), `(allow file-read* (subpath "`+home+`/.whatsapp-claude-bridge/media/a"))`), nil},
		{"bridge logs", home + "/.fridolin", home + "/.fridolin/bridge.db",
			[]string{both("subpath", home+"/Library/Logs/fridolin")[0], both("subpath", home+"/Library/Logs/whatsapp-claude-bridge")[0]}, nil},
		// Where a dashboard session, or an older dashboard's token, lives.
		{"browser profiles", home + "/.fridolin", home + "/.fridolin/bridge.db", browserProfileRules(home, func(filter, p string) []string { return both(filter, p)[:1] }), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{DataDir: tc.dataDir, DBPath: tc.db}
			lines := strings.Split(mustProfile(t, NewSandboxProfiler(map[string][]string{"a": {home + "/a"}}, cfg), "a"), "\n")
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
			wd := fsx.Canonical(t.TempDir())
			t.Chdir(wd)
			cfg := &config.Config{DataDir: home + "/.fridolin", DBPath: db}
			lines := strings.Split(mustProfile(t, NewSandboxProfiler(map[string][]string{"a": {home + "/a"}}, cfg), "a"), "\n")
			abs := filepath.Join(wd, db)
			for _, rule := range append(dbRules(abs), both("subpath", filepath.Dir(abs))...) {
				if !slices.Contains(lines, rule) {
					t.Errorf("profile lacks %s", rule)
				}
			}
		})
	}
}

// TestSandboxProfileDeniesTheConfigFile: --config or FRIDOLIN_CONFIG can
// put config.json outside the data dir, and with it the .env older versions
// read next to it. Brains could read both, and rewrite the config without
// the dashboard token. Inside the data dir the data dir deny covers them
// already.
func TestSandboxProfileDeniesTheConfigFile(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
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
			sp := NewSandboxProfiler(map[string][]string{"a": {home + "/a"}}, &config.Config{DataDir: home + "/.fridolin"})
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
			// The temp file fsx.WriteFileAtomic saves through,
			// .config.json.*.tmp.
			for _, op := range []string{"file-read*", "file-write*"} {
				rule := `(deny ` + op + ` (regex "^` + sbplRegex(dir) + `/\\.config\\.json\\.[^/]*\\.tmp$"))`
				if got := slices.Contains(lines, rule); got != tc.want {
					t.Errorf("profile has %s: %v, want %v", rule, got, tc.want)
				}
			}
			// The .env in the working directory, which the bridge used to read.
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

// TestSandboxGitConfigModes: "deny" hides all git configuration, the XDG
// folder (~/.config/git) as well as ~/.gitconfig and what it includes;
// "read" leaves them readable. In both modes none of them is writable, so
// a brain cannot set core.fsmonitor or an alias that your own git runs:
// "deny" used to write-protect nothing. git's credential file in
// ~/.config/git is unreadable in both.
func TestSandboxGitConfigModes(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	writeTestFile(t, filepath.Join(home, ".gitconfig"), "[include]\n\tpath = .gitconfig-extra ; local settings\n")
	for _, mode := range []string{"read", "deny"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &config.Config{Sandbox: config.SandboxConfig{GitConfig: mode}}
			lines := strings.Split(mustProfile(t, NewSandboxProfiler(map[string][]string{"a": {home + "/a"}}, cfg), "a"), "\n")
			for _, c := range []struct {
				rule string
				want bool
			}{
				{`(deny file-read* (literal "` + home + `/.gitconfig"))`, mode == "deny"},
				{`(deny file-read* (literal "` + home + `/.gitconfig-extra"))`, mode == "deny"},
				{`(deny file-read* (subpath "` + home + `/.config/git"))`, mode == "deny"},
				{`(deny file-read* (literal "` + home + `/.config/git/credentials"))`, true},
				{`(deny file-write* (literal "` + home + `/.gitconfig"))`, true},
				{`(deny file-write* (literal "` + home + `/.gitconfig-extra"))`, true},
				{`(deny file-write* (subpath "` + home + `/.config/git"))`, true},
			} {
				if got := slices.Contains(lines, c.rule); got != c.want {
					t.Errorf("profile has %s: %v, want %v", c.rule, got, c.want)
				}
			}
		})
	}
}

// TestSandboxAllowPathsCannotOpenBridgeSecrets: allow_paths re-opens what the
// home-folder defaults close, but not the bridge's own secrets or another
// brain's folder. Their denies follow the allow_paths allows, so an entry
// that happens to cover the data dir, the credentials dir, the database or
// the folder that holds the brains opens none of it.
func TestSandboxAllowPathsCannotOpenBridgeSecrets(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	cfg := &config.Config{
		DataDir: home + "/.fridolin",
		DBPath:  home + "/wa-store/bridge.db",
		Sandbox: config.SandboxConfig{
			CredentialsDir: home + "/creds",
			AllowPaths:     []string{home + "/.fridolin", home + "/creds", home + "/wa-store", home + "/assistant", home + "/Documents/share"},
		},
	}
	dirs := map[string][]string{"a": {home + "/assistant/a"}, "b": {home + "/assistant/b"}}
	lines := strings.Split(mustProfile(t, NewSandboxProfiler(dirs, cfg), "a"), "\n")
	for _, c := range []struct{ allowed, denied string }{
		{"/.fridolin", "/.fridolin"},
		{"/creds", "/creds"},
		{"/wa-store", "/wa-store"},
		{"/assistant", "/assistant/b"},
	} {
		for _, op := range []string{"file-read*", "file-write*"} {
			allow := slices.Index(lines, `(allow file-read* (subpath "`+home+c.allowed+`"))`)
			deny := slices.Index(lines, `(deny `+op+` (subpath "`+home+c.denied+`"))`)
			if allow < 0 || deny < allow {
				t.Errorf("%s: %s deny on line %d does not follow the allow_paths allow on line %d", c.denied, op, deny, allow)
			}
		}
	}
	// A home-folder default still opens: its deny comes before the allow.
	allow := slices.Index(lines, `(allow file-read* (subpath "`+home+`/Documents/share"))`)
	deny := slices.Index(lines, `(deny file-read* (subpath "`+home+`/Documents"))`)
	if deny < 0 || allow < deny {
		t.Errorf("allow_paths no longer re-opens Documents/share (deny line %d, allow line %d)", deny, allow)
	}
}

// profileRules are the rules of a profile, one per line, without comments.
func profileRules(profile string) []string {
	var rules []string
	for _, l := range strings.Split(profile, "\n") {
		if strings.HasPrefix(l, "(") && !strings.HasPrefix(l, "(version") {
			rules = append(rules, l)
		}
	}
	return rules
}

// precedenceFixture is brain a in ~/assistant/a next to brain b, with the
// data dir in HOME, the database and the credentials dir outside it, a
// config file outside the data dir, deny_paths entries inside and outside
// a's folder and allow_paths entries in a default-denied folder and over
// the data dir.
func precedenceFixture(t *testing.T, inherit bool) (sp *SandboxProfiler, home, own string) {
	t.Helper()
	home = fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	own = home + "/assistant/a"
	cfg := &config.Config{
		DataDir:                 home + "/.fridolin",
		DBPath:                  home + "/wa/bridge.db",
		InheritClaudeUserConfig: inherit,
		Sandbox: config.SandboxConfig{
			CredentialsDir: home + "/creds",
			DenyPaths:      []string{home + "/clients", own + "/work/private"},
			AllowPaths:     []string{home + "/Documents/share", home + "/.fridolin", own + "/work/private/shared"},
		},
	}
	sp = NewSandboxProfiler(map[string][]string{"a": {own}, "b": {home + "/assistant/b"}}, cfg)
	sp.SetConfigFile(home + "/etc/config.json")
	return sp, home, own
}

// TestSandboxProfilePrecedence: the sandbox applies the last rule that
// matches, so the order of the profile is its precedence model (see
// Profile). It used to let a brain's own folder, written last, win over
// every deny, those for what lay inside the folder included, and let
// deny_paths never restrict a subfolder of a brain. Now writing is denied
// before anything is allowed; the home-folder defaults and deny_paths give
// way to the brain's folder and allow_paths; the bridge's own files and
// other brains' folders follow both, so neither opens them; the brain's
// inbox, state and brain API tokens follow those; and what must stay
// closed inside the brain's own places comes last.
func TestSandboxProfilePrecedence(t *testing.T) {
	sp, home, own := precedenceFixture(t, false)
	rules := profileRules(mustProfile(t, sp, "a"))
	q := func(op, filter, p string) string { return `(` + op + ` (` + filter + ` "` + p + `"))` }
	// first and last fail the (sub)test they are given, so that a missing
	// rule reports every case rather than end the run at the first.
	first := func(t *testing.T, rule string) int {
		t.Helper()
		i := slices.Index(rules, rule)
		if i < 0 {
			t.Fatalf("profile lacks %s", rule)
		}
		return i
	}
	last := func(t *testing.T, rule string) int {
		t.Helper()
		first(t, rule)
		i := len(rules) - 1
		for rules[i] != rule {
			i--
		}
		return i
	}
	state := home + "/.fridolin/brains/a"
	cases := []struct {
		name            string
		earlier, later  string
		earlierIsLatest bool // compare the last copy of earlier
	}{
		{"writing is denied before anything is allowed", `(deny file-write* (subpath "/"))`, q("allow file-write*", "literal", "/dev/null"), false},
		{"its own folder wins over a home-folder default", q("deny file-read*", "subpath", home+"/Documents"), q("allow file-read*", "subpath", own), false},
		{"allow_paths wins over a home-folder default", q("deny file-read*", "subpath", home+"/Documents"), q("allow file-read*", "subpath", home+"/Documents/share"), false},
		{"allow_paths cannot open the data dir", q("allow file-read*", "subpath", home+"/.fridolin"), q("deny file-read*", "subpath", home+"/.fridolin"), false},
		{"its own folder cannot open another brain", q("allow file-read*", "subpath", own), q("deny file-read*", "subpath", home+"/assistant/b"), false},
		{"its own folder cannot open the database's folder", q("allow file-write*", "subpath", own), q("deny file-write*", "subpath", home+"/wa"), false},
		{"its own folder cannot open the config file", q("allow file-read*", "subpath", own), q("deny file-read*", "literal", home+"/etc/config.json"), false},
		{"its own folder cannot open the credentials dir", q("allow file-read*", "subpath", own), q("deny file-read*", "subpath", home+"/creds"), false},
		{"its inbox after the data dir", q("deny file-read*", "subpath", home+"/.fridolin"), q("allow file-read*", "subpath", home+"/.fridolin/media/a"), false},
		{"its state after the data dir", q("deny file-write*", "subpath", home+"/.fridolin"), q("allow file-write*", "subpath", state), false},
		{"its brain API tokens after the data dir", q("deny file-read*", "subpath", home+"/.fridolin"), q("allow file-read*", "subpath", home+"/.fridolin/run-tokens/a"), false},
		{"a deny_paths entry in its folder wins over the folder", q("allow file-read*", "subpath", own), q("deny file-read*", "subpath", own+"/work/private"), false},
		{"an allow_paths entry inside that wins over it", q("deny file-read*", "subpath", own+"/work/private"), q("allow file-read*", "subpath", own+"/work/private/shared"), true},
		{"nothing given to it opens the database", q("allow file-write*", "subpath", state), q("deny file-read*", "literal", home+"/wa/bridge.db"), false},
		{"its folder pinned after its allow", q("allow file-write*", "subpath", own), q("deny file-write*", "literal", own), false},
		{"its inbox pinned after its allow", q("allow file-write*", "subpath", home+"/.fridolin/media/a"), q("deny file-write*", "literal", home+"/.fridolin/media/a"), false},
		{"its state folders pinned after their allow", q("allow file-write*", "subpath", state), q("deny file-write*", "literal", state+"/claude"), false},
		{"the folder between it and a deny_paths entry pinned", q("allow file-write*", "subpath", own), q("deny file-write*", "literal", own+"/work"), false},
		{"the folders above its state may be looked at", q("deny file-read*", "subpath", home+"/.fridolin"), q("allow file-read-metadata", "literal", home+"/.fridolin"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := first(t, tc.earlier)
			if tc.earlierIsLatest {
				e = last(t, tc.earlier)
			}
			if l := last(t, tc.later); l < e {
				t.Errorf("%s (last on rule %d) comes before %s (rule %d)", tc.later, l, tc.earlier, e)
			}
		})
	}
	// A deny_paths entry outside the brain's folder stays a soft deny: it
	// is written before the brain's own folder, which it does not hold.
	if i, j := first(t, q("deny file-read*", "subpath", home+"/clients")), first(t, q("allow file-read*", "subpath", own)); i > j {
		t.Errorf("the deny_paths entry outside the folder (rule %d) follows the folder (rule %d)", i, j)
	}
}

// TestSandboxProfileWritesOnlyItsOwnPlaces: a brain could write anywhere
// outside a short denylist: ~/.claude/settings.json hooks and CLAUDE.md,
// which every other brain and your own sessions load, other sessions'
// transcripts, a LaunchAgent, ~/.zshrc, a program in /opt/homebrew, the
// bridge's install. Now writing is denied everywhere, and the only places
// the profile opens for it are the brain's own: a few devices, its folder,
// its inbox and its state. With
// inherit_claude_user_config it shares your config dir, and so writes the
// CLI's state in ~/.claude and ~/.claude.json too. With plaud.brain_access
// it may rewrite the Plaud login it shares and set its mode, which the
// Plaud CLI does when it refreshes it, and set the mode of its folder,
// but neither remove nor replace the file.
func TestSandboxProfileWritesOnlyItsOwnPlaces(t *testing.T) {
	for _, tc := range []struct{ inherit, plaud bool }{{false, false}, {true, false}, {false, true}} {
		t.Run(fmt.Sprintf("inherit_claude_user_config=%v,plaud.brain_access=%v", tc.inherit, tc.plaud), func(t *testing.T) {
			sp, home, own := precedenceFixture(t, tc.inherit)
			sp.PlaudShared = tc.plaud
			q := func(filter, p string) string { return `(allow file-write* (` + filter + ` "` + p + `"))` }
			want := []string{
				q("literal", "/dev/null"), q("literal", "/dev/zero"), q("literal", "/dev/dtracehelper"), q("subpath", "/dev/fd"),
				q("subpath", own),
				q("subpath", home+"/.fridolin/media/a"),
				q("subpath", home+"/.fridolin/brains/a"),
			}
			if tc.inherit {
				want = append(want, q("subpath", home+"/.claude"), q("literal", home+"/.claude.json"),
					`(allow file-write* (regex "^`+sbplRegex(home)+`/\\.claude\\.json\\.[^/]*$"))`)
			}
			if tc.plaud {
				want = append(want, `(allow file-write-data (literal "`+home+`/.plaud/tokens.json"))`,
					`(allow file-write-mode (literal "`+home+`/.plaud/tokens.json"))`,
					`(allow file-write-mode (literal "`+home+`/.plaud"))`)
			}
			var got []string
			for _, r := range profileRules(mustProfile(t, sp, "a")) {
				if strings.HasPrefix(r, "(allow file-write") {
					got = append(got, r)
				}
			}
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("write allows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
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

// bridgeProgramsFixture installs fake programs in a fake HOME: the Claude
// CLI in ~/.local/bin (a symlink to its versions folder, as its installer
// lays it out), rclone and tmux in ~/bin, and the Plaud CLI as a node script
// in an nvm node version, off PATH. PATH finds only the Claude CLI, and also
// names ~/bin2 and ~/tools/bin, which do not exist yet. It returns a config
// whose bridge runs all of them, the viewer's tmux included.
func bridgeProgramsFixture(t *testing.T, home string) *config.Config {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home+"/.local/bin:"+home+"/bin2:"+home+"/tools/bin:/usr/bin:/bin")
	nvm := ".nvm/versions/node/v22.0.0/"
	for _, f := range []string{
		".local/share/claude/versions/1.0.0",
		"bin/rclone",
		"bin/tmux",
		nvm + "bin/node",
		nvm + "lib/node_modules/@plaud-ai/cli/dist/index.js",
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
	tmuxPath = home + "/bin/tmux"
	t.Cleanup(func() { tmuxPath = old })
	return &config.Config{ClaudePath: "claude", Viewer: true, Backup: config.BackupConfig{RclonePath: home + "/bin/rclone"}}
}

// TestSandboxProfileProtectsTheBridgesPrograms: the bridge runs the Claude
// CLI, rclone, the Plaud CLI, tmux and node outside any sandbox, and finds
// them, and git, gh, npm and the rest, by name in the folders of its PATH.
// A brain that replaced one, or planted one ahead of it, would run code
// outside its sandbox the next time the bridge starts it. Writing is denied
// everywhere anyway; these hard denies keep them closed whatever is
// allowed before them, as found and as resolved, with the npm package and
// the node of a node script, nvm's node versions, rclone off PATH and the
// folders of PATH, those not there yet too. A PATH folder that holds the
// brain's own folder (HOME on PATH) is left to the default deny, or the
// brain could not write its own folder.
func TestSandboxProfileProtectsTheBridgesPrograms(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
	cfg := bridgeProgramsFixture(t, home)
	own := home + "/a"
	sp := NewSandboxProfiler(map[string][]string{"a": {own}}, cfg)
	rules := profileRules(mustProfile(t, sp, "a"))
	ownAllow := slices.Index(rules, `(allow file-write* (subpath "`+own+`"))`)
	if ownAllow < 0 {
		t.Fatal("profile does not let brain a write its own folder")
	}
	nvm := home + "/.nvm/versions/node/v22.0.0"
	cases := []struct{ name, filter, path string }{
		{"the Claude CLI on PATH", "literal", home + "/.local/bin/claude"},
		{"the Claude CLI it resolves to", "literal", home + "/.local/share/claude/versions/1.0.0"},
		{"the configured rclone", "literal", home + "/bin/rclone"},
		{"rclone off PATH", "literal", home + "/.local/bin/rclone"},
		{"the Plaud CLI found in nvm", "literal", nvm + "/bin/plaud"},
		{"the script it resolves to", "literal", nvm + "/lib/node_modules/@plaud-ai/cli/dist/index.js"},
		{"the Plaud CLI's npm package", "subpath", nvm + "/lib/node_modules/@plaud-ai/cli"},
		{"the node that runs it", "literal", nvm + "/bin/node"},
		{"tmux for the viewer", "literal", home + "/bin/tmux"},
		{"nvm's node versions", "subpath", home + "/.nvm/versions/node"},
		{"a PATH folder", "subpath", home + "/.local/bin"},
		{"a PATH folder not there yet", "subpath", home + "/bin2"},
		{"the Plaud CLI's own folder, first on its PATH", "subpath", nvm + "/bin"},
		{"a folder the bridge falls back on", "subpath", "/opt/homebrew/bin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule := `(deny file-write* (` + tc.filter + ` "` + tc.path + `"))`
			i := slices.Index(rules, rule)
			if i < 0 {
				t.Fatalf("profile lacks %s", rule)
			}
			if i < ownAllow {
				t.Errorf("%s (rule %d) comes before the brain's own folder (rule %d), which would open it", rule, i, ownAllow)
			}
			if r := `(deny file-read* (` + tc.filter + ` "` + tc.path + `"))`; slices.Contains(rules, r) {
				t.Errorf("profile denies running it: %s", r)
			}
		})
	}
	t.Run("a PATH folder that holds the brain's folder", func(t *testing.T) {
		t.Setenv("PATH", home+":"+os.Getenv("PATH"))
		rules := profileRules(mustProfile(t, sp, "a"))
		if r := `(deny file-write* (subpath "` + home + `"))`; slices.Contains(rules, r) {
			t.Errorf("profile has %s, which closes the brain's own folder", r)
		}
		if r := `(deny file-write* (subpath "` + home + `/.local/bin"))`; !slices.Contains(rules, r) {
			t.Errorf("profile lacks %s", r)
		}
	})
}

// TestSandboxProfileKeepsProgramsInASharedClaudeDir: with
// inherit_claude_user_config the allow for your config dir came after the
// hard denies of the programs the bridge runs, and the last rule wins, so
// the Claude CLI in ~/.claude/local and that folder on PATH were writable
// again. Their denies now come again after that allow; those outside the
// config dir do not, the profile goes inline on every run.
func TestSandboxProfileKeepsProgramsInASharedClaudeDir(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
	cfg := bridgeProgramsFixture(t, home)
	local := home + "/.claude/local"
	if err := os.MkdirAll(local, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local+"/claude", []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", local+":"+os.Getenv("PATH"))
	cfg.ClaudePath = local + "/claude"
	cfg.InheritClaudeUserConfig = true
	sp := NewSandboxProfiler(map[string][]string{"a": {home + "/a"}}, cfg)
	rules := profileRules(mustProfile(t, sp, "a"))
	allow := slices.Index(rules, `(allow file-write* (subpath "`+home+`/.claude"))`)
	if allow < 0 {
		t.Fatal("profile does not let brain a write the shared config dir")
	}
	last := func(rule string) int {
		for i := len(rules) - 1; i >= 0; i-- {
			if rules[i] == rule {
				return i
			}
		}
		return -1
	}
	for _, rule := range []string{
		`(deny file-write* (literal "` + local + `/claude"))`,
		`(deny file-write* (subpath "` + local + `"))`,
	} {
		if i := last(rule); i < allow {
			t.Errorf("%s comes last at rule %d, before the config dir's allow at %d, which opens it", rule, i, allow)
		}
	}
	if i := last(`(deny file-write* (subpath "` + home + `/.local/bin"))`); i > allow {
		t.Errorf("a deny outside the config dir came again after its allow, at rule %d", i)
	}
}

// TestSandboxRefusesBrainFoldersHoldingItsPrograms: a brain may write its
// own folder, so a folder that is or holds one of the programs the bridge
// runs outside the sandbox, or lies inside the npm package of one or nvm's
// node versions, would let it plant code there that the bridge runs. Such
// a folder is refused, by every profile and when the brains are loaded; a
// folder inside a PATH folder is not, since a brain there writes only
// below it. Nor is a folder that is or holds a folder of the bridge's PATH
// any more, which used to be refused: the bridge runs nothing from it once
// the folder is a brain's (see setBridgePATH).
func TestSandboxRefusesBrainFoldersHoldingItsPrograms(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
	cfg := bridgeProgramsFixture(t, home)
	nvm := home + "/.nvm/versions/node/v22.0.0"
	cases := []struct{ name, dir, want string }{
		{"a PATH folder not there yet", home + "/bin2", ""},
		{"a PATH folder with the Claude CLI", home + "/.local/bin", "holds the program " + home + "/.local/bin/claude the bridge runs"},
		{"a folder holding a PATH folder not there yet", home + "/tools", ""},
		// By the file the CLI on PATH resolves to.
		{"the Claude CLI's install", home + "/.local/share/claude", "holds the program " + home + "/.local/bin/claude the bridge runs"},
		{"the folder of rclone and tmux", home + "/bin", "holds the program " + home + "/bin/rclone the bridge runs"},
		{"inside nvm's node versions", home + "/.nvm/versions/node/v9", "is inside " + home + "/.nvm/versions/node, which holds programs the bridge runs"},
		{"inside the Plaud CLI's npm package", nvm + "/lib/node_modules/@plaud-ai/cli/x", "which holds programs the bridge runs"},
		{"inside a PATH folder", home + "/.local/bin/brain", ""},
		{"a folder of its own", home + "/a", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp := NewSandboxProfiler(map[string][]string{"a": {tc.dir}}, cfg)
			_, err := sp.Profile("a")
			dirErr := checkBrainDir(tc.dir, tc.dir, cfg, home)
			if tc.want == "" {
				if err != nil || dirErr != nil {
					t.Errorf("Profile: %v; checkBrainDir: %v; want both to accept it", err, dirErr)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Profile error = %v, want one saying %q", err, tc.want)
			}
			if dirErr == nil || !strings.Contains(dirErr.Error(), tc.want) {
				t.Errorf("checkBrainDir = %v, want one saying %q", dirErr, tc.want)
			}
		})
	}
}

// TestSandboxProfileKeepsYourClaudeSetup: brains used to share ~/.claude
// with you and each other: they could read your settings, plugins and
// ~/.claude.json with its MCP servers, and write state every other run
// loads. Each brain now has a config dir of its own, so all of yours is
// unreadable, ~/.claude/CLAUDE.md and rules even for an allow_paths entry
// that covers it. With inherit_claude_user_config brains load your setup
// again and write the CLI's state there, but never the configuration
// itself, nor another session's records, nor a symlink among them.
func TestSandboxProfileKeepsYourClaudeSetup(t *testing.T) {
	q := func(op, filter, p string) string { return `(` + op + ` (` + filter + ` "` + p + `"))` }
	for _, inherit := range []bool{false, true} {
		t.Run(fmt.Sprintf("inherit_claude_user_config=%v", inherit), func(t *testing.T) {
			sp, home, _ := precedenceFixture(t, inherit)
			rules := profileRules(mustProfile(t, sp, "a"))
			claude := home + "/.claude"
			cases := []struct {
				rule string
				want bool
			}{
				{q("deny file-read*", "subpath", claude), !inherit},
				{q("deny file-read*", "literal", home+"/.claude.json"), !inherit},
				{`(deny file-read* (regex "^` + sbplRegex(home) + `/\\.claude\\.json\\.[^/]*$"))`, !inherit},
				{q("deny file-read*", "literal", claude+"/CLAUDE.md"), !inherit},
				{q("deny file-read*", "subpath", claude+"/rules"), !inherit},
				{q("deny file-read*", "subpath", claude+"/projects"), true},
				{q("deny file-read*", "subpath", claude+"/sessions"), true},
				{q("deny file-write*", "literal", claude+"/settings.json"), inherit},
				{q("deny file-write*", "literal", claude+"/CLAUDE.md"), inherit},
				{q("deny file-write*", "subpath", claude+"/hooks"), inherit},
				{q("deny file-write*", "subpath", claude+"/skills"), inherit},
				{q("deny file-write-unlink", "subpath", claude+"/projects"), inherit},
				{`(deny file-write* (regex "^` + sbplRegex(claude+"/projects") + `/[^/]+/memory(/.*)?$"))`, inherit},
				{`(deny file-write-create (require-all (regex "^` + sbplRegex(claude+"/projects") + `(/.*)?$") (vnode-type SYMLINK)))`, inherit},
			}
			for _, c := range cases {
				// The read denies for projects and sessions are covered by
				// the deny on all of ~/.claude when it is not shared.
				if strings.Contains(c.rule, "/projects\"))") || strings.Contains(c.rule, "/sessions\"))") {
					if !inherit && strings.HasPrefix(c.rule, "(deny file-read*") {
						continue
					}
				}
				if got := slices.Contains(rules, c.rule); got != c.want {
					t.Errorf("profile has %s: %v, want %v", c.rule, got, c.want)
				}
			}
		})
	}
}

// TestSandboxRefusesBrainFoldersItCannotProtect: a brain whose folder is
// your home folder, or holds it, would open everything the sandbox protects
// there, ~/CLAUDE.md included, and its folder rule would deny all of HOME to
// every other brain; config.json accepts such a folder although the
// dashboard does not. A brain whose folder is itself something denied
// would open that. Both are refused rather than run with less protection,
// except a folder that is a deny_paths entry: that keeps it from the others.
func TestSandboxRefusesBrainFoldersItCannotProtect(t *testing.T) {
	root := fsx.Canonical(t.TempDir())
	home := root + "/home"
	t.Setenv("HOME", home)
	cases := []struct {
		name, a string // brain a's folder
		wantErr string
		bFails  bool // brain b's profile fails too
	}{
		{"home folder", home, "home folder", true},
		{"above it", root, "home folder", true},
		{"the filesystem root", "/", "home folder", true},
		{"the data dir", home + "/.fridolin", "keeps from brains", false},
		{"the old data dir", home + "/.whatsapp-claude-bridge", "keeps from brains", false},
		{"the credentials dir", home + "/creds", "keeps from brains", false},
		{"the database's folder", home + "/wa", "keeps from brains", false},
		{"the bridge's checkout", home + "/fridolin", "keeps from brains", false},
		{"another brain's folder", home + "/assistant/b", "keeps from brains", true},
		{"a default-denied folder", home + "/.ssh", "keeps from brains", false},
		{"code run outside the sandbox", home + "/Library/LaunchAgents", "keeps from brains", false},
		{"a deny_paths entry", home + "/clients", "", false},
		{"a folder of its own", home + "/assistant/a", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				DataDir: home + "/.fridolin",
				DBPath:  home + "/wa/bridge.db",
				Sandbox: config.SandboxConfig{CredentialsDir: home + "/creds", DenyPaths: []string{home + "/clients"}},
			}
			sp := NewSandboxProfiler(map[string][]string{"a": {tc.a}, "b": {home + "/assistant/b"}}, cfg)
			if err := os.MkdirAll(home+"/fridolin/tools", 0o700); err != nil {
				t.Fatal(err)
			}
			sp.bridgeExe = home + "/fridolin/fridolin"
			_, err := sp.Profile("a")
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Profile(a): %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("Profile(a) error = %v, want one saying %q", err, tc.wantErr)
			}
			// Every profile names every brain's folder, so a brain holding
			// HOME breaks the others' profiles too.
			if _, err := sp.Profile("b"); (err != nil) != tc.bFails {
				t.Errorf("Profile(b) error = %v, want an error %v", err, tc.bFails)
			}
		})
	}
}
