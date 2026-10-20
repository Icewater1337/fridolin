package trigger

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseSchedule(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	for _, c := range []struct {
		in   string
		want string
	}{
		{"2026-10-23T09:00", "2026-10-23T09:00"},
		{"2026-10-23 09:00", "2026-10-23T09:00"},
		{"daily 08:00", "daily 08:00"},
		{"weekly sun 18:00", "weekly sun 18:00"},
		{"Sunday 18:00", "weekly sun 18:00"},
		{"WEEKLY Fri 07:30", "weekly fri 07:30"},
	} {
		sc, err := ParseSchedule(c.in, loc)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got := sc.String(); got != c.want {
			t.Errorf("%q: %q, want %q", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "tomorrow", "daily 25:00", "daily 8", "weekly funday 10:00", "sundae 10:00", "weekly sun", "daily 08:0"} {
		if _, err := ParseSchedule(bad, loc); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestScheduleNextAndDue(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	at := func(s string) time.Time {
		tm, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	// 2026-10-20 is a Tuesday.
	weekly := Schedule{Kind: ScheduleWeekly, Day: "sun", Time: "18:00"}
	if next, ok := weekly.Next(at("2026-10-20 10:00")); !ok || !next.Equal(at("2026-10-25 18:00")) {
		t.Errorf("weekly next = %v %v", next, ok)
	}
	if next, _ := weekly.Next(at("2026-10-25 18:00")); !next.Equal(at("2026-11-01 18:00")) {
		t.Errorf("weekly next at its own time = %v, want a week later", next)
	}
	daily := Schedule{Kind: ScheduleDaily, Time: "08:00"}
	if next, _ := daily.Next(at("2026-10-20 07:59")); !next.Equal(at("2026-10-20 08:00")) {
		t.Errorf("daily next = %v", next)
	}
	once := Schedule{Kind: ScheduleOnce, At: at("2026-10-21 09:00")}
	if _, ok := once.Next(at("2026-10-21 09:00")); ok {
		t.Error("a once schedule falls due again after its time")
	}

	// Due fires once for any number of missed times.
	if !daily.Due(at("2026-10-10 09:00"), at("2026-10-20 09:00")) {
		t.Error("a daily schedule missed for ten days is not due")
	}
	if daily.Due(at("2026-10-20 08:00"), at("2026-10-20 23:00")) {
		t.Error("a daily schedule that ran at 08:00 is due again the same day")
	}
	if !weekly.Due(at("2026-10-20 10:00"), at("2026-10-25 18:00")) || weekly.Due(at("2026-10-20 10:00"), at("2026-10-25 17:59")) {
		t.Error("weekly due at the wrong time")
	}
	if !once.Due(at("2026-10-20 00:00"), at("2026-10-21 09:30")) || once.Due(at("2026-10-21 09:00"), at("2026-10-22 00:00")) {
		t.Error("once due at the wrong time")
	}
}

func TestScheduleJSON(t *testing.T) {
	var s Schedule
	if err := json.Unmarshal([]byte(`{"kind":"weekly","day":"sun","time":"18:00"}`), &s); err != nil || s.String() != "weekly sun 18:00" {
		t.Errorf("%v %v", s, err)
	}
	for _, bad := range []string{`{"kind":"hourly"}`, `{"kind":"weekly","day":"x","time":"18:00"}`, `{"kind":"once"}`, `"daily 08:00"`} {
		if err := json.Unmarshal([]byte(bad), &s); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
	data, _ := json.Marshal(Schedule{Kind: ScheduleDaily, Time: "08:00"})
	if string(data) != `{"kind":"daily","time":"08:00"}` {
		t.Errorf("marshal = %s", data)
	}
}

func FuzzParseSchedule(f *testing.F) {
	for _, s := range []string{"daily 08:00", "weekly sun 18:00", "2026-10-23T09:00", "sun 1:00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		sc, err := ParseSchedule(s, time.UTC)
		if err != nil {
			return
		}
		if err := sc.Validate(); err != nil {
			t.Fatalf("%q parsed to an invalid schedule %+v: %v", s, sc, err)
		}
		again, err := ParseSchedule(sc.String(), time.UTC)
		if err != nil || again.String() != sc.String() {
			t.Fatalf("%q → %q → %q, %v", s, sc.String(), again.String(), err)
		}
	})
}
