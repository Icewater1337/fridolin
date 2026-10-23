// Package sched is the scheduled agents' own logic (docs/BRAINS.md,
// "Scheduled agents"): when an agent falls due, with its jitter and what
// a run missed while the bridge was down does; the message a brain run is
// handed; the built-in templates; and the store of each agent's state,
// run history and run logs in the data dir. internal/app runs them.
package sched

import (
	"hash/fnv"
	"time"

	"github.com/Icewater1337/fridolin/internal/trigger"
)

// Offset is the agent's jitter: a fixed offset within jitter, the same
// for every run of the agent, so that agents due at the same time start
// spread over it, and one agent keeps its rhythm.
func Offset(brain, name string, jitter time.Duration) time.Duration {
	if jitter <= 0 {
		return 0
	}
	h := fnv.New64a()
	h.Write([]byte(brain + "/" + name))
	return time.Duration(h.Sum64() % uint64(jitter/time.Second+1) * uint64(time.Second))
}

// Missed is how late a run may start and still count as on time: the
// bridge looks every 30 seconds, and a busy machine may be slower.
const Missed = 5 * time.Minute

// Decision is what an agent does now.
type Decision struct {
	// Run starts a run; Late says it is one that fell due while the
	// bridge was down (or Missed ago).
	Run, Late bool
	// Skipped is a late run that its missed policy leaves out.
	Skipped bool
	// Last is the agent's new anchor: the scheduled time of the slot just
	// taken or skipped, the time of the one before when none is.
	Last time.Time
	// Next is when it falls due after that, with its offset; zero for
	// never.
	Next time.Time
}

// Decide says what an agent on schedule sc, last anchored at last, with
// offset off, does at now. skipMissed leaves out a run that is late. A
// zero last is an agent seen for the first time: it starts counting from
// now, so that one switched on does not run at once for times before.
func Decide(sc trigger.Schedule, last time.Time, off time.Duration, skipMissed bool, now time.Time) Decision {
	if last.IsZero() {
		last = now.Add(-off)
	}
	d := Decision{Last: last}
	slot, ok := sc.Next(last)
	if !ok {
		return d
	}
	if slot.Add(off).After(now) {
		d.Next = slot.Add(off)
		return d
	}
	// Due: take the latest slot that has come, once, however many came.
	latest := latestSlot(sc, slot, now.Add(-off))
	d.Last = latest
	d.Late = now.Sub(latest.Add(off)) > Missed
	d.Run = !(d.Late && skipMissed)
	d.Skipped = !d.Run
	if n, ok := sc.Next(latest); ok {
		d.Next = n.Add(off)
	}
	return d
}

// latestSlot is the last time of sc from slot on that is no later than
// by.
func latestSlot(sc trigger.Schedule, slot, by time.Time) time.Time {
	if sc.Kind == trigger.ScheduleEvery {
		if every, err := trigger.ParseEvery(sc.Every); err == nil && !by.Before(slot) {
			return slot.Add(by.Sub(slot) / every * every)
		}
		return slot
	}
	for range 10000 {
		n, ok := sc.Next(slot)
		if !ok || n.After(by) {
			return slot
		}
		slot = n
	}
	return slot
}

// NextAfter is when an agent anchored at last falls due next, with its
// offset, as Decide would say; zero for never.
func NextAfter(sc trigger.Schedule, last time.Time, off time.Duration) time.Time {
	n, ok := sc.Next(last)
	if !ok {
		return time.Time{}
	}
	return n.Add(off)
}
