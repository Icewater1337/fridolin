package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	_ "time/tzdata" // TestNextDue's time zone, on machines without one

	"github.com/Icewater1337/fridolin/internal/backup"
	brainruntime "github.com/Icewater1337/fridolin/internal/brain"

	"github.com/Icewater1337/fridolin/internal/devchat"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// TestBackupScheduleIsArmedOnlyBySavedChanges: switching the schedule on or
// moving it marks when, so that neither starts a backup at once, and only
// once the change is saved. One the config refuses changes nothing.
func TestBackupScheduleIsArmedOnlyBySavedChanges(t *testing.T) {
	tb := newTestBridge(t, `, "backup": {"daily_at": "03:30"}`)
	armed := func() time.Time { return persistedBackupState(t, tb).ArmedAt }
	if w := tb.do("PUT", "/api/backup/settings", map[string]any{"enabled": true, "root": "../x"}); w.Code != 422 {
		t.Fatalf("settings = %d %s, want 422", w.Code, w.Body.String())
	}
	if a := armed(); !a.IsZero() {
		t.Errorf("a refused change armed the schedule at %v", a)
	}
	before := time.Now()
	if w := tb.do("PUT", "/api/backup/settings", map[string]any{"enabled": true}); w.Code != 200 {
		t.Fatalf("settings = %d %s", w.Code, w.Body.String())
	}
	first := armed()
	if first.Before(before) || first.After(time.Now()) {
		t.Errorf("armed at %v, outside the successful save", first)
	}
	if w := tb.do("PUT", "/api/backup/settings", map[string]any{"keep_versions_days": 7}); w.Code != 200 {
		t.Fatalf("settings = %d %s", w.Code, w.Body.String())
	}
	if a := armed(); !a.Equal(first) {
		t.Error("another backup setting re-armed the schedule")
	}
	before = time.Now()
	if w := tb.do("PUT", "/api/backup/settings", map[string]any{"daily_at": "08:00"}); w.Code != 200 {
		t.Fatalf("settings = %d %s", w.Code, w.Body.String())
	}
	if a := armed(); a.Before(before) || a.After(time.Now()) {
		t.Errorf("moving the time armed at %v, outside the successful save", a)
	}
}

// fakeRunner scripts rclone's answers for the connect flow.
type fakeRunner struct {
	mu        sync.Mutex
	calls     [][]string
	scopes    []childScope // each call's helperScope
	conf      string
	authReady chan struct{}
	release   <-chan struct{}
}

func (f *fakeRunner) run(ctx context.Context, args []string, stderr io.Writer) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, args)
	f.scopes = append(f.scopes, helperScope(ctx))
	f.mu.Unlock()
	switch {
	case len(args) > 1 && args[0] == "config" && args[1] == "create":
		return []byte(`{"State":"*oauth-islocal,teamdrive,,","Option":{"Name":"config_is_local","DefaultStr":"true"}}`), nil
	case len(args) > 1 && args[0] == "config" && args[1] == "update" && slices.Contains(args, "*oauth-islocal,teamdrive,,"):
		if stderr != nil {
			fmt.Fprintln(stderr, "NOTICE: Please go to the following link: http://127.0.0.1:53682/auth?state=abc123")
		}
		if f.authReady != nil {
			close(f.authReady)
			<-f.release
		}
		// rclone writes the token into the config file — and prints it.
		_ = os.WriteFile(f.conf, []byte("[brain-gdrive]\ntype = drive\nscope = drive.file\ntoken = {\"access_token\":\"SECRET-TOKEN\"}\n"), 0o600)
		return []byte(`{"State":"*teamdrive","Option":{"Name":"config_change_team_drive","DefaultStr":"false"},"Result":"SECRET-TOKEN"}`), nil
	case len(args) > 1 && args[0] == "config" && args[1] == "update":
		if !slices.Contains(args, "false") {
			return nil, fmt.Errorf("team drive question must be answered false: %v", args)
		}
		return []byte(`{"State":""}`), nil
	}
	return nil, nil
}

func TestConnectDriveWalksQuestionsAndHidesToken(t *testing.T) {
	release := make(chan struct{})
	f := &fakeRunner{authReady: make(chan struct{}), release: release}
	tb := connectTestBridge(t, f.run)
	f.conf = tb.cfgs.Boot().RcloneConf()
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	if w := tb.do("POST", "/api/backup/connect", nil); w.Code != 202 {
		t.Fatalf("connect = %d %s", w.Code, w.Body.String())
	}
	select {
	case <-f.authReady:
	case <-time.After(10 * time.Second):
		t.Fatal("sign-in did not reach browser approval")
	}
	var st struct {
		Connect struct {
			AuthURL string `json:"auth_url"`
		} `json:"connect"`
	}
	if err := json.Unmarshal(tb.do("GET", "/api/backup/status", nil).Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Connect.AuthURL != "http://127.0.0.1:53682/auth?state=abc123" {
		t.Errorf("auth url = %q", st.Connect.AuthURL)
	}
	once.Do(func() { close(release) })
	waitBackupConnect(t, tb)
	if !connectedBackup(tb) {
		t.Error("not connected after the flow")
	}
	last := f.calls[len(f.calls)-1]
	if last[0] != "mkdir" || last[1] != "brain-gdrive:"+config.DefaultBackupRoot {
		t.Errorf("last call = %v, want mkdir of backup root", last)
	}
	for i, scope := range f.scopes {
		if scope != desktopChild {
			t.Errorf("call %v ran as scope %d, want desktopChild", f.calls[i], scope)
		}
	}
	body := tb.do("GET", "/api/backup/status", nil).Body.String()
	if strings.Contains(body, "SECRET-TOKEN") {
		t.Error("Drive token leaked into API")
	}
}

func TestRunRcloneUsesIsolatedConfig(t *testing.T) {
	bin := t.TempDir()
	script := filepath.Join(bin, "rclone")
	_ = os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\" > \"$(dirname \"$0\")/args\"\nenv > \"$(dirname \"$0\")/env\"\necho '[]'\n"), 0o700)
	tb := newTestBridge(t, `, "backup": {"rclone_path": "`+script+`"}`)
	t.Setenv("RCLONE_CONFIG_EVIL_TYPE", "local")
	connectAlias(t, tb)
	if _, err := tb.backup.Versions(context.Background(), "notes"); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(bin, "args"))
	env, _ := os.ReadFile(filepath.Join(bin, "env"))
	if !strings.HasPrefix(string(args), "--config "+tb.cfgs.Boot().RcloneConf()) {
		t.Errorf("args = %s", args)
	}
	if strings.Contains(string(env), "RCLONE_CONFIG_EVIL") || !strings.Contains(string(env), "RCLONE_CONFIG="+tb.cfgs.Boot().RcloneConf()) {
		t.Error("the user's RCLONE_* environment must not reach the bridge's rclone")
	}
}

// TestFindRclone: a configured rclone_path is used as it is, and one that
// does not work is an error that names it, never a quiet fallback to another
// rclone. Without one, PATH comes first, then the fallbacks, of which only an
// executable file counts. Of PATH, only the folders helperPath keeps count,
// also for an rclone_path that is a bare name: an rclone in another PATH
// folder may be one a brain planted there.
func TestFindRclone(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// A PATH folder under HOME, and one outside it.
	home := t.TempDir()
	onPath, outside := filepath.Join(home, "path"), filepath.Join(dir, "path")
	for _, d := range []string{onPath, outside} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "rclone"), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	pathRclone := filepath.Join(onPath, "rclone")
	configured := write("my-rclone", 0o700)
	plain := write("plain-file", 0o600)
	fallback := write("fallback-rclone", 0o700)
	missing := filepath.Join(dir, "missing")

	cases := []struct {
		name      string
		path      string   // backup.rclone_path
		pathDirs  string   // PATH
		fallbacks []string // rcloneFallbacks
		want      string
		wantErr   string // in the error; "" for none
	}{
		{"configured", configured, onPath, []string{fallback}, configured, ""},
		{"configured but missing", missing, onPath, []string{fallback}, "", "backup.rclone_path " + missing},
		{"configured but not executable", plain, onPath, []string{fallback}, "", "backup.rclone_path " + plain},
		{"configured by name", "rclone", outside + ":" + onPath, []string{fallback}, pathRclone, ""},
		{"configured by name, only outside HOME", "rclone", outside, []string{fallback}, "", "backup.rclone_path rclone"},
		{"on PATH", "", onPath, []string{fallback}, pathRclone, ""},
		{"on PATH outside HOME first", "", outside + ":" + onPath, []string{fallback}, pathRclone, ""},
		{"on PATH only outside HOME", "", outside, []string{fallback}, fallback, ""},
		{"fallback", "", dir, []string{missing, plain, fallback}, fallback, ""},
		{"none", "", dir, []string{missing, plain}, "", "rclone is not installed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", home)
			t.Setenv("PATH", tc.pathDirs)
			prev := rcloneFallbacks
			rcloneFallbacks = func() []string { return tc.fallbacks }
			t.Cleanup(func() { rcloneFallbacks = prev })
			got, err := findRclone(tc.path)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("findRclone(%q) failed: %v", tc.path, err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("findRclone(%q) = %q, %v; want an error with %q", tc.path, got, err, tc.wantErr)
			case got != tc.want:
				t.Errorf("findRclone(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// TestBackupStatusNamesABrokenRclonePath: the Backup page says what is
// wrong with a configured rclone_path, and Connect refuses with that rather
// than an install hint.
func TestBackupStatusNamesABrokenRclonePath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "rclone")
	tb := newTestBridge(t, `, "backup": {"rclone_path": "`+missing+`"}`)
	var st struct {
		Rclone struct {
			Found bool   `json:"found"`
			Error string `json:"error"`
		} `json:"rclone"`
	}
	_ = json.Unmarshal(tb.do("GET", "/api/backup/status", nil).Body.Bytes(), &st)
	if st.Rclone.Found || !strings.Contains(st.Rclone.Error, "backup.rclone_path "+missing) {
		t.Errorf("rclone status = %+v, want the broken path named", st.Rclone)
	}
	w := tb.do("POST", "/api/backup/connect", nil)
	if w.Code != 412 || !strings.Contains(w.Body.String(), "backup.rclone_path") || strings.Contains(w.Body.String(), "install") {
		t.Errorf("connect = %d %s, want the broken path named", w.Code, w.Body.String())
	}
}

// TestRcloneGetsTheAllowlistedEnvironment: rclone, for a backup, for the
// version the dashboard shows and while it connects Google Drive, gets what
// childEnv gives a helper program: no RCLONE_* setting of yours, not the
// Claude CLI's login, and not the names in child_env_passthrough, which are
// for brains. Only the connect run, which opens your browser, gets your
// desktop session's variables.
func TestRcloneGetsTheAllowlistedEnvironment(t *testing.T) {
	bin := t.TempDir()
	script := filepath.Join(bin, "rclone")
	// Each run records its environment under its command.
	fake := "#!/bin/sh\ndir=$(dirname \"$0\")\n[ \"$1\" = --config ] && shift 2\nenv > \"$dir/env.$1\"\ncase \"$1\" in version) echo rclone v9.9;; config) if [ \"$2\" = create ]; then printf '[brain-gdrive]\\ntype = alias\\nremote = /nowhere\\n' > \"$RCLONE_CONFIG\"; fi; echo '{\"State\":\"\"}';; *) echo '[]';; esac\n"
	if err := os.WriteFile(script, []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	tb := newTestBridge(t, `, "child_env_passthrough": ["GROQ_API_KEY", "RCLONE_CONFIG_EVIL_TYPE"], "backup": {"rclone_path": "`+script+`"}`)
	for k, v := range map[string]string{
		"HTTPS_PROXY":              "http://proxy.example.com:3128",
		"DISPLAY":                  ":0",
		"DBUS_SESSION_BUS_ADDRESS": "unix:path=/tmp/test-bus",
		"XDG_DATA_DIRS":            "/usr/share:/var/lib/snapd/desktop",
		"GROQ_API_KEY":             "test-groq-key",
		"RCLONE_CONFIG_EVIL_TYPE":  "local",
		"CLAUDE_CODE_OAUTH_TOKEN":  "test-oauth-token",
		"FRIDOLIN_TEST_SECRET":     "test-secret",
	} {
		t.Setenv(k, v)
	}
	connectAlias(t, tb)
	desktop := []string{"DISPLAY=:0", "DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/test-bus", "XDG_DATA_DIRS=/usr/share:/var/lib/snapd/desktop"}
	cases := []struct {
		name, log string
		run       func(*testing.T) error
		desktop   bool
	}{
		{"backup", "env.lsjson", func(t *testing.T) error { _, err := tb.backup.Versions(context.Background(), "notes"); return err }, false},
		{"version", "env.version", func(t *testing.T) error {
			if v := *tb.backup.Status().Rclone.Version; v != "rclone v9.9" {
				return fmt.Errorf("version = %q", v)
			}
			return nil
		}, false},
		{"connect", "env.config", func(t *testing.T) error {
			if err := os.Remove(tb.cfgs.Boot().RcloneConf()); err != nil {
				return err
			}
			if err := tb.backup.Connect(); err != nil {
				return err
			}
			waitBackupConnect(t, tb)
			return nil
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(t); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(bin, tc.log))
			if err != nil {
				t.Fatal(err)
			}
			env := strings.Split(string(data), "\n")
			for _, want := range []string{"HOME=" + os.Getenv("HOME"), "HTTPS_PROXY=http://proxy.example.com:3128", "RCLONE_CONFIG=" + tb.cfgs.Boot().RcloneConf()} {
				if !slices.Contains(env, want) {
					t.Errorf("rclone lacks %s", want)
				}
			}
			for _, kv := range desktop {
				if slices.Contains(env, kv) != tc.desktop {
					t.Errorf("rclone got %s: %v, want %v", kv, !tc.desktop, tc.desktop)
				}
			}
			for _, kv := range env {
				for _, k := range []string{"GROQ_API_KEY", "RCLONE_CONFIG_EVIL_TYPE", "CLAUDE_CODE_OAUTH_TOKEN", "FRIDOLIN_TEST_SECRET"} {
					if strings.HasPrefix(kv, k+"=") {
						t.Errorf("rclone got %s", kv)
					}
				}
			}
		})
	}
}

// TestBackupRoundTripWithRealRclone backs a brain up to a local folder
// (an rclone alias remote standing in for Google Drive), changes it, backs up
// again, and restores into a new folder.
func TestBackupRoundTripWithRealRclone(t *testing.T) {
	rclone := needRclone(t)
	tb := newTestBridge(t, `, "backup": {"keep_versions_days": 0}`)
	useRclone(t, tb, rclone)
	remoteDir := filepath.Join(t.TempDir(), "drive")
	_ = os.MkdirAll(filepath.Dir(tb.cfgs.Boot().RcloneConf()), 0o700)
	_ = os.WriteFile(tb.cfgs.Boot().RcloneConf(), []byte("[brain-gdrive]\ntype = alias\nremote = "+remoteDir+"\n"), 0o600)
	if !connectedBackup(tb) {
		t.Fatal("alias remote not recognised")
	}

	m := manifest.Manifest{ID: "notes", Name: "Notes", Backup: manifest.Backup{Enabled: true, Exclude: []string{"node_modules/", "*.log", "/private/"}}}
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
	// Never uploaded, whatever the excludes say.
	write(".env", "skip me")
	write("keys/id_ed25519", "skip me")

	run := func() {
		t.Helper()
		if err := tb.backup.StartRun(nil, true, "manual", false); err != nil {
			t.Fatal(err)
		}
		if res := waitBackup(t, tb, "notes"); !res.OK {
			t.Fatalf("backup result = %+v", res)
		}
	}
	run()
	cur := filepath.Join(remoteDir, config.DefaultBackupRoot, "notes", "current")
	if data, _ := os.ReadFile(filepath.Join(cur, "memories/a.md")); string(data) != "version 1" {
		t.Fatalf("file not backed up")
	}
	for _, skip := range []string{"node_modules", "run.log", "private", ".env", "keys/id_ed25519"} {
		if _, err := os.Stat(filepath.Join(cur, skip)); err == nil {
			t.Errorf("%s should have been excluded", skip)
		}
	}
	if _, err := os.Stat(filepath.Join(remoteDir, config.DefaultBackupRoot, "_bridge", "current", "config.json")); err != nil {
		t.Error("bridge config not backed up")
	}
	if _, err := os.Stat(filepath.Join(remoteDir, config.DefaultBackupRoot, "_bridge", "current", "dashboard.token")); err == nil {
		t.Error("the dashboard token must never be backed up")
	}

	write("memories/a.md", "version 2")
	time.Sleep(1100 * time.Millisecond) // a new version folder name
	run()
	versions, _ := os.ReadDir(filepath.Join(remoteDir, config.DefaultBackupRoot, "notes", "versions"))
	if len(versions) != 1 {
		t.Fatalf("versions = %d, want 1", len(versions))
	}
	old, _ := os.ReadFile(filepath.Join(remoteDir, config.DefaultBackupRoot, "notes", "versions", versions[0].Name(), "memories/a.md"))
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
	testutil.WaitFor(t, 30*time.Second, "restore", func() bool {
		data, _ := os.ReadFile(filepath.Join(tb.home, "restored", "memories/a.md"))
		return string(data) == "version 2"
	})
}

// TestBackupVersionsNewestFirst lists version folders by the time in their
// names, with anything else after them.
func TestBackupVersionsNewestFirst(t *testing.T) {
	tb := newTestBridge(t, "")
	connectAlias(t, tb)
	installBackupRunner(t, tb, func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
		return []byte(`[{"Name":"20261001-120000"},{"Name":"manual-copy"},{"Name":"20261016-080000Z"},{"Name":"20261015-080000"},{"Name":"20250101-000000"}]`), nil
	})
	w := tb.do("GET", "/api/brains/notes/backup/versions", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var resp struct {
		Versions []string `json:"versions"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	want := []string{"20261016-080000Z", "20261015-080000", "20261001-120000", "20250101-000000", "manual-copy"}
	if fmt.Sprint(resp.Versions) != fmt.Sprint(want) {
		t.Errorf("versions = %v, want %v", resp.Versions, want)
	}
}

// needRclone skips a test that runs the real rclone when there is none,
// unless FRIDOLIN_REQUIRE_RCLONE is set: CI installs rclone and sets it,
// so that these tests cannot quietly go unrun there. It returns the rclone
// on PATH.
func needRclone(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("rclone")
	if err != nil {
		if os.Getenv("FRIDOLIN_REQUIRE_RCLONE") != "" {
			t.Fatalf("FRIDOLIN_REQUIRE_RCLONE is set, but there is no rclone: %v", err)
		}
		t.Skip("rclone not installed")
	}
	return p
}

// useRclone sets tb's backup.rclone_path to rclone. The tests run with a
// HOME of their own, so an rclone installed in your home folder (in
// ~/.local/bin, say) is not under it, and the bridge would not look there.
func useRclone(t *testing.T, tb *testBridge, rclone string) {
	t.Helper()
	if _, err := tb.cfgs.Update("", func(c *config.File) error { c.Backup.RclonePath = rclone; return nil }); err != nil {
		t.Fatal(err)
	}
}

// rcloneTestBridge is a bridge whose backup remote is a local folder (an
// rclone alias remote standing in for Google Drive). It skips the test when
// rclone is not installed (see needRclone).
func rcloneTestBridge(t *testing.T, extra string) (*testBridge, string) {
	t.Helper()
	rclone := needRclone(t)
	tb := newTestBridge(t, extra)
	useRclone(t, tb, rclone)
	remoteDir := filepath.Join(t.TempDir(), "drive")
	_ = os.MkdirAll(filepath.Dir(tb.cfgs.Boot().RcloneConf()), 0o700)
	_ = os.WriteFile(tb.cfgs.Boot().RcloneConf(), []byte("[brain-gdrive]\ntype = alias\nremote = "+remoteDir+"\n"), 0o600)
	if !connectedBackup(tb) {
		t.Fatal("alias remote not recognised")
	}
	return tb, remoteDir
}

// addBackupBrain adds a brain with backups on and returns its folder.
func addBackupBrain(t *testing.T, tb *testBridge, id string) string {
	t.Helper()
	m := manifest.Manifest{ID: id, Name: id, Backup: manifest.Backup{Enabled: true}}
	if w := tb.do("POST", "/api/brains", map[string]any{"path": "~/" + id, "manifest": m, "create_dir": true}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	return filepath.Join(tb.home, id)
}

// runBackup starts a backup of one brain, waits for it, and returns its result.
func runBackup(t *testing.T, tb *testBridge, id string) *backup.Result {
	t.Helper()
	if err := tb.backup.StartRun([]string{id}, false, "manual", false); err != nil {
		t.Fatal(err)
	}
	return waitBackup(t, tb, id)
}

// waitBackup waits until no backup runs, as the Backup page sees it, and
// returns the last result of brain id.
func waitBackup(t *testing.T, tb *testBridge, id string) *backup.Result {
	t.Helper()
	var res *backup.Result
	testutil.WaitFor(t, 60*time.Second, "backup", func() bool {
		running, last := backupStatus(t, tb)
		res = last[id]
		return !running
	})
	if res == nil {
		t.Fatalf("no backup result for %s", id)
	}
	return res
}

// backupStatus reports from GET /api/backup/status whether a backup runs,
// and each brain's last result.
func backupStatus(t *testing.T, tb *testBridge) (running bool, last map[string]*backup.Result) {
	t.Helper()
	var st struct {
		Job    any `json:"job"`
		Brains []struct {
			ID   string         `json:"id"`
			Last *backup.Result `json:"last"`
		} `json:"brains"`
	}
	if w := tb.do("GET", "/api/backup/status", nil); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &st) != nil {
		t.Fatalf("backup status: %d %s", w.Code, w.Body.String())
	}
	last = map[string]*backup.Result{}
	for _, b := range st.Brains {
		last[b.ID] = b.Last
	}
	return st.Job != nil, last
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

	versions := filepath.Join(remoteDir, config.DefaultBackupRoot, "notes", "versions")
	seed := func(name string) {
		p := filepath.Join(versions, name, "memories", "older.md")
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		_ = os.WriteFile(p, []byte("older"), 0o600)
	}
	expired := backup.VersionName(time.Now().AddDate(0, 0, -40))
	recent := backup.VersionName(time.Now().AddDate(0, 0, -10))
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

// deletionRunner plays rclone for the mass-deletion check: lsjson lists src
// for the brain folder and cur for current/ (nil means rclone reports the
// folder as not found), and sync and lsjson record their arguments.
type deletionRunner struct {
	mu      sync.Mutex
	src     []string
	cur     []string
	srcErr  string // set: listing the brain folder fails with this message
	listErr string // set: listing current/ fails with this message
	syncErr string // set: the sync fails with this error in rclone's log
	syncs   [][]string
	lists   [][]string
}

func (d *deletionRunner) run(_ context.Context, args []string, stderr io.Writer) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch args[0] {
	case "sync":
		d.syncs = append(d.syncs, args)
		if d.syncErr != "" {
			fmt.Fprintf(stderr, "{\"level\":\"error\",\"msg\":%q}\n", d.syncErr)
			return nil, fmt.Errorf("exit status 1")
		}
		return nil, nil
	case "lsjson":
		if slices.Contains(args, "--dirs-only") {
			return []byte("[]"), nil
		}
		d.lists = append(d.lists, args)
		files, listErr := d.src, d.srcErr
		for _, a := range args {
			if strings.HasPrefix(a, "brain-gdrive:") { // the backup, not the brain folder
				files, listErr = d.cur, d.listErr
			}
		}
		if listErr != "" {
			fmt.Fprintln(stderr, listErr)
			return nil, fmt.Errorf("exit status 1")
		}
		if files == nil {
			fmt.Fprintln(stderr, "ERROR : : error listing: directory not found")
			return nil, fmt.Errorf("exit status 3")
		}
		var out []map[string]any
		for _, f := range files {
			out = append(out, map[string]any{"Path": f, "Name": filepath.Base(f), "IsDir": false})
		}
		data, _ := json.Marshal(out)
		return data, nil
	}
	return nil, nil
}

// notesBase is where the default config backs up the brain "notes".
const notesBase = config.DefaultBackupRemote + ":" + config.DefaultBackupRoot + "/notes"

// TestBackupRunForceOverridesRefusal: the API's force field reaches the run,
// and only for the brains it names.
func TestBackupRunForceOverridesRefusal(t *testing.T) {
	tb := newTestBridge(t, "")
	_ = os.MkdirAll(filepath.Dir(tb.cfgs.Boot().RcloneConf()), 0o700)
	_ = os.WriteFile(tb.cfgs.Boot().RcloneConf(), []byte("[brain-gdrive]\ntype = alias\nremote = /nowhere\n"), 0o600)
	addBackupBrain(t, tb, "notes")
	d := &deletionRunner{src: []string{}, cur: []string{"a", "b"}}
	installBackupRunner(t, tb, d.run)

	// Forcing every brain at once is refused: force names its brains.
	for _, body := range []map[string]any{{"force": true}, {"brains": []string{}, "force": true}} {
		if w := tb.do("POST", "/api/backup/run", body); w.Code != 400 || !strings.Contains(w.Body.String(), "force needs a brain") {
			t.Errorf("%v: %d %s, want 400", body, w.Code, w.Body.String())
		}
	}
	if running, last := backupStatus(t, tb); running || last["notes"] != nil {
		t.Fatal("a rejected forced run must not start a backup")
	}

	for _, force := range []bool{false, true} {
		if w := tb.do("POST", "/api/backup/run", map[string]any{"brains": []string{"notes"}, "include_bridge": false, "force": force}); w.Code != 202 {
			t.Fatal(w.Body.String())
		}
		res := waitBackup(t, tb, "notes")
		d.mu.Lock()
		syncs := len(d.syncs)
		d.mu.Unlock()
		if !force && (res.OK || !res.Refused || syncs != 0) {
			t.Errorf("without force: want a refusal, got %+v and %d syncs", res, syncs)
		}
		if force && (!res.OK || syncs != 1) {
			t.Errorf("with force: want the sync, got %+v and %d syncs", res, syncs)
		}
	}
	body := tb.do("GET", "/api/backup/status", nil).Body.String()
	if strings.Contains(body, `"refused"`) {
		t.Errorf("a forced run that succeeded must clear the refusal: %s", body)
	}
}

// scheduledBackupBridge is a bridge with the daily backup on and a brain
// "notes" whose folder lost every backed-up file, so each run refuses.
func scheduledBackupBridge(t *testing.T) (*testBridge, *deletionRunner) {
	t.Helper()
	tb := newTestBridge(t, `, "backup": {"enabled": true, "daily_at": "03:30", "include_bridge_config": false}`)
	_ = os.MkdirAll(filepath.Dir(tb.cfgs.Boot().RcloneConf()), 0o700)
	_ = os.WriteFile(tb.cfgs.Boot().RcloneConf(), []byte("[brain-gdrive]\ntype = alias\nremote = /nowhere\n"), 0o600)
	addBackupBrain(t, tb, "notes")
	d := &deletionRunner{src: []string{}, cur: []string{"a", "b"}}
	installBackupRunner(t, tb, d.run)
	return tb, d
}

// TestScheduledRefusalIsToldOnWhatsApp: a scheduled run that refuses tells
// the brain's chat once; later refusals stay quiet until a backup of the
// brain succeeds, manual refusals are never sent (the owner is looking at
// the dashboard), and a notice that failed to send is tried next time.
func TestScheduledRefusalIsToldOnWhatsApp(t *testing.T) {
	tb, runner := scheduledBackupBridge(t)
	var failNotice bool
	installBackupRunner(t, tb, runner.run, func(opts *backup.Options) {
		send := opts.Notify
		opts.Notify = func(ctx context.Context, target backup.Target, text string) error {
			if failNotice {
				return fmt.Errorf("WhatsApp is not connected")
			}
			return send(ctx, target, text)
		}
	})
	br := tb.brainMgr.Get("notes")
	jid := tb.replyChatFor(br, tb.cfgs.Get())
	if jid == "" {
		t.Fatal("the test brain has no chat")
	}
	sent := func() []devchat.Message { return tb.dev.Outbox() }
	told := func() bool {
		_, ok := persistedBackupState(t, tb).RefusalNotices["notes"]
		return ok
	}

	if res := startScheduledBackup(t, tb); !res.Refused {
		t.Fatalf("first scheduled run = %+v, want a refusal", res)
	}
	out := sent()
	if len(out) != 1 || out[0].JID != jid || !strings.Contains(out[0].Text, "Backup of notes: "+"refusing to mirror a mass deletion") ||
		!strings.Contains(out[0].Text, "2 of 2 backed-up files are gone") {
		t.Fatalf("want one notice to %s, got %+v", jid, out)
	}
	if _, ok := persistedBackupState(t, tb).RefusalNotices["notes"]; !ok {
		t.Error("the notice is not saved, so a restart would send it again")
	}

	startScheduledBackup(t, tb)
	if n := len(sent()); n != 1 {
		t.Errorf("%d messages after a second scheduled refusal, want still 1", n)
	}

	// A backup that succeeds ends the streak; the next scheduled refusal is
	// told again, once it can be sent.
	if err := tb.backup.StartRun([]string{"notes"}, false, "manual", true); err != nil {
		t.Fatal(err)
	}
	if res := waitBackup(t, tb, "notes"); !res.OK || told() {
		t.Fatalf("forced run = %+v, told = %v; want OK and the notice cleared", res, told())
	}
	if res := runBackup(t, tb, "notes"); !res.Refused || told() || len(sent()) != 1 {
		t.Errorf("a manual refusal must not be sent: %+v, %d messages", res, len(sent()))
	}
	failNotice = true
	startScheduledBackup(t, tb)
	if told() || len(sent()) != 1 {
		t.Errorf("a notice that failed to send must not count as told")
	}
	failNotice = false
	startScheduledBackup(t, tb)
	if out := sent(); len(out) != 2 || !told() {
		t.Errorf("want the second streak told once it could be sent, got %+v", out)
	}
}

// TestBackupMassDeletionWithRealRclone wipes a brain's memories/, which is
// far less than half of it, then all of it: both runs are refused and leave
// current/ alone, and a forced run from the API mirrors the deletion with
// the files kept in versions/.
func TestBackupMassDeletionWithRealRclone(t *testing.T) {
	tb, remoteDir := rcloneTestBridge(t, "")
	dir := addBackupBrain(t, tb, "notes")
	for i := 0; i < 6; i++ {
		_ = os.WriteFile(filepath.Join(dir, "memories", fmt.Sprintf("m%d.md", i)), []byte("memory"), 0o600)
	}
	for i := 0; i < 30; i++ {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("note%d.md", i)), []byte("note"), 0o600)
	}
	if res := runBackup(t, tb, "notes"); !res.OK {
		t.Fatalf("first backup = %+v", res)
	}
	cur := filepath.Join(remoteDir, config.DefaultBackupRoot, "notes", "current")
	count := func(root string) int {
		n := 0
		_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				n++
			}
			return nil
		})
		return n
	}
	backedUp := count(cur)

	for _, wipe := range []string{"memories", "everything"} {
		t.Run(wipe, func(t *testing.T) {
			if wipe == "memories" {
				_ = os.RemoveAll(filepath.Join(dir, "memories"))
			} else {
				entries, _ := os.ReadDir(dir)
				for _, e := range entries {
					_ = os.RemoveAll(filepath.Join(dir, e.Name()))
				}
			}
			res := runBackup(t, tb, "notes")
			if res.OK || !res.Refused || !strings.HasPrefix(res.Error, "refusing to mirror a mass deletion") {
				t.Errorf("result = %+v, want a refusal", res)
			}
			if wipe == "memories" && !strings.Contains(res.Error, "in memories/ are gone") {
				t.Errorf("error = %q, want it to name memories/", res.Error)
			}
			if n := count(cur); n != backedUp {
				t.Errorf("current/ has %d files after a refused run, want all %d", n, backedUp)
			}
			body := tb.do("GET", "/api/backup/status", nil).Body.String()
			if !strings.Contains(body, "refusing to mirror a mass deletion") || !strings.Contains(body, `"refused":true`) {
				t.Errorf("the refusal is not on the Backup page's status: %s", body)
			}
		})
	}

	if w := tb.do("POST", "/api/backup/run", map[string]any{"brains": []string{"notes"}, "include_bridge": false, "force": true}); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	res := waitBackup(t, tb, "notes")
	if !res.OK || res.Refused {
		t.Fatalf("forced run = %+v", res)
	}
	if n := count(cur); n != 0 {
		t.Errorf("current/ has %d files after mirroring the empty folder, want 0", n)
	}
	if n := count(filepath.Join(remoteDir, config.DefaultBackupRoot, "notes", "versions", res.Version)); n != backedUp {
		t.Errorf("versions/%s holds %d files, want all %d deleted ones", res.Version, n, backedUp)
	}
}

// TestBackupExcludedThenDeletedWithRealRclone: files backed up, then
// excluded, then deleted from the brain folder stay in current/ (a sync never
// deletes excluded files), so the check must not count them as gone. If it
// did, the brain would be refused every night, and a forced run could not
// clear it.
func TestBackupExcludedThenDeletedWithRealRclone(t *testing.T) {
	tb, remoteDir := rcloneTestBridge(t, "")
	dir := addBackupBrain(t, tb, "notes")
	_ = os.WriteFile(filepath.Join(dir, "memories", "keep.md"), []byte("keep"), 0o600)
	_ = os.MkdirAll(filepath.Join(dir, "exports"), 0o700)
	for i := 0; i < 20; i++ {
		_ = os.WriteFile(filepath.Join(dir, "exports", fmt.Sprintf("e%d.csv", i)), []byte("export"), 0o600)
	}
	if err := os.Symlink(filepath.Join("memories", "keep.md"), filepath.Join(dir, "latest.md")); err != nil {
		t.Fatal(err)
	}
	if res := runBackup(t, tb, "notes"); !res.OK {
		t.Fatalf("first backup = %+v", res)
	}

	if w := tb.do("PUT", "/api/brains/notes/backup", map[string]any{"exclude": []string{"exports/"}}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if err := os.RemoveAll(filepath.Join(dir, "exports")); err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		if res := runBackup(t, tb, "notes"); !res.OK || res.Refused {
			t.Fatalf("run %d after excluding and deleting exports/ = %+v, want OK", run, res)
		}
	}
	cur := filepath.Join(remoteDir, config.DefaultBackupRoot, "notes", "current")
	if entries, _ := os.ReadDir(filepath.Join(cur, "exports")); len(entries) != 20 {
		t.Errorf("current/exports has %d files, want all 20: the sync leaves excluded files in Drive", len(entries))
	}
	if _, err := os.Lstat(filepath.Join(cur, "latest.md")); err != nil {
		t.Errorf("the symlink is not backed up: %v", err)
	}
}

// TestBackupPartlyReadableFolderWithRealRclone: a brain folder rclone can
// only partly read is no wipe, and no reason to skip its backup either. The
// readable new file reaches current/, nothing is deleted from it, and the
// run fails with the read error.
func TestBackupPartlyReadableFolderWithRealRclone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a folder with mode 000")
	}
	tb, remoteDir := rcloneTestBridge(t, "")
	dir := addBackupBrain(t, tb, "notes")
	for _, rel := range []string{"memories/old.md", "locked/secret.md", "deleted.md"} {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(rel), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if res := runBackup(t, tb, "notes"); !res.OK {
		t.Fatalf("first backup = %+v", res)
	}

	_ = os.WriteFile(filepath.Join(dir, "memories", "new.md"), []byte("new"), 0o600)
	_ = os.Remove(filepath.Join(dir, "deleted.md"))
	locked := filepath.Join(dir, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) // so the temp folder can be removed
	res := runBackup(t, tb, "notes")
	if res.OK || res.Refused || !strings.Contains(res.Error, "parts of the brain folder could not be read") || !strings.Contains(res.Error, "locked") {
		t.Errorf("result = %+v, want a failure that names the unreadable folder", res)
	}
	cur := filepath.Join(remoteDir, config.DefaultBackupRoot, "notes", "current")
	for rel, why := range map[string]string{
		"memories/new.md":  "the readable new file must be backed up",
		"memories/old.md":  "an unchanged file must stay",
		"locked/secret.md": "a file that could not be read must not be deleted",
		"deleted.md":       "nothing may be deleted while the folder cannot be counted",
	} {
		if _, err := os.Stat(filepath.Join(cur, rel)); err != nil {
			t.Errorf("current/%s: %v; %s", rel, err, why)
		}
	}
}

// connectTestBridge is a bridge whose rclone_path is a script that does
// nothing, so that Connect gets past its rclone check, and whose runner is
// run.
func connectTestBridge(t *testing.T, run func(ctx context.Context, args []string, stderr io.Writer) ([]byte, error)) *testBridge {
	t.Helper()
	script := filepath.Join(t.TempDir(), "rclone")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	tb := newTestBridge(t, `, "backup": {"rclone_path": "`+script+`"}`)
	installBackupRunner(t, tb, run)
	if err := os.MkdirAll(filepath.Dir(tb.cfgs.Boot().RcloneConf()), 0o700); err != nil {
		t.Fatal(err)
	}
	return tb
}

// connectAlias gives the bridge's rclone config a remote, so that the
// bridge counts Drive as connected.
func connectAlias(t *testing.T, tb *testBridge) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(tb.cfgs.Boot().RcloneConf()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tb.cfgs.Boot().RcloneConf(), []byte("[brain-gdrive]\ntype = alias\nremote = /nowhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestBackupRestoreChecksWhatItIsGiven: the brain and version name one
// folder of the backup, and the target is a new or empty folder of yours
// that is neither a brain's nor the bridge's, nor inside what the sandbox
// keeps from brains: the copy is written outside any sandbox, so
// ~/.claude/skills/x would be loaded by your own Claude Code sessions.
func TestBackupRestoreChecksWhatItIsGiven(t *testing.T) {
	tb := newTestBridge(t, `, "sandbox": {"credentials_dir": "~/creds", "deny_paths": ["~/clients"]}`)
	connectAlias(t, tb)
	addBackupBrain(t, tb, "notes")
	var mu sync.Mutex
	var calls [][]string
	installBackupRunner(t, tb, func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
		mu.Lock()
		calls = append(calls, args)
		mu.Unlock()
		return []byte("[]"), nil
	})
	full := filepath.Join(tb.home, "full")
	_ = os.MkdirAll(full, 0o700)
	_ = os.WriteFile(filepath.Join(full, "x"), nil, 0o600)
	// Folders of yours that lead elsewhere: a new folder under one is made
	// where it points.
	for link, to := range map[string]string{"to-notes": "notes", "to-data": "data/media", "to-creds": "creds"} {
		if err := os.MkdirAll(filepath.Join(tb.home, to), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(tb.home, to), filepath.Join(tb.home, link)); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name, brain, version, target string
		code                         int
	}{
		{"brain ..", "..", "current", "~/r1", 400},
		{"brain .", ".", "current", "~/r1", 400},
		{"brain with a slash", "a/b", "current", "~/r1", 400},
		{"brain with a colon", "other:x", "current", "~/r1", 400},
		{"brain with a newline", "a\nb", "current", "~/r1", 400},
		{"no brain", "", "current", "~/r1", 400},
		{"version ..", "notes", "..", "~/r1", 400},
		{"version not a stamp", "notes", "latest", "~/r1", 400},
		{"version with a path", "notes", "20261018-093000/../../x", "~/r1", 400},
		{"into a brain", "notes", "current", "~/notes/restored", 422},
		{"around a brain", "notes", "current", "~", 422},
		{"into the data dir", "notes", "current", "~/data/media/restored", 422},
		{"into the credentials folder", "notes", "current", "~/creds/restored", 422},
		{"outside home", "notes", "current", "/tmp/restored", 422},
		{"not empty", "notes", "current", "~/full", 422},
		{"into a brain through a symlink", "notes", "current", "~/to-notes/restored", 422},
		{"into the data dir through a symlink", "notes", "current", "~/to-data/a/restored", 422},
		{"into the credentials folder through a symlink", "notes", "current", "~/to-creds/restored", 422},
		{"into ~/.claude", "notes", "current", "~/.claude/skills/restored", 422},
		{"into ~/.ssh", "notes", "current", "~/.ssh/restored", 422},
		{"into LaunchAgents", "notes", "current", "~/Library/LaunchAgents/restored", 422},
		{"into a deny_paths entry", "notes", "current", "~/clients/restored", 422},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := tb.do("POST", "/api/backup/restore", map[string]string{"brain": tc.brain, "version": tc.version, "target": tc.target})
			if w.Code != tc.code {
				t.Errorf("restore = %d %s, want %d", w.Code, w.Body.String(), tc.code)
			}
		})
	}
	for _, made := range []string{"notes/restored", "data/media/a", "creds/restored"} {
		if _, err := os.Lstat(filepath.Join(tb.home, made)); !os.IsNotExist(err) {
			t.Errorf("a refused restore made ~/%s (%v)", made, err)
		}
	}
	for _, id := range []string{"a/b", "a:b", "a\x01b"} {
		if w := tb.do("GET", "/api/brains/"+url.PathEscape(id)+"/backup/versions", nil); w.Code != 400 {
			t.Errorf("versions of %q = %d, want 400", id, w.Code)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 0 {
		t.Errorf("rclone ran %v", calls)
	}
}

// TestBackupRestoreIntoYourOwnFilesFolders: a restore refused every folder
// inside what the sandbox denies brains, ~/Documents too, where brain
// folders may live and a new one is accepted: restoring a brain to
// ~/Documents/assistant, where it used to be, failed. Only the folders of
// your own files are open to a restore now; secrets and deny_paths
// entries stay refused.
func TestBackupRestoreIntoYourOwnFilesFolders(t *testing.T) {
	tb := newTestBridge(t, `, "sandbox": {"deny_paths": ["~/clients"]}`)
	cases := []struct {
		target string
		ok     bool
	}{
		{"~/Documents/assistant/restored", true},
		{"~/Desktop/restored", true},
		{"~/Music/restored", true},
		{"~/.ssh/restored", false},
		{"~/.config/gh/restored", false},
		{"~/Library/Mail/restored", false},
		{"~/clients/restored", false},
	}
	for _, c := range cases {
		t.Run(c.target, func(t *testing.T) {
			if _, err := tb.restoreTarget(c.target); (err == nil) != c.ok {
				t.Errorf("restoreTarget(%s) = %v, want ok=%v", c.target, err, c.ok)
			}
		})
	}
}

// TestBackupRestoreNeedsDrive: a restore and the versions list say that
// Drive is not connected, rather than fail out of sight.
func TestBackupRestoreNeedsDrive(t *testing.T) {
	tb := newTestBridge(t, "")
	if w := tb.do("POST", "/api/backup/restore", map[string]string{"brain": "notes", "version": "current", "target": "~/restored"}); w.Code != 412 {
		t.Errorf("restore = %d %s, want 412", w.Code, w.Body.String())
	}
	if w := tb.do("GET", "/api/brains/notes/backup/versions", nil); w.Code != 412 {
		t.Errorf("versions = %d %s, want 412", w.Code, w.Body.String())
	}
}

// TestBackupRestoreRecordsItsResult: a restore runs as a job of its own
// kind, copies the folder it was asked for, and its outcome, a failure
// included, stays on the Backup page after it ended.
func TestBackupRestoreRecordsItsResult(t *testing.T) {
	cases := []struct {
		name    string
		version string
		fail    bool
		wantSrc string
	}{
		{"latest copy", "current", false, notesBase + "/current"},
		{"a version", "20261018-093000Z", false, notesBase + "/versions/20261018-093000Z"},
		{"a version named in local time", "20261001-120000", false, notesBase + "/versions/20261001-120000"},
		{"failed", "current", true, notesBase + "/current"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tb := newTestBridge(t, "")
			connectAlias(t, tb)
			release := make(chan struct{})
			var src string
			installBackupRunner(t, tb, func(_ context.Context, args []string, stderr io.Writer) ([]byte, error) {
				src = args[1]
				<-release
				if tc.fail {
					fmt.Fprintln(stderr, `{"level":"error","msg":"directory not found"}`)
					return nil, errors.New("exit status 3")
				}
				fmt.Fprintln(stderr, `{"level":"notice","msg":"stats","stats":{"bytes":2048,"transfers":3}}`)
				return nil, nil
			})
			w := tb.do("POST", "/api/backup/restore", map[string]string{"brain": "notes", "version": tc.version, "target": "~/restored"})
			if w.Code != 202 {
				t.Fatalf("restore = %d %s", w.Code, w.Body.String())
			}
			if fi, err := os.Stat(filepath.Join(tb.home, "restored")); err != nil || !fi.IsDir() {
				t.Errorf("the target was not made before the restore started (%v)", err)
			}
			var st struct {
				Job struct {
					Kind   string `json:"kind"`
					Brain  string `json:"brain"`
					Target string `json:"target"`
				} `json:"job"`
				LastRestore *backup.RestoreResult `json:"last_restore"`
			}
			_ = json.Unmarshal(tb.do("GET", "/api/backup/status", nil).Body.Bytes(), &st)
			if st.Job.Kind != "restore" || st.Job.Brain != "notes" || st.Job.Target != filepath.Join(tb.home, "restored") {
				t.Errorf("job = %+v, want the restore of notes", st.Job)
			}
			close(release)
			testutil.WaitFor(t, 10*time.Second, "the restore", func() bool {
				return !tb.backup.Summary().Running
			})
			if src != tc.wantSrc {
				t.Errorf("copied %s, want %s", src, tc.wantSrc)
			}
			_ = json.Unmarshal(tb.do("GET", "/api/backup/status", nil).Body.Bytes(), &st)
			r := st.LastRestore
			switch {
			case r == nil:
				t.Fatal("no last restore")
			case tc.fail && (r.OK || !strings.Contains(r.Error, "directory not found")):
				t.Errorf("last restore = %+v, want the failure", r)
			case !tc.fail && (!r.OK || r.Files != 3 || r.Bytes != 2048 || r.Version != tc.version):
				t.Errorf("last restore = %+v, want 3 files of %s", r, tc.version)
			}
		})
	}
}

// TestBackupRestoreFolderThatCannotBeMade: the restore folder is made
// before the job starts, so a failure is the request's error.
func TestBackupRestoreFolderThatCannotBeMade(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to read-only folders")
	}
	tb := newTestBridge(t, "")
	connectAlias(t, tb)
	ro := filepath.Join(tb.home, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	w := tb.do("POST", "/api/backup/restore", map[string]string{"brain": "notes", "version": "current", "target": "~/ro/restored"})
	if w.Code != 500 || !strings.Contains(w.Body.String(), "creating the restore folder") {
		t.Errorf("restore = %d %s, want 500", w.Code, w.Body.String())
	}
	if tb.backup.Summary().Running {
		t.Error("a restore started anyway")
	}
}

// TestBackupCancel: Cancel on the Backup page stops the running backup,
// whose result then says it was cancelled, and is refused when none runs.
func TestBackupCancel(t *testing.T) {
	tb := newTestBridge(t, "")
	connectAlias(t, tb)
	addBackupBrain(t, tb, "notes")
	if w := tb.do("POST", "/api/backup/cancel", nil); w.Code != 409 {
		t.Errorf("cancel with nothing running = %d, want 409", w.Code)
	}
	syncing := make(chan struct{})
	installBackupRunner(t, tb, func(ctx context.Context, args []string, _ io.Writer) ([]byte, error) {
		if args[0] != "sync" {
			return []byte("[]"), nil
		}
		close(syncing)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if w := tb.do("POST", "/api/backup/run", map[string]any{"brains": []string{"notes"}, "include_bridge": false, "force": true}); w.Code != 202 {
		t.Fatalf("run = %d %s", w.Code, w.Body.String())
	}
	<-syncing
	if w := tb.do("POST", "/api/backup/cancel", nil); w.Code != 200 {
		t.Fatalf("cancel = %d %s", w.Code, w.Body.String())
	}
	if res := waitBackup(t, tb, "notes"); res.OK || res.Error != "cancelled" {
		t.Errorf("result = %+v, want cancelled", res)
	}
}

// TestBackupLeavesSecretsOut: a brain folder was synced with only the
// brain's own excludes, and the defaults hold no secret patterns, so a
// .env, a private key or a copied calendar token went to Drive although
// the docs said .env files never do. The filter now starts with the secret
// files, which no exclude list can include again ('+' lines are dropped),
// even an empty one; include_secret_files leaves them to the brain's list.
func TestBackupLeavesSecretsOut(t *testing.T) {
	for _, tc := range []struct {
		name           string
		excludes       []string
		includeSecrets bool
	}{
		{"no excludes", nil, false},
		{"its own excludes", []string{"node_modules/", "*.log"}, false},
		{"an exclude list that tries to include them", []string{"+ .env", "+ *.pem"}, false},
		{"include_secret_files", []string{"*.log"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filter := backup.BrainFilter(tc.excludes, tc.includeSecrets)
			lines := strings.Split(strings.TrimSpace(filter), "\n")
			for _, p := range backup.SecretExcludes {
				if got := slices.Contains(lines, "- "+p); got == tc.includeSecrets {
					t.Errorf("filter has - %s: %v, want %v:\n%s", p, got, !tc.includeSecrets, filter)
				}
			}
			for _, l := range lines {
				if strings.HasPrefix(l, "+") {
					t.Errorf("filter includes %q", l)
				}
			}
			if !tc.includeSecrets && lines[0] != "- "+backup.SecretExcludes[0] {
				t.Errorf("filter starts with %q, want the secret files first", lines[0])
			}
		})
	}

	t.Run("a run writes it", func(t *testing.T) {
		tb := newTestBridge(t, "")
		addBackupBrain(t, tb, "notes")
		var filters []string
		installBackupRunner(t, tb, func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
			if i := slices.Index(args, "--filter-from"); i >= 0 && args[0] == "sync" {
				data, _ := os.ReadFile(args[i+1])
				filters = append(filters, string(data))
			}
			return []byte("[]"), nil
		})
		connectAlias(t, tb)
		if err := tb.backup.StartRun([]string{"notes"}, false, "manual", true); err != nil {
			t.Fatal(err)
		}
		if res := waitBackup(t, tb, "notes"); !res.OK {
			t.Fatalf("backup = %+v", res)
		}
		if len(filters) != 1 || !strings.HasPrefix(filters[0], "- .env\n") {
			t.Errorf("sync filters = %q, want one starting with the secret files", filters)
		}
	})
}

// TestBackupSecretExcludesAreDocumented: docs/BACKUP.md promises what is
// never uploaded, so it must name every pattern backupSecretExcludes holds.
func TestBackupSecretExcludesAreDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join(repoDir, "docs", "BACKUP.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range backup.SecretExcludes {
		if !strings.Contains(string(doc), "`"+p+"`") {
			t.Errorf("docs/BACKUP.md does not name %s", p)
		}
	}
}

// TestBackupWaitsForTheBrainToFinish: a brain's folder was synced while a
// run changed it, so the backup could hold a mix of before and after (a
// .git index newer than its objects). The backup now waits for the run to
// end before it copies the folder, but only so long: then it copies it
// anyway, and the log says so.
func TestBackupWaitsForTheBrainToFinish(t *testing.T) {
	cases := []struct {
		name        string
		wait        time.Duration
		wantRunning bool // the brain still runs when the sync starts
	}{
		{"until the run ends", 20 * time.Second, false},
		{"not for ever", 50 * time.Millisecond, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb := newTestBridge(t, "")
			addBackupBrain(t, tb, "notes")
			br := tb.brainMgr.Get("notes")
			running := func() bool {
				return br.Snapshot().Running != nil
			}
			var syncedWhileRunning []bool
			installBackupRunner(t, tb, func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
				if args[0] == "sync" {
					syncedWhileRunning = append(syncedWhileRunning, running())
				}
				return []byte("[]"), nil
			}, func(opts *backup.Options) {
				targets := opts.Targets
				opts.Targets = func() []backup.Target {
					all := targets()
					for i := range all {
						wait := all[i].WaitIdle
						all[i].WaitIdle = func(ctx context.Context, _ time.Duration) bool { return wait(ctx, c.wait) }
					}
					return all
				}
			})
			logged := captureLog(t)

			t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "1500")
			if _, _, err := br.Submit(brainruntime.Inbound{Inbound: chat.Inbound{JID: "15551234567@s.whatsapp.net", Sender: "15551234567", Text: "hello", Source: "whatsapp", At: time.Now()}}); err != nil {
				t.Fatal(err)
			}
			testutil.WaitFor(t, 10*time.Second, "the run to start", running)
			connectAlias(t, tb)
			if err := tb.backup.StartRun([]string{"notes"}, false, "manual", true); err != nil {
				t.Fatal(err)
			}
			if res := waitBackup(t, tb, "notes"); !res.OK {
				t.Fatalf("backup = %+v", res)
			}
			if !slices.Equal(syncedWhileRunning, []bool{c.wantRunning}) {
				t.Errorf("synced while the brain ran: %v, want [%v]", syncedWhileRunning, c.wantRunning)
			}
			if all := strings.Join(logged(), "\n"); strings.Contains(all, "the brain is still running") != c.wantRunning {
				t.Errorf("log says it copied a running brain: %v, want %v (log %q)", !c.wantRunning, c.wantRunning, all)
			}
			testutil.WaitFor(t, 10*time.Second, "the run to end", func() bool { return !running() })
		})
	}
}
