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
	on, err := s.FrideThreadOn(ctx, "abc", 7)
	if err != nil || on.ID != id || on.TaskID != "health" || on.Rationale != "/ws/health.md" {
		t.Fatalf("on: %+v %v", on, err)
	}
	if _, err := s.FrideThreadOn(ctx, "abc", 8); !errors.Is(err, ErrNoFrideThread) {
		t.Fatalf("another thread: %v", err)
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
