package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

func resolutionManifest(id string) manifest.Manifest {
	return manifest.Manifest{ID: id, Name: strings.ToUpper(id[:1]) + id[1:], Backup: manifest.Backup{Enabled: true, Exclude: manifest.DefaultBackupExcludes()}}
}

func scaffoldResolutionFixture(dir string, m *manifest.Manifest) ([]string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return nil, manifest.Write(dir, m, false)
}

func resolutionConfig(refs ...config.BrainRef) *config.Config {
	return &config.Config{BrainDirs: refs}
}

func allowResolutionDir(string, string) error { return nil }

func TestResolveBrainsUsesApprovedManifestAndReportsDrift(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
	t.Parallel()
	dir := filepath.Join(home, "fam")
	m := resolutionManifest("fam")
	if _, err := scaffoldResolutionFixture(dir, &m); err != nil {
		t.Fatal(err)
	}
	approved := m
	cfg := resolutionConfig(config.BrainRef{Path: dir, Approved: &approved})

	specs, issues := Resolve(cfg.BrainDirs, allowResolutionDir)
	if len(specs) != 1 || len(issues) != 0 {
		t.Fatalf("specs=%v issues=%v", specs, issues)
	}

	// The brain grants itself the DMs by editing its own brain.json.
	evil := m
	evil.Routing.HandlesDMs = true
	if err := manifest.Write(dir, &evil, true); err != nil {
		t.Fatal(err)
	}
	specs, issues = Resolve(cfg.BrainDirs, allowResolutionDir)
	if len(specs) != 1 || specs[0].Manifest.Routing.HandlesDMs {
		t.Error("an unapproved brain.json change must not take effect")
	}
	if len(issues) != 1 || issues[0].Kind != "drift" {
		t.Errorf("issues = %v, want one drift", issues)
	}
}

func TestResolveBrainsSkipsMissingAndClashing(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
	t.Parallel()
	a, b := resolutionManifest("a"), resolutionManifest("b")
	a.Routing.HandlesDMs, b.Routing.HandlesDMs = true, true
	for _, m := range []manifest.Manifest{a, b} {
		m := m
		if _, err := scaffoldResolutionFixture(filepath.Join(home, m.ID), &m); err != nil {
			t.Fatal(err)
		}
	}
	gone := resolutionManifest("gone")
	cfg := resolutionConfig(
		config.BrainRef{Path: filepath.Join(home, "a"), Approved: &a},
		config.BrainRef{Path: filepath.Join(home, "b"), Approved: &b},
		config.BrainRef{Path: filepath.Join(home, "gone"), Approved: &gone},
	)
	specs, issues := Resolve(cfg.BrainDirs, allowResolutionDir)
	if len(specs) != 1 || specs[0].Manifest.ID != "a" {
		t.Errorf("specs = %v, want only a", specs)
	}
	kinds := map[string]bool{}
	for _, is := range issues {
		kinds[string(is.Kind)] = true
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
		second *manifest.Manifest
		kind   string
		detail string
	}{
		{"same id", "b", &manifest.Manifest{ID: "a", Name: "Other A"}, "invalid", `two brains use the id "a"`},
		{"second DM brain", "b", &manifest.Manifest{ID: "b", Name: "B", Routing: manifest.Routing{HandlesDMs: true}}, "invalid", "both handle DMs"},
		{"second default", "b", &manifest.Manifest{ID: "b", Name: "B", Routing: manifest.Routing{Default: true}}, "invalid", "both marked default"},
		{"group claimed twice", "b", &manifest.Manifest{ID: "b", Name: "B", Routing: manifest.Routing{Groups: []string{group}}}, "invalid", "is claimed by both"},
		{"folder inside the other brain's", "a/b", &manifest.Manifest{ID: "b", Name: "B"}, "invalid", "is inside brain folder"},
		{"the other brain's folder", "a", &manifest.Manifest{ID: "b", Name: "B"}, "invalid", `brains "a" and "b" use the same folder`},
		{"id that is not a slug", "b", &manifest.Manifest{ID: "Work.Brain", Name: "B"}, "invalid", "must be 1–40 lowercase letters"},
		{"no name", "b", &manifest.Manifest{ID: "b"}, "invalid", "needs a name"},
		{"no approved manifest", "b", nil, "unapproved", "remove this entry and add the folder again"},
		{"no folder", "gone", &manifest.Manifest{ID: "b", Name: "B"}, "missing", "folder not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := fsx.Canonical(t.TempDir())
			t.Parallel()
			first := manifest.Manifest{ID: "a", Name: "A", Routing: manifest.Routing{HandlesDMs: true, Default: true, Groups: []string{group}}}
			for _, d := range []string{"a", "a/b", "b"} {
				if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			cfg := resolutionConfig(
				config.BrainRef{Path: filepath.Join(home, "a"), Approved: &first},
				config.BrainRef{Path: filepath.Join(home, tc.dir), Approved: tc.second},
			)
			specs, issues := Resolve(cfg.BrainDirs, allowResolutionDir)
			if len(specs) != 1 || specs[0].Manifest.ID != "a" {
				t.Errorf("specs = %+v, want only a", specs)
			}
			if len(issues) != 1 || string(issues[0].Kind) != tc.kind || !strings.Contains(issues[0].Detail, tc.detail) {
				t.Errorf("issues = %+v, want one %s issue saying %q", issues, tc.kind, tc.detail)
			}
		})
	}
}
