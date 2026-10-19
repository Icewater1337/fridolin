package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// TestSetBridgePATH: your shell's PATH may name a folder inside a brain's
// folder (~/assistant/personal/bin, say), and the bridge ran programs by
// name from it, outside the sandbox, where the brain may write; the
// sandbox then refused the brain. The bridge now takes every such folder
// out of its own PATH, by the brain folder as configured or by its real
// path, logs it, and hands it to that brain's runs only; one in a folder
// without an approved brain goes to nobody, and so does one whose brain
// was removed, which stays out of the bridge's. The service file's
// folders, kept out of PATH already, are logged with where they go.
func TestSetBridgePATH(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	for _, d := range []string{"real/q", "assistant/personal/bin", "assistant/personal/tools", "new/bin"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(home+"/real/q", home+"/q"); err != nil {
		t.Fatal(err)
	}
	personal := brainFolder(t, home+"/assistant/personal", manifest.Manifest{ID: "personal", Routing: manifest.Routing{HandlesDMs: true}})
	q := brainFolder(t, home+"/q", manifest.Manifest{ID: "q"})
	unapproved := config.BrainRef{Path: home + "/new"}
	path := strings.Join([]string{home + "/assistant/personal/bin", "/usr/bin", home + "/real/q/bin", "relative", home + "/new/bin", home + "/assistant/personal/tools/", "/bin"}, ":")
	cases := []struct {
		name     string
		refs     []config.BrainRef
		brainVar string
		want     string
		wantOwn  map[string][]string
		logged   []string
	}{
		{"no brain folder on PATH", nil, "", path, map[string][]string{}, nil},
		{"brain folders on PATH", []config.BrainRef{personal, q, unapproved}, "",
			"/usr/bin:relative:/bin",
			map[string][]string{"personal": {home + "/assistant/personal/bin", home + "/assistant/personal/tools"}, "q": {home + "/real/q/bin"}},
			[]string{home + "/assistant/personal/bin is in brain personal's folder", home + "/real/q/bin is in brain q's folder", home + "/new/bin is in the brain folder " + home + "/new"}},
		{"what the service file keeps for brains", []config.BrainRef{personal}, home + "/assistant/personal/scripts:" + home + "/gone/bin", "/usr/bin:" + home + "/real/q/bin:relative:" + home + "/new/bin:/bin",
			map[string][]string{"personal": {home + "/assistant/personal/bin", home + "/assistant/personal/tools", home + "/assistant/personal/scripts"}},
			[]string{home + "/assistant/personal/scripts, from " + brainPATHVar + ", goes to brain personal's runs only", home + "/gone/bin, from " + brainPATHVar + ", is in no approved brain's folder"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("PATH", path)
			t.Setenv(brainPATHVar, c.brainVar)
			var logs bytes.Buffer
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })
			own := setBridgePATH(&config.File{BrainDirs: c.refs})
			if got := os.Getenv("PATH"); got != c.want {
				t.Errorf("PATH = %q, want %q", got, c.want)
			}
			if len(own) != len(c.wantOwn) {
				t.Errorf("own = %v, want %v", own, c.wantOwn)
			}
			for id, want := range c.wantOwn {
				if !slices.Equal(own[id], want) {
					t.Errorf("brain %s gets %v, want %v", id, own[id], want)
				}
			}
			for _, l := range c.logged {
				if !strings.Contains(logs.String(), l) {
					t.Errorf("log %q, want it to say %q", logs.String(), l)
				}
			}
		})
	}
	t.Run("the service file's folders are logged once", func(t *testing.T) {
		t.Setenv("PATH", "/usr/bin:/bin")
		t.Setenv(brainPATHVar, home+"/assistant/personal/bin")
		var logs bytes.Buffer
		log.SetOutput(&logs)
		t.Cleanup(func() { log.SetOutput(os.Stderr) })
		for range 2 {
			setBridgePATH(&config.File{BrainDirs: []config.BrainRef{personal}})
		}
		if n := strings.Count(logs.String(), "goes to brain personal's runs"); n != 1 {
			t.Errorf("logged %d times, want once:\n%s", n, logs.String())
		}
	})
	t.Run("a removed brain's folder stays off the bridge's PATH", func(t *testing.T) {
		t.Setenv("PATH", path)
		t.Setenv(brainPATHVar, "")
		setBridgePATH(&config.File{BrainDirs: []config.BrainRef{personal}})
		var logs bytes.Buffer
		log.SetOutput(&logs)
		t.Cleanup(func() { log.SetOutput(os.Stderr) })
		own := setBridgePATH(&config.File{})
		if got := os.Getenv("PATH"); strings.Contains(got, "/assistant/personal/") {
			t.Errorf("PATH = %q after brain personal was removed, want its folders still out", got)
		}
		if len(own) != 0 {
			t.Errorf("own = %v, want nothing for a removed brain", own)
		}
		if !strings.Contains(logs.String(), home+"/assistant/personal/bin was in a brain's folder") {
			t.Errorf("log %q, want it to say why the folder stays out", logs.String())
		}
	})
}

// TestBrainRunsGetTheirOwnPATHFolders: a brain whose folder holds a folder
// of the bridge's PATH runs, rather than being refused, and its runs find
// the programs there first on PATH, while the bridge's own PATH and every
// other brain's run lack it.
func TestBrainRunsGetTheirOwnPATHFolders(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	if err := os.MkdirAll(home+"/assistant/personal/bin", 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", home+"/assistant/personal/bin:/usr/bin:/bin")
	t.Setenv("FRIDOLIN_FAKE_ENV", "PATH")
	claude := fakeClaudeEnv(t, 0)
	cfg, err := loadConfig(writeConfig(t, `{
		"brain_mode": true,
		"claude_path": "`+claude+`",
		"data_dir": "`+home+`/data"`+brainDirsJSON(t,
		brainFolder(t, home+"/assistant/personal", manifest.Manifest{ID: "personal", Routing: manifest.Routing{HandlesDMs: true}}),
		brainFolder(t, home+"/team", manifest.Manifest{ID: "team", Routing: manifest.Routing{Groups: []string{"120363000000000000@g.us"}}}),
	)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, issues := ResolveBrains(cfg); len(issues) != 0 {
		t.Fatalf("issues %+v, want brain personal to run", issues)
	}
	mgr := newTestManager(t, cfg)
	d := &deliveries{}
	mgr.SetHooks(BrainHooks{Deliver: d.add})
	t.Cleanup(mgr.Stop)
	if got := os.Getenv("PATH"); got != "/usr/bin:/bin" {
		t.Errorf("the bridge's PATH = %q, want it without the brain's folder", got)
	}
	if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
		t.Fatal(err)
	}
	group := brainruntime.Inbound{Inbound: chat.Inbound{JID: "120363000000000000@g.us", Sender: "15551234567@s.whatsapp.net", Text: "@claude hi"}}
	if _, _, _, err := mgr.Submit(group); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 20*time.Second, "both runs", func() bool { return d.count() == 2 })
	for _, r := range d.all() {
		if r.res.Err != nil {
			t.Fatalf("run failed: %v", r.res.Err)
		}
		want := " PATH=/usr/bin:/bin"
		if r.brain == "personal" {
			want = " PATH=" + home + "/assistant/personal/bin:/usr/bin:/bin"
		}
		if !strings.HasSuffix(r.res.Text, want) {
			t.Errorf("brain %s ran with %q, want it to end with %q", r.brain, r.res.Text, want)
		}
	}
}

// TestCheckRunsABrainWhoseFolderHoldsAPATHFolder: with your PATH starting
// with a bin/ in the DM brain's folder, `fridolin check` reported that
// brain invalid and left it out, so DMs went to the default brain. It now
// lists the brain with no issue, and prints its profile.
func TestCheckRunsABrainWhoseFolderHoldsAPATHFolder(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	if err := os.MkdirAll(home+"/assistant/personal/bin", 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", home+"/assistant/personal/bin:/usr/bin:/bin")
	path := writeConfig(t, `{
		"brain_mode": true,
		"claude_path": "`+fakeClaudeEnv(t, 0)+`",
		"data_dir": "`+home+`/data"`+brainDirsJSON(t,
		brainFolder(t, home+"/assistant/personal", manifest.Manifest{ID: "personal", Routing: manifest.Routing{HandlesDMs: true}}),
		brainFolder(t, home+"/work", manifest.Manifest{ID: "work", Routing: manifest.Routing{Default: true}}),
	)+`}`)
	o := checkOptions{config: path, routes: []string{"15551234567@s.whatsapp.net"}}
	if runtime.GOOS == "darwin" {
		o.sandboxFor = "personal"
	}
	var out bytes.Buffer
	if err := writeCheck(&out, o); err != nil {
		t.Fatalf("check: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "  ! ") {
		t.Errorf("check reports an issue:\n%s", out.String())
	}
	for _, want := range []string{"brain personal ", "route 15551234567@s.whatsapp.net → personal"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("check output lacks %q:\n%s", want, out.String())
		}
	}
	if o.sandboxFor != "" && !strings.Contains(out.String(), "(version 1)") {
		t.Errorf("check printed no profile:\n%s", out.String())
	}
}

// check probes the Claude version outside the sandbox, so it must remove
// brain-owned PATH folders before that first subprocess, just as startup
// does before the dashboard can probe it.
func TestCheckDoesNotExecuteBrainPATHPrograms(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	brain := filepath.Join(home, "brain")
	ownedBin, trustedBin := filepath.Join(brain, "bin"), filepath.Join(home, "trusted", "bin")
	for _, dir := range []string{ownedBin, trustedBin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(home, "probe-outside-brain")
	planted := "#!/bin/sh\nprintf planted > \"$HOME/probe-outside-brain\"\nprintf '9.9.9 planted\\n'\n"
	if err := os.WriteFile(filepath.Join(ownedBin, "claude"), []byte(planted), 0o700); err != nil {
		t.Fatal(err)
	}
	versionCLI(t, filepath.Join(trustedBin, "claude"), claudecli.TestedClaudeVersion+" trusted")
	t.Setenv("PATH", strings.Join([]string{ownedBin, trustedBin, "/usr/bin", "/bin"}, string(filepath.ListSeparator)))
	path := writeConfig(t, `{"claude_path":"claude","data_dir":"`+home+`/data"`+brainDirsJSON(t,
		brainFolder(t, brain, manifest.Manifest{ID: "a", Routing: manifest.Routing{HandlesDMs: true}}),
	)+`}`)
	var out bytes.Buffer
	if err := writeCheck(&out, checkOptions{config: path}); err != nil {
		t.Fatalf("check: %v\n%s", err, out.String())
	}
	if fsx.Exists(marker) {
		t.Error("check ran the brain's planted Claude outside the sandbox")
	}
	if got := lineWith(out.String(), "claude:"); !strings.Contains(got, claudecli.TestedClaudeVersion+" trusted") || strings.Contains(got, "planted") {
		t.Errorf("Claude summary %q, want the trusted CLI", got)
	}
}
