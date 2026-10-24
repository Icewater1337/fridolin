package app

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
)

func TestDoctorHousekeeping(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	cfg := &config.Settings{Boot: config.Boot{DataDir: "/data"}}
	cfg.Housekeeping = config.HousekeepingSettings{Enabled: true, DryRun: true, Every: 48 * time.Hour}
	run := func(file string) DoctorResult {
		contents := map[string]string{}
		if file != "" {
			contents["/data/core/housekeeping.json"] = file
		}
		s := &fakeSystem{goos: "linux", contents: contents, files: map[string]fs.FileMode{}, runs: map[string]fakeRun{}}
		e := s.doctorEnv(cfg, nil)
		e.Now = func() time.Time { return now }
		return checkHousekeeping(context.Background(), e)
	}
	report := func(started, findings string) string {
		return `{"last":{"started":"` + started + `","trigger":"schedule","dry_run":true,"scanned":300,"findings":[` + findings + `]}}`
	}
	for _, tc := range []struct {
		name, file, status, want string
	}{
		{"no run yet", "", DoctorOK, "no run yet"},
		{"fresh and clean", report("2026-10-20T12:00:00Z", ""), DoctorOK, "last run 24h0m0s ago"},
		{"would stop", report("2026-10-20T12:00:00Z", `{"kind":"run-orphan","action":"stop","pid":7,"reason":"r","outcome":"would stop"}`), DoctorOK, "dry_run to false"},
		{"overdue", report("2026-10-15T12:00:00Z", ""), DoctorWarn, "due every 48h0m0s"},
		{"a problem", report("2026-10-21T11:00:00Z", `{"kind":"zombie","action":"report","pid":9,"name":"sh","reason":"not collected","problem":true,"outcome":"reported"}`), DoctorWarn, "zombie PID 9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := run(tc.file)
			if got.Status != tc.status || !strings.Contains(got.Detail, tc.want) {
				t.Errorf("got %s %q, want %s with %q", got.Status, got.Detail, tc.status, tc.want)
			}
		})
	}
}
