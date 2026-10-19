package backup

import (
	"fmt"
	"testing"
	"time"
	_ "time/tzdata"
)

func TestFilterLines(t *testing.T) {
	got := FilterLines([]string{"node_modules/", "*.log", " /work/ ", "", "+ /sneaky", "- already-prefixed"})
	want := "- node_modules/**\n- *.log\n- /work/**\n- already-prefixed\n"
	if got != want {
		t.Errorf("filterLines =\n%q\nwant\n%q", got, want)
	}
}

// TestNextDue is the daily schedule: today's run is due at daily_at unless
// it already ran today or the schedule was switched on or moved after that
// time, and a run missed while the machine slept is due as soon as it is
// awake.
func TestNextDue(t *testing.T) {
	zurich, err := time.LoadLocation("Europe/Zurich")
	if err != nil {
		t.Fatal(err)
	}
	at := func(y int, mo time.Month, d, h, mi int) time.Time { return time.Date(y, mo, d, h, mi, 0, 0, zurich) }
	day := at(2026, 3, 10, 0, 0)
	cases := []struct {
		name    string
		now     time.Time
		dailyAt string
		lastRun string
		armed   time.Time
		wantDue time.Time
		wantNow bool
	}{
		{"before the time", at(2026, 3, 10, 2, 0), "03:30", "", time.Time{}, at(2026, 3, 10, 3, 30), false},
		{"at the time", at(2026, 3, 10, 3, 30), "03:30", "", time.Time{}, at(2026, 3, 10, 3, 30), true},
		{"after the time: missed while asleep", at(2026, 3, 10, 9, 0), "03:30", "2026-03-09", day.AddDate(0, 0, -5), at(2026, 3, 10, 3, 30), true},
		{"already ran today", at(2026, 3, 10, 9, 0), "03:30", "2026-03-10", time.Time{}, at(2026, 3, 11, 3, 30), false},
		{"ran yesterday, later today", at(2026, 3, 10, 9, 0), "23:15", "2026-03-09", time.Time{}, at(2026, 3, 10, 23, 15), false},
		{"switched on after the time", at(2026, 3, 10, 9, 0), "03:30", "", at(2026, 3, 10, 8, 59), at(2026, 3, 11, 3, 30), false},
		{"switched on before the time", at(2026, 3, 10, 9, 0), "10:00", "", at(2026, 3, 10, 8, 59), at(2026, 3, 10, 10, 0), false},
		{"moved to a time already past", at(2026, 3, 10, 15, 0), "14:00", "2026-03-09", at(2026, 3, 10, 14, 30), at(2026, 3, 11, 14, 0), false},
		{"switched on in the due minute", at(2026, 3, 10, 3, 30), "03:30", "", at(2026, 3, 10, 3, 30).Add(20 * time.Second), at(2026, 3, 11, 3, 30), false},
		{"end of the month", at(2026, 3, 31, 9, 0), "03:30", "2026-03-31", time.Time{}, at(2026, 4, 1, 3, 30), false},
		// 2026-03-29: the clocks jump from 02:00 to 03:00.
		{"a time the clocks skip", at(2026, 3, 29, 1, 0), "02:30", "", time.Time{}, at(2026, 3, 29, 3, 30), false},
		{"a time the clocks skip, once it came", at(2026, 3, 29, 3, 31), "02:30", "", time.Time{}, at(2026, 3, 29, 3, 30), true},
		{"the day before the clocks skip it", at(2026, 3, 28, 9, 0), "02:30", "2026-03-28", time.Time{}, at(2026, 3, 29, 3, 30), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			due, now := Due(tc.now, tc.dailyAt, tc.lastRun, tc.armed)
			if !due.Equal(tc.wantDue) || now != tc.wantNow {
				t.Errorf("nextDue = %v, %v; want %v, %v", due, now, tc.wantDue, tc.wantNow)
			}
		})
	}

	// 2026-10-25: the clocks go back from 03:00 to 02:00, so 02:30 comes
	// twice. The run is due once.
	t.Run("a time that comes twice", func(t *testing.T) {
		first := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC).In(zurich)  // 02:30 summer time
		second := time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC).In(zurich) // 02:30 winter time
		ranAt := time.Time{}
		for _, now := range []time.Time{first, second} {
			if _, runNow := Due(now, "02:30", ranOn(ranAt), time.Time{}); runNow {
				if !ranAt.IsZero() {
					t.Fatalf("due again at %v after running at %v", now, ranAt)
				}
				ranAt = now
			}
		}
		if ranAt.IsZero() {
			t.Error("never due")
		}
	})
}

// ranOn is the LastScheduledDay of a run at t, "" for none.
func ranOn(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(DayLayout)
}

func TestRcloneSection(t *testing.T) {
	conf := "[other]\ntype = s3\n\n[brain-gdrive]\ntype = drive\nscope = drive.file\ntoken = {\"x\":1}\n"
	s := RcloneSection(conf, "brain-gdrive")
	if s["type"] != "drive" || s["token"] == "" {
		t.Errorf("section = %v", s)
	}
	if RcloneSection(conf, "missing") != nil {
		t.Error("missing section should be nil")
	}
}

func TestExpiredVersions(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.Local)
	stamp := func(days int) string { return VersionName(now.AddDate(0, 0, -days)) }
	cases := []struct {
		name  string
		names []string
		days  int
		want  []string
	}{
		{"older than the window", []string{stamp(31), stamp(29)}, 30, []string{stamp(31)}},
		{"exactly on the cutoff stays", []string{stamp(30)}, 30, nil},
		{"one day window", []string{stamp(2), stamp(0)}, 1, []string{stamp(2)}},
		{"names that are not version stamps stay", []string{"keep-me", "2025", "20250101-0000", "20250101-000000X", stamp(90)}, 30, []string{stamp(90)}},
		// Folders named before the names were in UTC are in local time.
		{"local-time names", []string{now.AddDate(0, 0, -31).Format(legacyVersionLayout), now.AddDate(0, 0, -29).Format(legacyVersionLayout)}, 30,
			[]string{now.AddDate(0, 0, -31).Format(legacyVersionLayout)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ExpiredVersions(c.names, now, c.days)
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Errorf("expiredVersions = %v, want %v", got, c.want)
			}
		})
	}
}

// TestVersionNamesKeepTheirOrderWhenClocksGoBack: version folders are
// named in UTC, so a run in the hour the clocks go back sorts after the run
// an hour of local time "later" that came before it, and neither name can
// come twice.
func TestVersionNamesKeepTheirOrderWhenClocksGoBack(t *testing.T) {
	zurich, err := time.LoadLocation("Europe/Zurich")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-10-25: 02:30 summer time, then 02:15 winter time 45 minutes later.
	first := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC).In(zurich)
	second := first.Add(45 * time.Minute)
	names := []string{VersionName(first), VersionName(second)}
	SortVersions(names)
	if names[0] != VersionName(second) {
		t.Errorf("versions = %v, want the later run first", names)
	}
	for _, n := range names {
		if !VersionOK(n) {
			t.Errorf("%s cannot be restored", n)
		}
		if got, ok := ParseVersion(n); !ok || !(got.Equal(first) || got.Equal(second)) {
			t.Errorf("parseVersion(%s) = %v, %v", n, got, ok)
		}
	}
}
