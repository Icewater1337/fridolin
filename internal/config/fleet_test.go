package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A config without the sections gets every default, with every feature
// off, so it runs as before they existed.
func TestFleetSectionsDefaultOff(t *testing.T) {
	t.Parallel()
	s, err := Resolve(&File{}, Paths{Home: t.TempDir()}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	want := Live{
		Core: CoreConfig{
			Tailnet:       TailnetConfig{Hostname: "fridolin-core"},
			Lease:         LeaseConfig{RenewSeconds: 300, StaleSeconds: 1200},
			MemoryLimitGB: 8,
		},
		Vault: VaultConfig{
			HotBudgetGB: 50, ActivityDays: 60, ColdAfterDays: 90, HeadroomPercent: 10,
			Bucket: "fridolin-vault", SnapshotsBucket: "fridolin-snapshots",
			Endpoint: "s3.eu-central-003.backblazeb2.com", B2DailyBudget: 2000,
		},
		Nodes:  NodesConfig{Listen: ":8766"},
		Policy: PolicyConfig{MaxRunsPerHour: 20, MaxPendingIrreversible: 3, ApprovalTimeoutHours: 24},
	}
	if s.Core != want.Core || !reflect.DeepEqual(s.Vault, want.Vault) || s.Nodes != want.Nodes || s.Policy != want.Policy {
		t.Fatalf("defaults:\n got %+v %+v %+v %+v\nwant %+v %+v %+v %+v", s.Core, s.Vault, s.Nodes, s.Policy, want.Core, want.Vault, want.Nodes, want.Policy)
	}
	if s.Core.Lease.Renew() != 5*time.Minute || s.Core.Lease.Stale() != 20*time.Minute || s.Policy.ApprovalTimeout() != 24*time.Hour {
		t.Fatal("durations")
	}
}

// What a file sets is kept, and goes back into the file the dashboard
// shows unchanged.
func TestFleetSectionsKeepWhatIsSet(t *testing.T) {
	t.Parallel()
	data := []byte(`{"core": {"tailnet": {"enabled": true, "hostname": "core-2"}, "lease": {"enabled": true, "renew_seconds": 10, "stale_seconds": 11}, "memory_limit_gb": 2},
		"vault": {"enabled": true, "hot_budget_gb": 1, "activity_days": 2, "cold_after_days": 3, "headroom_percent": 4, "bucket": "my-vault", "snapshots_bucket": "my-snaps", "endpoint": "localhost", "b2_daily_budget": 900},
		"nodes": {"enabled": true, "listen": "[::1]:9000"},
		"policy": {"max_runs_per_hour": 1, "max_pending_irreversible": 1, "approval_timeout_hours": 1}}`)
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	want := f
	s, err := Resolve(&f, Paths{Home: t.TempDir()}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if s.Core != want.Core || !reflect.DeepEqual(s.Vault, want.Vault) || s.Nodes != want.Nodes || s.Policy != want.Policy {
		t.Fatalf("got %+v %+v %+v %+v", s.Core, s.Vault, s.Nodes, s.Policy)
	}
	eff := EffectiveFile(s)
	if eff.Core != want.Core || !reflect.DeepEqual(eff.Vault, want.Vault) || eff.Nodes != want.Nodes || eff.Policy != want.Policy {
		t.Fatal("the effective file lost a setting")
	}
}

func TestFleetSectionsRefuse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ config, want string }{
		{`{"core": {"tailnet": {"hostname": "Core"}}}`, "core.tailnet.hostname"},
		{`{"core": {"tailnet": {"hostname": "core.example"}}}`, "core.tailnet.hostname"},
		{`{"core": {"tailnet": {"hostname": "-core"}}}`, "core.tailnet.hostname"},
		{`{"core": {"lease": {"renew_seconds": -1}}}`, "core.lease.renew_seconds"},
		{`{"core": {"lease": {"stale_seconds": 86401}}}`, "core.lease.stale_seconds"},
		{`{"core": {"lease": {"renew_seconds": 300, "stale_seconds": 300}}}`, "must be more than"},
		{`{"core": {"lease": {"renew_seconds": 1200}}}`, "must be more than"},
		{`{"vault": {"b2_daily_budget": -1}}`, "vault.b2_daily_budget"},
		{`{"core": {"memory_limit_gb": -8}}`, "core.memory_limit_gb"},
		{`{"vault": {"hot_budget_gb": -1}}`, "vault.hot_budget_gb"},
		{`{"vault": {"activity_days": -1}}`, "vault.activity_days"},
		{`{"vault": {"cold_after_days": 99999}}`, "vault.cold_after_days"},
		{`{"vault": {"headroom_percent": 91}}`, "vault.headroom_percent"},
		{`{"vault": {"bucket": "Fridolin-Vault"}}`, "vault.bucket"},
		{`{"vault": {"bucket": "short"}}`, "vault.bucket"},
		{`{"vault": {"bucket": "b2-reserved"}}`, "vault.bucket"},
		{`{"vault": {"snapshots_bucket": "my_snapshots"}}`, "vault.snapshots_bucket"},
		{`{"vault": {"endpoint": "https://s3.example.com"}}`, "vault.endpoint"},
		{`{"vault": {"endpoint": "s3.example.com/path"}}`, "vault.endpoint"},
		{`{"vault": {"endpoint": "s3..example.com"}}`, "vault.endpoint"},
		{`{"nodes": {"listen": "8766"}}`, "nodes.listen"},
		{`{"nodes": {"listen": ":0"}}`, "nodes.listen"},
		{`{"nodes": {"listen": ":65536"}}`, "nodes.listen"},
		{`{"nodes": {"listen": ":08766"}}`, "nodes.listen"},
		{`{"nodes": {"listen": "bad_host:8766"}}`, "nodes.listen"},
		{`{"policy": {"max_runs_per_hour": -1}}`, "policy.max_runs_per_hour"},
		{`{"policy": {"max_pending_irreversible": 1001}}`, "policy.max_pending_irreversible"},
		{`{"policy": {"approval_timeout_hours": 721}}`, "policy.approval_timeout_hours"},
	} {
		var f File
		if err := json.Unmarshal([]byte(tc.config), &f); err != nil {
			t.Fatal(err)
		}
		_, err := Resolve(&f, Paths{Home: t.TempDir()}, "linux")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one naming %q", tc.config, err, tc.want)
		}
	}
}

func TestFleetValidators(t *testing.T) {
	t.Parallel()
	for addr, ok := range map[string]bool{
		":8766": true, "0.0.0.0:8766": true, "127.0.0.1:1": true, "[::1]:65535": true, "fridolin-core:8766": true,
		"fridolin-core.tailnet.ts.net:443": true, "": false, ":": false, "8766": false, "host:port": false, "[::1]:0": false,
		"Core:8766": false,
	} {
		if ValidListenAddr(addr) != ok {
			t.Errorf("ValidListenAddr(%q) = %v", addr, !ok)
		}
	}
	for name, ok := range map[string]bool{
		"localhost": true, "s3.eu-central-003.backblazeb2.com": true, "a": true, "": false, "a.": false, ".a": false,
		"a_b.com": false, strings.Repeat("a", 64): false, strings.Repeat("a.", 127) + "a": false,
	} {
		if ValidHostname(name) != ok {
			t.Errorf("ValidHostname(%q) = %v", name, !ok)
		}
	}
	for name, ok := range map[string]bool{
		"fridolin-vault": true, "abcdef": true, "abcde": false, strings.Repeat("a", 63): true, strings.Repeat("a", 64): false,
		"-abcdef": false, "abcdef-": false, "b2-vault": false, "UPPER-case": false,
	} {
		if ValidBucket(name) != ok {
			t.Errorf("ValidBucket(%q) = %v", name, !ok)
		}
	}
}

func TestLayoutNamesTheDataDirsPaths(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "data")
	l := Boot{DataDir: root}.Layout()
	for got, want := range map[string]string{
		l.Lock():              "bridge.lock",
		l.DashboardToken():    "dashboard.token",
		l.DashboardSessions(): "dashboard-sessions.json",
		l.OwnerApprovals():    "owner-approvals.json",
		l.ScriptsToken():      "scripts.token",
		l.Backup():            "backup",
		l.RcloneConf():        "rclone/rclone.conf",
		l.GoogleClient():      "google/oauth_client.json",
		l.Plaud():             "plaud",
		l.OTP():               "otp",
		l.SiteDevices():       "site/devices.json",
		l.Secrets():           "secrets",
		l.Audit():             "audit",
		l.VaultDB():           "vault.db",
		l.Vault():             "vault",
		l.VaultDocuments():    "vault/documents",
		l.VaultWorkspaces():   "vault/workspaces",
		l.Nodes():             "nodes",
		l.Tsnet():             "tsnet",
		l.Core():              "core",
	} {
		if want = filepath.Join(root, filepath.FromSlash(want)); got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
	// Naming a path creates nothing.
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("the layout created the data dir: %v", err)
	}
	dir, err := EnsureDir(l.VaultWorkspaces())
	if err != nil || dir != l.VaultWorkspaces() {
		t.Fatal(dir, err)
	}
	for _, d := range []string{root, l.Vault(), l.VaultWorkspaces()} {
		if st, err := os.Stat(d); err != nil || st.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v %v, want a 0700 folder", d, st, err)
		}
	}
}

// Opening the store, as the bridge does when it starts, makes none of the
// new folders: their features make them when they run.
func TestStartupMakesNoFleetFolders(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cs, err := Open(filepath.Join(home, "config.json"), Options{Paths: Paths{Home: home, DefaultDataDir: filepath.Join(home, DataDirName)}, GOOS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.Boot().EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	l := cs.Boot().Layout()
	for _, d := range []string{l.Secrets(), l.Audit(), l.VaultDB(), l.Vault(), l.Nodes(), l.Tsnet(), l.Core()} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("%s exists after start: %v", d, err)
		}
	}
}
