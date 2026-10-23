package housekeeping

import (
	"slices"
	"testing"
	"time"
)

func TestScheduleRunsToDrop(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return now.Add(-time.Duration(n) * 24 * time.Hour) }
	runs := map[string][]ScheduleRun{
		"a/x": {{Agent: "a/x", ID: "1", Started: day(40), Bytes: 10}, {Agent: "a/x", ID: "2", Started: day(5), Bytes: 10}, {Agent: "a/x", ID: "3", Started: day(1), Bytes: 10}},
		// Only one run, and old: the newest always stays.
		"a/y": {{Agent: "a/y", ID: "9", Started: day(100), Bytes: 500}},
	}
	ids := func(rs []ScheduleRun) []string {
		var out []string
		for _, r := range rs {
			out = append(out, r.ID)
		}
		slices.Sort(out)
		return out
	}
	if got := ids(ScheduleRunsToDrop(runs, ScheduleRetention{KeepDays: 30}, now)); !slices.Equal(got, []string{"1"}) {
		t.Errorf("by age: %v", got)
	}
	if got := ids(ScheduleRunsToDrop(runs, ScheduleRetention{KeepRuns: 1}, now)); !slices.Equal(got, []string{"1", "2"}) {
		t.Errorf("by count: %v", got)
	}
	// 510 bytes for the newest runs alone: the rest goes, oldest first.
	if got := ids(ScheduleRunsToDrop(runs, ScheduleRetention{MaxTotalBytes: 515}, now)); !slices.Equal(got, []string{"1", "2"}) {
		t.Errorf("by size: %v", got)
	}
	if got := ids(ScheduleRunsToDrop(runs, ScheduleRetention{MaxTotalBytes: 525}, now)); !slices.Equal(got, []string{"1"}) {
		t.Errorf("by size, room for one: %v", got)
	}
	if got := ScheduleRunsToDrop(runs, ScheduleRetention{}, now); len(got) != 0 {
		t.Errorf("no limits: %v", got)
	}
}
