package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func testManifest(id string) BrainManifest {
	return BrainManifest{ID: id, Name: strings.ToUpper(id[:1]) + id[1:], Backup: BrainBackup{Enabled: true, Exclude: DefaultBackupExcludes}}
}

func TestManifestValidate(t *testing.T) {
	good := testManifest("work-1")
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(m *BrainManifest){
		"uppercase id":       func(m *BrainManifest) { m.ID = "Work" },
		"empty name":         func(m *BrainManifest) { m.Name = " " },
		"absolute prompt":    func(m *BrainManifest) { m.PromptFile = "/etc/passwd" },
		"escaping prompt":    func(m *BrainManifest) { m.PromptFile = "../other/brain-prompt.md" },
		"bad effort":         func(m *BrainManifest) { m.Effort = "huge" },
		"group not a group":  func(m *BrainManifest) { m.Routing.Groups = []string{"15551234567@s.whatsapp.net"} },
		"filter injection":   func(m *BrainManifest) { m.Backup.Exclude = []string{"+ /secret"} },
		"newline in exclude": func(m *BrainManifest) { m.Backup.Exclude = []string{"a\n+ b"} },
	}
	for name, mutate := range bad {
		m := testManifest("work")
		mutate(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

func TestCheckBrainPath(t *testing.T) {
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	cfg := &Config{DataDir: filepath.Join(home, ".fridolin")}
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
	}
	for _, c := range cases {
		_, problems, _ := CheckBrainPath(c.path, cfg, others)
		if (len(problems) == 0) != c.ok {
			t.Errorf("CheckBrainPath(%q): problems=%v, want ok=%v", c.path, problems, c.ok)
		}
	}
}

func TestScaffoldBrainNeverOverwrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "brain")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "brain-prompt.md"), []byte("mine"), 0o600)
	m := testManifest("fam")
	created, err := ScaffoldBrain(dir, &m)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "brain-prompt.md")); string(data) != "mine" {
		t.Error("an existing prompt was overwritten")
	}
	if !slices.Contains(created, ManifestFile) || !slices.Contains(created, "memories/people/whatsapp-identity-map.md") {
		t.Errorf("created = %v", created)
	}
	if slices.Contains(created, "brain-prompt.md") {
		t.Error("brain-prompt.md reported as created although it existed")
	}
	// A second scaffold creates nothing and keeps the manifest.
	again, err := ScaffoldBrain(dir, &m)
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
	if _, err := ScaffoldBrain(dir, &m); err != nil {
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

func folderBrainConfig(t *testing.T, home string, refs ...BrainRef) *Config {
	t.Helper()
	cfg := &Config{DataDir: filepath.Join(home, "data"), BrainDirs: refs}
	eff, err := effective(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return eff
}

func TestResolveBrainsUsesApprovedManifestAndReportsDrift(t *testing.T) {
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "fam")
	m := testManifest("fam")
	if _, err := ScaffoldBrain(dir, &m); err != nil {
		t.Fatal(err)
	}
	approved := m
	cfg := folderBrainConfig(t, home, BrainRef{Path: dir, Approved: &approved})

	specs, issues := ResolveBrains(cfg)
	if len(specs) != 1 || len(issues) != 0 {
		t.Fatalf("specs=%v issues=%v", specs, issues)
	}

	// The brain grants itself the DMs by editing its own brain.json.
	evil := m
	evil.Routing.HandlesDMs = true
	if err := WriteManifest(dir, &evil, true); err != nil {
		t.Fatal(err)
	}
	specs, issues = ResolveBrains(cfg)
	if len(specs) != 1 || specs[0].Manifest.Routing.HandlesDMs {
		t.Error("an unapproved brain.json change must not take effect")
	}
	if len(issues) != 1 || issues[0].Kind != "drift" {
		t.Errorf("issues = %v, want one drift", issues)
	}
}

func TestResolveBrainsSkipsMissingAndClashing(t *testing.T) {
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	a, b := testManifest("a"), testManifest("b")
	a.Routing.HandlesDMs, b.Routing.HandlesDMs = true, true
	for _, m := range []BrainManifest{a, b} {
		m := m
		if _, err := ScaffoldBrain(filepath.Join(home, m.ID), &m); err != nil {
			t.Fatal(err)
		}
	}
	gone := testManifest("gone")
	cfg := folderBrainConfig(t, home,
		BrainRef{Path: filepath.Join(home, "a"), Approved: &a},
		BrainRef{Path: filepath.Join(home, "b"), Approved: &b},
		BrainRef{Path: filepath.Join(home, "gone"), Approved: &gone},
	)
	specs, issues := ResolveBrains(cfg)
	if len(specs) != 1 || specs[0].Manifest.ID != "a" {
		t.Errorf("specs = %v, want only a", specs)
	}
	kinds := map[string]bool{}
	for _, is := range issues {
		kinds[is.Kind] = true
	}
	if !kinds["missing"] || !kinds["invalid"] {
		t.Errorf("issues = %v, want missing and invalid", issues)
	}
}

// TestResolveBrainsLeavesOutBadBrains: a brain that is not valid on its own,
// or clashes with an earlier one, is left out and reported; the brain before
// it keeps running.
func TestResolveBrainsLeavesOutBadBrains(t *testing.T) {
	const group = "120363000000000000@g.us"
	cases := []struct {
		name   string
		dir    string // the second brain's folder, below home
		second *BrainManifest
		kind   string
		detail string
	}{
		{"same id", "b", &BrainManifest{ID: "a", Name: "Other A"}, "invalid", `two brains use the id "a"`},
		{"second DM brain", "b", &BrainManifest{ID: "b", Name: "B", Routing: BrainRouting{HandlesDMs: true}}, "invalid", "both handle DMs"},
		{"second default", "b", &BrainManifest{ID: "b", Name: "B", Routing: BrainRouting{Default: true}}, "invalid", "both marked default"},
		{"group claimed twice", "b", &BrainManifest{ID: "b", Name: "B", Routing: BrainRouting{Groups: []string{group}}}, "invalid", "is claimed by both"},
		{"folder inside the other brain's", "a/b", &BrainManifest{ID: "b", Name: "B"}, "invalid", "is inside brain folder"},
		{"the other brain's folder", "a", &BrainManifest{ID: "b", Name: "B"}, "invalid", `brains "a" and "b" use the same folder`},
		{"id that is not a slug", "b", &BrainManifest{ID: "Work.Brain", Name: "B"}, "invalid", "must be 1–40 lowercase letters"},
		{"no name", "b", &BrainManifest{ID: "b"}, "invalid", "needs a name"},
		{"no approved manifest", "b", nil, "unapproved", "re-add this brain"},
		{"no folder", "gone", &BrainManifest{ID: "b", Name: "B"}, "missing", "folder not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := canonicalDir(t.TempDir())
			t.Setenv("HOME", home)
			first := BrainManifest{ID: "a", Name: "A", Routing: BrainRouting{HandlesDMs: true, Default: true, Groups: []string{group}}}
			for _, d := range []string{"a", "a/b", "b"} {
				if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			cfg := folderBrainConfig(t, home,
				BrainRef{Path: filepath.Join(home, "a"), Approved: &first},
				BrainRef{Path: filepath.Join(home, tc.dir), Approved: tc.second},
			)
			specs, issues := ResolveBrains(cfg)
			if len(specs) != 1 || specs[0].Manifest.ID != "a" {
				t.Errorf("specs = %+v, want only a", specs)
			}
			if len(issues) != 1 || issues[0].Kind != tc.kind || !strings.Contains(issues[0].Detail, tc.detail) {
				t.Errorf("issues = %+v, want one %s issue saying %q", issues, tc.kind, tc.detail)
			}
		})
	}
}

func TestReadManifestMissing(t *testing.T) {
	if _, err := ReadManifest(t.TempDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want not-exist", err)
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
	full.Routing = BrainRouting{HandlesDMs: true, Groups: []string{"120363000000000000@g.us"}}
	full.Effort, full.Model, full.PromptFile, full.ReplyChat = "high", "opus", "prompts/main.md", "120363000000000000@g.us"
	full.DisallowedTools = &[]string{}
	full.Calendar = &BrainCalendar{Account: "you@example.com", Calendars: []string{"primary", "team"}, Default: "primary"}
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
		m, err := decodeManifest(data)
		if err != nil {
			return
		}
		if n := m.normalized(); !reflect.DeepEqual(n, *m) {
			t.Errorf("decoded %+v, normalised %+v", *m, n)
		}
		out, err := encodeManifest(m)
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

		back, err := decodeManifest(out)
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
	if got := examplePromptTemplate(); got != string(prompt) || !strings.Contains(got, "{{BRAIN_NAME}}") {
		t.Errorf("the prompt template is %q, want examples/brain-prompt.example.md with {{BRAIN_NAME}}", truncate(got, 80))
	}
	files, err := filepath.Glob(filepath.Join(repoDir, "examples", "skills", "*.md"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no skills in examples/skills: %v", err)
	}
	skills := exampleSkills()
	if len(skills) != len(files) {
		t.Fatalf("%d embedded skills, want the %d in examples/skills", len(skills), len(files))
	}
	for i, f := range files { // Glob sorts too
		want, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if skills[i].name != filepath.Base(f) || skills[i].content != string(want) {
			t.Errorf("skill %d is %s, want %s as in the checkout", i, skills[i].name, filepath.Base(f))
		}
	}
}
