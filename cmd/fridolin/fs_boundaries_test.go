package main

import (
	"encoding/json"
	"github.com/Icewater1337/fridolin/internal/registry"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestScaffoldRefusesEscapingParentSymlinks(t *testing.T) {
	for _, sub := range []string{"skills", "memories/people", "prompts"} {
		t.Run(sub, func(t *testing.T) {
			dir, outside := t.TempDir(), t.TempDir()
			link := filepath.Join(dir, sub)
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			m := testManifest("example")
			if sub == "prompts" {
				m.PromptFile = "prompts/new/prompt.md"
			}
			if _, err := registry.Scaffold(dir, &m, brainTemplates); err == nil {
				t.Error("scaffolding through an escaping directory symlink succeeded")
			}
			entries, err := os.ReadDir(outside)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("scaffolding created files or folders outside the brain: %v", entries)
			}
		})
	}
}

func TestScaffoldKeepsInternalParentSymlinks(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"skills", "memories", "prompts"} {
		if err := os.Mkdir(filepath.Join(dir, "local-"+sub), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("local-"+sub, filepath.Join(dir, sub)); err != nil {
			t.Fatal(err)
		}
	}
	m := testManifest("example")
	m.PromptFile = "prompts/custom.md"
	for i := range 2 {
		created, err := registry.Scaffold(dir, &m, brainTemplates)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 && len(created) != 0 {
			t.Fatalf("a repeated scaffold created %v", created)
		}
	}
	for _, rel := range []string{"local-skills/README.md", "local-memories/people/whatsapp-identity-map.md", "local-prompts/custom.md"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("internal symlink %s: %v", rel, err)
		}
	}
}

func TestAddBrainRefusesEscapingScaffold(t *testing.T) {
	tb := newTestBridge(t, "")
	dir, outside := filepath.Join(tb.home, "candidate"), t.TempDir()
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "skills")); err != nil {
		t.Fatal(err)
	}
	w := tb.do("POST", "/api/brains", map[string]any{"path": dir, "manifest": testManifest("candidate")})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("add returned %d: %s", w.Code, w.Body.String())
	}
	if len(tb.cfgs.Get().BrainDirs) != 0 {
		t.Fatal("the rejected folder was registered")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside folder changed: %v (%v)", entries, err)
	}
}

func TestFSListKeepsMetadataInsideHome(t *testing.T) {
	tb := newTestBridge(t, "")
	outside := t.TempDir()
	for _, dir := range []string{filepath.Join(outside, ".git"), filepath.Join(tb.home, "inside", ".git"), filepath.Join(tb.home, "markers")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		"outside-link": outside,
		"inside-link":  filepath.Join(tb.home, "inside"),
		"data-link":    tb.cfgs.Get().DataDir,
		"markers/.git": filepath.Join(outside, ".git"),
	} {
		if err := os.Symlink(target, filepath.Join(tb.home, link)); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{outside, filepath.Join(tb.home, "outside-link")} {
		w := tb.do("GET", "/api/fs/list?path="+url.QueryEscape(path), nil)
		if w.Code != http.StatusForbidden {
			t.Fatalf("browsing outside home returned %d: %s", w.Code, w.Body.String())
		}
	}
	w := tb.do("GET", "/api/fs/list?hidden=1&path="+url.QueryEscape(tb.home), nil)
	var got struct {
		Entries []struct {
			Name  string
			IsGit bool `json:"is_git"`
		}
	}
	if w.Code != http.StatusOK {
		t.Fatalf("list returned %d: %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range got.Entries {
		seen[e.Name] = true
		switch e.Name {
		case "outside-link", "data-link":
			t.Errorf("listed forbidden child %s", e.Name)
		case "inside", "inside-link":
			if !e.IsGit {
				t.Errorf("lost metadata for allowed child %s", e.Name)
			}
		case "markers":
			if e.IsGit {
				t.Error("followed a marker symlink outside home")
			}
		}
	}
	for _, name := range []string{"inside", "inside-link", "markers"} {
		if !seen[name] {
			t.Errorf("missing allowed child %s", name)
		}
	}
}

func TestInspectKeepsMarkerMetadataInsideHome(t *testing.T) {
	tb := newTestBridge(t, "")
	out, in := t.TempDir(), filepath.Join(tb.home, "inside")
	for _, dir := range []string{out, in} {
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, target string
		want         bool
	}{
		{"outside", out, false},
		{"inside", in, true},
	} {
		dir := filepath.Join(tb.home, "inspect-"+tc.name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(tc.target, ".git"), filepath.Join(dir, ".git")); err != nil {
			t.Fatal(err)
		}
		w := tb.do("POST", "/api/brains/inspect", map[string]string{"path": dir})
		var got struct {
			IsGit bool `json:"is_git"`
		}
		if w.Code != http.StatusOK {
			t.Fatalf("inspect returned %d: %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.IsGit != tc.want {
			t.Errorf("%s marker: is_git=%v, want %v", tc.name, got.IsGit, tc.want)
		}
	}
}
