package cloud

import (
	"errors"
	"maps"
	"math"
	"strings"
	"sync"
	"time"
)

// Backblaze B2 bills, and on a free account caps, its API calls by class
// (https://www.backblaze.com/cloud-storage/transaction-pricing):
//
//   - Class A (uploads, deletes, hides, starting large files) is free and
//     unlimited;
//   - Class B (downloads, HEAD and b2_get_file_info) is capped at 2,500 a
//     day on the free tier;
//   - Class C (b2_authorize_account, every listing, b2_list_buckets) is
//     capped at 2,500 a day too.
//
// The caps reset at midnight UTC; a call past one is refused with 403
// transaction_cap_exceeded, b2_authorize_account included, so a capped
// account cannot even log in until the reset.
//
// Usage counts the calls this process makes, by class, per UTC day. The
// counts are what the stores know they cost, not B2's own numbers: each
// store adds what one of its operations costs on B2 (see the cost model of
// Rclone and RcloneRC), and the vault adds restic's and Litestream's by the
// measured cost of their runs. Doctor and the dashboard compare them with
// a daily budget.

// Class is a B2 transaction class.
type Class string

const (
	ClassA Class = "A"
	ClassB Class = "B"
	ClassC Class = "C"
)

// Usage counts B2 transactions per UTC day. Its zero value is not ready;
// make one with NewUsage. A nil *Usage counts nothing, so stores need not
// check for one.
type Usage struct {
	mu  sync.Mutex
	now func() time.Time
	day UsageDay
	// prev is the day before, kept so a status just after midnight still
	// shows what used up the caps.
	prev *UsageDay
}

// UsageDay is one UTC day's counts.
type UsageDay struct {
	Day string `json:"day"` // 2006-01-02, UTC
	// Classes are the counts of A, B and C. They are float64 so that
	// a rate (Litestream's steady state) adds up minute by minute; shown,
	// they are rounded.
	Classes map[Class]float64 `json:"classes"`
	// Ops are the counts per class and operation, "C lease:get" and the
	// like, which say where they went.
	Ops map[string]float64 `json:"ops,omitempty"`
	// CapExceeded is the last time B2 refused a call for a cap this day,
	// and CapError what it said.
	CapExceeded time.Time `json:"cap_exceeded,omitzero"`
	CapError    string    `json:"cap_error,omitempty"`
	// LastOK is the last time this day a counted store's call to B2
	// succeeded: a refusal before it was a cap since lifted (the owner
	// raised it), one after it still holds.
	LastOK time.Time `json:"last_ok,omitzero"`
}

// Count is the rounded count of class c.
func (d UsageDay) Count(c Class) int64 { return int64(math.Round(d.Classes[c])) }

// NewUsage returns an empty Usage on the real clock.
func NewUsage() *Usage { return NewUsageAt(time.Now) }

// NewUsageAt returns an empty Usage on the clock now.
func NewUsageAt(now func() time.Time) *Usage {
	u := &Usage{now: now}
	u.day = newDay(dayOf(now()))
	return u
}

func dayOf(t time.Time) string { return t.UTC().Format(time.DateOnly) }

func newDay(day string) UsageDay {
	return UsageDay{Day: day, Classes: map[Class]float64{}, Ops: map[string]float64{}}
}

// rollLocked starts a new day when the date changed; the caller holds mu.
func (u *Usage) rollLocked() {
	today := dayOf(u.now())
	if u.day.Day == today {
		return
	}
	prev := u.day
	u.prev = &prev
	u.day = newDay(today)
}

// Add counts n transactions of class c for the operation op ("lease:get",
// "restic:backup", ...).
func (u *Usage) Add(c Class, op string, n float64) {
	if u == nil || n == 0 {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.rollLocked()
	u.day.Classes[c] += n
	u.day.Ops[string(c)+" "+op] += n
}

// AddCost counts one operation's cost.
func (u *Usage) AddCost(op string, cost Cost) {
	u.Add(ClassA, op, cost.A)
	u.Add(ClassB, op, cost.B)
	u.Add(ClassC, op, cost.C)
}

// NoteError records err when it is B2 refusing a call for a cap.
func (u *Usage) NoteError(err error) {
	if u == nil || !IsCapExceeded(err) {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.rollLocked()
	u.day.CapExceeded = u.now().UTC()
	msg := err.Error()
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	u.day.CapError = msg
}

// NoteOK records that a call to B2 succeeded now.
func (u *Usage) NoteOK() {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.rollLocked()
	u.day.LastOK = u.now().UTC()
}

// Today returns a copy of today's counts, and of the day before when this
// process counted one.
func (u *Usage) Today() (today UsageDay, prev *UsageDay) {
	if u == nil {
		return newDay(dayOf(time.Now())), nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.rollLocked()
	today = copyDay(u.day)
	if u.prev != nil {
		p := copyDay(*u.prev)
		prev = &p
	}
	return today, prev
}

func copyDay(d UsageDay) UsageDay {
	d.Classes = maps.Clone(d.Classes)
	d.Ops = maps.Clone(d.Ops)
	if d.Classes == nil {
		d.Classes = map[Class]float64{}
	}
	return d
}

// Restore takes up counts an earlier process kept (the bridge restarted):
// today's add to what this one counted, an earlier day becomes the day
// before.
func (u *Usage) Restore(today UsageDay, prev *UsageDay) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.rollLocked()
	for _, d := range []*UsageDay{prev, &today} {
		if d == nil || d.Day == "" {
			continue
		}
		switch {
		case d.Day == u.day.Day:
			for c, n := range d.Classes {
				u.day.Classes[c] += n
			}
			for op, n := range d.Ops {
				u.day.Ops[op] += n
			}
			if d.CapExceeded.After(u.day.CapExceeded) {
				u.day.CapExceeded, u.day.CapError = d.CapExceeded, d.CapError
			}
			if d.LastOK.After(u.day.LastOK) {
				u.day.LastOK = d.LastOK
			}
		case d.Day < u.day.Day && (u.prev == nil || d.Day >= u.prev.Day):
			p := copyDay(*d)
			u.prev = &p
		}
	}
}

// Cost is what one operation costs in each class.
type Cost struct{ A, B, C float64 }

// Plus adds two costs.
func (c Cost) Plus(o Cost) Cost { return Cost{c.A + o.A, c.B + o.B, c.C + o.C} }

// Times multiplies a cost.
func (c Cost) Times(n float64) Cost { return Cost{c.A * n, c.B * n, c.C * n} }

// IsCapExceeded reports whether err is Backblaze refusing a call because a
// daily cap of the account is used up (403 transaction_cap_exceeded, or
// download_cap_exceeded for the download bandwidth cap).
func IsCapExceeded(err error) bool {
	if err == nil {
		return false
	}
	var ce *CapError
	if errors.As(err, &ce) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "transaction_cap_exceeded") || strings.Contains(s, "transaction cap exceeded") ||
		strings.Contains(s, "download_cap_exceeded") || strings.Contains(s, "download cap exceeded")
}

// CapError is an error the stores return for a cap-exceeded refusal, so
// callers can tell it from the store being down.
type CapError struct{ Err error }

func (e *CapError) Error() string { return e.Err.Error() }
func (e *CapError) Unwrap() error { return e.Err }

// CapFix is what a person does about a cap-exceeded refusal.
const CapFix = "Backblaze B2 refused a call because a daily transaction cap of the account is used up (it resets at 00:00 UTC): " +
	"raise the caps under B2 → Caps & Alerts, or lower the vault's activity"
