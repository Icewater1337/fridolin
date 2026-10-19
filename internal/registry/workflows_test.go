package registry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// Workflow tests supply every environmental input and can run concurrently.
func testBrainRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	home := fsx.Canonical(t.TempDir())
	g := New(nil, Options{
		Home:        func() string { return home },
		CheckDir:    func(string, string, *config.File, string) error { return nil },
		HistoryFile: "history.md",
		Templates:   Templates{Prompt: "Test prompt", IdentityMapPath: "identity-map.md"},
	})
	cs, err := config.Open(filepath.Join(home, "config.json"), config.Options{
		Paths: config.Paths{Home: home, DefaultDataDir: filepath.Join(home, "data")},
		GOOS:  "linux", Getenv: func(string) string { return "" }, CheckBrains: g.CheckSet,
	})
	if err != nil {
		t.Fatal(err)
	}
	g.store = cs
	return g, home
}

func TestRegistryWorkflowsKeepApprovalAndAllowlistingAtomic(t *testing.T) {
	t.Parallel()
	g, home := testBrainRegistry(t)
	add := func(id string) AddResult {
		t.Helper()
		result, err := g.Add(AddInput{Path: filepath.Join(home, id), CreateDir: true,
			Manifest: manifest.Manifest{ID: id, Name: id}})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first, second := add("first"), add("second")
	const group = "120363000000000000@g.us"
	if err := g.AllowChat(false, group, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := g.AllowChat(false, group, second.ID); err != nil {
		t.Fatal(err)
	}
	diskFirst, err := manifest.Read(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	diskSecond, err := manifest.Read(second.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(diskFirst.Routing.Groups) != 0 || len(diskSecond.Routing.Groups) != 1 || diskSecond.Routing.Groups[0] != group {
		t.Fatalf("group reassignment did not reach both manifests: %#v / %#v", diskFirst.Routing, diskSecond.Routing)
	}
	before := g.store.Snapshot()
	err = g.AllowChat(false, "120363000000000001@g.us", "missing")
	if apperr.KindOf(err) != apperr.NotFound || err.Error() != `no brain "missing"` {
		t.Fatalf("missing target: %v", err)
	}
	after := g.store.Snapshot()
	if after.Version != before.Version || after.ETag != before.ETag {
		t.Fatal("failed target published an allowlist edit")
	}

	reviewed := *diskSecond
	changed := reviewed
	changed.Name = "Changed after review"
	data, err := json.MarshalIndent(changed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second.Path, manifest.File), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := g.Approve(second.ID, reviewed); !errors.Is(err, ErrReviewedManifestChanged) {
		t.Fatalf("stale approval: %v", err)
	}
	if g.store.Snapshot().Version != before.Version {
		t.Fatal("stale approval published config")
	}
	if err := g.Approve(second.ID, changed); err != nil {
		t.Fatal(err)
	}
	if got := g.store.Get().BrainDirs[1].Approved.Name; got != changed.Name {
		t.Fatalf("approved name: %q", got)
	}
	if err := g.UpdateBackup(second.ID, nil, []string{" *.tmp ", "", " cache/ "}); err != nil {
		t.Fatal(err)
	}
	if got := g.store.Get().BrainDirs[1].Approved.Backup.Exclude; strings.Join(got, ",") != "*.tmp,cache/" {
		t.Fatalf("backup exclusions: %v", got)
	}
	if err := g.Remove(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.Read(first.Path); err != nil {
		t.Fatalf("remove touched folder: %v", err)
	}
}

func TestRegistryInspectStopsAtRefusedPath(t *testing.T) {
	t.Parallel()
	g, home := testBrainRegistry(t)
	called := 0
	g.options.CheckDir = func(string, string, *config.File, string) error {
		called++
		return errors.New("protected test folder")
	}
	got, err := g.Inspect(filepath.Join(home, "blocked"))
	if err != nil {
		t.Fatal(err)
	}
	if called != 1 || len(got.Problems) == 0 {
		t.Fatalf("path checks: %#v, calls=%d", got, called)
	}
	if got.Exists != nil || got.IsDir != nil || got.Entries != nil || got.Manifest != nil || got.Suggested != nil {
		t.Errorf("refused path exposed metadata: %+v", got)
	}
}

// inHome gives back a folder below the home folder as it was, built from
// the home folder, and refuses the home folder and everything else.
func TestInHome(t *testing.T) {
	t.Parallel()
	home := fsx.Canonical(t.TempDir())
	g := New(nil, Options{Home: func() string { return home }})
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
			got, ok := g.inHome(c.path)
			if got != c.want || ok != (c.want != "") {
				t.Fatalf("inHome(%q) = %q, %v; want %q", c.path, got, ok, c.want)
			}
		})
	}
	t.Run("no home folder", func(t *testing.T) {
		g := New(nil, Options{Home: func() string { return "" }})
		if got, ok := g.inHome("/x"); ok {
			t.Fatalf("inHome with no home folder = %q", got)
		}
	})
}

// The name suggested for a new brain's folder starts with a capital, also
// when its first letter takes more than one byte.
func TestPrettyName(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"":             "Brain",
		"personal":     "Personal",
		"work_notes":   "Work notes",
		"éclair-notes": "Éclair notes",
		"ölkanne":      "Ölkanne",
		"日記":           "日記",
	} {
		if got := prettyName(in); got != want {
			t.Errorf("prettyName(%q) = %q, want %q", in, got, want)
		}
	}
}
