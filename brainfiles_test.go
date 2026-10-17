package main

import (
	"encoding/json"
	"errors"
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

// bareBrain is brain a in a fresh folder, outside any manager.
func bareBrain(t *testing.T) *Brain {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return &Brain{id: "a", workDir: dir, root: root}
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

// TestHistoryIsNotFollowedOutOfTheFolder: the bridge appends every message
// to the brain's history and reads it back for the chat-history preface,
// the one-time-code guard and the dashboard's activity feed. A history
// swapped for a symlink, to another brain's history say, is neither
// written to nor read through; nor is a missing file created at a
// dangling symlink's target.
func TestHistoryIsNotFollowedOutOfTheFolder(t *testing.T) {
	const otherLines = `{"ts":"2026-01-01T10:00:00Z","from":"15551234567","chat":"15551234567@s.whatsapp.net","text":"OTHER-BRAIN-SECRET","type":"in"}
{"ts":"2026-01-01T10:01:00Z","from":"a","chat":"15551234567@s.whatsapp.net","text":"🔐 send me the code","type":"out"}
`
	cases := []struct {
		name   string
		target func(home string) string // what the history points to; "" for a regular file
	}{
		{"regular file", nil},
		{"another brain's history", func(home string) string { return filepath.Join(home, "b", historyFileName) }},
		{"relative symlink up and out", func(string) string { return filepath.Join("..", "b", historyFileName) }},
		{"symlink inside the folder", func(string) string { return "notes.jsonl" }},
		{"dangling symlink out", func(home string) string { return filepath.Join(home, ".ssh", "authorized_keys") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			br, home := folderBrain(t)
			s := *br.settings.Load()
			s.otpGuard = true
			br.settings.Store(&s)
			dir := filepath.Join(home, "a")
			hist := filepath.Join(dir, historyFileName)
			for _, p := range []string{filepath.Join(home, "b", historyFileName), filepath.Join(dir, "notes.jsonl")} {
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(otherLines), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.target == nil {
				if err := os.WriteFile(hist, []byte(otherLines), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				plant(t, hist, tc.target(home))
			}
			logged := captureLog(t)

			br.appendHistory("15551234567@s.whatsapp.net", "15551234567", "NEW-MESSAGE", "in")
			preface := br.composePreface("15551234567@s.whatsapp.net", false)
			drops := br.DropsOTP("123456")
			feed := NewActivity(10)
			feed.Seed(br)
			var seeded []string
			for _, e := range feed.Recent(10, "") {
				seeded = append(seeded, e.Text)
			}

			if tc.target == nil {
				// The control: all of it works on a regular file.
				data, _ := os.ReadFile(hist)
				if !strings.HasSuffix(string(data), `"text":"NEW-MESSAGE","type":"in"}`+"\n") {
					t.Errorf("history = %s, want the new message appended", data)
				}
				if !strings.Contains(preface, "OTHER-BRAIN-SECRET") || !strings.Contains(preface, "NEW-MESSAGE") {
					t.Errorf("preface = %q, want the history", preface)
				}
				if !drops || len(seeded) != 3 {
					t.Errorf("DropsOTP = %v, activity = %q; want the code dropped and three entries", drops, seeded)
				}
				if lines := logged(); len(lines) > 0 {
					t.Errorf("unexpected log %q", lines)
				}
				return
			}
			for _, p := range []string{filepath.Join(home, "b", historyFileName), filepath.Join(dir, "notes.jsonl")} {
				if data, _ := os.ReadFile(p); string(data) != otherLines {
					t.Errorf("%s was written through the symlink:\n%s", p, data)
				}
			}
			if _, err := os.Lstat(filepath.Join(home, ".ssh", "authorized_keys")); !os.IsNotExist(err) {
				t.Errorf("the dangling symlink's target was created (%v)", err)
			}
			if fi, err := os.Lstat(hist); err != nil || fi.Mode()&os.ModeSymlink == 0 {
				t.Errorf("the history symlink was replaced (%v)", err)
			}
			if strings.Contains(preface, "OTHER-BRAIN-SECRET") || drops || len(seeded) > 0 {
				t.Errorf("read through the symlink: preface %q, DropsOTP %v, activity %q", preface, drops, seeded)
			}
			lines := logged()
			if len(lines) != 1 || !strings.Contains(lines[0], "brain a: failed to open history file "+hist+": "+errSymlink.Error()) {
				t.Errorf("log = %q, want one line refusing the symlink", lines)
			}
		})
	}
}

// TestViewerCreatesTheStreamLogInTheFolder: the activity viewer tails the
// stream log, so the bridge creates the log first when there is none. It
// does so through the folder, as a run opens the log: a dangling symlink
// there does not get the file it points to created, and a log the bridge
// refuses gets no viewer.
func TestViewerCreatesTheStreamLogInTheFolder(t *testing.T) {
	cases := []struct {
		name   string
		target string // what the log points to, in a folder outside; "" for no log yet
	}{
		{"no log yet", ""},
		{"symlink to a file outside", "id_test"},
		{"dangling symlink out", "authorized_keys"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			br := bareBrain(t)
			outside := t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "id_test"), []byte("FAKE-PRIVATE-KEY\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(br.workDir, streamLogName)
			if tc.target != "" {
				plant(t, logPath, filepath.Join(outside, tc.target))
			}
			var started []string
			prev := startViewer
			startViewer = func(_, streamLog, _ string) (*TmuxViewer, error) {
				started = append(started, streamLog)
				return nil, errors.New("no tmux in tests")
			}
			t.Cleanup(func() { startViewer = prev })
			logged := captureLog(t)

			br.ensureViewer(&brainSettings{viewer: true})

			if tc.target == "" {
				if len(started) != 1 || started[0] != logPath {
					t.Errorf("viewer started on %q, want %s", started, logPath)
				}
				if fi, err := os.Lstat(logPath); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
					t.Errorf("stream log: %v, %v; want a new 0600 file", fi, err)
				}
				return
			}
			if len(started) > 0 {
				t.Errorf("viewer started on %q, want none", started)
			}
			if _, err := os.Lstat(filepath.Join(outside, "authorized_keys")); !os.IsNotExist(err) {
				t.Errorf("the dangling symlink's target was created (%v)", err)
			}
			if lines := logged(); len(lines) != 1 || !strings.Contains(lines[0], "brain a: no tmux viewer: could not open stream log: "+errSymlink.Error()) {
				t.Errorf("log = %q, want one line refusing the symlink", lines)
			}
		})
	}
}

// TestStreamLogIsNotFollowedOutOfTheFolder: every run appends what the
// brain did to claude-stream.log. Swapped for a symlink, the log is not
// written through, so a brain cannot have the bridge write to a file its
// sandbox keeps it from writing; the run itself goes on.
func TestStreamLogIsNotFollowedOutOfTheFolder(t *testing.T) {
	const stream = `{"type":"result","result":"done"}` + "\n"
	cases := []struct {
		name   string
		target func(home string) string // what the log points to; nil for none yet
	}{
		{"no log yet", nil},
		{"symlink to a file outside", func(home string) string { return filepath.Join(home, ".ssh", "id_test") }},
		{"symlink inside the folder", func(string) string { return "notes.md" }},
		{"dangling symlink out", func(home string) string { return filepath.Join(home, ".ssh", "authorized_keys") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			br, home := folderBrain(t)
			dir := filepath.Join(home, "a")
			notes := filepath.Join(dir, "notes.md")
			if err := os.WriteFile(notes, []byte("NOTES\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(dir, streamLogName)
			if tc.target != nil {
				plant(t, logPath, tc.target(home))
			}
			logged := captureLog(t)
			if res := br.parseStream("x", strings.NewReader(stream)); res.Text != "done" {
				t.Errorf("run = %+v, want it to finish", res)
			}
			if tc.target == nil {
				if data, _ := os.ReadFile(logPath); !strings.Contains(string(data), "✅ Result: done") {
					t.Errorf("stream log = %q, want the result", data)
				}
				if lines := logged(); len(lines) > 0 {
					t.Errorf("unexpected log %q", lines)
				}
				return
			}
			if data, _ := os.ReadFile(filepath.Join(home, ".ssh", "id_test")); string(data) != "FAKE-PRIVATE-KEY\n" {
				t.Errorf("the key was written through the symlink: %q", data)
			}
			if data, _ := os.ReadFile(notes); string(data) != "NOTES\n" {
				t.Errorf("notes.md was written through the symlink: %q", data)
			}
			if _, err := os.Lstat(filepath.Join(home, ".ssh", "authorized_keys")); !os.IsNotExist(err) {
				t.Errorf("the dangling symlink's target was created (%v)", err)
			}
			lines := logged()
			if len(lines) != 1 || !strings.Contains(lines[0], "brain a: could not open stream log: "+errSymlink.Error()) {
				t.Errorf("log = %q, want one line refusing the symlink", lines)
			}
		})
	}
}

// TestIdentityMapIsNotFollowedOutOfTheFolder: the identity map names the
// people in the chat-history preface. Swapped for a symlink to another
// brain's map, it would put that brain's contacts in this brain's prompt;
// the bridge shows raw numbers instead, and logs why.
func TestIdentityMapIsNotFollowedOutOfTheFolder(t *testing.T) {
	const table = "| WhatsApp Number | Person | Notes |\n| --- | --- | --- |\n| 15551234567 | Alex | |\n"
	cases := []struct {
		name   string
		target func(home string) string // what the map points to; nil for a regular file
	}{
		{"regular file", nil},
		{"another brain's map", func(home string) string { return filepath.Join(home, "b", identityMapFile) }},
		{"symlink inside the folder", func(string) string { return "other.md" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			br, home := folderBrain(t)
			mapPath := filepath.Join(home, "a", identityMapFile)
			for _, p := range []string{filepath.Join(home, "b", identityMapFile), filepath.Join(filepath.Dir(mapPath), "other.md"), mapPath} {
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(table), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.target != nil {
				plant(t, mapPath, tc.target(home))
			}
			br.appendHistory("15551234567@s.whatsapp.net", "15551234567", "hi", "in")
			logged := captureLog(t)
			preface := br.composePreface("15551234567@s.whatsapp.net", false)
			lines := logged()
			if tc.target == nil {
				if !strings.Contains(preface, "Alex (15551234567): hi") || len(lines) > 0 {
					t.Errorf("preface = %q, log = %q; want the name from the map and no log", preface, lines)
				}
				return
			}
			if strings.Contains(preface, "Alex") || !strings.Contains(preface, "15551234567: hi") {
				t.Errorf("preface = %q, want the raw number", preface)
			}
			if len(lines) != 1 || !strings.Contains(lines[0], "brain a: could not read identity map "+identityMapFile+": "+errSymlink.Error()) {
				t.Errorf("log = %q, want one line refusing the symlink", lines)
			}
		})
	}
}
