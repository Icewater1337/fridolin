package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"
)

// The housekeeping check: the job's last run, from core/housekeeping.json,
// which the running bridge writes. Optional: a job that did not run or
// found what it cannot fix warns, it never fails doctor.
var housekeepingDoctorChecks = []DoctorCheck{
	{ID: "housekeeping", Title: "housekeeping ran", Applies: func(env *DoctorEnv) bool { return env.Config != nil }, Run: checkHousekeeping},
}

func init() { DoctorRegistry = append(DoctorRegistry, housekeepingDoctorChecks...) }

func checkHousekeeping(_ context.Context, env *DoctorEnv) DoctorResult {
	hk := env.Config.Housekeeping
	data, err := ReadHousekeepingFile(env.Config.DataDir, env.ReadFile)
	if errors.Is(err, fs.ErrNotExist) {
		if !hk.Enabled {
			return DoctorResult{Status: DoctorOK, Detail: "off (housekeeping.enabled), and never run here"}
		}
		return DoctorResult{Status: DoctorOK, Detail: "no run yet: the first comes 10 minutes after the bridge starts"}
	}
	if err != nil {
		return DoctorResult{Status: DoctorWarn, Detail: err.Error()}
	}
	var st housekeepingState
	if err := json.Unmarshal(data, &st); err != nil || st.Last == nil {
		return DoctorResult{Status: DoctorWarn, Detail: "core/housekeeping.json holds no report", Fix: "fridolin housekeeping run --dry-run"}
	}
	last := st.Last
	age := env.now().Sub(last.Started)
	detail := fmt.Sprintf("last run %s ago (%s): %s", age.Round(time.Minute), last.Trigger, last.Summary())
	if hk.Enabled && age > 2*hk.Every+time.Hour {
		return DoctorResult{Status: DoctorWarn, Detail: detail + fmt.Sprintf("; due every %s", hk.Every),
			Fix: "is the bridge running? fridolin housekeeping run starts one now"}
	}
	var problems []string
	for _, f := range last.Findings {
		if f.Problem {
			problems = append(problems, f.Describe())
		}
	}
	problems = append(problems, last.Errors...)
	if len(problems) > 0 {
		return DoctorResult{Status: DoctorWarn, Detail: detail + "; " + strings.Join(problems, "; "),
			Fix: "the Overview page of the dashboard and fridolin housekeeping status show the report"}
	}
	if c := last.Counts(); last.DryRun && c.WouldStop > 0 {
		return DoctorResult{Status: DoctorOK, Detail: detail + "; set housekeeping.dry_run to false to let it act"}
	}
	return DoctorResult{Status: DoctorOK, Detail: detail}
}
