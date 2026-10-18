package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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
	cfg, home := brainASetup(t)
	mgr := newTestManager(t, cfg)
	t.Cleanup(mgr.Stop)
	br := mgr.Get("a")
	if br == nil {
		t.Fatalf("brain a did not load: %v", mgr.Issues())
	}
	return br, home
}

// brainASetup sets up what folderBrain runs, and returns its
// configuration and home.
func brainASetup(t *testing.T) (*Config, string) {
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
	return cfg, home
}

// bareBrain is brain a in a fresh folder, outside any manager.
func bareBrain(t testing.TB) *Brain {
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
	dirs := brainDirsJSON(t,
		brainFolder(t, "~/a", BrainManifest{ID: "a", Routing: BrainRouting{HandlesDMs: true, Default: true}}),
		brainFolder(t, "~/b", BrainManifest{ID: "b", Routing: BrainRouting{Groups: []string{"120363000000000000@g.us"}}}),
	)
	locked := filepath.Join(home, "b")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	cfg, err := LoadConfig(writeConfig(t, `{
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
	if br := mgr.BrainFor("120363000000000000@g.us", true); br != mgr.Get("a") {
		t.Errorf("b's group went to %v, want the default brain a", br)
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

// TestReplacedFolderIsOpenedOnReload: the bridge goes through the folder
// it opened, not the path. When you move a brain's folder aside and put
// another at its path, a restored copy say, the next reload switches the
// brain to the new folder; with the folder unchanged, the brain runs on.
func TestReplacedFolderIsOpenedOnReload(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replace bool
	}{{"folder unchanged", false}, {"folder replaced", true}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, home := brainASetup(t)
			mgr := newTestManager(t, cfg)
			t.Cleanup(mgr.Stop)
			before := mgr.Get("a")
			dir := filepath.Join(home, "a")
			want := "OLD FOLDER"
			if err := os.WriteFile(filepath.Join(dir, "brain-prompt.md"), []byte(want+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.replace {
				want = "NEW FOLDER"
				if err := os.Rename(dir, dir+".old"); err != nil {
					t.Fatal(err)
				}
				manifest, err := os.ReadFile(filepath.Join(dir+".old", ManifestFile))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				for name, data := range map[string]string{ManifestFile: string(manifest), "brain-prompt.md": want + "\n"} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			logged := captureLog(t)
			if err := mgr.Apply(cfg); err != nil {
				t.Fatal(err)
			}

			br := mgr.Get("a")
			if br == nil || (br != before) != tc.replace {
				t.Fatalf("brain a made anew: %v, want %v", br != before, tc.replace)
			}
			if got := br.buildPrompt(br.settings.Load(), dm("hello")); !strings.HasPrefix(got, want) {
				t.Errorf("prompt = %q, want it to start with %q", got, want)
			}
			br.appendHistory("15551234567@s.whatsapp.net", "15551234567", "AFTER-RELOAD", historyIn)
			if data, _ := os.ReadFile(filepath.Join(dir, historyFileName)); !strings.Contains(string(data), "AFTER-RELOAD") {
				t.Errorf("history at %s = %q, want the message", dir, data)
			}
			lines := logged()
			if !tc.replace {
				if len(lines) > 0 {
					t.Errorf("unexpected log %q", lines)
				}
				return
			}
			if data, _ := os.ReadFile(filepath.Join(dir+".old", historyFileName)); strings.Contains(string(data), "AFTER-RELOAD") {
				t.Errorf("the message went to the folder moved aside: %q", data)
			}
			if len(lines) != 1 || !strings.Contains(lines[0], "brain a: now in the folder at "+dir+"; stopping it in the old one") {
				t.Errorf("log = %q, want one line switching folders", lines)
			}
		})
	}
}

// TestHistoryIsNotFollowedOutOfTheFolder: the bridge appends every message
// to the brain's history and reads it back for the chat-history preface,
// the one-time-code guard and the dashboard's activity feed. A history
// swapped for a symlink, to another brain's history say, is neither
// written to nor read through; nor is a missing file created at a
// dangling symlink's target.
func TestHistoryIsNotFollowedOutOfTheFolder(t *testing.T) {
	// The code request is recent, so that it arms the guard.
	asked := time.Now().Add(-time.Minute).Format(time.RFC3339)
	otherLines := `{"ts":"2026-01-01T10:00:00Z","from":"15551234567","chat":"15551234567@s.whatsapp.net","text":"OTHER-BRAIN-SECRET","type":"in"}
{"ts":"` + asked + `","from":"a","chat":"15551234567@s.whatsapp.net","text":"🔐 send me the code","type":"out"}
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

			br.appendHistory("15551234567@s.whatsapp.net", "15551234567", "NEW-MESSAGE", historyIn)
			preface := br.buildPrompt(br.settings.Load(), dm("hello"))
			_, drops := br.catchesCode(dm("123456"), time.Now())
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
					t.Errorf("code caught = %v, activity = %q; want the code caught and three entries", drops, seeded)
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
				t.Errorf("read through the symlink: preface %q, code caught %v, activity %q", preface, drops, seeded)
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
			startViewer = func(_, streamLog, _ string, _ []string) (*TmuxViewer, error) {
				started = append(started, streamLog)
				return nil, errors.New("no tmux in tests")
			}
			t.Cleanup(func() { startViewer = prev })
			logged := captureLog(t)

			br.ensureViewer(&brainSettings{viewer: true})
			viewerIdle(t, br)

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
			if res := br.parseStream(Inbound{JID: "x"}, strings.NewReader(stream)); res.Text != "done" {
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
			br.appendHistory("15551234567@s.whatsapp.net", "15551234567", "hi", historyIn)
			logged := captureLog(t)
			preface := br.buildPrompt(br.settings.Load(), dm("hello"))
			lines := logged()
			if tc.target == nil {
				if !strings.Contains(preface, `"from":"Alex (15551234567)","text":"hi"`) || len(lines) > 0 {
					t.Errorf("preface = %q, log = %q; want the name from the map and no log", preface, lines)
				}
				return
			}
			// The starter prompt names Alex too, in an example.
			if history := promptBlock(preface, "chat_history"); strings.Contains(history, "Alex") || !strings.Contains(history, `"from":"15551234567","text":"hi"`) {
				t.Errorf("preface = %q, want the raw number", preface)
			}
			if len(lines) != 1 || !strings.Contains(lines[0], "brain a: could not read identity map "+identityMapFile+": "+errSymlink.Error()) {
				t.Errorf("log = %q, want one line refusing the symlink", lines)
			}
		})
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
			plant(t, filepath.Join(home, "a", ManifestFile), filepath.Join(home, "b", ManifestFile))
		}, errSymlink.Error()},
		{"symlink inside the folder", func(t *testing.T, home string) {
			plant(t, filepath.Join(home, "a", ManifestFile), "copy.json")
		}, errSymlink.Error()},
		{"FIFO", func(t *testing.T, home string) {
			p := filepath.Join(home, "a", ManifestFile)
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
				if err := WriteManifest(dir, &other, false); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Rename(filepath.Join(home, "a", "copy", ManifestFile), filepath.Join(home, "a", "copy.json")); err != nil {
				t.Fatal(err)
			}
			tc.plant(t, home)

			type resolved struct {
				specs  []BrainSpec
				issues []BrainIssue
				err    error
			}
			done := make(chan resolved, 1)
			go func() {
				_, err := ReadManifest(filepath.Join(home, "a"))
				specs, issues := ResolveBrains(cfg)
				done <- resolved{specs, issues, err}
			}()
			var got resolved
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("reading brain.json hangs")
			}
			if got.err == nil || !strings.Contains(got.err.Error(), filepath.Join(home, "a", ManifestFile)+": "+tc.want) {
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

// TestBrainFilesAreReadUpToALimit: a brain can make its prompt and its
// identity map as large as it likes, and the bridge reads both for every
// message. It reads up to maxBrainFile and treats a larger file as
// missing, rather than load it all into memory.
func TestBrainFilesAreReadUpToALimit(t *testing.T) {
	cases := []struct {
		name string
		size int
		ok   bool
	}{
		{"at the limit", maxBrainFile, true},
		{"over the limit", maxBrainFile + 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			br, home := folderBrain(t)
			dir := filepath.Join(home, "a")
			// Each file is tc.size bytes, what to look for first.
			const prompt, table = "BIG PROMPT\n", "| 15551234567 | Alex | |\n"
			for name, head := range map[string]string{"brain-prompt.md": prompt, identityMapFile: table} {
				data := head + strings.Repeat(" ", tc.size-len(head))
				if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			br.appendHistory("15551234567@s.whatsapp.net", "15551234567", "hi", historyIn)
			logged := captureLog(t)
			got := br.buildPrompt(br.settings.Load(), dm("hello"))
			lines := logged()
			if tc.ok {
				if !strings.HasPrefix(got, "BIG PROMPT") || !strings.Contains(got, `"from":"Alex (15551234567)","text":"hi"`) || len(lines) > 0 {
					t.Errorf("prompt starts %.40q, log = %q; want both files read", got, lines)
				}
				return
			}
			if strings.Contains(got, "BIG PROMPT") || strings.Contains(got, "Alex") {
				t.Errorf("read a file over the limit; prompt starts %.40q", got)
			}
			// The prompt is read first, then the map for the preface.
			want := []string{
				"brain a: could not read prompt file brain-prompt.md: larger than 256 KiB",
				"brain a: could not read identity map " + identityMapFile + ": larger than 256 KiB",
			}
			if len(lines) != 2 || !strings.HasSuffix(lines[0], want[0]) || !strings.HasSuffix(lines[1], want[1]) {
				t.Errorf("log = %q, want %q", lines, want)
			}
		})
	}
}
