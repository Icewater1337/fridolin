package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/registry"
	"github.com/Icewater1337/fridolin/internal/route"
)

func TestBrainManagerOpenFailureReservesRoutesAndDeniedFolders(t *testing.T) {
	const group = "120363000000000000@g.us"
	base := t.TempDir()
	active := manifest.Spec{Dir: filepath.Join(base, "active"), Manifest: manifest.Manifest{ID: "active", Routing: manifest.Routing{Groups: []string{group}}}}
	missing := manifest.Spec{Dir: filepath.Join(base, "missing"), Path: filepath.Join(base, "alias"), Manifest: manifest.Manifest{ID: "missing", Routing: manifest.Routing{Default: true}}}
	if err := os.Mkdir(active.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	mgr := newBrainManager(brainManagerOptions{})
	t.Cleanup(mgr.Stop)
	var denied map[string][]string
	options := brainApplyOptions{SetDirs: func(dirs map[string][]string) {
		denied = dirs
		if mgr.Get("active") != nil {
			t.Fatal("published brains before their folder denies were updated")
		}
		if mgr.reloadMu.TryLock() {
			mgr.reloadMu.Unlock()
			t.Fatal("folder callback ran outside the reload lock")
		}
	}}
	failures, err := mgr.Apply([]manifest.Spec{active, missing}, brainGlobals{}, options)
	if err != nil || len(failures) != 1 || failures[0].Spec.Manifest.ID != "missing" || !os.IsNotExist(failures[0].Err) {
		t.Fatalf("apply = %+v, %v; want the private missing-root failure", failures, err)
	}
	if want := []string{missing.Path, missing.Dir}; !reflect.DeepEqual(denied["missing"], want) {
		t.Fatalf("failed root's denied folders = %v, want %v", denied["missing"], want)
	}
	if mgr.BrainFor(group, true) != mgr.Get("active") || mgr.HeldFor(group, true) != "" {
		t.Fatal("a held default displaced an explicit runnable group")
	}
	if mgr.DMBrain() != nil || mgr.Fallback() != nil || mgr.HeldFor("dm", false) != "missing" || mgr.BrainFor("other", true) != nil {
		t.Fatal("unrelated runnable brain took the unavailable explicit default's routes")
	}
	if err := os.Mkdir(missing.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	unchanged := mgr.Get("active")
	if failures, err := mgr.Apply([]manifest.Spec{active, missing}, brainGlobals{}, brainApplyOptions{}); err != nil || len(failures) != 0 {
		t.Fatalf("restore = %+v, %v", failures, err)
	}
	if mgr.Get("active") != unchanged || mgr.DMBrain() != mgr.Get("missing") || mgr.HeldFor("dm", false) != "" {
		t.Fatal("restoring the default lost existing runtime identity or retained its hold")
	}
}

func TestBrainManagerRemovedAndReaddedIDUsesNewRuntime(t *testing.T) {
	sp := manifest.Spec{Dir: t.TempDir(), Manifest: manifest.Manifest{ID: "a", Routing: manifest.Routing{HandlesDMs: true}}}
	mgr := newBrainManager(brainManagerOptions{})
	t.Cleanup(mgr.Stop)
	apply := func(specs []manifest.Spec, held []route.Held) {
		t.Helper()
		if failures, err := mgr.Apply(specs, brainGlobals{}, brainApplyOptions{Held: held}); err != nil || len(failures) != 0 {
			t.Fatalf("apply = %+v, %v", failures, err)
		}
	}
	apply([]manifest.Spec{sp}, nil)
	old := mgr.Get("a")
	apply(nil, []route.Held{{ID: "a", Routing: sp.Manifest.Routing}})
	if mgr.Get("a") != nil || mgr.DMBrain() != nil || mgr.HeldFor("dm", false) != "a" {
		t.Fatal("removed brain remained routable")
	}
	mgr.retiring.Wait()
	if _, err := old.root.Stat("."); err == nil {
		t.Fatal("removed runtime retained its open root")
	}
	apply([]manifest.Spec{sp}, nil)
	current := mgr.Get("a")
	if current == nil || current == old || mgr.DMBrain() != current || mgr.HeldFor("dm", false) != "" {
		t.Fatal("readded ID reused the retired runtime or kept its old held route")
	}
	if _, _, err := old.Submit(dm("retired")); err != errBrainRetired {
		t.Fatalf("retired runtime accepted a message after its ID was readded: %v", err)
	}
}

func TestBrainManagerCommandKeepsDiagnosticsOutsideRuntime(t *testing.T) {
	cfg := &config.Config{}
	mgr := newCommandBrainManager(cfg)
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
