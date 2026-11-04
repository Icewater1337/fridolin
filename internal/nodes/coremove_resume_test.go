package nodes

import (
	"testing"
	"time"
)

// A core that starts with a move whose core.release was sent (#494)
// follows it, as it does a handed-off one: marking it failed told the
// owner the move failed while the core ran on the target.
func TestResumeMovesFollowsAReleasingMove(t *testing.T) {
	dir := t.TempDir()
	first, err := New(Options{Dir: dir, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	id := "m-20261024-1200-beef"
	first.saveMoveLocked(&MoveRecord{ID: id, State: MoveReleasing, Started: time.Now()})
	first.Stop()

	s, err := New(Options{Dir: dir, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	if got := s.MoveActive(); got != id {
		t.Fatalf("the move is followed: active %q, want %q", got, id)
	}
	if r, err := s.Move(id); err != nil || r.State != MoveReleasing {
		t.Fatalf("state %q (%v), want %q", r.State, err, MoveReleasing)
	}
}
