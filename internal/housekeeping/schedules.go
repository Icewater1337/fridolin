package housekeeping

import (
	"cmp"
	"slices"
	"time"
)

// KindScheduleRuns is the scheduled agents' run history and logs beyond
// their limits (schedules.keep_days, keep_runs, max_total_mb).
const KindScheduleRuns = "schedule-runs"

// ScheduleRun is one run of a scheduled agent on disk: its line in the
// agent's history and its log.
type ScheduleRun struct {
	// Agent is the agent's key, brain/agent.
	Agent, ID string
	Started   time.Time
	Bytes     int64
}

// ScheduleRetention is how much of the history is kept.
type ScheduleRetention struct {
	// KeepDays drops runs older than this.
	KeepDays int
	// KeepRuns keeps at most this many runs of each agent.
	KeepRuns int
	// MaxTotalBytes drops the oldest runs of all agents, together, until
	// their logs take no more than this.
	MaxTotalBytes int64
}

// ScheduleRunsToDrop is what keeps the history within r at now: each
// agent's runs beyond KeepRuns or older than KeepDays, then the oldest
// runs of all until the logs fit MaxTotalBytes. Every agent keeps its
// newest run whatever the limits say, so its last outcome stays there to
// see. Zero limits are none.
func ScheduleRunsToDrop(runs map[string][]ScheduleRun, r ScheduleRetention, now time.Time) []ScheduleRun {
	var drop, kept []ScheduleRun
	// budget is what the runs other than the newest may take.
	budget := r.MaxTotalBytes
	cut := now.Add(-time.Duration(r.KeepDays) * 24 * time.Hour)
	agents := make([]string, 0, len(runs))
	for k := range runs {
		agents = append(agents, k)
	}
	slices.Sort(agents)
	for _, agent := range agents {
		list := slices.Clone(runs[agent])
		slices.SortFunc(list, func(a, b ScheduleRun) int { return cmp.Or(b.Started.Compare(a.Started), cmp.Compare(b.ID, a.ID)) })
		for i, run := range list {
			switch {
			case i == 0:
				budget -= run.Bytes // the newest stays, whatever it takes
			case r.KeepRuns > 0 && i >= r.KeepRuns, r.KeepDays > 0 && run.Started.Before(cut):
				drop = append(drop, run)
			default:
				kept = append(kept, run)
			}
		}
	}
	if r.MaxTotalBytes <= 0 {
		return drop
	}
	var total int64
	for _, run := range kept {
		total += run.Bytes
	}
	slices.SortFunc(kept, func(a, b ScheduleRun) int { return cmp.Or(a.Started.Compare(b.Started), cmp.Compare(a.ID, b.ID)) })
	for _, run := range kept {
		if total <= budget {
			break
		}
		drop = append(drop, run)
		total -= run.Bytes
	}
	return drop
}
