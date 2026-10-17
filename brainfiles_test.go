package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bridge reads and writes files in a brain's folder outside the
// sandbox, while the brain, which may write anything there, controls what
// they are. These tests plant symlinks after the brain is loaded, as a
// manipulated brain would between two messages.

// captureLog collects what the bridge logs until the test ends.
func captureLog(t *testing.T) func() []string {
	t.Helper()
	ring := NewLogRing(50)
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

// folderBrain runs folder brain a in <home>/a, scaffolded and approved as
// the dashboard adds one, with a fake key in <home>/.ssh/id_test for a
// brain to aim symlinks at. It returns the brain and home.
func folderBrain(t *testing.T) (*Brain, string) {
	t.Helper()
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	m := testManifest("a")
	m.Routing.HandlesDMs = true
	if _, err := ScaffoldBrain(filepath.Join(home, "a"), &m); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_test"), []byte("FAKE-PRIVATE-KEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	approved, err := json.Marshal(m.normalized())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data",
		"claude_path": "`+fakeClaudeEnv(t, 0)+`",
		"brain_dirs": [{"path": "`+home+`/a", "approved": `+string(approved)+`}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestManager(t, cfg)
	t.Cleanup(mgr.Stop)
	br := mgr.Get("a")
	if br == nil {
		t.Fatalf("brain a did not load: %v", mgr.Issues())
	}
	return br, home
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

// TestPromptFileIsNotFollowedOutOfTheFolder: the prompt file goes into
// every run's prompt, where the model can repeat it into the chat. A brain
// that swaps it for a symlink after it was loaded gets nothing through it,
// not even a file in its own folder, and the log says why.
func TestPromptFileIsNotFollowedOutOfTheFolder(t *testing.T) {
	br, home := folderBrain(t)
	dir := filepath.Join(home, "a")
	prompt := filepath.Join(dir, "brain-prompt.md")
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("IN-FOLDER-NOTES\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		plant   func(t *testing.T)
		want    string // in the prompt
		refused bool
	}{
		{"regular file", func(t *testing.T) {
			if err := os.Remove(prompt); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := os.WriteFile(prompt, []byte("YOU ARE BRAIN A\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "YOU ARE BRAIN A", false},
		{"absolute symlink to a key", func(t *testing.T) { plant(t, prompt, filepath.Join(home, ".ssh", "id_test")) }, "", true},
		{"relative symlink up and out", func(t *testing.T) { plant(t, prompt, filepath.Join("..", ".ssh", "id_test")) }, "", true},
		{"symlink inside the folder", func(t *testing.T) { plant(t, prompt, "notes.md") }, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logged := captureLog(t)
			tc.plant(t)
			got := br.buildPrompt(br.settings.Load(), dm("hello"))
			if strings.Contains(got, "FAKE-PRIVATE-KEY") || strings.Contains(got, "IN-FOLDER-NOTES") {
				t.Fatalf("the prompt has the symlink's target:\n%s", got)
			}
			if tc.want != "" && !strings.HasPrefix(got, tc.want) {
				t.Errorf("prompt = %q, want it to start with %q", got, tc.want)
			}
			lines := logged()
			if !tc.refused {
				if len(lines) > 0 {
					t.Errorf("unexpected log %q", lines)
				}
				return
			}
			if len(lines) != 1 || !strings.Contains(lines[0], "brain a: could not read prompt file brain-prompt.md: "+errSymlink.Error()) {
				t.Errorf("log = %q, want one line refusing the symlink", lines)
			}
		})
	}
}

// TestBrainWhoseFolderCannotBeOpenedIsLeftOut: a brain whose folder the
// bridge cannot open has nothing to reach its files through. It is left
// out and reported, and the other brains run, its chats falling back.
func TestBrainWhoseFolderCannotBeOpenedIsLeftOut(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens any folder")
	}
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	locked := filepath.Join(home, "b")
	for _, d := range []string{filepath.Join(home, "a"), locked} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	cfg, err := LoadConfig(writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data",
		"claude_path": "`+fakeClaudeEnv(t, 0)+`",
		"brains": [
			{"name": "a", "work_dir": "~/a", "handles_dms": true, "default": true},
			{"name": "b", "work_dir": "~/b", "groups": ["120363000000000000@g.us"]}
		]
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
	if br := mgr.BrainFor("120363000000000000@g.us", true); br != mgr.Get("a") {
		t.Errorf("b's group went to %v, want the default brain a", br)
	}
	issues := mgr.Issues()
	if len(issues) != 1 || issues[0].ID != "b" || issues[0].Kind != "missing" || !strings.Contains(issues[0].Detail, "the folder cannot be opened") {
		t.Errorf("issues = %+v, want b's folder reported", issues)
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
	if _, err := ScaffoldBrain(dir, &m); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	approved := m.normalized()
	if _, err := tb.cfgs.Update("", func(c *Config) error {
		c.BrainDirs = append(c.BrainDirs, BrainRef{Path: dir, Approved: &approved})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	view := func() brainView {
		t.Helper()
		var views []brainView
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

// TestInlinePromptOutsideTheFolderIsStillRead: an inline brain's
// prompt_file may be a path you chose outside its folder. It is read as
// configured until inline brains go away.
func TestInlinePromptOutsideTheFolderIsStillRead(t *testing.T) {
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	shared := filepath.Join(home, "prompts", "shared.md")
	if err := os.MkdirAll(filepath.Dir(shared), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, []byte("SHARED PROMPT\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data",
		"claude_path": "`+fakeClaudeEnv(t, 0)+`",
		"brains": [
			{"name": "out", "work_dir": "~/out", "prompt_file": "~/prompts/shared.md"},
			{"name": "in", "work_dir": "~/in", "groups": ["120363000000000000@g.us"]}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestManager(t, cfg)
	t.Cleanup(mgr.Stop)
	if err := os.WriteFile(filepath.Join(home, "in", "brain-prompt.md"), []byte("IN PROMPT\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"out": "SHARED PROMPT", "in": "IN PROMPT"} {
		br := mgr.Get(id)
		if got := br.buildPrompt(br.settings.Load(), dm("hello")); !strings.HasPrefix(got, want) {
			t.Errorf("brain %s: prompt = %q, want it to start with %q", id, got, want)
		}
	}
	// The default prompt file, inside the folder, goes through the folder.
	plant(t, filepath.Join(home, "in", "brain-prompt.md"), shared)
	br := mgr.Get("in")
	if got := br.buildPrompt(br.settings.Load(), dm("hello")); strings.Contains(got, "SHARED PROMPT") {
		t.Errorf("an inline brain's prompt in its folder followed a symlink:\n%s", got)
	}
}
