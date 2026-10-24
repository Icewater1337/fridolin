package sched

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Icewater1337/fridolin/internal/trigger"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.ParseInLocation("2006-01-02 15:04", s, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func TestOffset(t *testing.T) {
	a := Offset("main", "x", 10*time.Minute)
	if a != Offset("main", "x", 10*time.Minute) || a < 0 || a > 10*time.Minute {
		t.Errorf("offset %v is not stable within its jitter", a)
	}
	if Offset("main", "x", 0) != 0 {
		t.Error("no jitter, an offset")
	}
	if a%time.Second != 0 {
		t.Errorf("offset %v is not whole seconds", a)
	}
}

func TestDecide(t *testing.T) {
	daily, _ := trigger.ParseRecurring("daily 07:00", time.UTC)
	off := 2 * time.Minute

	// First sight: counts from now, runs at the next 07:00 and its offset.
	d := Decide(daily, time.Time{}, off, false, at(t, "2026-10-23 08:00"))
	if d.Run || !d.Next.Equal(at(t, "2026-10-24 07:02")) {
		t.Fatalf("first sight: %+v", d)
	}
	// Not yet at 07:01, due at 07:02, on time.
	if d := Decide(daily, at(t, "2026-10-23 07:00"), off, false, at(t, "2026-10-24 07:01")); d.Run {
		t.Errorf("ran before its offset: %+v", d)
	}
	d = Decide(daily, at(t, "2026-10-23 07:00"), off, false, at(t, "2026-10-24 07:02"))
	if !d.Run || d.Late || !d.Last.Equal(at(t, "2026-10-24 07:00")) || !d.Next.Equal(at(t, "2026-10-25 07:02")) {
		t.Errorf("on time: %+v", d)
	}
	// Down for three days: one late run, anchored at the latest slot.
	d = Decide(daily, at(t, "2026-10-20 07:00"), off, false, at(t, "2026-10-23 12:00"))
	if !d.Run || !d.Late || !d.Last.Equal(at(t, "2026-10-23 07:00")) {
		t.Errorf("missed, run: %+v", d)
	}
	d = Decide(daily, at(t, "2026-10-20 07:00"), off, true, at(t, "2026-10-23 12:00"))
	if d.Run || !d.Skipped || !d.Last.Equal(at(t, "2026-10-23 07:00")) {
		t.Errorf("missed, skip: %+v", d)
	}

	every, _ := trigger.ParseRecurring("every 6h", time.UTC)
	d = Decide(every, at(t, "2026-10-23 00:00"), 0, false, at(t, "2026-10-23 19:00"))
	if !d.Run || !d.Last.Equal(at(t, "2026-10-23 18:00")) || !d.Next.Equal(at(t, "2026-10-24 00:00")) {
		t.Errorf("every: %+v", d)
	}
	if n := NextAfter(every, at(t, "2026-10-23 00:00"), time.Minute, time.UTC); !n.Equal(at(t, "2026-10-23 06:01")) {
		t.Errorf("NextAfter = %v", n)
	}
}

func TestStore(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "schedules"))
	now := at(t, "2026-10-23 07:00")
	id := NewRunID(now)
	if !ValidRunID(id) || !strings.HasPrefix(id, "20261023T070000-") {
		t.Fatalf("run id %q", id)
	}
	r := Run{ID: id, Brain: "main", Agent: "brief", Kind: KindBrain, Trigger: TriggerSchedule, Started: now}
	if err := s.Begin(r); err != nil {
		t.Fatal(err)
	}
	log, err := s.OpenLog("main", "brief", id, 200)
	if err != nil {
		t.Fatal(err)
	}
	log.Line("🔧 Tool: Bash")
	log.Line(strings.Repeat("x", 300))
	log.Line("dropped")
	log.Close()
	log.Line("after close")
	text, cut, err := s.ReadLog("main", "brief", id, 1<<20)
	if err != nil || cut || !strings.Contains(text, "Tool: Bash") || !strings.Contains(text, "reached its limit") || strings.Contains(text, "dropped") {
		t.Fatalf("log %q, cut %v, %v", text, cut, err)
	}
	if text, cut, _ := s.ReadLog("main", "brief", id, 10); !cut || len(text) != 10 {
		t.Errorf("tail %q %v", text, cut)
	}

	// A bridge that went down mid-run finds it interrupted.
	got, err := s.Interrupted(now.Add(time.Hour))
	if err != nil || len(got) != 1 || got[0].Status != StatusInterrupted {
		t.Fatalf("interrupted: %+v %v", got, err)
	}
	r2 := r
	r2.ID, r2.Started, r2.Status = NewRunID(now.Add(2*time.Hour)), now.Add(2*time.Hour), StatusOK
	if err := s.Finish(r2); err != nil {
		t.Fatal(err)
	}
	runs, err := s.Runs("main", "brief", 0)
	if err != nil || len(runs) != 2 || runs[0].ID != r2.ID || runs[1].Status != StatusInterrupted {
		t.Fatalf("runs %+v %v", runs, err)
	}
	if one, err := s.Run("main", "brief", id); err != nil || one.LogBytes != 0 && one.ID != id {
		t.Errorf("Run: %+v %v", one, err)
	}
	if _, err := s.Run("main", "brief", "nope"); err != ErrNotFound {
		t.Errorf("missing run: %v", err)
	}
	states, _ := s.States()
	if st := states["main/brief"]; st.LastRun == nil || st.LastRun.ID != r2.ID {
		t.Errorf("state %+v", st)
	}
	if err := s.Update("main", "brief", func(a *AgentState) bool { a.Paused = true; return true }); err != nil {
		t.Fatal(err)
	}
	if err := s.Update("../x", "brief", func(*AgentState) bool { return true }); err == nil {
		t.Error("a brain id with a path in it")
	}

	listing, err := s.Listing()
	if err != nil || len(listing["main/brief"]) != 2 {
		t.Fatalf("listing %+v %v", listing, err)
	}
	if err := s.Drop([]RunFile{{Brain: "main", Agent: "brief", ID: id}}); err != nil {
		t.Fatal(err)
	}
	if runs, _ := s.Runs("main", "brief", 0); len(runs) != 1 || runs[0].ID != r2.ID {
		t.Errorf("after drop: %+v", runs)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), "main", "brief", "logs", id+".log")); !os.IsNotExist(err) {
		t.Errorf("the dropped run's log is still there: %v", err)
	}
	if err := s.Forget(func(string) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if states, _ := s.States(); len(states) != 0 {
		t.Errorf("forgotten: %+v", states)
	}
}

func TestTemplates(t *testing.T) {
	fsys := fstest.MapFS{
		"schedules/morning-briefing.md": {Data: []byte("---\ndescription: The day's appointments.\nwhen: daily 06:00\nreport: worth\nvars: greeting=Good morning\n---\n\n{{greeting}}! List today.\n")},
		"schedules/review.md":           {Data: []byte("---\ndescription: Review.\nvars: repos, max_issues=3\ngithub: issues=write, contents=read\noptional: true\n---\nReview {{repos}}, at most {{ max_issues }}.\n")},
		"schedules/README.txt":          {Data: []byte("not a template")},
	}
	list, err := Templates(fsys, "schedules")
	if err != nil || len(list) != 2 || list[0].Name != "morning-briefing" || list[1].GitHub["issues"] != "write" || !list[1].Optional {
		t.Fatalf("%+v %v", list, err)
	}
	out, err := Render(list[0], list[0].Body, nil)
	if err != nil || out != "Good morning! List today.\n" {
		t.Errorf("%q %v", out, err)
	}
	if _, err := Render(list[1], list[1].Body, nil); err == nil {
		t.Error("a required var left out")
	}
	out, err = Render(list[1], list[1].Body, map[string]string{"repos": "a/b"})
	if err != nil || out != "Review a/b, at most 3.\n" {
		t.Errorf("%q %v", out, err)
	}
	if _, err := Render(Template{}, "Hi {{who}}", nil); err == nil {
		t.Error("a hole in the task")
	}
}

func TestMessageAndWorth(t *testing.T) {
	m := Message(Task{Agent: "brief", When: "daily 06:00", Trigger: TriggerManual, Report: "worth", Chat: "15551234567@s.whatsapp.net", Preapproved: []string{"file issues"}, Text: "Do it."})
	for _, want := range []string{`"brief"`, "started by hand", NothingToReport, "- file issues", "The task:\n\nDo it."} {
		if !strings.Contains(m, want) {
			t.Errorf("message lacks %q:\n%s", want, m)
		}
	}
	if !strings.Contains(Message(Task{Report: "silent", Text: "x"}), "sent to nobody") {
		t.Error("silent")
	}
	for reply, worth := range map[string]bool{"": false, "NOTHING_TO_REPORT": false, " **NOTHING_TO_REPORT** ": false, "Checked.\nNOTHING_TO_REPORT": false, "You have 2 appointments": true} {
		if Worth(reply) != worth {
			t.Errorf("Worth(%q) = %v", reply, !worth)
		}
	}
}

// Ticking every 30 seconds across both DST changes, "daily 21:00" and
// "daily 02:30" run once a day at their wall-clock time in the owner's
// zone, whatever zone the anchor comes back from the state file in.
func TestDecideAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Zurich")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		when, from string
		want       []string // wall clock, Europe/Zurich
	}{
		{"daily 21:00", "2026-10-23 20:00", []string{"2026-10-23 21:00", "2026-10-24 21:00", "2026-10-25 21:00", "2026-10-26 21:00"}},
		{"daily 02:30", "2026-10-23 20:00", []string{"2026-10-24 02:30", "2026-10-25 02:30", "2026-10-26 02:30", "2026-10-27 02:30"}},
		{"daily 02:30", "2027-03-26 20:00", []string{"2027-03-27 02:30", "2027-03-28 03:00", "2027-03-29 02:30", "2027-03-30 02:30"}},
	} {
		sc, err := trigger.ParseRecurring(c.when, loc)
		if err != nil {
			t.Fatal(err)
		}
		now, _ := time.ParseInLocation("2006-01-02 15:04", c.from, loc)
		var last time.Time
		var runs []string
		for end := now.Add(80 * time.Hour); now.Before(end); now = now.Add(30 * time.Second) {
			d := Decide(sc, last, 0, false, now)
			// The state file keeps the anchor as RFC 3339, an offset.
			last = d.Last.In(time.FixedZone("", 7200))
			if d.Run {
				if d.Late {
					t.Errorf("%s: late at %s", c.when, now)
				}
				runs = append(runs, d.Last.In(loc).Format("2006-01-02 15:04"))
			}
		}
		if strings.Join(runs, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: ran at %v, want %v", c.when, runs, c.want)
		}
	}
}
