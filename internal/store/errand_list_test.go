package store

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// TestAllErrands: the dashboard's list holds every brain's errands, those
// that have not ended first, then the last ended ones up to its limit; and
// the people blocked, the latest first.
func TestAllErrands(t *testing.T) {
	t.Parallel()
	s := openTestStore(t, filepath.Join(t.TempDir(), "bridge.db"))
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	add := func(id, brain, state string, age time.Duration) {
		t.Helper()
		e := Errand{ID: id, Brain: brain, Contact: "15551234567", Name: "Alex", Goal: "g", Brief: "b", Message: "m",
			State: state, Chat: "c", Days: 3, CreatedAt: now.Add(-age), Deadline: now.Add(time.Hour)}
		if err := s.AddErrand(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	add("old-done", "core", ErrandDone, 5*time.Hour)
	add("open", "core", ErrandOpen, 4*time.Hour)
	add("done", "other", ErrandDone, 3*time.Hour)
	add("asking", "other", ErrandAsking, 2*time.Hour)
	add("stopped", "core", ErrandStopped, time.Hour)
	list, err := s.AllErrands(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range list {
		ids = append(ids, e.ID)
	}
	if want := []string{"asking", "open", "stopped", "done"}; !slices.Equal(ids, want) {
		t.Errorf("errands %q, want %q", ids, want)
	}

	for i, c := range []ErrandContact{
		{Contact: "15551234567", Name: "Alex", State: ContactBlocked, Reason: "asked for the address", UpdatedAt: now.Add(-time.Hour)},
		{Contact: "15559876543", Name: "Kim", State: ContactKnown, UpdatedAt: now},
	} {
		if err := s.SaveErrandContact(ctx, c); err != nil {
			t.Fatal(i, err)
		}
	}
	blocked, err := s.BlockedErrandContacts(ctx)
	if err != nil || len(blocked) != 1 || blocked[0].Name != "Alex" || blocked[0].Reason != "asked for the address" {
		t.Errorf("blocked: %+v %v", blocked, err)
	}
}
