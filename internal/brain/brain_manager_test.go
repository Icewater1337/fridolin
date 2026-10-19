package brain

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/route"
)

func TestBrainManagerApplyUpdatesSharedRunLimit(t *testing.T) {
	sp := manifest.Spec{Dir: t.TempDir(), Manifest: manifest.Manifest{ID: "a"}}
	mgr := NewManager(Options{})
	t.Cleanup(mgr.Stop)
	var original *Brain
	for _, limit := range []int{2, 0, 5, 7} {
		if failures, err := mgr.Apply([]manifest.Spec{sp}, Globals{RunLimit: limit}, ApplyOptions{}); err != nil || len(failures) != 0 {
			t.Fatalf("apply limit %d = %+v, %v", limit, failures, err)
		}
		br := mgr.Get("a")
		if original == nil {
			original = br
		} else if br != original {
			t.Fatal("changing the run limit replaced the active runtime")
		}
		mgr.slots.mu.Lock()
		got := mgr.slots.limit
		mgr.slots.mu.Unlock()
		if br.slots != mgr.slots || got != limit {
			t.Fatalf("runtime shares slots = %v, limit = %d, want %d", br.slots == mgr.slots, got, limit)
		}
	}
}

func TestBrainManagerForceStopRejectsApplyAtPublication(t *testing.T) {
	initial := manifest.Spec{Dir: t.TempDir(), Manifest: manifest.Manifest{ID: "a"}}
	replacement := manifest.Spec{Dir: t.TempDir(), Manifest: manifest.Manifest{ID: "b"}}
	mgr := NewManager(Options{})
	t.Cleanup(mgr.Stop)
	if _, err := mgr.Apply([]manifest.Spec{initial}, Globals{}, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	original := mgr.Get("a")
	entered, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	defer release()
	applied := make(chan error, 1)
	go func() {
		_, err := mgr.Apply([]manifest.Spec{replacement}, Globals{}, ApplyOptions{SetDirs: func(map[string][]string) {
			close(entered)
			<-resume
		}})
		applied <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Apply did not reach its pre-publication callback")
	}
	stopped := make(chan struct{})
	go func() { mgr.KillRuns(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("force-stop waited for the blocked Apply")
	}
	release()
	if err := <-applied; !errors.Is(err, ErrStopped) {
		t.Fatalf("Apply passed its initial check before force-stop but returned %v", err)
	}
	if mgr.Get("a") != original || mgr.Get("b") != nil {
		t.Fatal("Apply published its replacement set after force-stop")
	}
	if _, _, err := original.Submit(dm("late")); !errors.Is(err, ErrRetired) {
		t.Fatalf("original runtime accepted work after force-stop: %v", err)
	}
}

func TestBrainManagerOpenFailureReservesRoutesAndDeniedFolders(t *testing.T) {
	const group = "120363000000000000@g.us"
	base := t.TempDir()
	active := manifest.Spec{Dir: filepath.Join(base, "active"), Manifest: manifest.Manifest{ID: "active", Routing: manifest.Routing{Groups: []string{group}}}}
	missing := manifest.Spec{Dir: filepath.Join(base, "missing"), Path: filepath.Join(base, "alias"), Manifest: manifest.Manifest{ID: "missing", Routing: manifest.Routing{Default: true}}}
	if err := os.Mkdir(active.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(Options{})
	t.Cleanup(mgr.Stop)
	var denied map[string][]string
	options := ApplyOptions{SetDirs: func(dirs map[string][]string) {
		denied = dirs
		if mgr.Get("active") != nil {
			t.Fatal("published brains before their folder denies were updated")
		}
		if mgr.reloadMu.TryLock() {
			mgr.reloadMu.Unlock()
			t.Fatal("folder callback ran outside the reload lock")
		}
	}}
	failures, err := mgr.Apply([]manifest.Spec{active, missing}, Globals{}, options)
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
	if failures, err := mgr.Apply([]manifest.Spec{active, missing}, Globals{}, ApplyOptions{}); err != nil || len(failures) != 0 {
		t.Fatalf("restore = %+v, %v", failures, err)
	}
	if mgr.Get("active") != unchanged || mgr.DMBrain() != mgr.Get("missing") || mgr.HeldFor("dm", false) != "" {
		t.Fatal("restoring the default lost existing runtime identity or retained its hold")
	}
}

func TestBrainManagerRemovedAndReaddedIDUsesNewRuntime(t *testing.T) {
	sp := manifest.Spec{Dir: t.TempDir(), Manifest: manifest.Manifest{ID: "a", Routing: manifest.Routing{HandlesDMs: true}}}
	mgr := NewManager(Options{})
	t.Cleanup(mgr.Stop)
	apply := func(specs []manifest.Spec, held []route.Held) {
		t.Helper()
		if failures, err := mgr.Apply(specs, Globals{}, ApplyOptions{Held: held}); err != nil || len(failures) != 0 {
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
	if _, _, err := old.Submit(dm("retired")); err != ErrRetired {
		t.Fatalf("retired runtime accepted a message after its ID was readded: %v", err)
	}
}
