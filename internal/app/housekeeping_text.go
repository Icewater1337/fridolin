package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/housekeeping"
)

// HousekeepingReportText is a report for the terminal.
func HousekeepingReportText(raw []byte, now time.Time) (string, error) {
	var r housekeeping.Report
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("reading the report: %w", err)
	}
	return reportText(r, now), nil
}

func reportText(r housekeeping.Report, now time.Time) string {
	var b strings.Builder
	mode := "acting"
	if r.DryRun {
		mode = "dry run"
	}
	fmt.Fprintf(&b, "Housekeeping run %s ago (%s, %s): %s\n", now.Sub(r.Started).Round(time.Second), r.Trigger, mode, r.Summary())
	if r.Note != "" {
		fmt.Fprintf(&b, "  note: %s\n", r.Note)
	}
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "  [%s] %s\n", f.Outcome, f.Describe())
		if f.Command != "" && f.PID > 0 {
			fmt.Fprintf(&b, "      %s\n", f.Command)
		}
	}
	if len(r.Findings) == 0 {
		b.WriteString("  nothing found\n")
	}
	for _, e := range r.Errors {
		fmt.Fprintf(&b, "  error: %s\n", e)
	}
	return b.String()
}

// HousekeepingStatusText is GET /api/housekeeping, or the state file, for
// the terminal.
func HousekeepingStatusText(raw []byte, now time.Time) (string, error) {
	var v struct {
		Enabled    *bool                `json:"enabled"`
		DryRun     *bool                `json:"dry_run"`
		EveryHours int                  `json:"every_hours"`
		NextRun    string               `json:"next_run"`
		Last       *housekeeping.Report `json:"last"`
		// Asked is a list in the API's answer, a map by approval in the
		// state file.
		Asked json.RawMessage `json:"asked"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("reading the status: %w", err)
	}
	var b strings.Builder
	if v.Enabled != nil {
		state := "off"
		if *v.Enabled {
			state = fmt.Sprintf("every %d h", v.EveryHours)
		}
		dry := "acts"
		if v.DryRun != nil && *v.DryRun {
			dry = "dry run (housekeeping.dry_run)"
		}
		fmt.Fprintf(&b, "Housekeeping: %s, %s", state, dry)
		if t, err := time.Parse(time.RFC3339, v.NextRun); err == nil {
			fmt.Fprintf(&b, "; next run in %s", t.Sub(now).Round(time.Minute))
		}
		b.WriteString("\n")
	}
	if v.Last == nil {
		b.WriteString("No run yet.\n")
	} else {
		b.WriteString(reportText(*v.Last, now))
	}
	var asked []housekeeping.Finding
	if json.Unmarshal(v.Asked, &asked) != nil {
		var byID map[string]housekeeping.Finding
		_ = json.Unmarshal(v.Asked, &byID)
		for _, f := range byID {
			asked = append(asked, f)
		}
	}
	for _, f := range asked {
		fmt.Fprintf(&b, "Waiting for the owner's answer (%s): %s\n", f.Approval, f.Describe())
	}
	return b.String(), nil
}
