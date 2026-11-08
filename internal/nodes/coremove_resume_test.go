package nodes

import (
	"context"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// Only a release that went out unanswered leaves it unclear where the core
// runs (#555): a refusal or a release never sent changed nothing.
func TestReleaseFailedRecordsAnUnansweredRelease(t *testing.T) {
	s, err := New(Options{Dir: t.TempDir(), Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // watchMove returns at once
	for _, c := range []struct {
		name       string
		err        error
		state      string
		unanswered bool
	}{
		{"never sent", notSent("node is not connected"), MoveFailed, false},
		{"refused", nodeproto.Errorf(nodeproto.CodeDenied, "a bad plan"), MoveFailed, false},
		{"timed out", nodeproto.Errorf(nodeproto.CodeTimeout, "no answer"), MoveHandedOff, true},
		{"connection lost", nodeproto.Errorf(nodeproto.CodeUnavailable, "gone"), MoveHandedOff, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &MoveRecord{ID: "m-20261024-1200-beef", State: MoveReleasing, Started: time.Now()}
			s.releaseFailed(ctx, r, c.err)
			if r.State != c.state || r.ReleaseUnanswered != c.unanswered {
				t.Errorf("state %q, unanswered %v; want %q, %v", r.State, r.ReleaseUnanswered, c.state, c.unanswered)
			}
		})
	}
}

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
