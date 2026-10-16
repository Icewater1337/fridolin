package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFilterLines(t *testing.T) {
	got := filterLines([]string{"node_modules/", "*.log", " /work/ ", "", "+ /sneaky", "- already-prefixed"})
	want := "- node_modules/**\n- *.log\n- /work/**\n- already-prefixed\n"
	if got != want {
		t.Errorf("filterLines =\n%q\nwant\n%q", got, want)
	}
}

func TestNextRun(t *testing.T) {
	loc := time.FixedZone("x", 0)
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, loc)
	if got := nextRun("03:30", now); !got.Equal(time.Date(2026, 3, 2, 3, 30, 0, 0, loc)) {
		t.Errorf("nextRun before = %v", got)
	}
	if got := nextRun("23:15", now); !got.Equal(time.Date(2026, 3, 1, 23, 15, 0, 0, loc)) {
		t.Errorf("nextRun later today = %v", got)
	}
}

func TestBackupConfigValidate(t *testing.T) {
	for _, bad := range []BackupConfig{{DailyAt: "25:00"}, {Remote: "my remote"}, {Root: "../x"}, {Root: "/abs"}, {Root: "a:b"}} {
		if bad.validate() == nil {
			t.Errorf("%+v should be rejected", bad)
		}
	}
}

func TestRcloneSection(t *testing.T) {
	conf := "[other]\ntype = s3\n\n[brain-gdrive]\ntype = drive\nscope = drive.file\ntoken = {\"x\":1}\n"
	s := rcloneSection(conf, "brain-gdrive")
	if s["type"] != "drive" || s["token"] == "" {
		t.Errorf("section = %v", s)
	}
	if rcloneSection(conf, "missing") != nil {
		t.Error("missing section should be nil")
	}
}

// fakeRunner scripts rclone's answers for the connect flow.
type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string
	conf  string
}

func (f *fakeRunner) run(_ context.Context, args []string, stderr io.Writer) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, args)
	f.mu.Unlock()
	switch {
	case len(args) > 1 && args[0] == "config" && args[1] == "create":
		return []byte(`{"State":"*oauth-islocal,teamdrive,,","Option":{"Name":"config_is_local","DefaultStr":"true"}}`), nil
	case len(args) > 1 && args[0] == "config" && args[1] == "update" && contains(args, "*oauth-islocal,teamdrive,,"):
		if stderr != nil {
			fmt.Fprintln(stderr, "NOTICE: Please go to the following link: http://127.0.0.1:53682/auth?state=abc123")
		}
		// rclone writes the token into the config file — and prints it.
		_ = os.WriteFile(f.conf, []byte("[brain-gdrive]\ntype = drive\nscope = drive.file\ntoken = {\"access_token\":\"SECRET-TOKEN\"}\n"), 0o600)
		return []byte(`{"State":"*teamdrive","Option":{"Name":"config_change_team_drive","DefaultStr":"false"},"Result":"SECRET-TOKEN"}`), nil
	case len(args) > 1 && args[0] == "config" && args[1] == "update":
		if !contains(args, "false") {
			return nil, fmt.Errorf("team drive question must be answered false: %v", args)
		}
		return []byte(`{"State":""}`), nil
	}
	return nil, nil
}

func TestConnectDriveWalksQuestionsAndHidesToken(t *testing.T) {
	tb := newTestBridge(t, "")
	m := tb.backup
	f := &fakeRunner{conf: m.rcloneConf()}
	m.runner = f.run
	_ = os.MkdirAll(filepath.Dir(m.rcloneConf()), 0o700)

	job := &connectJob{State: "running"}
	if err := m.connectDrive(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if job.AuthURL != "http://127.0.0.1:53682/auth?state=abc123" {
		t.Errorf("auth url = %q", job.AuthURL)
	}
	if !m.connected() {
		t.Error("not connected after the flow")
	}
	last := f.calls[len(f.calls)-1]
	if last[0] != "mkdir" || last[1] != "brain-gdrive:"+defaultBackupRoot {
		t.Errorf("last call = %v, want mkdir of the backup root", last)
	}
	body := tb.do("GET", "/api/backup/status", nil).Body.String()
	if strings.Contains(body, "SECRET-TOKEN") {
		t.Error("the Drive token leaked into the API")
	}
}

func TestRunRcloneUsesIsolatedConfig(t *testing.T) {
	bin := t.TempDir()
	script := filepath.Join(bin, "rclone")
	_ = os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\" > \"$(dirname \"$0\")/args\"\nenv > \"$(dirname \"$0\")/env\"\n"), 0o700)
	tb := newTestBridge(t, `, "backup": {"rclone_path": "`+script+`"}`)
	t.Setenv("RCLONE_CONFIG_EVIL_TYPE", "local")
	if _, err := tb.backup.runRclone(context.Background(), []string{"lsd", "x:"}, nil); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(bin, "args"))
	env, _ := os.ReadFile(filepath.Join(bin, "env"))
	if !strings.HasPrefix(string(args), "--config "+tb.backup.rcloneConf()) {
		t.Errorf("args = %s", args)
	}
	if strings.Contains(string(env), "RCLONE_CONFIG_EVIL") || !strings.Contains(string(env), "RCLONE_CONFIG="+tb.backup.rcloneConf()) {
		t.Error("the user's RCLONE_* environment must not reach the bridge's rclone")
	}
}

// TestBackupRoundTripWithRealRclone backs a brain up to a local folder
// (an rclone alias remote standing in for Google Drive), changes it, backs up
// again, and restores into a new folder.
func TestBackupRoundTripWithRealRclone(t *testing.T) {
	if _, err := exec.LookPath("rclone"); err != nil {
		t.Skip("rclone not installed")
	}
	tb := newTestBridge(t, `, "backup": {"keep_versions_days": 0}`)
	remoteDir := filepath.Join(t.TempDir(), "drive")
	_ = os.MkdirAll(filepath.Dir(tb.backup.rcloneConf()), 0o700)
	_ = os.WriteFile(tb.backup.rcloneConf(), []byte("[brain-gdrive]\ntype = alias\nremote = "+remoteDir+"\n"), 0o600)
	if !tb.backup.connected() {
		t.Fatal("alias remote not recognised")
	}

	m := BrainManifest{ID: "notes", Name: "Notes", Backup: BrainBackup{Enabled: true, Exclude: []string{"node_modules/", "*.log", "/private/"}}}
	if w := tb.do("POST", "/api/brains", map[string]any{"path": "~/notes", "manifest": m, "create_dir": true}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	dir := filepath.Join(tb.home, "notes")
	write := func(rel, s string) {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		_ = os.WriteFile(p, []byte(s), 0o600)
	}
	write("memories/a.md", "version 1")
	write("node_modules/x/index.js", "skip me")
	write("run.log", "skip me")
	write("private/secret.md", "skip me")

	run := func() {
		t.Helper()
		if err := tb.backup.StartRun(nil, true, "manual"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 60*time.Second, "backup", func() bool {
			tb.backup.mu.Lock()
			defer tb.backup.mu.Unlock()
			return tb.backup.job == nil
		})
		tb.backup.mu.Lock()
		res := tb.backup.state.Brains["notes"]
		tb.backup.mu.Unlock()
		if res == nil || !res.OK {
			t.Fatalf("backup result = %+v", res)
		}
	}
	run()
	cur := filepath.Join(remoteDir, defaultBackupRoot, "notes", "current")
	if data, _ := os.ReadFile(filepath.Join(cur, "memories/a.md")); string(data) != "version 1" {
		t.Fatalf("file not backed up")
	}
	for _, skip := range []string{"node_modules", "run.log", "private"} {
		if _, err := os.Stat(filepath.Join(cur, skip)); err == nil {
			t.Errorf("%s should have been excluded", skip)
		}
	}
	if _, err := os.Stat(filepath.Join(remoteDir, defaultBackupRoot, "_bridge", "current", "config.json")); err != nil {
		t.Error("bridge config not backed up")
	}
	if _, err := os.Stat(filepath.Join(remoteDir, defaultBackupRoot, "_bridge", "current", "dashboard.token")); err == nil {
		t.Error("the dashboard token must never be backed up")
	}

	write("memories/a.md", "version 2")
	time.Sleep(1100 * time.Millisecond) // a new version folder name
	run()
	versions, _ := os.ReadDir(filepath.Join(remoteDir, defaultBackupRoot, "notes", "versions"))
	if len(versions) != 1 {
		t.Fatalf("versions = %d, want 1", len(versions))
	}
	old, _ := os.ReadFile(filepath.Join(remoteDir, defaultBackupRoot, "notes", "versions", versions[0].Name(), "memories/a.md"))
	if string(old) != "version 1" {
		t.Errorf("the overwritten file must be kept in versions/, got %q", old)
	}

	// Restore refuses an existing brain folder, then restores into a new one.
	if w := tb.do("POST", "/api/backup/restore", map[string]string{"brain": "notes", "version": "current", "target": "~/notes"}); w.Code != 422 {
		t.Errorf("restore over a brain: %d, want 422", w.Code)
	}
	w := tb.do("POST", "/api/backup/restore", map[string]string{"brain": "notes", "version": "current", "target": "~/restored"})
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	waitFor(t, 30*time.Second, "restore", func() bool {
		data, _ := os.ReadFile(filepath.Join(tb.home, "restored", "memories/a.md"))
		return string(data) == "version 2"
	})
}

func TestExpiredVersions(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.Local)
	stamp := func(days int) string { return now.AddDate(0, 0, -days).Format(versionLayout) }
	cases := []struct {
		name  string
		names []string
		days  int
		want  []string
	}{
		{"older than the window", []string{stamp(31), stamp(29)}, 30, []string{stamp(31)}},
		{"exactly on the cutoff stays", []string{stamp(30)}, 30, nil},
		{"one day window", []string{stamp(2), stamp(0)}, 1, []string{stamp(2)}},
		{"names that are not version stamps stay", []string{"keep-me", "2025", "20250101-0000", stamp(90)}, 30, []string{stamp(90)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := expiredVersions(c.names, now, c.days)
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Errorf("expiredVersions = %v, want %v", got, c.want)
			}
		})
	}
}

// TestBackupVersionsNewestFirst lists version folders by the time in their
// names, with anything else after them.
func TestBackupVersionsNewestFirst(t *testing.T) {
	tb := newTestBridge(t, "")
	tb.backup.runner = func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
		return []byte(`[{"Name":"20261001-120000"},{"Name":"manual-copy"},{"Name":"20261015-080000"},{"Name":"20250101-000000"}]`), nil
	}
	w := tb.do("GET", "/api/backup/versions?brain=notes", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var resp struct {
		Versions []string `json:"versions"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	want := []string{"20261015-080000", "20261001-120000", "20250101-000000", "manual-copy"}
	if fmt.Sprint(resp.Versions) != fmt.Sprint(want) {
		t.Errorf("versions = %v, want %v", resp.Versions, want)
	}
}

// rcloneTestBridge is a bridge whose backup remote is a local folder (an
// rclone alias remote standing in for Google Drive). It skips the test when
// rclone is not installed.
func rcloneTestBridge(t *testing.T, extra string) (*testBridge, string) {
	t.Helper()
	if _, err := exec.LookPath("rclone"); err != nil {
		t.Skip("rclone not installed")
	}
	tb := newTestBridge(t, extra)
	remoteDir := filepath.Join(t.TempDir(), "drive")
	_ = os.MkdirAll(filepath.Dir(tb.backup.rcloneConf()), 0o700)
	_ = os.WriteFile(tb.backup.rcloneConf(), []byte("[brain-gdrive]\ntype = alias\nremote = "+remoteDir+"\n"), 0o600)
	if !tb.backup.connected() {
		t.Fatal("alias remote not recognised")
	}
	return tb, remoteDir
}

// addBackupBrain adds a brain with backups on and returns its folder.
func addBackupBrain(t *testing.T, tb *testBridge, id string) string {
	t.Helper()
	m := BrainManifest{ID: id, Name: id, Backup: BrainBackup{Enabled: true}}
	if w := tb.do("POST", "/api/brains", map[string]any{"path": "~/" + id, "manifest": m, "create_dir": true}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	return filepath.Join(tb.home, id)
}

// runBackup starts a backup of one brain, waits for it, and returns its result.
func runBackup(t *testing.T, tb *testBridge, id string) *BackupResult {
	t.Helper()
	if err := tb.backup.StartRun([]string{id}, false, "manual"); err != nil {
		t.Fatal(err)
	}
	return waitBackup(t, tb, id)
}

func waitBackup(t *testing.T, tb *testBridge, id string) *BackupResult {
	t.Helper()
	waitFor(t, 60*time.Second, "backup", func() bool {
		tb.backup.mu.Lock()
		defer tb.backup.mu.Unlock()
		return tb.backup.job == nil
	})
	tb.backup.mu.Lock()
	defer tb.backup.mu.Unlock()
	res := tb.backup.state.Brains[id]
	if res == nil {
		t.Fatalf("no backup result for %s", id)
	}
	return res
}

// TestBackupPruneKeepsVersionsByRunDate is the retention promise: a file
// replaced or deleted today stays in versions/ for keep_versions_days, however
// old the file itself is, and whole version folders go once their run is
// older than that.
func TestBackupPruneKeepsVersionsByRunDate(t *testing.T) {
	tb, remoteDir := rcloneTestBridge(t, `, "backup": {"keep_versions_days": 30}`)
	dir := addBackupBrain(t, tb, "notes")
	old := time.Now().AddDate(0, 0, -90)
	for _, rel := range []string{"memories/edited.md", "memories/deleted.md", "memories/kept.md"} {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		_ = os.WriteFile(p, []byte("v1 "+rel), 0o600)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if res := runBackup(t, tb, "notes"); !res.OK {
		t.Fatalf("first backup = %+v", res)
	}

	versions := filepath.Join(remoteDir, defaultBackupRoot, "notes", "versions")
	seed := func(name string) {
		p := filepath.Join(versions, name, "memories", "older.md")
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		_ = os.WriteFile(p, []byte("older"), 0o600)
	}
	expired := time.Now().AddDate(0, 0, -40).Format(versionLayout)
	recent := time.Now().AddDate(0, 0, -10).Format(versionLayout)
	seed(expired)
	seed(recent)
	seed("not-a-version")

	_ = os.WriteFile(filepath.Join(dir, "memories/edited.md"), []byte("v2"), 0o600)
	_ = os.Remove(filepath.Join(dir, "memories/deleted.md"))
	res := runBackup(t, tb, "notes")
	if !res.OK {
		t.Fatalf("second backup = %+v", res)
	}

	for _, rel := range []string{"memories/edited.md", "memories/deleted.md"} {
		data, err := os.ReadFile(filepath.Join(versions, res.Version, rel))
		if err != nil || string(data) != "v1 "+rel {
			t.Errorf("the previous copy of %s must survive in versions/%s: %q %v", rel, res.Version, data, err)
		}
	}
	for name, keep := range map[string]bool{expired: false, recent: true, "not-a-version": true} {
		_, err := os.Stat(filepath.Join(versions, name))
		if keep && err != nil {
			t.Errorf("version folder %s should be kept: %v", name, err)
		}
		if !keep && err == nil {
			t.Errorf("version folder %s is older than 30 days and should be purged", name)
		}
	}
}

// TestPrunePurgesExpiredVersionFolders checks the rclone calls prune makes, for
// machines without rclone: it purges the expired version folders by name and
// never filters files by their age.
func TestPrunePurgesExpiredVersionFolders(t *testing.T) {
	tb := newTestBridge(t, `, "backup": {"keep_versions_days": 30}`)
	expired := time.Now().AddDate(0, 0, -31).Format(versionLayout)
	recent := time.Now().AddDate(0, 0, -29).Format(versionLayout)
	var mu sync.Mutex
	var calls [][]string
	tb.backup.runner = func(_ context.Context, args []string, stderr io.Writer) ([]byte, error) {
		mu.Lock()
		calls = append(calls, args)
		mu.Unlock()
		if args[0] != "lsjson" {
			return nil, nil
		}
		if strings.HasSuffix(args[len(args)-1], "/_bridge/versions") {
			fmt.Fprintln(stderr, "ERROR : : error listing: directory not found")
			return nil, fmt.Errorf("exit status 3")
		}
		return []byte(`[{"Name":"` + expired + `"},{"Name":"` + recent + `"},{"Name":"keep-me"}]`), nil
	}
	notes := &Brain{id: "notes"}
	tb.backup.prune(context.Background(), []*Brain{notes})
	var purged []string
	for _, c := range calls {
		switch c[0] {
		case "purge":
			purged = append(purged, c[1])
		case "lsjson":
		default:
			t.Errorf("unexpected rclone call %v", c)
		}
	}
	want := []string{"brain-gdrive:" + defaultBackupRoot + "/notes/versions/" + expired}
	if fmt.Sprint(purged) != fmt.Sprint(want) {
		t.Errorf("purged %v, want %v", purged, want)
	}
}

// TestPruneKeepsItsConfigAndStopsWhenCancelled: the listings and purges of
// one prune all use the config it started with, and a cancelled job stops
// it at once instead of trying (and logging) every remaining id.
func TestPruneKeepsItsConfigAndStopsWhenCancelled(t *testing.T) {
	listing := []byte(`[{"Name":"` + time.Now().AddDate(0, 0, -31).Format(versionLayout) + `"},` +
		`{"Name":"` + time.Now().AddDate(0, 0, -60).Format(versionLayout) + `"}]`)
	brains := []*Brain{{id: "notes"}, {id: "work"}}

	t.Run("backup.root changed mid-run", func(t *testing.T) {
		tb := newTestBridge(t, `, "backup": {"keep_versions_days": 30}`)
		var calls [][]string
		tb.backup.runner = func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
			calls = append(calls, args)
			if len(calls) == 1 {
				if _, err := tb.cfgs.Update("", func(c *Config) error { c.Backup.Root = "moved"; return nil }); err != nil {
					t.Error(err)
				}
			}
			if args[0] == "lsjson" {
				return listing, nil
			}
			return nil, nil
		}
		tb.backup.prune(context.Background(), brains)
		if len(calls) != 9 {
			t.Errorf("%d rclone calls, want a listing and two purges for each of 3 ids: %v", len(calls), calls)
		}
		for _, c := range calls {
			if p := c[len(c)-1]; !strings.HasPrefix(p, "brain-gdrive:"+defaultBackupRoot+"/") {
				t.Errorf("%s %s is not under the backup.root prune started with", c[0], p)
			}
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		tb := newTestBridge(t, `, "backup": {"keep_versions_days": 30}`)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var calls [][]string
		tb.backup.runner = func(ctx context.Context, args []string, _ io.Writer) ([]byte, error) {
			calls = append(calls, args)
			if args[0] == "purge" {
				cancel() // the job is cancelled while rclone purges
				return nil, ctx.Err()
			}
			return listing, nil
		}
		tb.backup.prune(ctx, brains)
		if len(calls) != 2 {
			t.Errorf("prune went on after the cancel: %v", calls)
		}
	})
}
