package app

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/registry"
)

func TestBrainManagerCommandKeepsDiagnosticsOutsideRuntime(t *testing.T) {
	t.Parallel()
	cfg := resolvedSettings(t, &config.File{})
	mgr := newCommandBrainManager(cfg, envOf(t))
	t.Cleanup(mgr.Stop)
	sp := manifest.Spec{Dir: filepath.Join(t.TempDir(), "absent"), Entry: 3, Manifest: manifest.Manifest{ID: "missing", Routing: manifest.Routing{HandlesDMs: true}}}
	drift := registry.Issue{ID: "changed", Kind: "drift", Detail: "approved values still run"}
	if err := mgr.reload([]manifest.Spec{sp}, []registry.Issue{drift}, cfg, nil); err != nil {
		t.Fatal(err)
	}
	issues := mgr.Issues()
	if len(issues) != 2 || !reflect.DeepEqual(issues[0], drift) {
		t.Fatalf("diagnostics = %+v", issues)
	}
	missing := issues[1]
	if missing.ID != "missing" || missing.Path != sp.Dir || missing.Kind != "missing" || !missing.AtLoad || missing.Entry != sp.Entry || !missing.Routing.HandlesDMs || !missing.LeavesOut() {
		t.Fatalf("runtime open failure lost its registry meaning: %+v", missing)
	}
	if mgr.HeldFor("dm", false) != "missing" {
		t.Fatal("runtime did not reserve the private open failure's route")
	}
	// A rejected reload must preserve both the routing and the diagnostics.
	if err := mgr.reload([]manifest.Spec{sp, sp}, nil, cfg, nil); err == nil {
		t.Fatal("duplicate runtime set was accepted")
	}
	if !reflect.DeepEqual(mgr.Issues(), issues) || mgr.HeldFor("dm", false) != "missing" {
		t.Fatal("failed reload replaced diagnostics or routing")
	}
}
