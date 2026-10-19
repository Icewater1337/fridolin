package toolcli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestFreeWindows(t *testing.T) {
	t.Parallel()
	loc, _ := time.LoadLocation("Europe/Zurich")
	at := func(day, h, m int) time.Time { return time.Date(2026, 3, day, h, m, 0, 0, loc) }
	cases := []struct {
		name       string
		busy       [][2]time.Time
		start, end time.Time
		minSlot    int
		want       string
	}{
		{"free all day", nil, at(5, 8, 0), at(5, 18, 0), 30, "08:00-18:00/600"},
		{"overlapping and unsorted", [][2]time.Time{{at(5, 11, 0), at(5, 12, 0)}, {at(5, 9, 0), at(5, 10, 0)}, {at(5, 9, 30), at(5, 11, 30)}}, at(5, 8, 0), at(5, 18, 0), 30, "08:00-09:00/60 12:00-18:00/360"},
		{"short gaps left out", [][2]time.Time{{at(5, 8, 0), at(5, 9, 0)}, {at(5, 9, 15), at(5, 18, 0)}}, at(5, 8, 0), at(5, 18, 0), 30, ""},
		{"clipped to the window", [][2]time.Time{{at(5, 7, 0), at(5, 8, 30)}, {at(5, 17, 30), at(5, 19, 0)}}, at(5, 8, 0), at(5, 18, 0), 30, "08:30-17:30/540"},
		{"to midnight", nil, at(5, 20, 0), at(6, 0, 0), 30, "20:00-24:00/240"},
		// 29 March 2026: the clocks go from 02:00 to 03:00, so the day has
		// 23 hours; 00:00-24:00 is 1380 minutes.
		{"the day the clocks change", nil, at(29, 0, 0), at(30, 0, 0), 30, "00:00-24:00/1380"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var busy []timeRangeOf
			for _, b := range tc.busy {
				busy = append(busy, timeRangeOf{b[0], b[1]})
			}
			var got []string
			for _, w := range freeWindows(busy, tc.start, tc.end, tc.minSlot) {
				got = append(got, fmt.Sprintf("%s-%s/%d", w.Start, w.End, w.Minutes))
			}
			if strings.Join(got, " ") != tc.want {
				t.Errorf("free windows = %q, want %q", strings.Join(got, " "), tc.want)
			}
		})
	}
}

// TestCalendarZone: the timezone comes from --timezone, CALENDAR_TZ, TZ,
// then the zoneinfo file /etc/localtime links to, and a missing one is an
// error rather than a guess.
func TestCalendarZone(t *testing.T) {
	dir := t.TempDir()
	zoneinfo := dir + "/usr/share/zoneinfo/Asia"
	if err := os.MkdirAll(zoneinfo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zoneinfo+"/Tokyo", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := dir + "/localtime"
	if err := os.Symlink(zoneinfo+"/Tokyo", link); err != nil {
		t.Fatal(err)
	}
	prev := zoneinfoLink
	t.Cleanup(func() { zoneinfoLink = prev })
	cases := []struct {
		name, flag string
		env        map[string]string
		link       string
		want       string
	}{
		{"the flag", "America/New_York", map[string]string{"CALENDAR_TZ": "Europe/Zurich"}, link, "America/New_York"},
		{"CALENDAR_TZ", "", map[string]string{"CALENDAR_TZ": "Europe/Zurich", "TZ": "Asia/Kolkata"}, link, "Europe/Zurich"},
		{"TZ", "", map[string]string{"TZ": ":Asia/Kolkata"}, link, "Asia/Kolkata"},
		{"the system's", "", nil, link, "Asia/Tokyo"},
		{"none", "", nil, dir + "/missing", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			zoneinfoLink = tc.link
			loc, err := calendarZone(tc.flag, IO{Getenv: func(k string) string { return tc.env[k] }, Stderr: io.Discard})
			switch {
			case tc.want == "" && (err == nil || !strings.Contains(err.Error(), "CALENDAR_TZ")):
				t.Errorf("zone = %v, %v; want an error naming CALENDAR_TZ", loc, err)
			case tc.want != "" && (err != nil || loc.String() != tc.want):
				t.Errorf("zone = %v, %v; want %s", loc, err, tc.want)
			}
		})
	}
}
