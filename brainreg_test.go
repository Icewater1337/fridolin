package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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

func TestConfinedPathRejectsSymlinkEscape(t *testing.T) {
	dir := canonicalDir(t.TempDir())
	outside := filepath.Join(t.TempDir(), "secret")
	_ = os.WriteFile(outside, []byte("x"), 0o600)
	if err := os.Symlink(outside, filepath.Join(dir, "brain-prompt.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := confinedPath(dir, "brain-prompt.md"); err == nil {
		t.Error("a prompt symlinked outside the folder must be rejected")
	}
	if _, err := confinedPath(dir, "../x.md"); err == nil {
		t.Error("a relative escape must be rejected")
	}
}

func TestCheckBrainPath(t *testing.T) {
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	cfg := &Config{DataDir: filepath.Join(home, ".whatsapp-claude-bridge")}
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
		{"~/.whatsapp-claude-bridge/x", false},
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
	if !contains(created, ManifestFile) || !contains(created, "memories/people/whatsapp-identity-map.md") {
		t.Errorf("created = %v", created)
	}
	if contains(created, "brain-prompt.md") {
		t.Error("brain-prompt.md reported as created although it existed")
	}
	// A second scaffold creates nothing and keeps the manifest.
	again, err := ScaffoldBrain(dir, &m)
	if err != nil || len(again) != 0 {
		t.Errorf("second scaffold created %v (err %v)", again, err)
	}
}

func folderBrainConfig(t *testing.T, home string, refs ...BrainRef) *Config {
	t.Helper()
	cfg := &Config{DataDir: filepath.Join(home, "data"), BrainMode: true, BrainDirs: refs}
	eff, _, err := effective(cfg)
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

func TestReadManifestMissing(t *testing.T) {
	if _, err := ReadManifest(t.TempDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want not-exist", err)
	}
}

func TestMigrateInlineBrains(t *testing.T) {
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	path := writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data",
		"brains": [
			{"name": "personal", "work_dir": "~/p", "handles_dms": true, "effort": "high"},
			{"name": "team", "work_dir": "~/t", "groups": ["120363000000000000@g.us"]}
		]
	}`)
	for _, d := range []string{"p", "t"} {
		_ = os.MkdirAll(filepath.Join(home, d), 0o700)
	}
	// team already has a different brain.json: a conflict, left alone.
	other := testManifest("other")
	if err := WriteManifest(filepath.Join(home, "t"), &other, false); err != nil {
		t.Fatal(err)
	}
	cs, err := OpenConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}

	rep, err := MigrateInlineBrains(cs, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "p", ManifestFile)); err == nil {
		t.Error("dry run wrote a manifest")
	}
	if rep.Brains[0].Action != "written" || rep.Brains[1].Action != "conflict" {
		t.Errorf("dry run report = %+v", rep.Brains)
	}

	if _, err := MigrateInlineBrains(cs, false, false); err != nil {
		t.Fatal(err)
	}
	m, err := ReadManifest(filepath.Join(home, "p"))
	if err != nil || m.ID != "personal" || !m.Routing.HandlesDMs || m.Effort != "high" {
		t.Fatalf("manifest = %+v, err %v", m, err)
	}
	file := cs.FileCopy()
	if len(file.Brains) != 1 || file.Brains[0].Name != "team" {
		t.Errorf("inline brains left = %+v, want only the conflicting team", file.Brains)
	}
	if len(file.BrainDirs) != 1 || file.BrainDirs[0].Path != "~/p" {
		t.Errorf("brain_dirs = %+v", file.BrainDirs)
	}
	specs, _ := ResolveBrains(cs.Get())
	if len(specs) != 2 {
		t.Errorf("after migration %d brains resolve, want 2", len(specs))
	}
}
