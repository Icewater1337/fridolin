package app

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
)

func TestDoctorEncryption(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	cfg := &config.Settings{Boot: config.Boot{DataDir: "/data"}}
	cfg.Vault.Enabled = true
	run := func(status string) DoctorResult {
		s := &fakeSystem{goos: "linux", contents: map[string]string{"/data/core/vault-status.json": status}, files: map[string]fs.FileMode{}, runs: map[string]fakeRun{}}
		e := s.doctorEnv(cfg, nil)
		e.Now = func() time.Time { return now }
		return checkVaultEncryption(context.Background(), e)
	}
	head := `{"state":"running","updated_at":"2026-10-21T11:59:00Z",`
	two := `{"store":"cold","layers":["a","b"]},{"store":"restic","layers":["a","b"]},{"store":"exports","layers":["age","seal"],"pq":true}`
	for _, tc := range []struct {
		name, status, want, detail string
	}{
		{"all two", head + `"encryption":[` + two + `]}`, DoctorOK, "exports: age + seal"},
		{"one layer", head + `"encryption":[` + two + `,{"store":"litestream","layers":["none"]}]}`, DoctorWarn, "one layer only: litestream"},
		{"copying", head + `"encryption":[{"store":"cold","layers":["a","b"],"old":"copying"}]}`, DoctorWarn, "single-layer copies of cold"},
		{"retired, a Drive folder left by hand", head + `"encryption":[` + two + `,{"store":"drive","layers":["a","b"],"old":"retired","left":"delete \"fridolin-restic\" in Google Drive"}]}`, DoctorOK, `By hand: drive: delete "fridolin-restic"`},
		{"a target of before", head + `"encryption":[` + two + `],"targets":[{"name":"nas","layers":"restic"}]}`, DoctorWarn, "target nas"},
		{"not described", head + `"snapshots":{}}`, DoctorWarn, "not described"},
	} {
		got := run(tc.status)
		if got.Status != tc.want || !strings.Contains(got.Detail, tc.detail) {
			t.Errorf("%s: %+v", tc.name, got)
		}
	}
}
