package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/testutil"
)

// installBackupRunner retains the production projections while replacing only
// external process execution. The old manager has no work in these fixtures.
func installBackupRunner(t *testing.T, tb *testBridge, runner BackupRunFunc, configure ...func(*BackupOptions)) {
	t.Helper()
	tb.backup.Stop()
	opts := backupOptions(tb.cfgs, tb.brainMgr, tb.notifyBackup)
	opts.Runner = runner
	for _, apply := range configure {
		apply(&opts)
	}
	tb.backup = newBackupManager(opts)
}

// persistedBackupState reads the on-disk contract instead of service internals.
func persistedBackupState(t *testing.T, tb *testBridge) struct {
	ArmedAt        time.Time            `json:"armed_at"`
	RefusalNotices map[string]time.Time `json:"refusal_notices"`
} {
	t.Helper()
	var state struct {
		ArmedAt        time.Time            `json:"armed_at"`
		RefusalNotices map[string]time.Time `json:"refusal_notices"`
	}
	data, err := os.ReadFile(filepath.Join(tb.cfgs.Boot().BackupDir(), "state.json"))
	if os.IsNotExist(err) {
		return state
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func connectedBackup(tb *testBridge) bool { return tb.backup.Status()["connected"].(bool) }

// startScheduledBackup exercises scheduled-result delivery without invoking a
// private scheduler tick; the schedule itself is covered by service fixtures.
func startScheduledBackup(t *testing.T, tb *testBridge) *BackupResult {
	t.Helper()
	if err := tb.backup.StartRun([]string{"notes"}, false, "scheduled", false); err != nil {
		t.Fatal(err)
	}
	return waitBackup(t, tb, "notes")
}

func waitBackupConnect(t *testing.T, tb *testBridge) {
	t.Helper()
	testutil.WaitFor(t, 10*time.Second, "Drive connection", func() bool {
		var st struct {
			Connect *struct {
				State string `json:"state"`
				Error string `json:"error"`
			} `json:"connect"`
		}
		if err := json.Unmarshal(tb.do("GET", "/api/backup/status", nil).Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		if st.Connect != nil && st.Connect.State != "running" {
			if st.Connect.State != "done" {
				t.Fatalf("connect: %+v", st.Connect)
			}
			return true
		}
		return false
	})
}
