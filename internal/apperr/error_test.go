package apperr

import (
	"errors"
	"fmt"
	"testing"
)

func TestKindsPreserveTextAndCauses(t *testing.T) {
	t.Parallel()
	cause := errors.New("original error")
	err := fmt.Errorf("context: %w", Wrap(Invalid, cause))
	if err.Error() != "context: original error" || !errors.Is(err, cause) || KindOf(err) != Invalid {
		t.Fatalf("wrapped diagnostic: %v (%v)", err, KindOf(err))
	}
	if KindOf(cause) != Unknown || KindOf(nil) != Unknown || Wrap(Conflict, nil) != nil {
		t.Fatal("unclassified/nil errors changed")
	}
	if KindOf(Wrap(Conflict, New(NotFound, "no brain"))) != Conflict {
		t.Fatal("outer operation kind must win")
	}
}
