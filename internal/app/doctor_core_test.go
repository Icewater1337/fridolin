package app

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/sandbox"
)

// The gate checks of M1 read what the running core wrote to <data>/core,
// and run bubblewrap as a brain's run would.
func TestDoctorCoreChecks(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	cfg := &config.Settings{Boot: config.Boot{DataDir: "/data"}}
	cfg.Core.Tailnet.Enabled = true
	cfg.Core.Lease.Enabled = true
	cfg.Sandbox.Enabled = true
	env := func(contents map[string]string) *DoctorEnv {
		s := &fakeSystem{goos: "linux", contents: contents, files: map[string]fs.FileMode{sandbox.BwrapPath: 0o755},
			runs: map[string]fakeRun{}}
		e := s.doctorEnv(cfg, nil)
		e.ConfigGOOS = config.ContainerGOOS
		e.Now = func() time.Time { return now }
		return e
	}
	tests := []struct {
		check  func(context.Context, *DoctorEnv) DoctorResult
		file   string
		status string
		detail string
	}{
		{checkTailnetUp, "", DoctorMissing, "has not started"},
		{checkTailnetUp, `{"state":"up","dns_name":"fridolin-core.example.ts.net","magic_dns":true,"url":"https://fridolin-core.example.ts.net/","updated_at":"2026-10-20T11:59:00Z"}`, DoctorOK, "up as fridolin-core.example.ts.net"},
		{checkTailnetUp, `{"state":"up","dns_name":"fridolin-core.example.ts.net","magic_dns":false}`, DoctorWarn, "MagicDNS is off"},
		{checkTailnetUp, `{"state":"error","error":"invalid key"}`, DoctorFail, "invalid key"},
		{checkLeaseFresh, "", DoctorMissing, "has not taken"},
		{checkLeaseFresh, `{"state":"held","device_id":"d1","hostname":"mini","renewed_at":"2026-10-20T11:59:30Z","renew_seconds":60,"stale_seconds":300}`, DoctorOK, "renewed 30s ago"},
		{checkLeaseFresh, `{"state":"held","device_id":"d1","renewed_at":"2026-10-20T11:57:00Z","renew_seconds":60,"stale_seconds":300}`, DoctorWarn, "late"},
		{checkLeaseFresh, `{"state":"held","device_id":"d1","renewed_at":"2026-10-20T11:50:00Z","renew_seconds":60,"stale_seconds":300}`, DoctorFail, "stale"},
		{checkLeaseFresh, `{"state":"lost","error":"taken","holder":"d2 (vm)"}`, DoctorFail, "d2 (vm) holds it"},
	}
	for _, tc := range tests {
		contents := map[string]string{}
		if tc.file != "" {
			name := tailnetStateFile
			if strings.Contains(tc.file, "renew") || strings.Contains(tc.file, "lost") {
				name = leaseStateFile
			}
			contents["/data/core/"+name] = tc.file
		}
		got := tc.check(context.Background(), env(contents))
		if got.Status != tc.status || !strings.Contains(got.Detail, tc.detail) {
			t.Errorf("%s: %+v, want %s with %q", tc.file, got, tc.status, tc.detail)
		}
	}

	e := env(nil)
	if got := checkContainerSandbox(context.Background(), e); got.Status != DoctorFail {
		t.Errorf("bubblewrap that fails: %+v", got)
	}
	s := &fakeSystem{goos: "linux", files: map[string]fs.FileMode{sandbox.BwrapPath: 0o755}, runs: map[string]fakeRun{
		sandbox.BwrapPath + " --unshare-user --unshare-pid --unshare-ipc --ro-bind / / --proc /proc --dev /dev --die-with-parent -- /bin/true": {},
	}}
	if got := checkContainerSandbox(context.Background(), s.doctorEnv(cfg, nil)); got.Status != DoctorOK {
		t.Errorf("bubblewrap that works: %+v", got)
	}

	// The host's checks stay out of the container, and the core's out of
	// a config without them.
	for _, c := range DoctorRegistry {
		if c.ID == "docker" && c.Applies(e) {
			t.Error("the Docker check applies inside the container")
		}
		if c.ID == "tailnet" && c.Applies(&DoctorEnv{Config: &config.Settings{}}) {
			t.Error("the tailnet check applies with core.tailnet off")
		}
	}
}
