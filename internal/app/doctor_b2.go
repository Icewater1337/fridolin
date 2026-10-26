package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/doctor"
)

// The B2 transactions check: what the running bridge counted today in
// core/b2-usage.json (b2usage.go) against vault.b2_daily_budget, and any
// refusal of Backblaze for a used-up cap, in the counts or in what the
// lease and the vault last said. A refusal that a successful call came
// after is a cap the owner raised since: it warns, and does not fail. It
// applies with the lease or the vault on.
var b2DoctorChecks = []doctor.Check{
	{ID: "b2-transactions", Title: "B2 transactions within the daily caps", Required: true, Applies: b2On, Run: checkB2Transactions},
}

func b2On(env *doctor.Env) bool {
	return env.Config != nil && (env.Config.Vault.Enabled || env.Config.Core.Lease.Enabled)
}

func checkB2Transactions(_ context.Context, env *doctor.Env) doctor.Result {
	budget := env.Config.Vault.B2DailyBudget
	today := env.Time().UTC().Format("2006-01-02")
	rec, err := ReadB2UsageFile(env.Config.DataDir, env.ReadFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return doctor.Result{Status: doctor.Fail, Detail: err.Error()}
	}
	counted := err == nil && rec.Today.Day == today
	var day cloud.UsageDay
	if counted {
		day = rec.Today
	}
	ev := b2EvidenceOf(env, day)
	detail := "no B2 transactions counted today (UTC)"
	if counted {
		detail = fmt.Sprintf("today (UTC) class B %d, class C %d of a daily budget of %d each; class A (free) %d",
			day.Count(cloud.ClassB), day.Count(cloud.ClassC), budget, day.Count(cloud.ClassA))
	}
	// A refusal for a cap is a failure wherever it shows, in the counts or
	// in the last error of the lease or the vault, while no call to B2
	// succeeded after it.
	if r, ok := ev.active(); ok {
		if r.fromUsage {
			return doctor.Result{Status: doctor.Fail, Detail: fmt.Sprintf("Backblaze B2 refused a call at %s UTC (%s); %s",
				r.at.Format("15:04"), r.msg, detail), Fix: cloud.CapFix}
		}
		return doctor.Result{Status: doctor.Fail, Detail: "Backblaze B2 refused a call: " + r.msg, Fix: cloud.CapFix}
	}
	// A refusal calls succeeded after is said, and only warns.
	lifted := ""
	if r, ok := ev.lifted(today); ok {
		lifted = fmt.Sprintf("Backblaze B2 refused a call at %s UTC (%s); calls since succeed (the last at %s UTC); ",
			r.at.Format("15:04"), r.msg, ev.lastOK.Format("15:04"))
	}
	for _, c := range []cloud.Class{cloud.ClassB, cloud.ClassC} {
		if counted && budget > 0 && day.Count(c)*100 >= int64(budget)*b2WarnPercent {
			return doctor.Result{Status: doctor.Warn, Detail: fmt.Sprintf("%s%s: class %s is over %d%% of the budget (most: %s)", lifted, detail, c, b2WarnPercent, topOps(day, c, 3)),
				Fix: "B2's free tier refuses every call past 2,500 a class until midnight UTC: raise the caps under B2 → Caps & Alerts, or lower the vault's activity"}
		}
	}
	if lifted != "" {
		return doctor.Result{Status: doctor.Warn, Detail: lifted + detail,
			Fix: "nothing to do if the caps were raised under B2 → Caps & Alerts; this note goes at 00:00 UTC"}
	}
	return doctor.Result{Status: doctor.OK, Detail: detail}
}

// b2Refusal is a cap-exceeded error of B2 and when it came; a zero time
// is a record that does not say, which counts as holding still.
type b2Refusal struct {
	msg       string
	at        time.Time
	fromUsage bool // today's counts recorded it
}

// b2Evidence is what the bridge recorded about its B2 calls: the cap
// refusals, and the last time a call succeeded.
type b2Evidence struct {
	refusals []b2Refusal
	lastOK   time.Time
}

// active is the first refusal that no successful call came after.
func (ev b2Evidence) active() (b2Refusal, bool) {
	for _, r := range ev.refusals {
		if r.at.IsZero() || !ev.lastOK.After(r.at) {
			return r, true
		}
	}
	return b2Refusal{}, false
}

// lifted is the latest refusal on day (UTC); call it once active found
// none, so calls succeeded after each.
func (ev b2Evidence) lifted(day string) (b2Refusal, bool) {
	var out b2Refusal
	for _, r := range ev.refusals {
		if r.at.Format("2006-01-02") == day && r.at.After(out.at) {
			out = r
		}
	}
	return out, !out.at.IsZero()
}

// b2EvidenceOf reads the refusals and the successes from today's counts
// (day, zero for none) and the lease's and the vault's state files.
func b2EvidenceOf(env *doctor.Env, day cloud.UsageDay) b2Evidence {
	var ev b2Evidence
	ok := func(at time.Time) {
		if at.After(ev.lastOK) {
			ev.lastOK = at.UTC()
		}
	}
	refused := func(msg string, at time.Time) {
		if msg == "" || !cloud.IsCapExceeded(errors.New(msg)) {
			return
		}
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		ev.refusals = append(ev.refusals, b2Refusal{msg: strings.TrimSpace(msg), at: at.UTC()})
	}
	// record is a record of one B2 job: a success when it has no error.
	record := func(msg string, at time.Time) {
		if msg == "" {
			ok(at)
		} else {
			refused(msg, at)
		}
	}
	if !day.CapExceeded.IsZero() {
		ev.refusals = append(ev.refusals, b2Refusal{msg: day.CapError, at: day.CapExceeded.UTC(), fromUsage: true})
	}
	ok(day.LastOK)
	var ls leaseStateRecord
	if found, _ := readCoreState(env, leaseStateFile, &ls); found {
		switch {
		case ls.Error != "":
			refused(ls.Error, ls.UpdatedAt)
		case ls.RenewedAt != nil:
			ok(*ls.RenewedAt)
		}
	}
	if data, err := ReadVaultStatusFile(env.Config.DataDir, env.ReadFile); err == nil {
		if st, err := parseVaultStatus(data); err == nil {
			// The vault's own error is the state it is in: it holds.
			refused(st.Error, time.Time{})
			refused(st.Litestream.LastError, st.Litestream.LastExit)
			if r := st.Reachable; r != nil {
				if r.OK && r.Error == "" {
					ok(r.At)
				} else {
					refused(r.Error, r.At)
				}
			}
			if r := st.LastSync; r != nil {
				record(r.Error, r.At)
			}
			for _, r := range []*snapshotRecord{st.Snapshots.LastNightly, st.Snapshots.LastHourly} {
				if r != nil {
					record(r.Error, r.At)
				}
			}
		}
	}
	return ev
}
