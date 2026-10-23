package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestSchedulesUsageGolden(t *testing.T) {
	assertGolden(t, "cli/schedules-usage.golden", []byte(schedulesUsage))
}

func TestSchedulesCmdArguments(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{nil, 2, "fridolin schedules list"},
		{[]string{"--help"}, 0, "fridolin schedules logs AGENT"},
		{[]string{"nope"}, 2, "unknown schedules command"},
		{[]string{"run"}, 2, "wrong arguments"},
		{[]string{"list", "extra"}, 2, "wrong arguments"},
		{[]string{"logs", "a/b", "r", "x"}, 2, "wrong arguments"},
		{[]string{"run", "main/brief", "--wait", "--config", "/nonexistent/config.json"}, 1, "loading config"},
	} {
		var out, errb bytes.Buffer
		code := schedulesCmd(tc.args, &out, &errb)
		if code != tc.code || !strings.Contains(out.String()+errb.String(), tc.want) {
			t.Errorf("schedules %q: exit %d, out %q, err %q", tc.args, code, out.String(), errb.String())
		}
	}
}

func TestResolveAgent(t *testing.T) {
	l := schedulesList{Agents: []scheduleAgent{{Brain: "main", Name: "brief"}, {Brain: "main", Name: "review"}, {Brain: "work", Name: "review"}}}
	if b, n, err := resolveAgent(l, "brief"); err != nil || b != "main" || n != "brief" {
		t.Errorf("unique name: %s/%s %v", b, n, err)
	}
	if _, _, err := resolveAgent(l, "review"); err == nil || !strings.Contains(err.Error(), "BRAIN/review") {
		t.Errorf("two brains: %v", err)
	}
	if _, _, err := resolveAgent(l, "nope"); err == nil {
		t.Error("no such agent")
	}
	if b, n, _ := resolveAgent(l, "work/review"); b != "work" || n != "review" {
		t.Errorf("brain/name: %s/%s", b, n)
	}
}

func TestSchedulesText(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	next := now.Add(90 * time.Minute)
	exit := 3
	l := schedulesList{Enabled: false, Agents: []scheduleAgent{
		{Brain: "main", Name: "brief", Enabled: true, Next: &next, LastRun: &scheduleRun{Status: "ok", Started: now.Add(-time.Hour)}},
		{Brain: "main", Name: "scrape", Enabled: true, Paused: true, Problem: "no node"},
	}}
	l.Agents[0].When.When = "daily 06:00"
	text := schedulesListText(l, now)
	for _, want := range []string{"schedules.enabled is off", "main/brief", "daily 06:00", "in 1h30m0s", "ok, 1h0m0s ago", "paused (!)", "(!) main/scrape: no node"} {
		if !strings.Contains(text, want) {
			t.Errorf("list lacks %q:\n%s", want, text)
		}
	}
	runs := scheduleRunsText([]scheduleRun{
		{ID: "r1", Status: "ok", DurationMS: 61000, Sent: true, SentTo: "15551234567@s.whatsapp.net", CostUSD: 0.0123},
		{ID: "r2", Status: "failed", Error: "it exited with 3", ExitCode: &exit},
		{ID: "r3", Status: "nothing", InTokens: 10, OutTokens: 2},
	})
	for _, want := range []string{"r1", "1m1s", "$0.0123", "failed: it exited with 3", "exit 3", "10+2 tokens"} {
		if !strings.Contains(runs, want) {
			t.Errorf("runs lack %q:\n%s", want, runs)
		}
	}
	if exitOf(scheduleRun{Status: "failed"}) != 1 || exitOf(scheduleRun{Status: "nothing"}) != 0 {
		t.Error("exitOf")
	}
}
