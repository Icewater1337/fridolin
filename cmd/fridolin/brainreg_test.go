package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/registry"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/textx"
)

func testManifest(id string) manifest.Manifest {
	return manifest.Manifest{ID: id, Name: strings.ToUpper(id[:1]) + id[1:], Backup: manifest.Backup{Enabled: true, Exclude: manifest.DefaultBackupExcludes()}}
}

func TestCheckBrainPath(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	cfg := &config.Config{DataDir: filepath.Join(home, ".fridolin")}
	others := []string{filepath.Join(home, "brains", "medical")}

	cases := []struct {
		path string
		ok   bool
	}{
		{"~/brains/family", true},
		{"~/brains/medical-study", true}, // a sibling with a shared prefix is fine
		{"~", false},
		{"/", false},
		{"/var/tmp/x", false},
		{"~/brains/medical/sub", false}, // inside another brain
		{"~/brains", false},             // contains another brain
		{"~/.fridolin/x", false},
		{"relative/path", false},
		// What the sandbox keeps from brains: a brain may write its own
		// folder, so one inside ~/.claude/skills writes what your own
		// sessions load, one at ~/.ssh opens your keys.
		{"~/.claude/skills/x", false},
		{"~/.ssh", false},
		{"~/.config", false}, // holds ~/.config/gh
		{"~/Library/LaunchAgents/x", false},
		{"~/Library/Keychains/x", false},
		{"~/creds/x", false},
		{"~/linked/new", false}, // ~/linked is a symlink out of HOME
		{"~/Documents/assistant", true},
	}
	cfg.Sandbox.CredentialsDir = filepath.Join(home, "creds")
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A folder the dashboard is to create is checked where MkdirAll would
	// create it: through the symlink, out of HOME.
	if err := os.Symlink(fsx.Canonical(t.TempDir()), filepath.Join(home, "linked")); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		_, problems, _ := CheckBrainPath(c.path, cfg, others)
		if (len(problems) == 0) != c.ok {
			t.Errorf("CheckBrainPath(%q): problems=%v, want ok=%v", c.path, problems, c.ok)
		}
	}
	// macOS guards ~/Documents from the bridge itself, not the sandbox.
	if _, _, warnings := CheckBrainPath("~/Documents/assistant", cfg, others); len(warnings) != 1 || !strings.Contains(warnings[0], "Privacy & Security") {
		t.Errorf("warnings for ~/Documents = %q, want one naming the privacy settings", warnings)
	}
}

// TestResolveBrainsRefusesFoldersTheSandboxProtects: the dashboard refuses
// a brain folder the sandbox could not keep apart, but brain_dirs can be
// edited by hand, and a folder can change after it was added. ResolveBrains
// used to accept any of them, and the brain's profile then opened what the
// folder held: a brain at your home folder read the data dir and ~/.ssh.
// Now each is left out with an issue, and the others still run.
func TestResolveBrainsRefusesFoldersTheSandboxProtects(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "elsewhere"), 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, path string
		want       string // in the issue's detail, "" for a brain that runs
	}{
		{"your home folder", "~", "your home folder"},
		{"a folder holding the data folder", "~/box", "holds the bridge's data folder"},
		{"inside ~/.claude", "~/.claude/skills/x", "inside your Claude Code folder"},
		{"your ssh keys", "~/.ssh", "is ~/.ssh"},
		{"inside LaunchAgents", "~/Library/LaunchAgents/x", "inside ~/Library/LaunchAgents"},
		// The bridge takes ~/tools/bin off its PATH instead.
		{"a folder holding one on the bridge's PATH", "~/tools", ""},
		{"a folder of its own", "~/assistant/a", ""},
	}
	t.Setenv("PATH", home+"/tools/bin:/usr/bin:/bin")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref := brainFolder(t, tc.path, manifest.Manifest{ID: "x", Routing: manifest.Routing{HandlesDMs: true}})
			other := brainFolder(t, "~/elsewhere/y", manifest.Manifest{ID: "y"})
			// x second: the dashboard shows an issue on the entry it
			// names, which was the first one for these.
			cfg := &config.Config{DataDir: filepath.Join(home, "box", ".fridolin"), BrainDirs: []config.BrainRef{other, ref}}
			for i := range cfg.BrainDirs {
				cfg.BrainDirs[i].Path = fsx.ExpandHome(cfg.BrainDirs[i].Path, home)
			}
			specs, issues := ResolveBrains(cfg)
			var ids []string
			for _, s := range specs {
				ids = append(ids, s.Manifest.ID)
			}
			if tc.want == "" {
				if !slices.Equal(ids, []string{"y", "x"}) || len(issues) != 0 {
					t.Errorf("brains %v, issues %+v; want both to run", ids, issues)
				}
				return
			}
			if !slices.Equal(ids, []string{"y"}) {
				t.Errorf("brains %v, want only y", ids)
			}
			if len(issues) != 1 || issues[0].ID != "x" || issues[0].Kind != "invalid" || !strings.Contains(issues[0].Detail, tc.want) || issues[0].Entry != 1 {
				t.Errorf("issues = %+v, want one for x, on its entry 1, saying %q", issues, tc.want)
			}
		})
	}
}

// inHome gives back a folder below the home folder as it was, built from
// the home folder, and refuses the home folder and everything else.
func TestInHome(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	cases := []struct {
		name, path, want string
	}{
		{"a folder", filepath.Join(home, "brains", "work"), filepath.Join(home, "brains", "work")},
		{"a name starting with dots", filepath.Join(home, "..work"), filepath.Join(home, "..work")},
		{"untidy", home + "/brains//work/", filepath.Join(home, "brains", "work")},
		{"the home folder", home, ""},
		{"out and back in", home + "/brains/../../" + filepath.Base(home) + "/x", filepath.Join(home, "x")},
		{"out of it", home + "/../elsewhere", ""},
		{"a sibling with its name as a prefix", home + "-other/x", ""},
		{"the root", "/", ""},
		{"relative", "brains/work", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := inHome(c.path)
			if got != c.want || ok != (c.want != "") {
				t.Fatalf("inHome(%q) = %q, %v; want %q", c.path, got, ok, c.want)
			}
		})
	}
	t.Run("no home folder", func(t *testing.T) {
		t.Setenv("HOME", "")
		if got, ok := inHome("/x"); ok {
			t.Fatalf("inHome with no home folder = %q", got)
		}
	})
}

func TestScaffoldBrainNeverOverwrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "brain")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "brain-prompt.md"), []byte("mine"), 0o600)
	m := testManifest("fam")
	created, err := registry.Scaffold(dir, &m, brainTemplates)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "brain-prompt.md")); string(data) != "mine" {
		t.Error("an existing prompt was overwritten")
	}
	if !slices.Contains(created, manifest.File) || !slices.Contains(created, "memories/people/whatsapp-identity-map.md") {
		t.Errorf("created = %v", created)
	}
	if slices.Contains(created, "brain-prompt.md") {
		t.Error("brain-prompt.md reported as created although it existed")
	}
	// A second scaffold creates nothing and keeps the manifest.
	again, err := registry.Scaffold(dir, &m, brainTemplates)
	if err != nil || len(again) != 0 {
		t.Errorf("second scaffold created %v (err %v)", again, err)
	}
}

// TestScaffoldedPromptSaysWhatItIs: a new brain's prompt is sent ahead of
// each message, but it is not the brain's whole system prompt: Claude Code
// adds its own and loads the folder's CLAUDE.md and .claude/. A brain told
// otherwise looks for its instructions in the wrong place.
func TestScaffoldedPromptSaysWhatItIs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "brain")
	m := testManifest("fam")
	if _, err := registry.Scaffold(dir, &m, brainTemplates); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "brain-prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	prompt := strings.Join(strings.Fields(string(data)), " ")
	cases := []struct {
		phrase string
		want   bool
	}{
		{"This file is your system prompt", false},
		{"so everything you need to know", false},
		{"sent ahead of every message you receive, so most of what you need to know", true},
		{"not your whole system prompt: Claude Code adds its own", true},
		{"loads the `CLAUDE.md` and the `.claude/` folder in this directory", true},
	}
	for _, tc := range cases {
		t.Run(tc.phrase, func(t *testing.T) {
			if got := strings.Contains(prompt, tc.phrase); got != tc.want {
				t.Errorf("starter prompt has %q: %v, want %v", tc.phrase, got, tc.want)
			}
		})
	}
}

// FuzzDecodeManifest: the brain can write its own brain.json, so it can
// hold anything. What decodes is normalised, and what also validates is
// safe to use: the id is a plain file name, the prompt file stays in the
// folder, the backup excludes become nothing but exclude rules for rclone,
// and the calendar tool gets exactly the calendars listed. It survives
// being written and read back unchanged; what does not validate is not
// written. (ReadManifest and WriteManifest add only the file around these.)
func FuzzDecodeManifest(f *testing.F) {
	full := testManifest("work")
	full.Routing = manifest.Routing{HandlesDMs: true, Groups: []string{"120363000000000000@g.us"}}
	full.Effort, full.Model, full.PromptFile, full.ReplyChat = "high", "opus", "prompts/main.md", "120363000000000000@g.us"
	full.DisallowedTools = &[]string{}
	full.Calendar = &manifest.Calendar{Account: "you@example.com", Calendars: []string{"primary", "team"}, Default: "primary"}
	data, err := json.Marshal(full)
	if err != nil {
		f.Fatal(err)
	}
	for _, s := range []string{
		string(data),
		`{"id":"a","name":"A"}`,
		`{"version":7,"id":"a","name":"A","prompt_file":".","backup":{"exclude":["- x/","+ y","a\nb","",null]},"calendar":{"calendars":[]}}`,
		`{"id":"a","name":"A","prompt_file":"../x","routing":{"groups":["15551234567@s.whatsapp.net"]}}`,
		`{"id":"Work","name":" "}`,
		`{"id":"a","name":"A","calendar":{"calendars":["a,b"]}}`,
		`{"id":"a","name":"A","disallowed_tools":null,"calendar":null}`,
		`{}`, `null`, `[]`, `{"version":1.5}`, `{"id":"a\u0000","name":"\ud800"}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := manifest.Decode(data)
		if err != nil {
			return
		}
		if n := m.Normalized(); !reflect.DeepEqual(n, *m) {
			t.Errorf("decoded %+v, normalised %+v", *m, n)
		}
		out, err := manifest.Encode(m)
		if m.Validate() != nil {
			if err == nil {
				t.Errorf("encoded %+v, which does not validate", *m)
			}
			return
		}
		if err != nil {
			t.Fatalf("encoding %+v: %v", *m, err)
		}

		if len(m.ID) > 40 || filepath.Base(m.ID) != m.ID || m.ID == "." || m.ID == ".." || strings.ContainsAny(m.ID, "\x00\\") {
			t.Errorf("id %q is not a plain file name", m.ID)
		}
		if !filepath.IsLocal(m.PromptFile) {
			t.Errorf("prompt file %q is not inside the folder", m.PromptFile)
		}
		for _, line := range strings.SplitAfter(filterLines(m.Backup.Exclude), "\n") {
			if line != "" && (!strings.HasPrefix(line, "- ") || !strings.HasSuffix(line, "\n")) {
				t.Errorf("excludes %q gave the filter line %q", m.Backup.Exclude, line)
			}
		}
		if c := m.Calendar; c != nil && len(c.Calendars) > 0 {
			for _, kv := range calendarEnv(m.ID, *m, "/creds") {
				if v, ok := strings.CutPrefix(kv, "BRIDGE_CALENDARS="); ok && !reflect.DeepEqual(strings.Split(v, ","), c.Calendars) {
					t.Errorf("calendars %q reach the tool as %q", c.Calendars, v)
				}
			}
		}

		back, err := manifest.Decode(out)
		if err != nil {
			t.Fatalf("decoding %s again: %v", out, err)
		}
		if !reflect.DeepEqual(*back, *m) || !back.Equal(*m) {
			t.Errorf("encoded %+v, decoded back %+v", *m, *back)
		}
	})
}

// The templates a new brain is scaffolded from are the files in examples/,
// as they are in the checkout: all the skills, in name order, and the
// prompt with its placeholder.
func TestEmbeddedTemplatesAreTheExamples(t *testing.T) {
	prompt, err := os.ReadFile(filepath.Join(repoDir, "examples", "brain-prompt.example.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got := brainTemplates.Prompt; got != string(prompt) || !strings.Contains(got, "{{BRAIN_NAME}}") {
		t.Errorf("the prompt template is %q, want examples/brain-prompt.example.md with {{BRAIN_NAME}}", textx.Truncate(got, 80))
	}
	files, err := filepath.Glob(filepath.Join(repoDir, "examples", "skills", "*.md"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no skills in examples/skills: %v", err)
	}
	skills := brainTemplates.Skills
	if len(skills) != len(files) {
		t.Fatalf("%d embedded skills, want the %d in examples/skills", len(skills), len(files))
	}
	for i, f := range files { // Glob sorts too
		want, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if skills[i].Name != filepath.Base(f) || skills[i].Content != string(want) {
			t.Errorf("skill %d is %s, want %s as in the checkout", i, skills[i].Name, filepath.Base(f))
		}
	}
}

// The brain.json in docs/BRAINS.md is the one example of a manifest, so it
// must be one this version accepts as it stands.
func TestBrainsDocExampleIsAValidManifest(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join(repoDir, "docs", "BRAINS.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(doc), "\n## brain.json\n")
	_, block, ok2 := strings.Cut(section, "```json\n")
	block, _, ok3 := strings.Cut(block, "```")
	if !ok || !ok2 || !ok3 {
		t.Fatal("docs/BRAINS.md has no json block under ## brain.json")
	}
	dec := json.NewDecoder(strings.NewReader(block))
	dec.DisallowUnknownFields()
	var m manifest.Manifest
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("the example does not decode as brain.json: %v", err)
	}
	m = m.Normalized()
	if err := m.Validate(); err != nil {
		t.Errorf("the example is not a valid manifest: %v", err)
	}
}

// TestBrainTemplatesNameNoPaths: the starter prompt and the example skills
// a new brain gets name the bridge's commands and helpers by the variables
// every run sets, not by a path, and keep no placeholder of the template.
// Older versions wrote the checkout's path into the prompt, and a literal
// <bridge-repo> into the skills.
func TestBrainTemplatesNameNoPaths(t *testing.T) {
	m := testManifest("a")
	m.Name = "Family"
	exe, _ := os.Executable()
	files := map[string]string{"brain-prompt.md": strings.ReplaceAll(brainTemplates.Prompt, "{{BRAIN_NAME}}", m.Name)}
	for _, sk := range brainTemplates.Skills {
		files["skills/"+sk.Name] = strings.ReplaceAll(sk.Content, "{{BRAIN_NAME}}", m.Name)
	}
	if len(files) < 4 {
		t.Fatalf("only %d templates", len(files))
	}
	for name, content := range files {
		// <work_dir> and ~/.fridolin/media were never filled in either: the
		// brain runs in its folder, and the message gives each file's path.
		for _, bad := range []string{"{{", "}}", "<bridge-repo>", "<work_dir>", "~/.fridolin", "tools/history_search.py", "tools/poll_results.py", "tools/calendar_tool.py", filepath.Dir(exe), repoDir} {
			if strings.Contains(content, bad) {
				t.Errorf("%s contains %q", name, bad)
			}
		}
		// A helper is "$FRIDOLIN_TOOLS_DIR/<name>.py", wherever the bridge
		// is installed, never a tools/ path.
		if strings.Contains(content, "tools/") {
			t.Errorf("%s names a helper by a tools/ path", name)
		}
	}
	if p := files["brain-prompt.md"]; !strings.HasPrefix(p, "# Family assistant") || !strings.Contains(p, `"$FRIDOLIN_BIN" history`) || !strings.Contains(p, `"$FRIDOLIN_BIN" polls`) || !strings.Contains(p, `"$FRIDOLIN_BIN" calendar`) {
		t.Errorf("brain-prompt.md does not name the brain or the commands:\n%s", p)
	}
	if s := files["skills/transcription.md"]; !strings.Contains(s, `"$FRIDOLIN_TOOLS_DIR/transcribe.py"`) || !strings.Contains(s, "--move-original") {
		t.Errorf("skills/transcription.md does not run the helper by $FRIDOLIN_TOOLS_DIR, moving the original out of the inbox:\n%s", s)
	}

	// ScaffoldBrain writes them so.
	dir := t.TempDir()
	if _, err := registry.Scaffold(dir, &m, brainTemplates); err != nil {
		t.Fatal(err)
	}
	for name, want := range files {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != want {
			t.Errorf("%s as written differs from the rendered template (%v)", name, err)
		}
	}
}

// TestLeftOutBrainsKeepTheirChats: a brain left out (its folder missing or
// one that cannot hold a brain) used to hand its chats to the default
// brain: with the DM brain's folder refused, your DMs, and the Plaud
// recordings sent as DMs, went to the work brain. The chats of a brain
// left out now go to no brain, check says whose they are, and the chat is
// told the brain cannot run; chats no brain left out claims still go to
// the default brain. A default brain left out keeps what it would take.
func TestLeftOutBrainsKeepTheirChats(t *testing.T) {
	const otherGroup = "120363000000000009@g.us"
	cases := []struct {
		name            string
		brains          func(home string) []config.BrainRef
		dm, group, rest string // who takes a DM, testGroup and otherGroup; "!id" for held by brain id
	}{
		{"every brain runs", func(home string) []config.BrainRef {
			return []config.BrainRef{
				brainFolder(t, home+"/personal", manifest.Manifest{ID: "personal", Routing: manifest.Routing{HandlesDMs: true}}),
				brainFolder(t, home+"/team", manifest.Manifest{ID: "team", Routing: manifest.Routing{Groups: []string{testGroup}}}),
				brainFolder(t, home+"/work", manifest.Manifest{ID: "work", Routing: manifest.Routing{Default: true}}),
			}
		}, "personal", "team", "work"},
		{"the DM brain's folder cannot hold a brain", func(home string) []config.BrainRef {
			return []config.BrainRef{
				brainFolder(t, home+"/data/personal", manifest.Manifest{ID: "personal", Routing: manifest.Routing{HandlesDMs: true}}),
				brainFolder(t, home+"/team", manifest.Manifest{ID: "team", Routing: manifest.Routing{Groups: []string{testGroup}}}),
				brainFolder(t, home+"/work", manifest.Manifest{ID: "work", Routing: manifest.Routing{Default: true}}),
			}
		}, "!personal", "team", "work"},
		{"a group brain's folder is gone", func(home string) []config.BrainRef {
			return []config.BrainRef{
				brainFolder(t, home+"/personal", manifest.Manifest{ID: "personal", Routing: manifest.Routing{HandlesDMs: true}}),
				{Path: home + "/gone", Approved: &manifest.Manifest{ID: "team", Name: "team", Routing: manifest.Routing{Groups: []string{testGroup}}}},
				brainFolder(t, home+"/work", manifest.Manifest{ID: "work", Routing: manifest.Routing{Default: true}}),
			}
		}, "personal", "!team", "work"},
		{"the default brain's folder is gone", func(home string) []config.BrainRef {
			return []config.BrainRef{
				brainFolder(t, home+"/team", manifest.Manifest{ID: "team", Routing: manifest.Routing{Groups: []string{testGroup}}}),
				{Path: home + "/gone", Approved: &manifest.Manifest{ID: "work", Name: "work", Routing: manifest.Routing{Default: true}}},
			}
		}, "!work", "team", "!work"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := fsx.Canonical(t.TempDir())
			t.Setenv("HOME", home)
			path := writeConfig(t, `{"brain_mode": true, "data_dir": "`+home+`/data", "claude_path": "`+home+`/no-claude", "allowed_groups": ["`+testGroup+`", "`+otherGroup+`"]`+brainDirsJSON(t, c.brains(home)...)+`}`)
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			mgr := newTestManager(t, cfg)
			t.Cleanup(mgr.Stop)
			var report bytes.Buffer
			_ = checkReport(&report, path, cfg, checkOptions{routes: []string{testDMChat, testGroup, otherGroup}})
			for jid, want := range map[string]string{testDMChat: c.dm, testGroup: c.group, otherGroup: c.rest} {
				isGroup := chat.IsGroupJID(jid)
				got := ""
				if br := mgr.BrainFor(jid, isGroup); br != nil {
					got = br.ID()
				} else if id := mgr.HeldFor(jid, isGroup); id != "" {
					got = "!" + id
				}
				if got != want {
					t.Errorf("%s goes to %q, want %q", jid, got, want)
				}
				line := "route " + jid + " → " + want
				if id, held := strings.CutPrefix(want, "!"); held {
					line = "route " + jid + " → no brain: " + id + " handles it but cannot run"
				}
				if !strings.Contains(report.String(), line) {
					t.Errorf("check lacks %q:\n%s", line, report.String())
				}
			}
		})
	}
	t.Run("Plaud recordings wait", func(t *testing.T) {
		home := fsx.Canonical(t.TempDir())
		t.Setenv("HOME", home)
		cfg, err := LoadConfig(writeConfig(t, `{"brain_mode": true, "data_dir": "`+home+`/data", "allowed_numbers": ["15551234567"]`+brainDirsJSON(t,
			config.BrainRef{Path: home + "/gone", Approved: &manifest.Manifest{ID: "personal", Name: "personal", Routing: manifest.Routing{HandlesDMs: true}}},
			brainFolder(t, home+"/work", manifest.Manifest{ID: "work", Routing: manifest.Routing{Default: true}}),
		)+`}`))
		if err != nil {
			t.Fatal(err)
		}
		mgr := newTestManager(t, cfg)
		t.Cleanup(mgr.Stop)
		br, _, err := (&PlaudManager{brains: mgr}).destination(cfg)
		if br != nil || err == nil || !strings.Contains(err.Error(), "brain personal, which gets Plaud recordings, cannot run") {
			t.Errorf("recordings go to %v (%v), want them to wait for brain personal", br, err)
		}
	})
	t.Run("the chat is told", func(t *testing.T) {
		brains := fsx.Canonical(t.TempDir())
		tb := newTestBridge(t, brainDirsJSON(t,
			config.BrainRef{Path: brains + "/personal", Approved: &manifest.Manifest{ID: "personal", Name: "personal", Routing: manifest.Routing{HandlesDMs: true}}},
			brainFolder(t, brains+"/work", manifest.Manifest{ID: "work", Routing: manifest.Routing{Default: true}}),
		))
		tb.handleInbound(t.Context(), Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "hi", Source: chat.SourceWhatsApp}})
		sent := tb.sentTo(testDMChat)
		if len(sent) != 1 || !strings.Contains(sent[0], "cannot run right now") {
			t.Errorf("the chat got %q, want to hear its brain cannot run", sent)
		}
		if h := readHistory(t, tb.brainMgr.Get("work")); len(h) != 0 {
			t.Errorf("brain work got the DM: %+v", h)
		}
	})
}
