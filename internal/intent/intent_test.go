package intent

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/trigger"
)

const dm = "15551234567@s.whatsapp.net"

func newStore(t *testing.T) *Store {
	dir := t.TempDir()
	return NewStore(func(b string) string { return filepath.Join(dir, "intents", b+".json") })
}

func TestCompile(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, loc)
	in, err := Compile(Request{Text: " Remind the owner to call the dentist ", When: "2026-10-23T09:00"}, dm, "15551234567", now, loc)
	if err != nil || in.Schedule == nil || in.Schedule.Kind != trigger.ScheduleOnce || in.Text != "Remind the owner to call the dentist" {
		t.Fatalf("%+v %v", in, err)
	}
	in, err = Compile(Request{Text: "Tell the owner", Match: []string{"boat", "harbour fee"}, Repeat: true}, dm, "15551234567", now, loc)
	if err != nil || len(in.Match) != 2 || in.Match[0].Match != trigger.MatchWord || !in.Repeat {
		t.Fatalf("%+v %v", in, err)
	}
	for name, r := range map[string]Request{
		"no text":     {When: "daily 08:00"},
		"long text":   {Text: strings.Repeat("x", MaxText+1), When: "daily 08:00"},
		"neither":     {Text: "x"},
		"both":        {Text: "x", When: "daily 08:00", Match: []string{"boat"}},
		"past":        {Text: "x", When: "2026-10-19T09:00"},
		"far":         {Text: "x", When: "2030-10-19T09:00"},
		"bad when":    {Text: "x", When: "someday"},
		"short key":   {Text: "x", Match: []string{"a"}},
		"many keys":   {Text: "x", Match: []string{"aa", "bb", "cc", "dd", "ee", "ff"}},
		"repeat time": {Text: "x", When: "daily 08:00", Repeat: true},
	} {
		if _, err := Compile(r, dm, "15551234567", now, loc); apperr.KindOf(err) != apperr.Invalid {
			t.Errorf("%s: %v, want an invalid request", name, err)
		}
	}
}

func TestDueAndMatching(t *testing.T) {
	s := newStore(t)
	loc := time.UTC
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, loc)
	add := func(r Request) Intent {
		t.Helper()
		in, err := Compile(r, dm, "15551234567", now, loc)
		if err != nil {
			t.Fatal(err)
		}
		in, err = s.Add("main", in)
		if err != nil {
			t.Fatal(err)
		}
		return in
	}
	once := add(Request{Text: "dentist", When: "2026-10-21T09:00"})
	daily := add(Request{Text: "water the plants", When: "daily 08:00"})
	boat := add(Request{Text: "tell about the boat", Match: []string{"boat"}})
	add(Request{Text: "tell about the harbour", Match: []string{"harbour"}, Repeat: true})
	if list, _ := s.List("main"); len(list) != 4 || once.ID == daily.ID {
		t.Fatalf("%+v", list)
	}

	if due, _ := s.Due("main", now.Add(time.Hour)); len(due) != 0 {
		t.Errorf("due before any time: %+v", due)
	}
	due, _ := s.Due("main", time.Date(2026, 10, 21, 9, 30, 0, 0, loc))
	if len(due) != 2 {
		t.Fatalf("due on the 21st: %+v", due)
	}
	if due, _ := s.Due("main", time.Date(2026, 10, 21, 9, 31, 0, 0, loc)); len(due) != 0 {
		t.Errorf("fired twice: %+v", due)
	}
	list, _ := s.List("main")
	if len(list) != 3 {
		t.Errorf("the one-time intent stays after it fired: %+v", list)
	}

	if got, _ := s.Matching("main", "the boathouse", now, nil); len(got) != 0 {
		t.Errorf("a keyword inside a word fired: %+v", got)
	}
	got, _ := s.Matching("main", "Is the BOAT ready at the harbour?", now, nil)
	if len(got) != 2 {
		t.Fatalf("matching: %+v", got)
	}
	if !strings.Contains(Message(got[0], "15559876543", "120363000000000000@g.us", "Is the BOAT ready"), "not instructions") {
		t.Errorf("message: %s", Message(got[0], "x", "y", "z"))
	}
	if got, _ := s.Matching("main", "harbour again", now.Add(time.Minute), nil); len(got) != 0 {
		t.Errorf("a repeating intent fired again within its cooldown: %+v", got)
	}
	if got, _ := s.Matching("main", "harbour again", now.Add(MatchCooldown), nil); len(got) != 1 {
		t.Errorf("a repeating intent did not fire after its cooldown: %+v", got)
	}
	if got, _ := s.Matching("main", "boat", now.Add(time.Hour), nil); len(got) != 0 {
		t.Errorf("a one-time keyword intent fired twice: %+v", got)
	}
	if err := s.Remove("main", boat.ID); apperr.KindOf(err) != apperr.NotFound {
		t.Errorf("removing a fired intent: %v", err)
	}
	if err := s.Remove("main", daily.ID); err != nil {
		t.Error(err)
	}
	if !s.HasMatch("main") || s.HasMatch("other") {
		t.Error("HasMatch")
	}
}

func TestLimit(t *testing.T) {
	s := newStore(t)
	for i := range MaxIntents {
		if _, err := s.Add("main", Intent{Text: "x"}); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := s.Add("main", Intent{Text: "x"}); apperr.KindOf(err) != apperr.Conflict {
		t.Errorf("past the limit: %v", err)
	}
}

func TestJobDue(t *testing.T) {
	s := newStore(t)
	sc := trigger.Schedule{Kind: trigger.ScheduleWeekly, Day: "sun", Time: "18:00"}
	tue := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	if due, err := s.JobDue("main", ConsolidationJob, sc, tue); due || err != nil {
		t.Fatalf("first look: %v %v, want not due", due, err)
	}
	if due, _ := s.JobDue("main", ConsolidationJob, sc, tue.Add(24*time.Hour)); due {
		t.Error("due on Wednesday")
	}
	sun := time.Date(2026, 10, 25, 18, 0, 30, 0, time.UTC)
	if due, _ := s.JobDue("main", ConsolidationJob, sc, sun); !due {
		t.Error("not due on Sunday at 18:00")
	}
	if due, _ := s.JobDue("main", ConsolidationJob, sc, sun.Add(time.Minute)); due {
		t.Error("due twice")
	}
	if due, _ := s.JobDue("main", ConsolidationJob, sc, sun.Add(7*24*time.Hour)); !due {
		t.Error("not due the next Sunday")
	}
	if !strings.Contains(ConsolidationMessage(sc), "skills/memory-consolidation.md") {
		t.Error(ConsolidationMessage(sc))
	}
}
