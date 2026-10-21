package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/Icewater1337/fridolin/internal/cloud"
)

// The B2 transactions check: what the running bridge counted today in
// core/b2-usage.json (b2usage.go) against vault.b2_daily_budget, and any
// refusal of Backblaze for a used-up cap, in the counts or in what the
// lease and the vault last said. It applies with the lease or the vault on.
var b2DoctorChecks = []DoctorCheck{
	{ID: "b2-transactions", Title: "B2 transactions within the daily caps", Required: true, Applies: b2On, Run: checkB2Transactions},
}

func init() { DoctorRegistry = append(DoctorRegistry, b2DoctorChecks...) }

func b2On(env *DoctorEnv) bool {
	return env.Config != nil && (env.Config.Vault.Enabled || env.Config.Core.Lease.Enabled)
}

func checkB2Transactions(_ context.Context, env *DoctorEnv) DoctorResult {
	budget := env.Config.Vault.B2DailyBudget
	today := env.now().UTC().Format("2006-01-02")
	rec, err := ReadB2UsageFile(env.Config.DataDir, env.ReadFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return DoctorResult{Status: DoctorFail, Detail: err.Error()}
	}
	// A refusal for a cap is a failure wherever it shows: in the counts,
	// or in the last error of the lease or the vault.
	if capErr := env.capErrorSeen(); capErr != "" && (rec.Today.Day != today || rec.Today.CapExceeded.IsZero()) {
		return DoctorResult{Status: DoctorFail, Detail: "Backblaze B2 refused a call: " + capErr, Fix: cloud.CapFix}
	}
	if errors.Is(err, fs.ErrNotExist) || rec.Today.Day != today {
		return DoctorResult{Status: DoctorOK, Detail: "no B2 transactions counted today (UTC)"}
	}
	day := rec.Today
	detail := fmt.Sprintf("today (UTC) class B %d, class C %d of a daily budget of %d each; class A (free) %d",
		day.Count(cloud.ClassB), day.Count(cloud.ClassC), budget, day.Count(cloud.ClassA))
	if !day.CapExceeded.IsZero() {
		return DoctorResult{Status: DoctorFail, Detail: fmt.Sprintf("Backblaze B2 refused a call at %s UTC (%s); %s",
			day.CapExceeded.Format("15:04"), day.CapError, detail), Fix: cloud.CapFix}
	}
	for _, c := range []cloud.Class{cloud.ClassB, cloud.ClassC} {
		if budget > 0 && day.Count(c)*100 >= int64(budget)*b2WarnPercent {
			return DoctorResult{Status: DoctorWarn, Detail: fmt.Sprintf("%s: class %s is over %d%% of the budget (most: %s)", detail, c, b2WarnPercent, topOps(day, c, 3)),
				Fix: "B2's free tier refuses every call past 2,500 a class until midnight UTC: raise the caps under B2 → Caps & Alerts, or lower the vault's activity"}
		}
	}
	return DoctorResult{Status: DoctorOK, Detail: detail}
}

// capErrorSeen is the first cap-exceeded error the lease's or the vault's
// state files show, "" for none.
func (env *DoctorEnv) capErrorSeen() string {
	var errs []string
	var ls leaseStateRecord
	if ok, _ := env.readCoreState(leaseStateFile, &ls); ok {
		errs = append(errs, ls.Error)
	}
	if data, err := ReadVaultStatusFile(env.Config.DataDir, env.ReadFile); err == nil {
		if st, err := parseVaultStatus(data); err == nil {
			errs = append(errs, st.Error, st.Litestream.LastError)
			if r := st.Reachable; r != nil {
				errs = append(errs, r.Error)
			}
			if r := st.LastSync; r != nil {
				errs = append(errs, r.Error)
			}
			for _, r := range []*snapshotRecord{st.Snapshots.LastNightly, st.Snapshots.LastHourly} {
				if r != nil {
					errs = append(errs, r.Error)
				}
			}
		}
	}
	for _, e := range errs {
		if e != "" && cloud.IsCapExceeded(errors.New(e)) {
			if len(e) > 300 {
				e = e[:300] + "…"
			}
			return strings.TrimSpace(e)
		}
	}
	return ""
}
