package brain

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/platform"
	prompttext "github.com/Icewater1337/fridolin/internal/prompt"
)

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
			if len(lines) != 1 || !strings.Contains(lines[0], "brain a: could not read prompt file brain-prompt.md: "+fsx.ErrSymlink.Error()) {
				t.Errorf("log = %q, want one line refusing the symlink", lines)
			}
		})
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
			mgr, _, home := queueTestManager(t, 0, "")
			specs := []manifest.Spec{}
			for _, br := range mgr.Brains() {
				specs = append(specs, manifest.Spec{Dir: br.Dir(), PromptFile: "brain-prompt.md", Manifest: br.Manifest()})
			}
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
				manifestData, err := os.ReadFile(filepath.Join(dir+".old", manifest.File))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				for name, data := range map[string]string{manifest.File: string(manifestData), "brain-prompt.md": want + "\n"} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			logged := captureLog(t)
			if _, err := mgr.Apply(specs, Globals{Defaults: Settings{MaxPending: 100}}, ApplyOptions{}); err != nil {
				t.Fatal(err)
			}

			br := mgr.Get("a")
			if br == nil || (br != before) != tc.replace {
				t.Fatalf("brain a made anew: %v, want %v", br != before, tc.replace)
			}
			if got := br.buildPrompt(br.settings.Load(), dm("hello")); !strings.HasPrefix(got, want) {
				t.Errorf("prompt = %q, want it to start with %q", got, want)
			}
			br.AppendHistory("15551234567@s.whatsapp.net", "15551234567", "AFTER-RELOAD", history.In)
			if data, _ := os.ReadFile(filepath.Join(dir, history.FileName)); !strings.Contains(string(data), "AFTER-RELOAD") {
				t.Errorf("history at %s = %q, want the message", dir, data)
			}
			lines := logged()
			if !tc.replace {
				if len(lines) > 0 {
					t.Errorf("unexpected log %q", lines)
				}
				return
			}
			if data, _ := os.ReadFile(filepath.Join(dir+".old", history.FileName)); strings.Contains(string(data), "AFTER-RELOAD") {
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
		{"another brain's history", func(home string) string { return filepath.Join(home, "b", history.FileName) }},
		{"relative symlink up and out", func(string) string { return filepath.Join("..", "b", history.FileName) }},
		{"symlink inside the folder", func(string) string { return "notes.jsonl" }},
		{"dangling symlink out", func(home string) string { return filepath.Join(home, ".ssh", "authorized_keys") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			br, home := folderBrain(t)
			s := *br.settings.Load()
			s.OTPGuard = true
			br.settings.Store(&s)
			dir := filepath.Join(home, "a")
			hist := filepath.Join(dir, history.FileName)
			for _, p := range []string{filepath.Join(home, "b", history.FileName), filepath.Join(dir, "notes.jsonl")} {
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

			br.AppendHistory("15551234567@s.whatsapp.net", "15551234567", "NEW-MESSAGE", history.In)
			preface := br.buildPrompt(br.settings.Load(), dm("hello"))
			_, drops := br.CatchesCode(dm("123456"), time.Now())
			var seeded []string
			for _, e := range br.Tail(50, 128<<10) {
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
			for _, p := range []string{filepath.Join(home, "b", history.FileName), filepath.Join(dir, "notes.jsonl")} {
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
			if len(lines) != 1 || !strings.Contains(lines[0], "brain a: failed to open history file "+hist+": "+fsx.ErrSymlink.Error()) {
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
			logPath := filepath.Join(br.workDir, StreamLogName)
			if tc.target != "" {
				plant(t, logPath, filepath.Join(outside, tc.target))
			}
			var started []string
			br.viewerStart = func(_, streamLog, _ string, _ []string) (*platform.Viewer, error) {
				started = append(started, streamLog)
				return nil, errors.New("no tmux in tests")
			}
			logged := captureLog(t)

			br.ensureViewer(&Settings{Viewer: true})
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
			if lines := logged(); len(lines) != 1 || !strings.Contains(lines[0], "brain a: no tmux viewer: could not open stream log: "+fsx.ErrSymlink.Error()) {
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
		{"another brain's map", func(home string) string { return filepath.Join(home, "b", prompttext.IdentityMapPath) }},
		{"symlink inside the folder", func(string) string { return "other.md" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			br, home := folderBrain(t)
			mapPath := filepath.Join(home, "a", prompttext.IdentityMapPath)
			for _, p := range []string{filepath.Join(home, "b", prompttext.IdentityMapPath), filepath.Join(filepath.Dir(mapPath), "other.md"), mapPath} {
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
			br.AppendHistory("15551234567@s.whatsapp.net", "15551234567", "hi", history.In)
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
			if len(lines) != 1 || !strings.Contains(lines[0], "brain a: could not read identity map "+prompttext.IdentityMapPath+": "+fsx.ErrSymlink.Error()) {
				t.Errorf("log = %q, want one line refusing the symlink", lines)
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
		{"at the limit", fsx.MaxBrainFile, true},
		{"over the limit", fsx.MaxBrainFile + 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			br, home := folderBrain(t)
			dir := filepath.Join(home, "a")
			// Each file is tc.size bytes, what to look for first.
			const prompt, table = "BIG PROMPT\n", "| 15551234567 | Alex | |\n"
			for name, head := range map[string]string{"brain-prompt.md": prompt, prompttext.IdentityMapPath: table} {
				data := head + strings.Repeat(" ", tc.size-len(head))
				if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			br.AppendHistory("15551234567@s.whatsapp.net", "15551234567", "hi", history.In)
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
				"brain a: could not read identity map " + prompttext.IdentityMapPath + ": larger than 256 KiB",
			}
			if len(lines) != 2 || !strings.HasSuffix(lines[0], want[0]) || !strings.HasSuffix(lines[1], want[1]) {
				t.Errorf("log = %q, want %q", lines, want)
			}
		})
	}
}
