package trigger

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The kinds of Schedule that only scheduled agents take (ParseRecurring):
// a standing intent is once, daily or weekly (ParseSchedule).
const (
	// ScheduleEvery falls due a fixed interval after it last ran:
	// "every 6h", "every 30m", "every 2d".
	ScheduleEvery ScheduleKind = "every"
	// ScheduleCron falls due at the minutes a five-field cron expression
	// names: "cron 0 7 * * 1-5".
	ScheduleCron ScheduleKind = "cron"
)

// Bounds of an every schedule.
const (
	MinEvery = 5 * time.Minute
	MaxEvery = 366 * 24 * time.Hour
)

// ParseRecurring reads the schedule of a scheduled agent, in loc: what
// ParseSchedule reads but a single time, and
//
//	every 6h            every six hours (m, h and d are the units)
//	cron 0 7 * * 1-5    at 07:00 on weekdays (minute hour day month weekday)
//
// A bare five-field cron expression reads as one too.
func ParseRecurring(s string, loc *time.Location) (Schedule, error) {
	f := strings.Fields(strings.ToLower(strings.TrimSpace(s)))
	switch {
	case len(f) == 2 && f[0] == "every":
		sc := Schedule{Kind: ScheduleEvery, Every: f[1]}
		return sc, sc.Validate()
	case len(f) == 6 && f[0] == "cron":
		sc := Schedule{Kind: ScheduleCron, Cron: strings.Join(f[1:], " ")}
		return sc, sc.Validate()
	case len(f) == 5:
		sc := Schedule{Kind: ScheduleCron, Cron: strings.Join(f, " ")}
		return sc, sc.Validate()
	}
	sc, err := ParseSchedule(s, loc)
	if err != nil {
		return Schedule{}, fmt.Errorf("schedule %q: want \"every 6h\", \"daily HH:MM\", \"weekly DAY HH:MM\" or a cron expression (\"cron 0 7 * * 1-5\")", s)
	}
	if sc.Kind == ScheduleOnce {
		return Schedule{}, fmt.Errorf("schedule %q: a scheduled agent repeats; for a single time, ask the brain for a standing intent", s)
	}
	return sc, nil
}

// ParseEvery reads an every schedule's interval: a whole number and a
// unit, m, h or d.
func ParseEvery(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return 0, fmt.Errorf("interval %q must be a number and a unit: 30m, 6h, 2d", s)
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	unit := map[byte]time.Duration{'m': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour}[s[len(s)-1]]
	if err != nil || unit == 0 || n <= 0 || n > int(MaxEvery/unit) {
		return 0, fmt.Errorf("interval %q must be a number and a unit: 30m, 6h, 2d", s)
	}
	d := time.Duration(n) * unit
	if d < MinEvery || d > MaxEvery {
		return 0, fmt.Errorf("interval %q must be between %v and 366 days", s, MinEvery)
	}
	return d, nil
}

// cronSpec is a parsed cron expression: the values each field allows.
type cronSpec struct {
	minute, hour, dom, month, dow [64]bool
	// domAny and dowAny are "*" in those fields: with both restricted, a
	// day matches either, as cron has it.
	domAny, dowAny bool
}

var cronFields = []struct {
	name     string
	min, max int
}{
	{"minute", 0, 59}, {"hour", 0, 23}, {"day of month", 1, 31}, {"month", 1, 12}, {"day of week", 0, 7},
}

// parseCron reads five fields: numbers, *, ranges a-b, lists a,b and
// steps */n or a-b/n; weekdays may also be mon…sun, and 7 is Sunday too.
func parseCron(expr string) (*cronSpec, error) {
	f := strings.Fields(strings.ToLower(expr))
	if len(f) != 5 {
		return nil, fmt.Errorf("cron expression %q must have five fields: minute hour day-of-month month day-of-week", expr)
	}
	c := &cronSpec{domAny: f[2] == "*", dowAny: f[4] == "*"}
	sets := []*[64]bool{&c.minute, &c.hour, &c.dom, &c.month, &c.dow}
	for i, field := range f {
		spec := cronFields[i]
		for part := range strings.SplitSeq(field, ",") {
			if err := cronPart(part, spec.min, spec.max, i == 4, sets[i]); err != nil {
				return nil, fmt.Errorf("cron expression %q: %s %q: %v", expr, spec.name, part, err)
			}
		}
	}
	if c.dow[7] {
		c.dow[0] = true
	}
	return c, nil
}

func cronPart(part string, lo, hi int, weekday bool, set *[64]bool) error {
	rng, stepText, hasStep := strings.Cut(part, "/")
	step := 1
	if hasStep {
		n, err := strconv.Atoi(stepText)
		if err != nil || n < 1 || n > hi {
			return fmt.Errorf("step must be between 1 and %d", hi)
		}
		step = n
	}
	from, to := lo, hi
	if rng != "*" {
		a, b, isRange := strings.Cut(rng, "-")
		x, err := cronValue(a, lo, hi, weekday)
		if err != nil {
			return err
		}
		from, to = x, x
		if isRange {
			if to, err = cronValue(b, lo, hi, weekday); err != nil {
				return err
			}
			if to < from {
				return fmt.Errorf("range runs backwards")
			}
		} else if hasStep {
			to = hi
		}
	}
	for v := from; v <= to; v += step {
		set[v] = true
	}
	return nil
}

func cronValue(s string, lo, hi int, weekday bool) (int, error) {
	if weekday {
		if d, ok := weekdays[dayName(s)]; ok && len(s) >= 3 {
			return int(d), nil
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("must be a number between %d and %d", lo, hi)
	}
	return n, nil
}

// dayMatches reports whether t's day is one the expression runs on.
func (c *cronSpec) dayMatches(t time.Time) bool {
	dom, dow := c.dom[t.Day()], c.dow[int(t.Weekday())]
	switch {
	case c.domAny && c.dowAny:
		return true
	case c.domAny:
		return dow
	case c.dowAny:
		return dom
	}
	return dom || dow
}

// next is the first minute after after that the expression names, in
// after's zone, looking at most five years ahead (31 February never
// comes).
func (c *cronSpec) next(after time.Time) (time.Time, bool) {
	y, mo, d := after.Date()
	for i := 0; i <= 5*366; i++ {
		day := time.Date(y, mo, d+i, 0, 0, 0, 0, after.Location())
		if !c.month[int(day.Month())] || !c.dayMatches(day) {
			continue
		}
		for h := 0; h < 24; h++ {
			if !c.hour[h] {
				continue
			}
			for m := 0; m < 60; m++ {
				if !c.minute[m] {
					continue
				}
				t := wallTime(day.Year(), day.Month(), day.Day(), h, m, after.Location())
				// A wall time a DST change skips is the moment the
				// clocks jump to, one it repeats its first showing
				// (wallTime); keep to the day.
				if t.After(after) && t.Day() == day.Day() {
					return t, true
				}
			}
		}
	}
	return time.Time{}, false
}

// canonicalCron is expr with its fields single-spaced and lower case.
func canonicalCron(expr string) string {
	return strings.Join(strings.Fields(strings.ToLower(expr)), " ")
}

// recurringNext is Next for the every and cron kinds.
func (s Schedule) recurringNext(after time.Time) (time.Time, bool) {
	switch s.Kind {
	case ScheduleEvery:
		d, err := ParseEvery(s.Every)
		if err != nil {
			return time.Time{}, false
		}
		return after.Add(d), true
	case ScheduleCron:
		c, err := parseCron(s.Cron)
		if err != nil {
			return time.Time{}, false
		}
		return c.next(after)
	}
	return time.Time{}, false
}

// Recurring reports whether s repeats.
func (s Schedule) Recurring() bool {
	return slices.Contains([]ScheduleKind{ScheduleDaily, ScheduleWeekly, ScheduleEvery, ScheduleCron}, s.Kind)
}
