package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestErrands(t *testing.T) {
	t.Parallel()
	s := openTestStore(t, filepath.Join(t.TempDir(), "bridge.db"))
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	e := Errand{ID: "e1", Brain: "core", Contact: "15551234567", Name: "Alex", Goal: "dinner", Brief: "Tuesday", Message: "Hi Alex",
		State: ErrandAsking, Approval: "a1", Chat: "15551234567@s.whatsapp.net", Days: 3, CreatedAt: now, Deadline: now.Add(72 * time.Hour)}
	if err := s.AddErrand(ctx, e); err != nil {
		t.Fatal(err)
	}
	got, err := s.ErrandByApproval(ctx, "a1")
	if err != nil || got.ID != "e1" || got.State != ErrandAsking || !got.Live() || !got.ClosedAt.IsZero() || !got.CreatedAt.Equal(now) {
		t.Fatalf("by approval: %+v %v", got, err)
	}
	if _, err := s.ErrandByApproval(ctx, ""); !errors.Is(err, ErrNoErrand) {
		t.Errorf("no approval: %v", err)
	}
	if live, err := s.LiveErrandWith(ctx, "15551234567"); err != nil || live.ID != "e1" {
		t.Errorf("live: %+v %v", live, err)
	}
	if open, _ := s.OpenErrands(ctx); len(open) != 0 {
		t.Errorf("open before it opened: %+v", open)
	}
	if ok, err := s.OpenErrand(ctx, "e1", now.Add(time.Hour)); !ok || err != nil {
		t.Fatalf("open: %v %v", ok, err)
	}
	if ok, _ := s.OpenErrand(ctx, "e1", now); ok {
		t.Error("opened twice")
	}
	if err := s.AddErrandLine(ctx, "e1", ErrandLine{Sender: "you", Text: "Hi Alex", At: now}, true); err != nil {
		t.Fatal(err)
	}
	if err := s.AddErrandLine(ctx, "e1", ErrandLine{Sender: "them", Text: "Hi!", At: now}, false); err != nil {
		t.Fatal(err)
	}
	lines, err := s.ErrandLines(ctx, "e1")
	if err != nil || len(lines) != 2 || lines[0].Sender != "you" || lines[1].Text != "Hi!" {
		t.Fatalf("lines: %+v %v", lines, err)
	}
	got, _ = s.GetErrand(ctx, "e1")
	if got.Sent != 1 || got.State != ErrandOpen || !got.Deadline.Equal(now.Add(time.Hour)) {
		t.Errorf("after a line: %+v", got)
	}
	if open, _ := s.OpenErrands(ctx); len(open) != 1 {
		t.Errorf("open: %+v", open)
	}
	if ok, err := s.EndErrand(ctx, "e1", ErrandDone, "Tuesday it is", now); !ok || err != nil {
		t.Fatalf("end: %v %v", ok, err)
	}
	if ok, _ := s.EndErrand(ctx, "e1", ErrandAborted, "", now); ok {
		t.Error("ended twice")
	}
	got, _ = s.GetErrand(ctx, "e1")
	if got.State != ErrandDone || got.Outcome != "Tuesday it is" || got.ClosedAt.IsZero() || got.Live() {
		t.Errorf("ended: %+v", got)
	}
	if _, err := s.LiveErrandWith(ctx, "15551234567"); !errors.Is(err, ErrNoErrand) {
		t.Errorf("live after the end: %v", err)
	}
	_ = s.AddErrand(ctx, Errand{ID: "e2", Brain: "core", Contact: "15557654321", State: ErrandOpen, Chat: "c", Days: 1, CreatedAt: now.Add(time.Minute), Deadline: now}) // personal-data:ignore (a fictional 555 number)
	_ = s.AddErrand(ctx, Errand{ID: "e3", Brain: "other", Contact: "15557654321", State: ErrandOpen, Chat: "c", Days: 1, CreatedAt: now, Deadline: now})                 // personal-data:ignore (a fictional 555 number)
	if recent, _ := s.RecentErrands(ctx, "core", 5); len(recent) != 2 || recent[0].ID != "e2" || recent[1].ID != "e1" {
		t.Errorf("recent: %+v", recent)
	}
}

func TestErrandContacts(t *testing.T) {
	t.Parallel()
	s := openTestStore(t, filepath.Join(t.TempDir(), "bridge.db"))
	ctx := context.Background()
	if _, err := s.ErrandContactOf(ctx, "15551234567"); !errors.Is(err, ErrNoErrand) {
		t.Fatalf("unknown: %v", err)
	}
	c := ErrandContact{Contact: "15551234567", LID: "123456789", Name: "Alex", Brain: "core", State: ContactKnown, UpdatedAt: time.Now()}
	if err := s.SaveErrandContact(ctx, c); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"15551234567", "123456789"} {
		if got, err := s.ErrandContactOf(ctx, id); err != nil || got.Name != "Alex" || got.State != ContactKnown {
			t.Errorf("%s: %+v %v", id, got, err)
		}
	}
	if _, err := s.ErrandContactOf(ctx, ""); !errors.Is(err, ErrNoErrand) {
		t.Errorf("an empty id matched: %v", err)
	}
	c.State, c.Reason = ContactBlocked, "asked for the owner's address"
	if err := s.SaveErrandContact(ctx, c); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ErrandContactOf(ctx, "15551234567"); got.State != ContactBlocked || got.Reason != c.Reason {
		t.Errorf("blocked: %+v", got)
	}
	if ok, err := s.ForgetErrandContact(ctx, "15551234567"); !ok || err != nil {
		t.Errorf("forget: %v %v", ok, err)
	}
	if ok, _ := s.ForgetErrandContact(ctx, "15551234567"); ok {
		t.Error("forgot twice")
	}
}
