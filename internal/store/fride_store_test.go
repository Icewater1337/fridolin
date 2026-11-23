package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestFrideThreads(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "bridge.db"))
	ctx := context.Background()
	id, err := s.AddFrideThread(ctx, FrideThread{Chat: "15551234567@s.whatsapp.net", Brain: "core", Message: "add a health endpoint", Repos: []string{"api"}, WantNode: "laptop"})
	if err != nil || id != 1 {
		t.Fatalf("add: %d %v", id, err)
	}
	other, _ := s.AddFrideThread(ctx, FrideThread{Chat: "15551234567@s.whatsapp.net", Message: "docs"})
	got, err := s.GetFrideThread(ctx, id)
	if err != nil || got.State != FrideQueued || got.Repos[0] != "api" || got.WantNode != "laptop" || got.CreatedAt.IsZero() {
		t.Fatalf("get: %+v %v", got, err)
	}
	if queued, _ := s.FrideThreads(ctx, FrideQueued); len(queued) != 2 {
		t.Fatalf("queued: %d", len(queued))
	}
	got.Node, got.FrideThread, got.State, got.TaskID, got.Branch, got.Rationale = "abc", 7, FrideSent, "health", "feat/health", "/ws/health.md"
	if err := s.SaveFrideThread(ctx, got); err != nil {
		t.Fatal(err)
	}
	on, err := s.FrideThreadOn(ctx, "abc", "", 7)
	if err != nil || on.ID != id || on.TaskID != "health" || on.Rationale != "/ws/health.md" || on.Instance != "" {
		t.Fatalf("on: %+v %v", on, err)
	}
	if _, err := s.FrideThreadOn(ctx, "abc", "", 8); !errors.Is(err, ErrNoFrideThread) {
		t.Fatalf("another thread: %v", err)
	}
	if _, err := s.FrideThreadOn(ctx, "abc", "work", 7); !errors.Is(err, ErrNoFrideThread) {
		t.Fatalf("the same thread number in an instance: %v", err)
	}
	if _, err := s.GetFrideThread(ctx, 99); !errors.Is(err, ErrNoFrideThread) {
		t.Fatalf("no such thread: %v", err)
	}
	if err := s.SaveFrideThread(ctx, FrideThread{ID: 99}); !errors.Is(err, ErrNoFrideThread) {
		t.Fatalf("saving a thread that is not there: %v", err)
	}
	all, _ := s.FrideThreads(ctx, "")
	if len(all) != 2 || all[1].ID != other {
		t.Fatalf("all: %+v", all)
	}
}

// A thread on a FrIDE instance is found by its node, its instance and
// FrIDE's thread there; a save keeps its instance.
func TestFrideThreadsOnInstances(t *testing.T) {
	t.Parallel()
	s := openTestStore(t, filepath.Join(t.TempDir(), "bridge.db"))
	ctx := context.Background()
	native, err := s.AddFrideThread(ctx, FrideThread{Chat: "15551234567@s.whatsapp.net", Message: "native"})
	if err != nil {
		t.Fatal(err)
	}
	work, err := s.AddFrideThread(ctx, FrideThread{Chat: "15551234567@s.whatsapp.net", Message: "work", Instance: "work"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id       int64
		instance string
	}{{native, ""}, {work, "work"}} {
		got, err := s.GetFrideThread(ctx, c.id)
		if err != nil || got.Instance != c.instance {
			t.Fatalf("thread %d: %+v %v", c.id, got, err)
		}
		got.Node, got.FrideThread, got.State, got.Instance = "mini", 1, FrideSent, "changed"
		if err := s.SaveFrideThread(ctx, got); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		instance string
		want     int64
	}{{"", native}, {"work", work}} {
		got, err := s.FrideThreadOn(ctx, "mini", c.instance, 1)
		if err != nil || got.ID != c.want || got.Instance != c.instance {
			t.Errorf("thread 1 of %q on mini: %+v %v", c.instance, got, err)
		}
	}
	if _, err := s.FrideThreadOn(ctx, "mini", "changed", 1); !errors.Is(err, ErrNoFrideThread) {
		t.Errorf("a save changed the instance: %v", err)
	}
}

func TestFrideOrphans(t *testing.T) {
	s, ctx := openTestStore(t, filepath.Join(t.TempDir(), "bridge.db")), context.Background()
	for _, o := range []FrideOrphan{{Thread: 3, Node: "abc", TaskID: "health"}, {Thread: 4, Node: "abc", Instance: "work", FrideThread: 9}} {
		if err := s.AddFrideOrphan(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.FrideOrphans(ctx)
	if err != nil || len(got) != 2 || got[0].TaskID != "health" || got[1].Instance != "work" || got[1].FrideThread != 9 || got[0].CreatedAt.IsZero() {
		t.Fatalf("FrideOrphans = %+v, %v", got, err)
	}
	if err := s.DeleteFrideOrphan(ctx, got[0].ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FrideOrphans(ctx); len(got) != 1 || got[0].Thread != 4 {
		t.Errorf("after delete: %+v", got)
	}
}
