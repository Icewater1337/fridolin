package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestNotes: a note is found by its message id until it is deleted or
// pruned.
func TestNotes(t *testing.T) {
	t.Parallel()
	s := openTestStore(t, filepath.Join(t.TempDir(), "bridge.db"))
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	n := Note{MsgID: "3EB0A", Chat: "15551234567@s.whatsapp.net", Brain: "core", Ref: "errand-question:e1", History: "❓ left out", CreatedAt: now}
	if err := s.SaveNote(ctx, n); err != nil {
		t.Fatal(err)
	}
	got, err := s.NoteOf(ctx, "3EB0A")
	if err != nil || !got.CreatedAt.Equal(n.CreatedAt) {
		t.Fatalf("note: %+v %v, want %+v", got, err, n)
	}
	if got.CreatedAt = n.CreatedAt; got != n {
		t.Errorf("note: %+v, want %+v", got, n)
	}
	if _, err := s.NoteOf(ctx, "3EB0B"); !errors.Is(err, ErrNoNote) {
		t.Errorf("no note: %v", err)
	}
	old := Note{MsgID: "3EB0OLD", Chat: n.Chat, Ref: "errand:e0", History: "✅ done", CreatedAt: now.Add(-NoteKeep - time.Hour)}
	if err := s.SaveNote(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneNotes(ctx, now.Add(-NoteKeep)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NoteOf(ctx, "3EB0OLD"); !errors.Is(err, ErrNoNote) {
		t.Errorf("a note older than NoteKeep stayed: %v", err)
	}
	if err := s.DeleteNote(ctx, "3EB0A"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NoteOf(ctx, "3EB0A"); !errors.Is(err, ErrNoNote) {
		t.Errorf("a deleted note stayed: %v", err)
	}
}
