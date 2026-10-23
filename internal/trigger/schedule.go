package trigger

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is when a standing intent or a brain's scheduled job falls
// due, in the bridge's time zone: once at a time, every day at a time of
// day, or every week on a weekday at a time of day. It is the time half of
// prospective memory, as List is the keyword half: "remind me on Friday at
// 9" is a Schedule, "remind me when Sam mentions the boat" a List.
type Schedule struct {
	Kind ScheduleKind `json:"kind"`
	// At is the time of a ScheduleOnce, RFC 3339.
	At time.Time `json:"at,omitzero"`
	// Day is the weekday of a ScheduleWeekly: mon, tue, …, sun.
	Day string `json:"day,omitempty"`
	// Time is the time of day of ScheduleDaily and ScheduleWeekly, HH:MM
	// on a 24-hour clock.
	Time string `json:"time,omitempty"`
	// Every is the interval of a ScheduleEvery: "30m", "6h", "2d".
	Every string `json:"every,omitempty"`
	// Cron is the five-field expression of a ScheduleCron.
	Cron string `json:"cron,omitempty"`
}

// ScheduleKind is what kind of Schedule it is.
type ScheduleKind string

// The kinds of Schedule.
const (
	ScheduleOnce   ScheduleKind = "once"
	ScheduleDaily  ScheduleKind = "daily"
	ScheduleWeekly ScheduleKind = "weekly"
)

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// ParseSchedule reads a schedule as people and brains write it, in loc:
//
//	2026-10-23T09:00   once, at that time (RFC 3339 with an offset works too)
//	daily 08:00        every day at 08:00
//	weekly sun 18:00   every Sunday at 18:00 ("sun 18:00" alone means the same)
//
// Weekdays are their first three letters or their whole English names, in
// any case.
func ParseSchedule(s string, loc *time.Location) (Schedule, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, strings.TrimSpace(s), loc); err == nil {
			return Schedule{Kind: ScheduleOnce, At: t}, nil
		}
	}
	f := strings.Fields(strings.ToLower(strings.TrimSpace(s)))
	switch {
	case len(f) == 2 && f[0] == "daily":
		sc := Schedule{Kind: ScheduleDaily, Time: f[1]}
		return sc, sc.Validate()
	case len(f) == 2 && f[0] != "weekly":
		sc := Schedule{Kind: ScheduleWeekly, Day: dayName(f[0]), Time: f[1]}
		return sc, sc.Validate()
	case len(f) == 3 && f[0] == "weekly":
		sc := Schedule{Kind: ScheduleWeekly, Day: dayName(f[1]), Time: f[2]}
		return sc, sc.Validate()
	}
	return Schedule{}, fmt.Errorf("schedule %q: want a time (2026-10-23T09:00), \"daily HH:MM\" or \"weekly DAY HH:MM\"", s)
}

// dayName is a weekday's three-letter name, or d as it was when it is no
// weekday (Validate says so).
func dayName(d string) string {
	if len(d) >= 3 {
		if _, ok := weekdays[d[:3]]; ok && strings.HasPrefix(fullDay(d[:3]), d) {
			return d[:3]
		}
	}
	return d
}

func fullDay(short string) string {
	return strings.ToLower(weekdays[short].String())
}

// clock is a time of day, HH:MM, as hour and minute.
func clock(s string) (int, int, error) {
	h, m, ok := strings.Cut(s, ":")
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if !ok || err1 != nil || err2 != nil || len(m) != 2 || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, 0, fmt.Errorf("time %q must be HH:MM on a 24-hour clock", s)
	}
	return hh, mm, nil
}

// Validate checks that the schedule is one the bridge can run.
func (s Schedule) Validate() error {
	switch s.Kind {
	case ScheduleOnce:
		if s.At.IsZero() {
			return fmt.Errorf("a once schedule needs its time")
		}
		return nil
	case ScheduleWeekly:
		if _, ok := weekdays[s.Day]; !ok {
			return fmt.Errorf("weekday %q must be one of mon, tue, wed, thu, fri, sat, sun", s.Day)
		}
		fallthrough
	case ScheduleDaily:
		_, _, err := clock(s.Time)
		return err
	case ScheduleEvery:
		_, err := ParseEvery(s.Every)
		return err
	case ScheduleCron:
		_, err := parseCron(s.Cron)
		return err
	}
	return fmt.Errorf("schedule kind %q must be once, daily, weekly, every or cron", s.Kind)
}

// Next is the first time after after at which s falls due, in after's
// time zone, and false when it never does again (a once schedule whose
// time has passed).
func (s Schedule) Next(after time.Time) (time.Time, bool) {
	switch s.Kind {
	case ScheduleOnce:
		return s.At, s.At.After(after)
	case ScheduleEvery, ScheduleCron:
		return s.recurringNext(after)
	case ScheduleDaily, ScheduleWeekly:
		hh, mm, err := clock(s.Time)
		if err != nil {
			return time.Time{}, false
		}
		y, mo, d := after.Date()
		// Eight days cover any weekday after any time; time.Date
		// normalizes the day and keeps the wall clock across DST changes.
		for i := 0; i <= 8; i++ {
			t := time.Date(y, mo, d+i, hh, mm, 0, 0, after.Location())
			if !t.After(after) {
				continue
			}
			if s.Kind == ScheduleWeekly && t.Weekday() != weekdays[s.Day] {
				continue
			}
			return t, true
		}
	}
	return time.Time{}, false
}

// Due reports whether s fell due after last and by now: once, however
// many times it would have since (a bridge that was off for a week runs a
// daily job once, not seven times).
func (s Schedule) Due(last, now time.Time) bool {
	next, ok := s.Next(last)
	return ok && !next.After(now)
}

// String is s as ParseSchedule reads it.
func (s Schedule) String() string {
	switch s.Kind {
	case ScheduleOnce:
		return s.At.Format("2006-01-02T15:04")
	case ScheduleDaily:
		return "daily " + s.Time
	case ScheduleWeekly:
		return "weekly " + s.Day + " " + s.Time
	case ScheduleEvery:
		return "every " + s.Every
	case ScheduleCron:
		return "cron " + canonicalCron(s.Cron)
	}
	return string(s.Kind)
}

// UnmarshalJSON refuses a schedule the bridge could not run.
func (s *Schedule) UnmarshalJSON(data []byte) error {
	type plain Schedule
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	if err := Schedule(p).Validate(); err != nil {
		return err
	}
	*s = Schedule(p)
	return nil
}
