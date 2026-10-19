package main

import (
	"encoding/json"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/logx"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/registry"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// The bridge reads and writes files in a brain's folder outside the
// sandbox, while the brain, which may write anything there, controls what
// they are. These tests plant symlinks after the brain is loaded, as a
// manipulated brain would between two messages.

// captureLog collects what the bridge logs until the test ends.
func captureLog(t *testing.T) func() []string {
	t.Helper()
	ring := logx.NewRing(50)
	prev := log.Writer()
	log.SetOutput(ring)
	t.Cleanup(func() { log.SetOutput(prev) })
	return func() []string {
		lines, _ := ring.Since(0, 50)
		var out []string
		for _, l := range lines {
			out = append(out, l.Text)
		}
		return out
	}
}

// brainASetup sets up what folderBrain runs, and returns its
// configuration and home.
func brainASetup(t *testing.T) (*config.Config, string) {
	t.Helper()
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	m := testManifest("a")
	m.Routing.HandlesDMs = true
	if _, err := registry.Scaffold(filepath.Join(home, "a"), &m, brainTemplates); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_test"), []byte("FAKE-PRIVATE-KEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	approved, err := json.Marshal(m.Normalized())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data",
		"claude_path": "`+fakeClaudeEnv(t, 0)+`",
		"brain_dirs": [{"path": "`+home+`/a", "approved": `+string(approved)+`}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg, home
}

// bareBrain is brain a in a fresh folder, outside any manager.
func bareBrain(t testing.TB) *brain.Brain {
	t.Helper()
	mgr := brain.NewManager(brain.Options{Input: commandBrainInputRules()})
	t.Cleanup(mgr.Stop)
	_, err := mgr.Apply([]manifest.Spec{{Dir: t.TempDir(), Manifest: manifest.Manifest{ID: "a"}}}, brain.Globals{}, brain.ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return mgr.Get("a")
}

// plant replaces path with a symlink to target.
func plant(t *testing.T, path, target string) {
	t.Helper()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

// TestBrainWhoseFolderCannotBeOpenedIsLeftOut: a brain whose folder the
// bridge cannot open has nothing to reach its files through. It is left
// out and reported, and the other brains run. Its chats go to no brain:
// they used to fall back to the default brain, which then read a group
// meant for another (see TestLeftOutBrainsKeepTheirChats).
func TestBrainWhoseFolderCannotBeOpenedIsLeftOut(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens any folder")
	}
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	dirs := brainDirsJSON(t,
		brainFolder(t, "~/a", manifest.Manifest{ID: "a", Routing: manifest.Routing{HandlesDMs: true, Default: true}}),
		brainFolder(t, "~/b", manifest.Manifest{ID: "b", Routing: manifest.Routing{Groups: []string{"120363000000000000@g.us"}}}),
	)
	locked := filepath.Join(home, "b")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	cfg, err := loadConfig(writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data",
		"claude_path": "`+fakeClaudeEnv(t, 0)+`"`+dirs+`
	}`))
	if err != nil {
		t.Fatal(err)
	}
	logged := captureLog(t)
	mgr := newTestManager(t, cfg)
	t.Cleanup(mgr.Stop)
	if mgr.Get("a") == nil || mgr.Get("b") != nil {
		t.Fatalf("brains = %v, want a without b", mgr.Brains())
	}
	if br, held := mgr.BrainFor("120363000000000000@g.us", true), mgr.HeldFor("120363000000000000@g.us", true); br != nil || held != "b" {
		t.Errorf("b's group went to %v (held for %q), want it held for b", br, held)
	}
	// The drift check cannot read b's brain.json either, and says so too.
	reported := false
	for _, is := range mgr.Issues() {
		if is.ID != "b" {
			t.Errorf("issue %+v, want only b's", is)
		}
		reported = reported || is.Kind == "missing" && strings.Contains(is.Detail, "the folder cannot be opened")
	}
	if !reported {
		t.Errorf("issues = %+v, want b's folder reported", mgr.Issues())
	}
	if lines := logged(); len(lines) != 1 || !strings.Contains(lines[0], "brain b: leaving it out") {
		t.Errorf("log = %q, want one line leaving b out", lines)
	}
}

// TestLeftOutBrainShowsWhyOnItsPage: the brain's card and page, not only
// the overview, say why a brain whose folder cannot be opened is not
// running, until a reload finds the folder fine.
func TestLeftOutBrainShowsWhyOnItsPage(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens any folder")
	}
	tb := newTestBridge(t, "")
	dir := filepath.Join(tb.home, "b")
	m := testManifest("b")
	if _, err := registry.Scaffold(dir, &m, brainTemplates); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	approved := m.Normalized()
	if _, err := tb.cfgs.Update("", func(c *config.Config) error {
		c.BrainDirs = append(c.BrainDirs, config.BrainRef{Path: dir, Approved: &approved})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	view := func() httpapi.BrainView {
		t.Helper()
		var views []httpapi.BrainView
		_ = json.Unmarshal(tb.do("GET", "/api/brains", nil).Body.Bytes(), &views)
		if len(views) != 1 {
			t.Fatalf("views = %+v, want brain b", views)
		}
		return views[0]
	}

	v := view()
	reported := false
	for _, is := range v.Issues {
		reported = reported || is.Kind == "missing" && strings.Contains(is.Detail, "the folder cannot be opened")
	}
	if v.Status != "missing" || v.Runtime != nil || !reported {
		t.Errorf("view = %+v, want b missing, not running, with the reason", v)
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := tb.brainMgr.Apply(tb.cfgs.Get()); err != nil {
		t.Fatal(err)
	}
	if v := view(); v.Status != "ok" || v.Runtime == nil || len(v.Issues) != 0 {
		t.Errorf("view = %+v, want b running without issues", v)
	}
}

// TestManifestIsNotFollowedOutOfTheFolder: the bridge reads brain.json to
// tell when it changed on disk, outside the sandbox. A brain.json swapped
// for a symlink is not read through, and shows as one the bridge cannot
// read, while the brain runs on with the manifest you approved. A FIFO
// there must not hang the reload either.
func TestManifestIsNotFollowedOutOfTheFolder(t *testing.T) {
	cases := []struct {
		name  string
		plant func(t *testing.T, home string)
		want  string // in the error
	}{
		{"symlink to a manifest outside", func(t *testing.T, home string) {
			plant(t, filepath.Join(home, "a", manifest.File), filepath.Join(home, "b", manifest.File))
		}, fsx.ErrSymlink.Error()},
		{"symlink inside the folder", func(t *testing.T, home string) {
			plant(t, filepath.Join(home, "a", manifest.File), "copy.json")
		}, fsx.ErrSymlink.Error()},
		{"FIFO", func(t *testing.T, home string) {
			p := filepath.Join(home, "a", manifest.File)
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(p, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "not a regular file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, home := brainASetup(t)
			// Another brain's manifest, and a copy in the folder, both with
			// a's id but taking a group: read through, they would show as a
			// change to approve.
			other := testManifest("a")
			other.Routing.Groups = []string{"120363000000000000@g.us"}
			for _, dir := range []string{filepath.Join(home, "b"), filepath.Join(home, "a", "copy")} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := manifest.Write(dir, &other, false); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Rename(filepath.Join(home, "a", "copy", manifest.File), filepath.Join(home, "a", "copy.json")); err != nil {
				t.Fatal(err)
			}
			tc.plant(t, home)

			type resolved struct {
				specs  []manifest.Spec
				issues []registry.Issue
				err    error
			}
			done := make(chan resolved, 1)
			go func() {
				_, err := manifest.Read(filepath.Join(home, "a"))
				specs, issues := ResolveBrains(cfg)
				done <- resolved{specs, issues, err}
			}()
			var got resolved
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("reading brain.json hangs")
			}
			if got.err == nil || !strings.Contains(got.err.Error(), filepath.Join(home, "a", manifest.File)+": "+tc.want) {
				t.Errorf("ReadManifest: %v, want %q for it", got.err, tc.want)
			}
			if len(got.specs) != 1 || len(got.specs[0].Manifest.Routing.Groups) != 0 || !got.specs[0].Manifest.Routing.HandlesDMs {
				t.Errorf("specs = %+v, want brain a as approved", got.specs)
			}
			if len(got.issues) != 1 || got.issues[0].Kind != "drift" || !strings.Contains(got.issues[0].Detail, tc.want) {
				t.Errorf("issues = %+v, want brain.json reported as unreadable", got.issues)
			}
		})
	}
}

func hasRun(t *testing.T, br *brain.Brain) {
	t.Helper()
	testutil.WaitFor(t, 5*time.Second, br.ID()+"'s run", func() bool { r := br.Snapshot().Running; return r != nil && r.PID > 0 })
}
