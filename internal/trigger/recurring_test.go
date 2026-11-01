package trigger

import (
	"testing"
	"time"
)

func TestParseRecurring(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	for _, c := range []struct{ in, want string }{
		{"every 6h", "every 6h"},
		{"Every 30m", "every 30m"},
		{"every 2d", "every 2d"},
		{"daily 07:00", "daily 07:00"},
		{"weekly mon 09:00", "weekly mon 09:00"},
		{"cron 0 7 * * 1-5", "cron 0 7 * * 1-5"},
		{"0  7 * *  MON-fri", "cron 0 7 * * mon-fri"},
		{"*/15 * * * *", "cron */15 * * * *"},
	} {
		sc, err := ParseRecurring(c.in, loc)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got := sc.String(); got != c.want {
			t.Errorf("%q: %q, want %q", c.in, got, c.want)
		}
		if !sc.Recurring() {
			t.Errorf("%q does not repeat", c.in)
		}
	}
	for _, bad := range []string{"", "2026-10-23T09:00", "every", "every 1m", "every 6", "every 6w", "every 0h", "every 400d",
		"cron 60 * * * *", "cron * 24 * * *", "cron * * 0 * *", "cron * * * 13 *", "cron * * * * 8", "cron 5-1 * * * *", "cron */0 * * * *", "* * * *", "cron x * * * *"} {
		if _, err := ParseRecurring(bad, loc); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestRecurringNext(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	at := func(s string) time.Time {
		tm, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	every, _ := ParseRecurring("every 6h", loc)
	if next, ok := every.Next(at("2026-10-20 10:00")); !ok || !next.Equal(at("2026-10-20 16:00")) {
		t.Errorf("every next = %v %v", next, ok)
	}
	if !every.Due(at("2026-10-20 10:00"), at("2026-10-20 16:00")) || every.Due(at("2026-10-20 10:00"), at("2026-10-20 15:59")) {
		t.Error("every due at the wrong time")
	}
	// 2026-10-23 is a Friday.
	weekdays, _ := ParseRecurring("cron 0 7 * * 1-5", loc)
	if next, _ := weekdays.Next(at("2026-10-23 08:00")); !next.Equal(at("2026-10-26 07:00")) {
		t.Errorf("weekday cron next = %v, want Monday 07:00", next)
	}
	quarter, _ := ParseRecurring("*/15 * * * *", loc)
	if next, _ := quarter.Next(at("2026-10-23 08:14")); !next.Equal(at("2026-10-23 08:15")) {
		t.Errorf("*/15 next = %v", next)
	}
	// Day of month and weekday both restricted: either matches.
	either, _ := ParseRecurring("cron 0 9 1 * sun", loc)
	if next, _ := either.Next(at("2026-10-23 10:00")); !next.Equal(at("2026-10-25 09:00")) {
		t.Errorf("1st-or-Sunday next = %v", next)
	}
	// A day field starting with * (also */n) is unrestricted: the other
	// field must match too, as in Vixie cron. 2026-10-25 is a Sunday.
	oddWeekdays, _ := ParseRecurring("cron 0 9 */2 * 1-5", loc)
	if next, _ := oddWeekdays.Next(at("2026-10-23 10:00")); !next.Equal(at("2026-10-27 09:00")) {
		t.Errorf("*/2 with weekdays next = %v, want Tuesday the 27th", next)
	}
	oddDays, _ := ParseRecurring("cron 0 9 */2 * *", loc)
	if next, _ := oddDays.Next(at("2026-10-23 10:00")); !next.Equal(at("2026-10-25 09:00")) {
		t.Errorf("*/2 next = %v, want the 25th", next)
	}
	never, _ := ParseRecurring("cron 0 0 31 2 *", loc)
	if _, ok := never.Next(at("2026-10-23 10:00")); ok {
		t.Error("31 February came")
	}
	sunday7, _ := ParseRecurring("cron 30 6 * * 7", loc)
	if next, _ := sunday7.Next(at("2026-10-23 10:00")); !next.Equal(at("2026-10-25 06:30")) {
		t.Errorf("weekday 7 next = %v, want Sunday", next)
	}
}

func FuzzParseRecurring(f *testing.F) {
	for _, s := range []string{"every 6h", "daily 08:00", "weekly sun 18:00", "cron 0 7 * * 1-5", "*/5 1-3 1,15 * mon"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		sc, err := ParseRecurring(s, time.UTC)
		if err != nil {
			return
		}
		if err := sc.Validate(); err != nil || !sc.Recurring() {
			t.Fatalf("%q parsed to an invalid or single schedule %+v: %v", s, sc, err)
		}
		again, err := ParseRecurring(sc.String(), time.UTC)
		if err != nil || again.String() != sc.String() {
			t.Fatalf("%q → %q → %q, %v", s, sc.String(), again.String(), err)
		}
		_, _ = sc.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	})
}
