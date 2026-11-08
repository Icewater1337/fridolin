package store

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
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

// TestErrandConversations: a new contact talks with Fridolin; their mode
// and brief stay when an errand saves them again; a warning counts for
// ErrandWarningKeep; unblocking leaves them known, forwarded and with no
// warning; their conversations count, and the earlier ones' lines read
// back oldest first.
func TestErrandConversations(t *testing.T) {
	t.Parallel()
	s := openTestStore(t, filepath.Join(t.TempDir(), "bridge.db"))
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	const alex = "15551234567"
	if err := s.SaveErrandContact(ctx, ErrandContact{Contact: alex, Name: "Alex", Brain: "core", State: ContactKnown, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	c, err := s.ErrandContactOf(ctx, alex)
	if err != nil || c.Mode != ContactConverse || c.Brief != "" || !c.WarnedAt.IsZero() || c.Warned(now) {
		t.Fatalf("a new contact: %+v %v", c, err)
	}
	if ok, err := s.SetErrandContactWays(ctx, alex, ContactForward, "Alex is Sam's brother.", "offer", now); !ok || err != nil {
		t.Fatalf("setting the ways: %v %v", ok, err)
	}
	if ok, _ := s.SetErrandContactWays(ctx, "15550000000", ContactForward, "", "", now); ok { // personal-data:ignore (a fictional 555 number)
		t.Error("set the ways of nobody")
	}
	if err := s.WarnErrandContact(ctx, alex, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveErrandContact(ctx, ErrandContact{Contact: alex, Name: "Alex P.", Brain: "core", State: ContactKnown, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	c, _ = s.ErrandContactOf(ctx, alex)
	if c.Name != "Alex P." || c.Mode != ContactForward || c.Brief != "Alex is Sam's brother." || c.Calendar != "offer" || !c.WarnedAt.Equal(now) || !c.Warned(now.Add(ErrandWarningKeep-time.Minute)) || c.Warned(now.Add(ErrandWarningKeep)) {
		t.Errorf("saved again: %+v", c)
	}
	if ok, _ := s.UnblockErrandContact(ctx, alex, now); ok {
		t.Error("unblocked someone not blocked")
	}
	c.State, c.Reason = ContactBlocked, "a prank"
	if err := s.SaveErrandContact(ctx, c); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.UnblockErrandContact(ctx, alex, now); !ok || err != nil {
		t.Fatalf("unblock: %v %v", ok, err)
	}
	c, _ = s.ErrandContactOf(ctx, alex)
	if c.State != ContactKnown || c.Mode != ContactForward || c.Reason != "" || !c.WarnedAt.IsZero() || c.Brief != "Alex is Sam's brother." {
		t.Errorf("unblocked: %+v", c)
	}
	if all, _ := s.ErrandContacts(ctx); len(all) != 1 || all[0].Contact != alex {
		t.Errorf("all contacts: %+v", all)
	}

	for i, opened := range []string{"", OpenedByThem, OpenedByThem} {
		id := []string{"e1", "e2", "e3"}[i]
		at := now.Add(time.Duration(i) * time.Hour)
		if err := s.AddErrand(ctx, Errand{ID: id, Brain: "core", Contact: alex, Name: "Alex", State: ErrandOpen, Chat: "c", Days: 1, CreatedAt: at, Deadline: at.Add(time.Hour), OpenedBy: opened}); err != nil {
			t.Fatal(err)
		}
		for j, who := range []string{"them", "you"} {
			if err := s.AddErrandLine(ctx, id, ErrandLine{Sender: who, Text: id + who, At: at.Add(time.Duration(j) * time.Minute)}, who == "you"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if e, _ := s.GetErrand(ctx, "e2"); e.OpenedBy != OpenedByThem {
		t.Errorf("opened by: %+v", e)
	}
	if n, err := s.ErrandsOpenedByThemSince(ctx, alex, now.Add(90*time.Minute)); n != 1 || err != nil {
		t.Errorf("opened by them since: %d %v", n, err)
	}
	if n, err := s.ErrandLinesFromSince(ctx, alex, "them", now); n != 3 || err != nil {
		t.Errorf("their lines: %d %v", n, err)
	}
	lines, err := s.EarlierErrandLines(ctx, alex, "e3", 3)
	if err != nil || len(lines) != 3 || lines[0].Text != "e1you" || lines[2].Text != "e2you" {
		t.Errorf("earlier lines: %+v %v", lines, err)
	}
}

// TestErrandBooking: an errand keeps how far it may use the calendar,
// and makes one booking: the move from one state to the next takes only
// from the state it expects.
func TestErrandBooking(t *testing.T) {
	t.Parallel()
	s := openTestStore(t, filepath.Join(t.TempDir(), "bridge.db"))
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.AddErrand(ctx, Errand{ID: "e1", Brain: "core", Contact: "15551234567", Name: "Alex", State: ErrandOpen, Chat: "c", Days: 1, CreatedAt: now, Deadline: now.Add(time.Hour), Calendar: "offer"}); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.GetErrand(ctx, "e1"); e.Calendar != "offer" || e.Booked != "" || !e.BookedFrom.IsZero() {
		t.Fatalf("a new errand: %+v", e)
	}
	from, to := now.Add(24*time.Hour), now.Add(25*time.Hour)
	if ok, err := s.SetErrandBooking(ctx, "e1", "", BookingAsking+"a1", from, to); !ok || err != nil {
		t.Fatalf("asking: %v %v", ok, err)
	}
	if ok, _ := s.SetErrandBooking(ctx, "e1", "", BookingMaking, from, to); ok {
		t.Error("a second booking while the first is asked for")
	}
	if ok, _ := s.SetErrandBooking(ctx, "e1", BookingAsking+"a1", BookingEvent+"ev1", from, to); !ok {
		t.Error("booking what was asked for")
	}
	e, _ := s.GetErrand(ctx, "e1")
	if e.Booked != BookingEvent+"ev1" || !e.BookedFrom.Equal(from) || !e.BookedTo.Equal(to) {
		t.Errorf("booked: %+v", e)
	}
	if ok, _ := s.SetErrandBooking(ctx, "e1", BookingEvent+"ev1", "", time.Time{}, time.Time{}); !ok {
		t.Error("clearing it")
	}
	if e, _ := s.GetErrand(ctx, "e1"); e.Booked != "" || !e.BookedFrom.IsZero() {
		t.Errorf("cleared: %+v", e)
	}
}

func TestErrandCallsSince(t *testing.T) {
	t.Parallel()
	s := openTestStore(t, filepath.Join(t.TempDir(), "bridge.db"))
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	add := func(id, state, mode string) {
		t.Helper()
		e := Errand{ID: id, Brain: "core", Contact: "1555000" + id, Name: "Alex", Message: "Hi", State: state, Mode: mode, Days: 1, CreatedAt: now, Deadline: now.Add(time.Hour)}
		if err := s.AddErrand(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	for i, st := range []string{ErrandDone, ErrandOpen, ErrandStopped, ErrandExpired} {
		add(string(rune('a'+i)), st, ErrandByCall)
	}
	add("w", ErrandAsking, ErrandByCall)   // not yet approved: no call made
	add("x", ErrandDeclined, ErrandByCall) // refused: no call made
	add("y", ErrandDone, ErrandByMessage)  // not a call
	since := now.Add(-time.Hour)
	if n, err := s.ErrandCallsSince(ctx, since, ""); err != nil || n != 4 {
		t.Fatalf("before the 5th: %d %v, want 4", n, err)
	}
	add("e", ErrandOpen, ErrandByCall) // the 5th, as startErrand has stored it
	if n, _ := s.ErrandCallsSince(ctx, since, "e"); n != 4 {
		t.Errorf("the errand starting counts itself: %d, want 4", n)
	}
	if n, _ := s.ErrandCallsSince(ctx, since, "a"); n != 4 {
		t.Errorf("the next one after 5: %d, want 4 (the 5 made, minus itself)", n)
	}
	if n, _ := s.ErrandCallsSince(ctx, since, ""); n != 5 {
		t.Errorf("five calls made: %d, want 5", n)
	}
}

func TestTakeBackErrandLine(t *testing.T) {
	t.Parallel()
	s := openTestStore(t, filepath.Join(t.TempDir(), "bridge.db"))
	ctx := context.Background()
	now := time.Now()
	if err := s.AddErrand(ctx, Errand{ID: "e1", Contact: "15551234567", State: ErrandOpen, Deadline: now.Add(time.Hour), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	line, ok, err := s.AddErrandLineWhileOpen(ctx, "e1", ErrandLine{Sender: "you", Text: "unsent", At: now})
	if err != nil || !ok {
		t.Fatalf("add: %v %v", ok, err)
	}
	// A line that came in meanwhile stays.
	if err := s.AddErrandLine(ctx, "e1", ErrandLine{Sender: "them", Text: "Hi!", At: now}, false); err != nil {
		t.Fatal(err)
	}
	for range 2 { // only the first counts
		if err := s.TakeBackErrandLine(ctx, "e1", line); err != nil {
			t.Fatal(err)
		}
	}
	lines, _ := s.ErrandLines(ctx, "e1")
	got, _ := s.GetErrand(ctx, "e1")
	if len(lines) != 1 || lines[0].Text != "Hi!" || got.Sent != 0 {
		t.Errorf("lines %+v, sent %d", lines, got.Sent)
	}
}

// TestAddErrandNoteOnce (#611): the same note, trimmed and in any case,
// is recorded once per errand; another errand or another note is new.
func TestAddErrandNoteOnce(t *testing.T) {
	t.Parallel()
	s := openTestStore(t, filepath.Join(t.TempDir(), "bridge.db"))
	ctx := context.Background()
	now := time.Now()
	for i, c := range []struct {
		errand, note string
		want         bool
	}{
		{"e1", "Alex says: late.", true},
		{"e1", "  alex SAYS: late. \n", false},
		{"e2", "Alex says: late.", true},
		{"e1", "Alex says: bring wine.", true},
	} {
		if got, err := s.AddErrandNote(ctx, c.errand, c.note, now.Add(time.Duration(i)*time.Second)); err != nil || got != c.want {
			t.Errorf("%d %q: %v %v, want %v", i, c.note, got, err, c.want)
		}
	}
	if got, err := s.ErrandNotes(ctx, "e1"); err != nil || !slices.Equal(got, []string{"Alex says: late.", "Alex says: bring wine."}) {
		t.Errorf("notes %q %v", got, err)
	}
}
