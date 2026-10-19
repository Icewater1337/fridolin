package app

import (
	"regexp"
	"testing"
)

// TestBootIDIsTheBootSession: the boot ID is the boot session's UUID, which
// stays the same for the whole boot, unlike the boot time, which moves when
// the wall clock is stepped.
func TestBootIDIsTheBootSession(t *testing.T) {
	id, err := bootID()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?i)^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Errorf("bootID = %q, want the boot session's UUID", id)
	}
	if again, err := bootID(); again != id || err != nil {
		t.Errorf("bootID = %q, %v the second time, want %q", again, err, id)
	}
}
