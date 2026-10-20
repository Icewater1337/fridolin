package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// TestResolveChecksRolesAndGrants: one core brain, grants only on it and
// only on brains that are configured. A brain that breaks a rule is left
// out with an issue that says which; the brains before it keep running.
func TestResolveChecksRolesAndGrants(t *testing.T) {
	core := func(id string, grants map[string][]string) *manifest.Manifest {
		return &manifest.Manifest{ID: id, Name: id, Role: manifest.RoleCore, Grants: grants}
	}
	for _, tc := range []struct {
		name     string
		brains   []*manifest.Manifest
		runs     []string
		detail   string // the one issue's, "" for none
		issuesOn string
	}{
		{"core granting a sub-brain", []*manifest.Manifest{core("core", map[string][]string{"family": {"delegate"}}), {ID: "family", Name: "Family"}}, []string{"core", "family"}, "", ""},
		{"grant on a sub-brain configured after the core", []*manifest.Manifest{{ID: "family", Name: "Family"}, core("core", map[string][]string{"family": {"read"}})}, []string{"family", "core"}, "", ""},
		{"two cores", []*manifest.Manifest{core("core", nil), core("other", nil)}, []string{"core"}, `are both the core brain`, "other"},
		{"grants on a sub-brain", []*manifest.Manifest{core("core", nil), {ID: "family", Name: "Family", Grants: map[string][]string{"core": {"read"}}}}, []string{"core"}, "only the core brain", "family"},
		{"grant on a brain not configured", []*manifest.Manifest{core("core", map[string][]string{"taxes": {"delegate"}}), {ID: "family", Name: "Family"}}, []string{"family"}, `grants access to brain "taxes", which is not configured`, "core"},
		{"grant on itself", []*manifest.Manifest{core("core", map[string][]string{"core": {"delegate"}})}, nil, "cannot grant itself", "core"},
		{"unknown grant", []*manifest.Manifest{core("core", map[string][]string{"family": {"admin"}}), {ID: "family", Name: "Family"}}, []string{"family"}, `grant "admin"`, "core"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := fsx.Canonical(t.TempDir())
			t.Parallel()
			var refs []config.BrainRef
			for _, m := range tc.brains {
				dir := filepath.Join(home, m.ID)
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				refs = append(refs, config.BrainRef{Path: dir, Approved: m})
			}
			specs, issues := Resolve(refs, allowResolutionDir)
			var ran []string
			for _, s := range specs {
				ran = append(ran, s.Manifest.ID)
			}
			if strings.Join(ran, ",") != strings.Join(tc.runs, ",") {
				t.Errorf("running %v, want %v", ran, tc.runs)
			}
			switch {
			case tc.detail == "" && len(issues) != 0:
				t.Errorf("issues = %+v, want none", issues)
			case tc.detail != "" && (len(issues) != 1 || issues[0].ID != tc.issuesOn || issues[0].Kind != IssueInvalid || !strings.Contains(issues[0].Detail, tc.detail)):
				t.Errorf("issues = %+v, want one invalid on %s saying %q", issues, tc.issuesOn, tc.detail)
			}
		})
	}
}

// TestGrantsGoThroughTheApprovalFlow: grants are part of brain.json, so
// a brain that edits its own grants (or makes itself the core) on disk
// changes nothing until you approve the edit in the dashboard, and an
// edit there that grants a brain that does not exist is refused.
// Removing a brain takes it out of the core's grants too.
func TestGrantsGoThroughTheApprovalFlow(t *testing.T) {
	t.Parallel()
	g, home := testBrainRegistry(t)
	for _, id := range []string{"core", "family", "taxes"} {
		m := manifest.Manifest{ID: id, Name: id}
		if id == "core" {
			m.Role = manifest.RoleCore
		}
		if _, err := g.Add(AddInput{Path: filepath.Join(home, id), CreateDir: true, Manifest: m}); err != nil {
			t.Fatal(err)
		}
	}
	approved := func(id string) manifest.Manifest {
		t.Helper()
		for _, ref := range g.store.Get().BrainDirs {
			if ref.Approved != nil && ref.Approved.ID == id {
				return *ref.Approved
			}
		}
		t.Fatalf("no approved brain %s", id)
		return manifest.Manifest{}
	}

	// The core grants itself more on disk, as a manipulated run could.
	disk, err := manifest.Read(filepath.Join(home, "core"))
	if err != nil {
		t.Fatal(err)
	}
	edited := *disk
	edited.Grants = map[string][]string{"family": {"delegate", "read"}, "taxes": {"files"}}
	writeRaw := func(dir string, m manifest.Manifest) {
		t.Helper()
		data, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, manifest.File), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeRaw(filepath.Join(home, "core"), edited)
	if got := approved("core"); len(got.Grants) != 0 {
		t.Fatalf("a grant written to disk took effect without approval: %v", got.Grants)
	}
	_, issues := g.resolve(g.store.Get())
	if len(issues) != 1 || issues[0].ID != "core" || issues[0].Kind != IssueDrift {
		t.Fatalf("issues = %+v, want the core's drift", issues)
	}
	// A sub-brain that makes itself a core is drift too, not a second core.
	sub, _ := manifest.Read(filepath.Join(home, "family"))
	promoted := *sub
	promoted.Role = manifest.RoleCore
	writeRaw(filepath.Join(home, "family"), promoted)
	if approved("family").IsCore() {
		t.Fatal("a sub-brain made itself the core without approval")
	}
	if err := g.Approve("family", promoted); err == nil || !strings.Contains(err.Error(), "both the core brain") {
		t.Fatalf("approving a second core: %v", err)
	}
	writeRaw(filepath.Join(home, "family"), *sub)

	if err := g.Approve("core", edited); err != nil {
		t.Fatal(err)
	}
	if got := approved("core"); !got.Granted("family", manifest.GrantDelegate) || !got.Granted("taxes", manifest.GrantFiles) {
		t.Fatalf("approved grants = %v", got.Grants)
	}

	// The dashboard's edit refuses a grant on a brain that is not there.
	bad := approved("core")
	bad.Grants = map[string][]string{"nobody": {"read"}}
	if err := g.Update("core", bad); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("update granting a missing brain: %v", err)
	}
	// ... and on a sub-brain.
	subEdit := approved("family")
	subEdit.Grants = map[string][]string{"taxes": {"read"}}
	if err := g.Update("family", subEdit); err == nil || !strings.Contains(err.Error(), "only the core brain") {
		t.Fatalf("update granting on a sub-brain: %v", err)
	}

	if err := g.Remove("taxes"); err != nil {
		t.Fatal(err)
	}
	got := approved("core")
	if _, ok := got.Grants["taxes"]; ok || !got.Granted("family", manifest.GrantRead) {
		t.Fatalf("after removing taxes the core grants %v", got.Grants)
	}
	if disk, err := manifest.Read(filepath.Join(home, "core")); err != nil || !disk.Equal(got) {
		t.Fatalf("brain.json after the remove: %+v, %v", disk, err)
	}
	if _, issues := g.resolve(g.store.Get()); len(issues) != 0 {
		t.Fatalf("issues after the remove: %+v", issues)
	}
}
