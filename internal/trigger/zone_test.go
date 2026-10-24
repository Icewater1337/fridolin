package trigger

import (
	"testing"
	"time"
)

func zurich(t *testing.T) *time.Location {
	t.Helper()
	loc, err := LoadZone("Europe/Zurich")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func utc(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func TestLoadZone(t *testing.T) {
	if _, err := LoadZone("Europe/Zurich"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "Mars/Olympus", "../etc/passwd"} {
		if _, err := LoadZone(bad); err == nil {
			t.Errorf("LoadZone(%q) took it", bad)
		}
	}
	if Zone("") != time.Local || Zone("Mars/Olympus") != time.Local {
		t.Error("an unset or unknown zone is not the process's")
	}
	if Zone("Europe/Zurich").String() != "Europe/Zurich" {
		t.Error("Zone")
	}
}

// "daily 21:00" is 21:00 on the wall clock on both sides of the autumn
// change (Europe: 2026-10-25, 03:00 CEST back to 02:00 CET) and of the
// spring one (2027-03-28, 02:00 CET on to 03:00 CEST).
func TestNextKeepsTheWallClockAcrossDST(t *testing.T) {
	loc := zurich(t)
	cases := []struct {
		when  string
		after string // UTC
		want  []string
	}{
		{"daily 21:00", "2026-10-23 19:00", []string{"2026-10-24 19:00", "2026-10-25 20:00", "2026-10-26 20:00"}},
		{"daily 21:00", "2027-03-26 20:00", []string{"2027-03-27 20:00", "2027-03-28 19:00", "2027-03-29 19:00"}},
		{"weekly sun 21:00", "2026-10-18 19:00", []string{"2026-10-25 20:00", "2026-11-01 20:00"}},
		{"cron 0 21 * * *", "2026-10-24 19:00", []string{"2026-10-25 20:00", "2026-10-26 20:00"}},
		// The autumn change shows 02:00–02:59 twice: its first showing,
		// once.
		{"daily 02:30", "2026-10-24 00:30", []string{"2026-10-25 00:30", "2026-10-26 01:30"}},
		{"cron 30 2 * * *", "2026-10-24 00:30", []string{"2026-10-25 00:30", "2026-10-26 01:30"}},
		// The spring change skips 02:00–02:59: the next minute that
		// exists, 03:00 CEST, once.
		{"daily 02:30", "2027-03-27 01:30", []string{"2027-03-28 01:00", "2027-03-29 00:30"}},
		{"cron 30 2 * * *", "2027-03-27 01:30", []string{"2027-03-28 01:00", "2027-03-29 00:30"}},
		{"weekly sun 02:00", "2027-03-21 01:00", []string{"2027-03-28 01:00", "2027-04-04 00:00"}},
	}
	for _, c := range cases {
		sc, err := ParseRecurring(c.when, loc)
		if err != nil {
			t.Fatal(err)
		}
		at := utc(t, c.after).In(loc)
		for _, w := range c.want {
			n, ok := sc.Next(at)
			if !ok || !n.Equal(utc(t, w)) {
				t.Errorf("%s after %s: got %s, want %s UTC", c.when, at, n.UTC(), w)
				break
			}
			at = n
		}
	}
}

// A cron that runs every 15 minutes runs each wall-clock time once
// across both changes, and none twice.
func TestCronAcrossDST(t *testing.T) {
	loc := zurich(t)
	sc, err := ParseRecurring("cron */15 * * * *", loc)
	if err != nil {
		t.Fatal(err)
	}
	for _, day := range []string{"2026-10-24 22:00", "2027-03-27 23:00"} {
		at := utc(t, day).In(loc)
		seen := map[string]bool{}
		for range 24 {
			n, ok := sc.Next(at)
			if !ok || !n.After(at) {
				t.Fatalf("%s: stuck at %s", day, at)
			}
			wall := n.Format("2006-01-02 15:04")
			if seen[wall] {
				t.Errorf("%s: %s ran twice", day, wall)
			}
			seen[wall] = true
			at = n
		}
	}
}
