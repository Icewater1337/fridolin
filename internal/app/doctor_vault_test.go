package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/doctor"
	"github.com/Icewater1337/fridolin/internal/snapshot"
)

func TestDoctorVaultChecks(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	healthy := func() vaultStatus {
		return vaultStatus{
			Enabled: true, State: vaultRunning, UpdatedAt: now.Add(-time.Minute),
			Reachable:  &vaultReach{At: now.Add(-5 * time.Minute), OK: true},
			LastUpload: now.Add(-3 * time.Hour),
			Files:      vaultFileCounts{Both: 10, Cold: 2},
			Snapshots: snapshotsStatus{Enabled: true,
				LastNightly: &snapshotRecord{At: now.Add(-9 * time.Hour), Kind: "nightly", ID: "abcd1234", Files: 10},
				LastHourly:  &snapshotRecord{At: now.Add(-30 * time.Minute), Kind: "hourly", ID: "beef0001", Files: 4}},
			Litestream: litestreamView{Enabled: true, LitestreamStatus: snapshot.LitestreamStatus{Running: true, PID: 42}},
		}
	}
	tests := []struct {
		name   string
		mut    func(*vaultStatus)
		noFile bool
		id     string
		status string
		detail string
	}{
		{name: "reachable", id: "vault", status: doctor.OK, detail: "reached 5m0s ago"},
		{name: "unreachable", mut: func(s *vaultStatus) { s.Reachable = &vaultReach{At: now, Error: "403 forbidden"} }, id: "vault", status: doctor.Fail, detail: "403 forbidden"},
		{name: "no status", noFile: true, id: "vault", status: doctor.Fail, detail: "no status yet"},
		{name: "stale status", mut: func(s *vaultStatus) { s.UpdatedAt = now.Add(-time.Hour) }, id: "vault", status: doctor.Fail, detail: "does not run it"},
		{name: "vault error", mut: func(s *vaultStatus) { s.State, s.Error = vaultError, "no keys" }, id: "vault-upload", status: doctor.Fail, detail: "no keys"},
		{name: "all uploaded", id: "vault-upload", status: doctor.OK, detail: "every file is in the cold tier"},
		{name: "waiting, recent upload", mut: func(s *vaultStatus) { s.Files.Hot, s.LastUpload = 3, now.Add(-10*time.Minute) }, id: "vault-upload", status: doctor.OK, detail: "3 files wait"},
		{name: "waiting too long", mut: func(s *vaultStatus) {
			s.Files.Hot = 3
			s.LastSync = &vaultSyncRecord{At: now, Error: "media/x: 401"}
		}, id: "vault-upload", status: doctor.Fail, detail: "media/x: 401"},
		{name: "fresh snapshot", id: "vault-snapshot", status: doctor.OK, detail: "hourly snapshot beef0001"},
		{name: "old snapshot", mut: func(s *vaultStatus) {
			s.Snapshots.LastHourly = nil
			s.Snapshots.LastNightly.At = now.Add(-27 * time.Hour)
		}, id: "vault-snapshot", status: doctor.Fail, detail: "is 27h0m0s old"},
		{name: "failed snapshot only", mut: func(s *vaultStatus) {
			s.Snapshots.LastHourly, s.Snapshots.LastNightly = nil, &snapshotRecord{At: now, Kind: "nightly", Error: "lock"}
		}, id: "vault-snapshot", status: doctor.Fail, detail: "no snapshot yet"},
		{name: "snapshots off", mut: func(s *vaultStatus) { s.Snapshots = snapshotsStatus{Error: "restic is not installed"} }, id: "vault-snapshot", status: doctor.Fail, detail: "restic is not installed"},
		{name: "deletion hold", mut: func(s *vaultStatus) {
			s.Snapshots.DeletionHold = &deletionHold{Since: now, Snapshot: "beef0001", PrevFiles: 900, Files: 4}
		}, id: "vault-snapshot", status: doctor.Warn, detail: "pruning is held"},
		{name: "every brain.json read", id: "vault-brain-backup", status: doctor.OK, detail: "every brain.json read"},
		{name: "a brain's backup off", mut: func(s *vaultStatus) {
			s.Snapshots.SkippedBrains = []brainBackup{{ID: "family", Dir: "/home/you/family", Skip: "its backup is off (backup.enabled)"}}
		}, id: "vault-brain-backup", status: doctor.OK, detail: "backup is off: family"},
		{name: "an unreadable brain.json", mut: func(s *vaultStatus) {
			s.Snapshots.SkippedBrains = []brainBackup{{ID: "work", Dir: "/home/you/work", Skip: "its brain.json cannot be read: bad JSON", Unreadable: true}}
		}, id: "vault-brain-backup", status: doctor.Warn, detail: "work (/home/you/work): its brain.json cannot be read"},
		{name: "litestream running", id: "vault-litestream", status: doctor.OK, detail: "pid 42"},
		{name: "litestream down", mut: func(s *vaultStatus) {
			s.Litestream.Running, s.Litestream.Restarts, s.Litestream.LastError = false, 3, "exit status 1"
		}, id: "vault-litestream", status: doctor.Fail, detail: "3 restarts"},
		{name: "litestream runs but logs errors", mut: func(s *vaultStatus) {
			s.Litestream.LastProblem, s.Litestream.LastProblemAt = `level=ERROR msg="monitor error" db=bridge.db hint="LTX file is missing"`, now.Add(-2*time.Minute)
		}, id: "vault-litestream", status: doctor.Warn, detail: "logged 2m0s ago"},
		{name: "litestream problem long ago, repaired", mut: func(s *vaultStatus) {
			s.Litestream.LastProblem, s.Litestream.LastProblemAt = "level=ERROR", now.Add(-time.Hour)
			s.Litestream.Repairs, s.Litestream.LastRepair = 1, now.Add(-time.Hour)
		}, id: "vault-litestream", status: doctor.OK, detail: "started over after a lost file: 1"},
		{name: "litestream status of the last run", mut: func(s *vaultStatus) {
			s.StartedAt, s.Litestream.At = now.Add(-time.Minute), now.Add(-time.Hour)
			s.Litestream.Running, s.Litestream.PID, s.Litestream.Restarts, s.Litestream.LastError = false, 0, 2, "signal: killed"
		}, id: "vault-litestream", status: doctor.Warn, detail: "the vault started 1m0s ago; Litestream has not said yet"},
		{name: "litestream silent since the start", mut: func(s *vaultStatus) {
			s.StartedAt, s.Litestream.LitestreamStatus = now.Add(-10*time.Minute), snapshot.LitestreamStatus{}
		}, id: "vault-litestream", status: doctor.Fail, detail: "the vault started 10m0s ago, and Litestream has not said"},
		{name: "litestream reported since the start", mut: func(s *vaultStatus) {
			s.StartedAt, s.Litestream.At = now.Add(-10*time.Minute), now.Add(-time.Minute)
		}, id: "vault-litestream", status: doctor.OK, detail: "pid 42, 0 restarts"},
		{name: "litestream down since the start", mut: func(s *vaultStatus) {
			s.StartedAt, s.Litestream.At = now.Add(-10*time.Minute), now.Add(-time.Minute)
			s.Litestream.Running, s.Litestream.Restarts, s.Litestream.LastError = false, 1, "exit status 1"
		}, id: "vault-litestream", status: doctor.Fail, detail: "not running (1 restarts)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := healthyMac()
			st := healthy()
			if tc.mut != nil {
				tc.mut(&st)
			}
			s.contents = map[string]string{}
			if !tc.noFile {
				data, _ := json.Marshal(st)
				s.contents["/home/you/.fridolin/core/vault-status.json"] = string(data)
			}
			env := s.doctorEnv(doctorConfig(func(c *config.Settings) { c.Vault.Enabled = true }), nil)
			env.Now = func() time.Time { return now }
			rep := doctor.Run(context.Background(), env, DoctorRegistry.Checks())
			got := result(t, rep, tc.id)
			if got.Status != tc.status || !strings.Contains(got.Detail, tc.detail) || !got.Required {
				t.Errorf("%s: %+v, want %s with %q", tc.id, got, tc.status, tc.detail)
			}
		})
	}
	// Without vault.enabled the checks do not apply.
	rep := doctor.Run(context.Background(), healthyMac().doctorEnv(doctorConfig(nil), nil), DoctorRegistry.Checks())
	for _, c := range rep.Checks {
		if strings.HasPrefix(c.ID, "vault") {
			t.Errorf("%s applies with the vault off", c.ID)
		}
	}
}
