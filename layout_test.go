package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// useLayout makes the bridge believe its own files are laid out as l.
func useLayout(t *testing.T, l bridgeLayout) {
	t.Helper()
	prev := ownLayout
	ownLayout = func() bridgeLayout { return l }
	t.Cleanup(func() { ownLayout = prev })
}

// makeFiles creates each path under root, a folder when it ends in "/".
func makeFiles(t *testing.T, root string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		full := filepath.Join(root, p)
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(full, 0o700); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLayoutOf(t *testing.T) {
	root := canonicalDir(t.TempDir())
	makeFiles(t, root,
		"src/fridolin/go.mod", "src/fridolin/tools/poll_results.py", "src/fridolin/fridolin", "src/fridolin/bin/fridolin",
		"lib/fridolin/fridolin", "lib/fridolin/tools/poll_results.py",
		"installed/fridolin", "installed/tools/poll_results.py",
		"bare/fridolin", "bare/fridolin.prev", "bare/tools.prev/poll_results.py",
		"stale/fridolin", "stale/tools/poll_results.py",
		"other/bin/fridolin", "other/go.mod",
		"alone/fridolin",
	)
	src, lib := root+"/src/fridolin", root+"/lib/fridolin"
	for dir, source := range map[string]string{"installed": src, "bare": src, "stale": root + "/other"} {
		if err := writeManifest(root+"/"+dir, source, false); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name string
		exe  string
		want bridgeLayout
	}{
		{"built in the checkout's root", src + "/fridolin", bridgeLayout{Dir: src, Tools: src + "/tools", Source: src}},
		{"dev build in the checkout's bin/", src + "/bin/fridolin", bridgeLayout{Dir: src, Tools: src + "/tools", Source: src}},
		{"tools next to the binary, no checkout", lib + "/fridolin", bridgeLayout{Dir: lib, Tools: lib + "/tools"}},
		{"installed from a checkout", root + "/installed/fridolin", bridgeLayout{Dir: root + "/installed", Tools: root + "/installed/tools", Source: src}},
		{"installed without tools", root + "/bare/fridolin", bridgeLayout{Dir: root + "/bare", Source: src}},
		{"installed from a folder that is no checkout now", root + "/stale/fridolin", bridgeLayout{Dir: root + "/stale", Tools: root + "/stale/tools"}},
		{"bin/ of a folder without tools", root + "/other/bin/fridolin", bridgeLayout{}},
		{"a binary on its own", root + "/alone/fridolin", bridgeLayout{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := layoutOf(c.exe); got != c.want {
				t.Errorf("layoutOf(%s) = %+v, want %+v", c.exe, got, c.want)
			}
		})
	}
}

func TestBrainRunsKnowWhereTheToolsAre(t *testing.T) {
	// The starter prompt and the example skills run the helpers as
	// $FRIDOLIN_TOOLS_DIR/<tool>, wherever the bridge is installed.
	cases := []struct {
		name, tools, want string
	}{
		{"tools found", "/opt/fridolin/tools", "FRIDOLIN_TOOLS_DIR=/opt/fridolin/tools"},
		{"no tools", "", "FRIDOLIN_TOOLS_DIR unset"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			useLayout(t, bridgeLayout{Tools: c.tools})
			t.Setenv("FRIDOLIN_TOOLS_DIR", "")
			os.Unsetenv("FRIDOLIN_TOOLS_DIR")
			t.Setenv("WCB_FAKE_ENV", "FRIDOLIN_TOOLS_DIR")
			cfg, _ := brainASetup(t)
			mgr := newTestManager(t, cfg)
			d := &deliveries{}
			mgr.SetHooks(BrainHooks{Deliver: d.add})
			t.Cleanup(mgr.Stop)
			if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 20*time.Second, "the run", func() bool { return d.count() == 1 })
			res := d.all()[0].res
			if res.Err != nil {
				t.Fatalf("run failed: %v", res.Err)
			}
			if !strings.HasSuffix(res.Text, " "+c.want) {
				t.Errorf("run = %q, want it to end with %q", res.Text, c.want)
			}
		})
	}
}

func TestStarterFilesNameToolsByTheVariable(t *testing.T) {
	// They used to embed the checkout's absolute path, or a "<bridge-repo>"
	// placeholder when the binary did not run from the checkout.
	files := map[string]string{"brain-prompt.md": starterPrompt(&BrainManifest{Name: "Family"})}
	for _, sk := range exampleSkills() {
		files["skills/"+sk.name] = sk.content
	}
	for name, content := range files {
		if strings.Contains(content, "<bridge-repo>") || strings.Contains(content, "tools/") && !strings.Contains(content, "$FRIDOLIN_TOOLS_DIR") {
			t.Errorf("%s names the tools by a path:\n%s", name, content)
		}
	}
	for _, want := range []string{`"$FRIDOLIN_TOOLS_DIR/poll_results.py"`, "$FRIDOLIN_TOOLS_DIR/history_search.py"} {
		if !strings.Contains(files["brain-prompt.md"], want) {
			t.Errorf("the starter prompt lacks %s", want)
		}
	}
	if !strings.Contains(files["skills/transcription.md"], `"$FRIDOLIN_TOOLS_DIR/transcribe.py"`) {
		t.Errorf("the transcription skill does not run $FRIDOLIN_TOOLS_DIR/transcribe.py")
	}
}

func TestCheckBrainPathWarnsAboutTheBridgesOwnFolders(t *testing.T) {
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	src, lib := home+"/src/fridolin", home+"/.local/lib/fridolin"
	cfg := &Config{DataDir: home + "/.fridolin"}
	const (
		checkout = "inside the bridge's source checkout"
		install  = "inside the folder the bridge is installed in"
	)
	cases := []struct {
		name   string
		layout bridgeLayout
		path   string
		want   string // "" for neither warning
	}{
		{"in the checkout of a dev build", bridgeLayout{Dir: src, Tools: src + "/tools", Source: src}, src + "/brains/a", checkout},
		{"in the checkout an install came from", bridgeLayout{Dir: lib, Tools: lib + "/tools", Source: src}, src + "/a", checkout},
		{"in the install folder", bridgeLayout{Dir: lib, Tools: lib + "/tools", Source: src}, lib + "/a", install},
		{"a folder of its own", bridgeLayout{Dir: lib, Tools: lib + "/tools", Source: src}, home + "/assistant/a", ""},
		{"a binary on its own", bridgeLayout{}, home + "/assistant/a", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			useLayout(t, c.layout)
			_, problems, warnings := CheckBrainPath(c.path, cfg, nil)
			if len(problems) != 0 {
				t.Fatalf("problems: %v", problems)
			}
			all := strings.Join(warnings, "\n")
			for _, w := range []string{checkout, install} {
				if got := strings.Contains(all, w); got != (w == c.want) {
					t.Errorf("warnings %q: %q present = %v, want %v", warnings, w, got, w == c.want)
				}
			}
		})
	}
}

func TestSandboxProfileProtectsTheBridgesOwnFiles(t *testing.T) {
	// launchd runs the bridge outside the sandbox, and setup.sh in the
	// checkout builds what it runs next: a brain must not write either.
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	makeFiles(t, home,
		"src/fridolin/go.mod", "src/fridolin/tools/poll_results.py", "src/fridolin/fridolin", "src/fridolin/bin/fridolin",
		"lib/fridolin/fridolin", "lib/fridolin/tools/poll_results.py",
		"installed/fridolin", "installed/tools/poll_results.py",
		"bare/fridolin", "bare/fridolin.prev", "bare/tools.prev/poll_results.py",
		"alone/fridolin", "a/notes.md",
	)
	src, lib := home+"/src/fridolin", home+"/lib/fridolin"
	for _, dir := range []string{"installed", "bare"} {
		if err := writeManifest(home+"/"+dir, src, false); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name string
		exe  string
		want []string // subpaths denied for writing
	}{
		{"built in the checkout's root", src + "/fridolin", []string{src}},
		{"dev build in the checkout's bin/", src + "/bin/fridolin", []string{src}},
		{"tools next to the binary", lib + "/fridolin", []string{lib}},
		{"installed from a checkout", home + "/installed/fridolin", []string{home + "/installed", src}},
		// Else a brain could plant a fridolin.prev that `service
		// rollback` starts outside any sandbox.
		{"installed without tools", home + "/bare/fridolin", []string{home + "/bare", src}},
		{"a binary on its own", home + "/alone/fridolin", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sp := NewSandboxProfiler(map[string][]string{"a": {home + "/a"}}, &Config{ClaudePath: "claude", DataDir: home + "/.fridolin"})
			sp.bridgeExe = c.exe
			profile := mustProfile(t, sp, "a")
			if !strings.Contains(profile, `(deny file-write* (literal "`+c.exe+`"))`) {
				t.Errorf("the binary itself is writable")
			}
			for _, dir := range []string{src, lib, home + "/installed", home + "/bare", home + "/alone"} {
				rule := `(deny file-write* (subpath "` + dir + `"))`
				want := 0
				if slices.Contains(c.want, dir) {
					want = 1
				}
				if got := strings.Count(profile, rule); got != want {
					t.Errorf("%s appears %d times, want %d", rule, got, want)
				}
			}
		})
	}
}
