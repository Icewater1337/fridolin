package app

import (
	"context"
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
)

// The B2 check reads the counts the bridge wrote to core/b2-usage.json:
// fine under 70% of the budget, a warning above, a failure once B2 refused
// a call for a cap, in the counts or in what the lease last said.
func TestDoctorB2Transactions(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	cfg := &config.Settings{Boot: config.Boot{DataDir: "/data"}}
	cfg.Core.Lease.Enabled = true
	cfg.Vault.B2DailyBudget = 2000
	env := func(contents map[string]string) *DoctorEnv {
		s := &fakeSystem{goos: "linux", contents: contents, files: map[string]fs.FileMode{}, runs: map[string]fakeRun{}}
		e := s.doctorEnv(cfg, nil)
		e.Now = func() time.Time { return now }
		return e
	}
	usage := func(day string, b, c int, capAt string) string {
		s := `{"today":{"day":"` + day + `","classes":{"A":5,"B":` + strconv.Itoa(b) + `,"C":` + strconv.Itoa(c) + `},"ops":{"B lease:get":` + strconv.Itoa(b) + `,"C vault:list":` + strconv.Itoa(c) + `}`
		if capAt != "" {
			s += `,"cap_exceeded":"` + capAt + `","cap_error":"failed to authorize account: Transaction cap exceeded"`
		}
		return s + `}}`
	}
	for _, tc := range []struct {
		name     string
		files    map[string]string
		status   string
		detail   string
		fixMatch string
	}{
		{"nothing counted", nil, DoctorOK, "no B2 transactions counted today", ""},
		{"yesterday only", map[string]string{"/data/core/b2-usage.json": usage("2026-10-20", 2400, 100, "")}, DoctorOK, "no B2 transactions counted today", ""},
		{"quiet", map[string]string{"/data/core/b2-usage.json": usage("2026-10-21", 600, 200, "")}, DoctorOK, "class B 600, class C 200 of a daily budget of 2000", ""},
		{"busy", map[string]string{"/data/core/b2-usage.json": usage("2026-10-21", 1500, 200, "")}, DoctorWarn, "class B is over 70%", "Caps & Alerts"},
		{"capped", map[string]string{"/data/core/b2-usage.json": usage("2026-10-21", 2500, 2500, "2026-10-21T09:30:00Z")}, DoctorFail, "refused a call at 09:30 UTC", "Caps & Alerts"},
		{"the lease says so", map[string]string{"/data/core/lease-state.json": `{"state":"unverified","device_id":"d1","error":"reading the core lease: 403 transaction_cap_exceeded"}`},
			DoctorFail, "transaction_cap_exceeded", "Caps & Alerts"},
	} {
		got := checkB2Transactions(context.Background(), env(tc.files))
		if got.Status != tc.status || !strings.Contains(got.Detail, tc.detail) || !strings.Contains(got.Fix, tc.fixMatch) {
			t.Errorf("%s: %+v, want %s with %q", tc.name, got, tc.status, tc.detail)
		}
	}
	// The lease's own check says what the unverified state means.
	got := checkLeaseFresh(context.Background(), env(map[string]string{"/data/core/lease-state.json": `{"state":"unverified","device_id":"d1","error":"reading the core lease: 403 transaction_cap_exceeded"}`}))
	if got.Status != DoctorFail || !strings.Contains(got.Detail, "channels keep running") || !strings.Contains(got.Fix, "Caps & Alerts") {
		t.Errorf("unverified lease: %+v", got)
	}
	got = checkLeaseFresh(context.Background(), env(map[string]string{"/data/core/lease-state.json": `{"state":"unverified","device_id":"d1","error":"dial tcp: no route to host"}`}))
	if got.Status != DoctorWarn {
		t.Errorf("unverified lease, network down: %+v", got)
	}
	// Off without the lease and the vault.
	for _, c := range DoctorRegistry {
		if c.ID == "b2-transactions" && c.Applies(&DoctorEnv{Config: &config.Settings{}}) {
			t.Error("the B2 check applies with the lease and the vault off")
		}
	}
}
