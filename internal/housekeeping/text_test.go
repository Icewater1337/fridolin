package housekeeping

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var textNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestReportText: a run's report as the terminal shows it, with its
// note, each finding (a process's command under it), and its errors.
func TestReportText(t *testing.T) {
	r := Report{
		Started: textNow.Add(-90 * time.Second),
		Trigger: "cli",
		DryRun:  true,
		Note:    "lockdown: only reporting",
		Scanned: 12,
		Findings: []Finding{
			{Kind: "orphan", PID: 42, Name: "claude", Command: "claude -p hi", Reason: "its run is gone", Outcome: OutcomeWouldStop},
			{Kind: "lock", Target: "restic-1", Command: "not shown without a PID", Reason: "stale", Outcome: OutcomeReported},
		},
		Errors: []string{"ps failed once"},
	}
	got, err := ReportText(mustJSON(t, r), textNow)
	if err != nil {
		t.Fatal(err)
	}
	want := "Housekeeping run 1m30s ago (cli, dry run): " + r.Summary() + "\n" +
		"  note: lockdown: only reporting\n" +
		"  [would stop] orphan PID 42 (claude): its run is gone\n" +
		"      claude -p hi\n" +
		"  [reported] lock restic-1: stale\n" +
		"  error: ps failed once\n"
	if got != want {
		t.Errorf("ReportText =\n%s\nwant\n%s", got, want)
	}

	got, err = ReportText(mustJSON(t, Report{Started: textNow.Add(-time.Hour), Trigger: "schedule"}), textNow)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "Housekeeping run 1h0m0s ago (schedule, acting): ") || !strings.HasSuffix(got, "\n  nothing found\n") {
		t.Errorf("an empty run: %q", got)
	}

	if _, err := ReportText([]byte("{"), textNow); err == nil || !strings.HasPrefix(err.Error(), "reading the report: ") {
		t.Errorf("bad JSON: %v", err)
	}
}

// TestStatusText: the API's answer and the state file, which keeps what
// it asked about as a map by approval, for the terminal.
func TestStatusText(t *testing.T) {
	yes, no := true, false
	asked := Finding{Kind: "heavy", PID: 7, Name: "node", Reason: "uses 90% CPU", Approval: "A1"}
	last := Report{Started: textNow.Add(-2 * time.Minute), Trigger: "schedule"}
	for _, tc := range []struct {
		name string
		v    any
		want string
	}{
		{
			name: "on, dry run, asked as a list",
			v: map[string]any{
				"enabled": yes, "dry_run": yes, "every_hours": 6,
				"next_run": textNow.Add(90 * time.Minute).Format(time.RFC3339),
				"last":     last, "asked": []Finding{asked},
			},
			want: "Housekeeping: every 6 h, dry run (housekeeping.dry_run); next run in 1h30m0s\n" +
				reportText(last, textNow) +
				"Waiting for the owner's answer (A1): heavy PID 7 (node): uses 90% CPU\n",
		},
		{
			name: "off, acts, no run yet",
			v:    map[string]any{"enabled": no, "dry_run": no, "next_run": "soon"},
			want: "Housekeeping: off, acts\nNo run yet.\n",
		},
		{
			name: "the state file",
			v:    map[string]any{"last": last, "asked": map[string]Finding{"A1": asked}},
			want: reportText(last, textNow) +
				"Waiting for the owner's answer (A1): heavy PID 7 (node): uses 90% CPU\n",
		},
	} {
		got, err := StatusText(mustJSON(t, tc.v), textNow)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: StatusText =\n%s\nwant\n%s", tc.name, got, tc.want)
		}
	}

	if _, err := StatusText([]byte("[]"), textNow); err == nil || !strings.HasPrefix(err.Error(), "reading the status: ") {
		t.Errorf("bad JSON: %v", err)
	}
}
