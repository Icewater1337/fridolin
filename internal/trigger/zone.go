package trigger

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// zones caches the locations Zone loaded, by name: every tick of the
// schedulers asks for the owner's zone.
var zones sync.Map

// LoadZone is the IANA time zone name ("Europe/Zurich"), or an error that
// says what to write instead. "UTC" and "Local" load as Go has them.
func LoadZone(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("a time zone needs its IANA name, such as Europe/Zurich")
	}
	if loc, ok := zones.Load(name); ok {
		return loc.(*time.Location), nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q: use an IANA name such as Europe/Zurich", name)
	}
	zones.Store(name, loc)
	return loc, nil
}

// Zone is the location of name, else, for an empty or unknown name, the
// process's own zone (time.Local, which TZ sets): the owner's time zone
// as the bridge reads its schedules in. Config validation refuses an
// unknown name, so the fallback is only an unset one.
func Zone(name string) *time.Location {
	if name == "" {
		return time.Local
	}
	loc, err := LoadZone(name)
	if err != nil {
		return time.Local
	}
	return loc
}

// wallTime is the instant the wall clock in loc shows hh:mm on y-mo-d,
// across daylight saving changes:
//
//   - a time the clocks skip (spring forward, 02:30 on the night they jump
//     from 02:00 to 03:00) is the next minute that exists, the moment
//     they jump to (03:00);
//   - a time the clocks show twice (fall back, 02:30 when they go from
//     03:00 back to 02:00) is its first showing, so a schedule there runs
//     once.
//
// time.Date leaves both to chance ("the choice of zone is not
// guaranteed"); a schedule needs it decided.
func wallTime(y int, mo time.Month, d, hh, mm int, loc *time.Location) time.Time {
	t := time.Date(y, mo, d, hh, mm, 0, 0, loc)
	want := time.Date(y, mo, d, hh, mm, 0, 0, time.UTC)
	got := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)
	if !got.Equal(want) {
		// In a gap: time.Date read it with one side's offset. The next
		// minute that exists is the change itself, the start of the
		// period t fell in when it came out late, the end of it when
		// early.
		start, end := t.ZoneBounds()
		if got.After(want) {
			return start
		}
		return end
	}
	// Shown twice: the same wall clock an offset change earlier is the
	// first showing.
	start, _ := t.ZoneBounds()
	if !start.IsZero() {
		_, before := start.Add(-time.Second).Zone()
		_, now := t.Zone()
		if before > now {
			if e := t.Add(-time.Duration(before-now) * time.Second); e.Before(start) && e.Hour() == hh && e.Minute() == mm {
				return e
			}
		}
	}
	return t
}
