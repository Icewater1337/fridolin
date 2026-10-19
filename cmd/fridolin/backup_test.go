package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	_ "time/tzdata" // TestNextDue's time zone, on machines without one

	"github.com/Icewater1337/fridolin/internal/devchat"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

func TestFilterLines(t *testing.T) {
	got := filterLines([]string{"node_modules/", "*.log", " /work/ ", "", "+ /sneaky", "- already-prefixed"})
	want := "- node_modules/**\n- *.log\n- /work/**\n- already-prefixed\n"
	if got != want {
		t.Errorf("filterLines =\n%q\nwant\n%q", got, want)
	}
}

// TestNextDue is the daily schedule: today's run is due at daily_at unless
// it already ran today or the schedule was switched on or moved after that
// time, and a run missed while the machine slept is due as soon as it is
// awake.
func TestNextDue(t *testing.T) {
	zurich, err := time.LoadLocation("Europe/Zurich")
	if err != nil {
		t.Fatal(err)
	}
	at := func(y int, mo time.Month, d, h, mi int) time.Time { return time.Date(y, mo, d, h, mi, 0, 0, zurich) }
	day := at(2026, 3, 10, 0, 0)
	cases := []struct {
		name    string
		now     time.Time
		dailyAt string
		lastRun string
		armed   time.Time
		wantDue time.Time
		wantNow bool
	}{
		{"before the time", at(2026, 3, 10, 2, 0), "03:30", "", time.Time{}, at(2026, 3, 10, 3, 30), false},
		{"at the time", at(2026, 3, 10, 3, 30), "03:30", "", time.Time{}, at(2026, 3, 10, 3, 30), true},
		{"after the time: missed while asleep", at(2026, 3, 10, 9, 0), "03:30", "2026-03-09", day.AddDate(0, 0, -5), at(2026, 3, 10, 3, 30), true},
		{"already ran today", at(2026, 3, 10, 9, 0), "03:30", "2026-03-10", time.Time{}, at(2026, 3, 11, 3, 30), false},
		{"ran yesterday, later today", at(2026, 3, 10, 9, 0), "23:15", "2026-03-09", time.Time{}, at(2026, 3, 10, 23, 15), false},
		{"switched on after the time", at(2026, 3, 10, 9, 0), "03:30", "", at(2026, 3, 10, 8, 59), at(2026, 3, 11, 3, 30), false},
		{"switched on before the time", at(2026, 3, 10, 9, 0), "10:00", "", at(2026, 3, 10, 8, 59), at(2026, 3, 10, 10, 0), false},
		{"moved to a time already past", at(2026, 3, 10, 15, 0), "14:00", "2026-03-09", at(2026, 3, 10, 14, 30), at(2026, 3, 11, 14, 0), false},
		{"switched on in the due minute", at(2026, 3, 10, 3, 30), "03:30", "", at(2026, 3, 10, 3, 30).Add(20 * time.Second), at(2026, 3, 11, 3, 30), false},
		{"end of the month", at(2026, 3, 31, 9, 0), "03:30", "2026-03-31", time.Time{}, at(2026, 4, 1, 3, 30), false},
		// 2026-03-29: the clocks jump from 02:00 to 03:00.
		{"a time the clocks skip", at(2026, 3, 29, 1, 0), "02:30", "", time.Time{}, at(2026, 3, 29, 3, 30), false},
		{"a time the clocks skip, once it came", at(2026, 3, 29, 3, 31), "02:30", "", time.Time{}, at(2026, 3, 29, 3, 30), true},
		{"the day before the clocks skip it", at(2026, 3, 28, 9, 0), "02:30", "2026-03-28", time.Time{}, at(2026, 3, 29, 3, 30), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			due, now := nextDue(tc.now, tc.dailyAt, tc.lastRun, tc.armed)
			if !due.Equal(tc.wantDue) || now != tc.wantNow {
				t.Errorf("nextDue = %v, %v; want %v, %v", due, now, tc.wantDue, tc.wantNow)
			}
		})
	}

	// 2026-10-25: the clocks go back from 03:00 to 02:00, so 02:30 comes
	// twice. The run is due once.
	t.Run("a time that comes twice", func(t *testing.T) {
		first := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC).In(zurich)  // 02:30 summer time
		second := time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC).In(zurich) // 02:30 winter time
		ranAt := time.Time{}
		for _, now := range []time.Time{first, second} {
			if _, runNow := nextDue(now, "02:30", ranOn(ranAt), time.Time{}); runNow {
				if !ranAt.IsZero() {
					t.Fatalf("due again at %v after running at %v", now, ranAt)
				}
				ranAt = now
			}
		}
		if ranAt.IsZero() {
			t.Error("never due")
		}
	})
}

// ranOn is the LastScheduledDay of a run at t, "" for none.
func ranOn(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(dayLayout)
}

// TestBackupScheduleIsArmedOnlyBySavedChanges: switching the schedule on or
// moving it marks when, so that neither starts a backup at once, and only
// once the change is saved. One the config refuses changes nothing.
func TestBackupScheduleIsArmedOnlyBySavedChanges(t *testing.T) {
	tb := newTestBridge(t, `, "backup": {"daily_at": "03:30"}`)
	m := tb.backup
	clock := time.Date(2026, 10, 18, 9, 0, 0, 0, time.Local)
	m.now = func() time.Time { return clock }
	armed := func() time.Time {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.state.ArmedAt
	}

	// Refused (the root is no plain folder name): not armed.
	if w := tb.do("PUT", "/api/backup/settings", map[string]any{"enabled": true, "root": "../x"}); w.Code != 422 {
		t.Fatalf("settings = %d %s, want 422", w.Code, w.Body.String())
	}
	if a := armed(); !a.IsZero() {
		t.Errorf("a refused change armed the schedule at %v", a)
	}
	if w := tb.do("PUT", "/api/backup/settings", map[string]any{"enabled": true}); w.Code != 200 {
		t.Fatalf("settings = %d %s", w.Code, w.Body.String())
	}
	if a := armed(); !a.Equal(clock) {
		t.Errorf("armed at %v, want %v", a, clock)
	}
	if _, runNow := nextDue(clock.Add(time.Minute), "03:30", "", armed()); runNow {
		t.Error("switching on after today's time starts a backup at once")
	}
	clock = clock.Add(time.Hour)
	if w := tb.do("PUT", "/api/backup/settings", map[string]any{"keep_versions_days": 7}); w.Code != 200 {
		t.Fatalf("settings = %d %s", w.Code, w.Body.String())
	}
	if a := armed(); a.Equal(clock) {
		t.Error("another backup setting re-armed the schedule")
	}
	if w := tb.do("PUT", "/api/backup/settings", map[string]any{"daily_at": "08:00"}); w.Code != 200 {
		t.Fatalf("settings = %d %s", w.Code, w.Body.String())
	}
	if a := armed(); !a.Equal(clock) {
		t.Errorf("moving the time armed at %v, want %v", a, clock)
	}
	// Saved, it is there after a restart.
	if a := NewBackupManager(tb.cfgs, tb.brainMgr).state.ArmedAt; !a.Equal(clock) {
		t.Errorf("armed time after a restart = %v, want %v", a, clock)
	}
}

// TestBackupSchedulerTicks: Start looks at the schedule every minute, runs
// the backup once its time has come, only once that day, and stops with
// Stop.
func TestBackupSchedulerTicks(t *testing.T) {
	// A bubble's clock starts at 2000-01-01 00:00 UTC. Where 90 minutes on
	// is the next day already (a zone a little behind UTC), today's time
	// would be past at the first tick: the test then starts at midnight.
	bubble := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	start := bubble.In(time.Local)
	if due := start.Add(90 * time.Minute); due.Day() != start.Day() {
		start = time.Date(due.Year(), due.Month(), due.Day(), 0, 0, 0, 0, time.Local)
	}
	dailyAt := start.Add(90 * time.Minute).Format("15:04")
	tb := newTestBridge(t, `, "backup": {"enabled": true, "include_bridge_config": false, "daily_at": "`+dailyAt+`"}`)
	_ = os.MkdirAll(filepath.Dir(tb.backup.rcloneConf()), 0o700)
	_ = os.WriteFile(tb.backup.rcloneConf(), []byte("[brain-gdrive]\ntype = alias\nremote = /nowhere\n"), 0o600)
	addBackupBrain(t, tb, "notes")
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(start.Sub(bubble))
		m := NewBackupManager(tb.cfgs, tb.brainMgr)
		d := &deletionRunner{src: []string{"a"}, cur: []string{"a"}}
		m.runner = d.run
		m.Start(context.Background())
		syncs := func() int {
			synctest.Wait()
			d.mu.Lock()
			defer d.mu.Unlock()
			return len(d.syncs)
		}
		time.Sleep(89*time.Minute + time.Second)
		if n := syncs(); n != 0 {
			t.Fatalf("%d syncs before the time", n)
		}
		time.Sleep(time.Minute)
		if n := syncs(); n != 1 {
			t.Fatalf("%d syncs once the time came, want 1", n)
		}
		time.Sleep(3 * time.Hour)
		if n := syncs(); n != 1 {
			t.Errorf("%d syncs later that day, want still 1", n)
		}
		m.mu.Lock()
		res, last := m.state.Brains["notes"], m.state.LastScheduledDay
		m.mu.Unlock()
		if res == nil || !res.OK || res.Trigger != "scheduled" || last != start.Format(dayLayout) {
			t.Errorf("result %+v, last scheduled day %q; want a scheduled run on %s", res, last, start.Format(dayLayout))
		}
		m.Stop()
	})
}

// TestBackupNextRun: the status shows the run that will happen: tomorrow's
// once today's ran, now for one that is due, and none while Drive is not
// connected.
func TestBackupNextRun(t *testing.T) {
	tb := newTestBridge(t, `, "backup": {"enabled": true, "daily_at": "03:30"}`)
	m := tb.backup
	clock := time.Date(2026, 10, 18, 9, 0, 0, 0, time.Local)
	m.now = func() time.Time { return clock }
	next := func() *time.Time {
		var st struct {
			NextRun *time.Time `json:"next_run"`
		}
		_ = json.Unmarshal(tb.do("GET", "/api/backup/status", nil).Body.Bytes(), &st)
		if s := m.Summary().NextRun; (s == nil) != (st.NextRun == nil) || s != nil && !s.Equal(*st.NextRun) {
			t.Errorf("the overview's next run %v differs from the Backup page's %v", s, st.NextRun)
		}
		return st.NextRun
	}
	if n := next(); n != nil {
		t.Errorf("next run %v while Drive is not connected", n)
	}
	_ = os.MkdirAll(filepath.Dir(m.rcloneConf()), 0o700)
	_ = os.WriteFile(m.rcloneConf(), []byte("[brain-gdrive]\ntype = alias\nremote = /nowhere\n"), 0o600)
	if n := next(); n == nil || !n.Equal(clock) {
		t.Errorf("next run = %v, want now (today's is due)", n)
	}
	m.mu.Lock()
	m.state.LastScheduledDay = clock.Format(dayLayout)
	m.mu.Unlock()
	if n, want := next(), time.Date(2026, 10, 19, 3, 30, 0, 0, time.Local); n == nil || !n.Equal(want) {
		t.Errorf("next run = %v, want %v", n, want)
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
	mu     sync.Mutex
	calls  [][]string
	scopes []childScope // each call's helperScope
	conf   string
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
	if last[0] != "mkdir" || last[1] != "brain-gdrive:"+config.DefaultBackupRoot {
		t.Errorf("last call = %v, want mkdir of the backup root", last)
	}
	// rclone opens the browser from one of these runs, so they all get the
	// desktop session's variables.
	for i, scope := range f.scopes {
		if scope != desktopChild {
			t.Errorf("call %v ran as scope %d, want desktopChild", f.calls[i], scope)
		}
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

// TestRcloneVersionNoticesAnUpgrade: the version is asked once per build,
// so the dashboard shows the new one after rclone was upgraded in place.
func TestRcloneVersionNoticesAnUpgrade(t *testing.T) {
	script := filepath.Join(t.TempDir(), "rclone")
	install := func(version string, mod time.Time) {
		t.Helper()
		if err := os.WriteFile(script, []byte("#!/bin/sh\necho rclone "+version+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(script, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	day := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	install("v1.70.0", day)
	if v := rcloneVersion(script, nil); v != "rclone v1.70.0" {
		t.Fatalf("version = %q", v)
	}
	install("v1.71.0", day.Add(24*time.Hour))
	if v := rcloneVersion(script, nil); v != "rclone v1.71.0" {
		t.Errorf("version after the upgrade = %q, want v1.71.0", v)
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
	fake := "#!/bin/sh\ndir=$(dirname \"$0\")\n[ \"$1\" = --config ] && shift 2\nenv > \"$dir/env.$1\"\necho rclone v9.9\n"
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
	run := func(ctx context.Context, arg string) error {
		_, err := tb.backup.runRclone(ctx, []string{arg, "x:"}, nil)
		return err
	}
	desktop := []string{"DISPLAY=:0", "DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/test-bus", "XDG_DATA_DIRS=/usr/share:/var/lib/snapd/desktop"}
	cases := []struct {
		name, log string
		run       func() error
		desktop   bool
	}{
		{"backup", "env.lsd", func() error { return run(context.Background(), "lsd") }, false},
		{"version", "env.version", func() error {
			if v := rcloneVersion(script, tb.backup.rcloneEnv(anyChild)); v != "rclone v9.9" {
				return fmt.Errorf("version = %q", v)
			}
			return nil
		}, false},
		{"connect", "env.config", func() error { return run(withDesktop(context.Background()), "config") }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(bin, tc.log))
			if err != nil {
				t.Fatal(err)
			}
			env := strings.Split(string(data), "\n")
			for _, want := range []string{"HOME=" + os.Getenv("HOME"), "HTTPS_PROXY=http://proxy.example.com:3128", "RCLONE_CONFIG=" + tb.backup.rcloneConf()} {
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
	_ = os.MkdirAll(filepath.Dir(tb.backup.rcloneConf()), 0o700)
	_ = os.WriteFile(tb.backup.rcloneConf(), []byte("[brain-gdrive]\ntype = alias\nremote = "+remoteDir+"\n"), 0o600)
	if !tb.backup.connected() {
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

func TestExpiredVersions(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.Local)
	stamp := func(days int) string { return versionName(now.AddDate(0, 0, -days)) }
	cases := []struct {
		name  string
		names []string
		days  int
		want  []string
	}{
		{"older than the window", []string{stamp(31), stamp(29)}, 30, []string{stamp(31)}},
		{"exactly on the cutoff stays", []string{stamp(30)}, 30, nil},
		{"one day window", []string{stamp(2), stamp(0)}, 1, []string{stamp(2)}},
		{"names that are not version stamps stay", []string{"keep-me", "2025", "20250101-0000", "20250101-000000X", stamp(90)}, 30, []string{stamp(90)}},
		// Folders named before the names were in UTC are in local time.
		{"local-time names", []string{now.AddDate(0, 0, -31).Format(legacyVersionLayout), now.AddDate(0, 0, -29).Format(legacyVersionLayout)}, 30,
			[]string{now.AddDate(0, 0, -31).Format(legacyVersionLayout)}},
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
	connectAlias(t, tb)
	tb.backup.runner = func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
		return []byte(`[{"Name":"20261001-120000"},{"Name":"manual-copy"},{"Name":"20261016-080000Z"},{"Name":"20261015-080000"},{"Name":"20250101-000000"}]`), nil
	}
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

// TestVersionNamesKeepTheirOrderWhenClocksGoBack: version folders are
// named in UTC, so a run in the hour the clocks go back sorts after the run
// an hour of local time "later" that came before it, and neither name can
// come twice.
func TestVersionNamesKeepTheirOrderWhenClocksGoBack(t *testing.T) {
	zurich, err := time.LoadLocation("Europe/Zurich")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-10-25: 02:30 summer time, then 02:15 winter time 45 minutes later.
	first := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC).In(zurich)
	second := first.Add(45 * time.Minute)
	names := []string{versionName(first), versionName(second)}
	sortVersions(names)
	if names[0] != versionName(second) {
		t.Errorf("versions = %v, want the later run first", names)
	}
	for _, n := range names {
		if !restoreVersionRe.MatchString(n) {
			t.Errorf("%s cannot be restored", n)
		}
		if got, ok := parseVersion(n); !ok || !(got.Equal(first) || got.Equal(second)) {
			t.Errorf("parseVersion(%s) = %v, %v", n, got, ok)
		}
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
	if _, err := tb.cfgs.Update("", func(c *config.Config) error { c.Backup.RclonePath = rclone; return nil }); err != nil {
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
	m := manifest.Manifest{ID: id, Name: id, Backup: manifest.Backup{Enabled: true}}
	if w := tb.do("POST", "/api/brains", map[string]any{"path": "~/" + id, "manifest": m, "create_dir": true}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	return filepath.Join(tb.home, id)
}

// runBackup starts a backup of one brain, waits for it, and returns its result.
func runBackup(t *testing.T, tb *testBridge, id string) *BackupResult {
	t.Helper()
	if err := tb.backup.StartRun([]string{id}, false, "manual", false); err != nil {
		t.Fatal(err)
	}
	return waitBackup(t, tb, id)
}

// waitBackup waits until no backup runs, as the Backup page sees it, and
// returns the last result of brain id.
func waitBackup(t *testing.T, tb *testBridge, id string) *BackupResult {
	t.Helper()
	var res *BackupResult
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
func backupStatus(t *testing.T, tb *testBridge) (running bool, last map[string]*BackupResult) {
	t.Helper()
	var st struct {
		Job    any `json:"job"`
		Brains []struct {
			ID   string        `json:"id"`
			Last *BackupResult `json:"last"`
		} `json:"brains"`
	}
	if w := tb.do("GET", "/api/backup/status", nil); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &st) != nil {
		t.Fatalf("backup status: %d %s", w.Code, w.Body.String())
	}
	last = map[string]*BackupResult{}
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
	expired := versionName(time.Now().AddDate(0, 0, -40))
	recent := versionName(time.Now().AddDate(0, 0, -10))
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
	expired := versionName(time.Now().AddDate(0, 0, -31))
	recent := versionName(time.Now().AddDate(0, 0, -29))
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
	tb.backup.prune(context.Background(), backupTargets([]*Brain{notes}, nil))
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
	want := []string{"brain-gdrive:" + config.DefaultBackupRoot + "/notes/versions/" + expired}
	if fmt.Sprint(purged) != fmt.Sprint(want) {
		t.Errorf("purged %v, want %v", purged, want)
	}
}

// TestPruneKeepsItsConfigAndStopsWhenCancelled: the listings and purges of
// one prune all use the config it started with, and a cancelled job stops
// it at once instead of trying (and logging) every remaining id.
func TestPruneKeepsItsConfigAndStopsWhenCancelled(t *testing.T) {
	listing := []byte(`[{"Name":"` + versionName(time.Now().AddDate(0, 0, -31)) + `"},` +
		`{"Name":"` + versionName(time.Now().AddDate(0, 0, -60)) + `"}]`)
	brains := []*Brain{{id: "notes"}, {id: "work"}}

	t.Run("backup.root changed mid-run", func(t *testing.T) {
		tb := newTestBridge(t, `, "backup": {"keep_versions_days": 30}`)
		var calls [][]string
		tb.backup.runner = func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
			calls = append(calls, args)
			if len(calls) == 1 {
				if _, err := tb.cfgs.Update("", func(c *config.Config) error { c.Backup.Root = "moved"; return nil }); err != nil {
					t.Error(err)
				}
			}
			if args[0] == "lsjson" {
				return listing, nil
			}
			return nil, nil
		}
		tb.backup.prune(context.Background(), backupTargets(brains, nil))
		if len(calls) != 9 {
			t.Errorf("%d rclone calls, want a listing and two purges for each of 3 ids: %v", len(calls), calls)
		}
		for _, c := range calls {
			if p := c[len(c)-1]; !strings.HasPrefix(p, "brain-gdrive:"+config.DefaultBackupRoot+"/") {
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
		tb.backup.prune(ctx, backupTargets(brains, nil))
		if len(calls) != 2 {
			t.Errorf("prune went on after the cancel: %v", calls)
		}
	})
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

// flagValue returns the value after an rclone flag, "" when it is not set.
func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// syncMaxDelete returns a sync's --max-delete value, "" for none.
func syncMaxDelete(args []string) string { return flagValue(args, "--max-delete") }

// TestBackupRefusesMassDeletion runs the check before a brain's sync: it
// mirrors ordinary changes, and unless forced refuses an empty or missing
// brain folder, one that lost more than half of its backed-up files, more
// than a fifth once that is ten or more, or 250 or more, and one with a top
// folder of five or more files that lost more than half of them, as a brain
// that wiped its memories/ leaves it. The sync's --max-delete is the most
// the counts allow. A folder rclone can only partly read is synced with no
// deletion allowed.
func TestBackupRefusesMassDeletion(t *testing.T) {
	tb := newTestBridge(t, "")
	dir := addBackupBrain(t, tb, "notes")
	br := tb.brainMgr.Get("notes")
	files := func(names ...string) []string { return append([]string{}, names...) }
	// seq is n files, prefix0 to prefix<n-1>.
	seq := func(prefix string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = prefix + strconv.Itoa(i)
		}
		return out
	}
	// A larger brain folder: 88 files of its own and a memories/ of 12.
	notes, memories := seq("n", 88), seq("memories/m", 12)
	large := append(slices.Clone(notes), memories...)
	const unreadable = `ERROR : locked: failed to open directory "locked": open locked: permission denied`
	cases := []struct {
		name      string
		src, cur  []string
		missing   bool // the brain folder does not exist
		srcErr    string
		listErr   string
		syncErr   string // the sync fails with this; refused and failed then expect it to have run
		force     bool
		refused   bool
		says      string // a refusal's error says this
		failed    string // the result's error contains this, no sync ran unless syncErr is set
		maxDelete string // the sync's --max-delete, "" for none
	}{
		{name: "unchanged", src: files("a", "b", "c", "d"), cur: files("a", "b", "c", "d"), maxDelete: "2"},
		{name: "first backup", src: files("a"), cur: nil, maxDelete: "0"},
		{name: "empty folder, empty backup", src: files(), cur: files(), maxDelete: "0"},
		{name: "half gone is allowed", src: files("a", "b"), cur: files("a", "b", "c", "d"), maxDelete: "2"},
		{name: "more than half gone", src: files("a"), cur: files("a", "b", "c", "d"), refused: true, says: "3 of 4 backed-up files are gone"},
		{name: "empty brain folder", src: files(), cur: files("a", "b"), refused: true},
		{name: "missing brain folder", missing: true, cur: files("a"), refused: true},
		{name: "new files do not offset deletions", src: files("v", "w", "x", "y", "z"), cur: files("a", "b", "c"), refused: true},
		{name: "force mirrors anyway", src: files(), cur: files("a", "b"), force: true},
		{name: "3 of 100 gone", src: seq("n", 97), cur: seq("n", 100), maxDelete: "20"},
		{name: "a fifth gone", src: seq("n", 40), cur: seq("n", 50), maxDelete: "10"},
		{name: "more than a fifth, fewer than ten gone", src: seq("n", 21), cur: seq("n", 30), maxDelete: "9"},
		{name: "more than a fifth, ten or more gone", src: seq("n", 38), cur: seq("n", 50), refused: true, says: "12 of 50 backed-up files are gone"},
		{name: "a few of a large folder gone", src: seq("n", 1990), cur: seq("n", 2000), maxDelete: "249"},
		{name: "300 of 2000 gone", src: seq("n", 1700), cur: seq("n", 2000), refused: true, says: "300 of 2000 backed-up files are gone"},
		{name: "a wiped memories/", src: notes, cur: large, refused: true, says: "12 of the 12 backed-up files in memories/ are gone"},
		{name: "half of memories/ gone", src: append(slices.Clone(notes), memories[:6]...), cur: large, maxDelete: "20"},
		{name: "a wiped top folder of four files", src: seq("n", 96), cur: append(seq("n", 96), seq("tmp/t", 4)...), maxDelete: "20"},
		{name: "deeper folders count for their top folder", src: append(slices.Clone(notes), "memories/a/1", "memories/b/2"),
			cur: append(slices.Clone(large), "memories/a/1", "memories/b/2"), refused: true, says: "12 of the 14 backed-up files in memories/"},
		{name: "force mirrors a wiped memories/", src: notes, cur: large, force: true},
		{name: "listing the backup fails", src: files("a"), listErr: "ERROR : couldn't connect", failed: "checking the backup before syncing failed"},
		// The folder is there, so rclone failing to read it is no proof it
		// is empty, and no reason to leave its readable files out either.
		{name: "brain folder partly unreadable", srcErr: unreadable, cur: files("a"), syncErr: "not deleting files as there were IO errors",
			failed: "parts of the brain folder could not be read, so the backup synced the rest and deleted nothing", maxDelete: "0"},
		{name: "brain folder unreadable for a moment", srcErr: unreadable, cur: files("a"), maxDelete: "0"},
		{name: "brain folder vanishes while listed", src: nil, cur: files("a"), maxDelete: "0"},
		{name: "the sync of a partly read folder finds deletions", srcErr: unreadable, cur: files("a"),
			syncErr: "Got fatal error on delete: --max-delete threshold reached", refused: true, maxDelete: "0"},
		{name: "brain folder and backup unreadable", srcErr: unreadable, listErr: "ERROR : couldn't connect", failed: "checking the backup before syncing failed"},
		// rclone sync treats differently normalized names as the same file.
		{name: "decomposed names in the backup", src: files("Gr\u00fcsse.md", "M\u00e4rz.md"), cur: files("Gru\u0308sse.md", "Ma\u0308rz.md"), maxDelete: "1"},
		{name: "decomposed names in the folder", src: files("Gru\u0308sse.md", "Ma\u0308rz.md"), cur: files("Gr\u00fcsse.md", "M\u00e4rz.md"), maxDelete: "1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if c.missing {
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
			}
			d := &deletionRunner{src: c.src, cur: c.cur, srcErr: c.srcErr, listErr: c.listErr, syncErr: c.syncErr}
			tb.backup.runner = d.run
			res := tb.backup.backupBrain(context.Background(), backupTarget(br, nil), "manual", c.force)
			synced := 0
			if c.syncErr != "" {
				synced = 1
			}
			switch {
			case c.refused:
				if res.OK || !res.Refused || !strings.HasPrefix(res.Error, massDeletionError) || !strings.Contains(res.Error, c.says) || len(d.syncs) != synced {
					t.Errorf("want a refusal saying %q after %d syncs, got %+v, syncs %v", c.says, synced, res, d.syncs)
				}
				if res.Seconds != res.Finished.Sub(res.Started).Seconds() {
					t.Errorf("seconds = %v, want the time the run took", res.Seconds)
				}
			case c.failed != "":
				if res.OK || res.Refused || !strings.Contains(res.Error, c.failed) || len(d.syncs) != synced {
					t.Errorf("want a failure %q after %d syncs, got %+v", c.failed, synced, res)
				}
			default:
				if !res.OK || res.Refused || len(d.syncs) != 1 {
					t.Fatalf("want one sync, got %+v, syncs %v", res, d.syncs)
				}
			}
			if len(d.syncs) == 1 {
				if got := syncMaxDelete(d.syncs[0]); got != c.maxDelete {
					t.Errorf("--max-delete = %q, want %q", got, c.maxDelete)
				}
			}
		})
	}
}

// TestBackupBrainKeepsItsConfig: the check and the sync of one brain use
// the config the brain's backup started with, so a backup.root changed in
// between cannot count one folder in Drive and sync into another.
func TestBackupBrainKeepsItsConfig(t *testing.T) {
	tb := newTestBridge(t, "")
	addBackupBrain(t, tb, "notes")
	d := &deletionRunner{src: []string{"a"}, cur: []string{"a"}}
	moved := false
	tb.backup.runner = func(ctx context.Context, args []string, stderr io.Writer) ([]byte, error) {
		if args[0] == "lsjson" && !moved {
			moved = true
			if _, err := tb.cfgs.Update("", func(c *config.Config) error { c.Backup.Root = "moved"; return nil }); err != nil {
				t.Error(err)
			}
		}
		return d.run(ctx, args, stderr)
	}
	if res := tb.backup.backupBrain(context.Background(), backupTarget(tb.brainMgr.Get("notes"), nil), "manual", false); !res.OK {
		t.Fatalf("backup = %+v", res)
	}
	if len(d.syncs) != 1 {
		t.Fatalf("want one sync, got %v", d.syncs)
	}
	for _, p := range []string{d.syncs[0][2], flagValue(d.syncs[0], "--backup-dir")} {
		if !strings.HasPrefix(p, notesBase+"/") {
			t.Errorf("the sync writes to %s, not under the backup.root the check counted", p)
		}
	}
}

// TestMassDeletionCheckListsLikeTheSync: the check lists both sides with the
// sync's filter file and symlink handling. Without the brain's excludes,
// files backed up before they were excluded, which the sync never deletes
// from Drive, would count as gone on every run; without --links, a symlink
// the sync backed up as .rclonelink would be missing from the brain folder.
func TestMassDeletionCheckListsLikeTheSync(t *testing.T) {
	tb := newTestBridge(t, "")
	addBackupBrain(t, tb, "notes")
	br := tb.brainMgr.Get("notes")
	d := &deletionRunner{src: []string{"a"}, cur: []string{"a"}}
	tb.backup.runner = d.run
	if res := tb.backup.backupBrain(context.Background(), backupTarget(br, nil), "manual", false); !res.OK {
		t.Fatalf("backup = %+v", res)
	}
	filter := filepath.Join(tb.backup.dir(), "filters-notes.txt")
	if len(d.syncs) != 1 || flagValue(d.syncs[0], "--filter-from") != filter || !slices.Contains(d.syncs[0], "--links") {
		t.Fatalf("syncs = %v, want one with --filter-from %s and --links", d.syncs, filter)
	}
	for _, side := range []struct{ name, path string }{
		{"brain folder", br.Dir() + "/"},
		{"backup", notesBase + "/current"},
	} {
		t.Run(side.name, func(t *testing.T) {
			var args []string
			for _, l := range d.lists {
				if slices.Contains(l, side.path) {
					args = l
				}
			}
			if args == nil {
				t.Fatalf("no listing of %s in %v", side.path, d.lists)
			}
			if got := flagValue(args, "--filter-from"); got != filter {
				t.Errorf("--filter-from = %q, want the sync's %s", got, filter)
			}
			if !slices.Contains(args, "--links") {
				t.Errorf("listed without --links: %v", args)
			}
		})
	}
}

// TestBackupBridgeHasNoDeletionLimit: the bridge's own settings are staged
// by the bridge, so they get neither the check nor --max-delete; removing a
// brain drops its manifest from them, and that must reach the backup.
func TestBackupBridgeHasNoDeletionLimit(t *testing.T) {
	tb := newTestBridge(t, "")
	d := &deletionRunner{src: []string{}, cur: []string{"config.json", "brains/a.brain.json", "brains/b.brain.json"}}
	tb.backup.runner = d.run
	if res := tb.backup.backupBridge(context.Background(), "manual"); !res.OK {
		t.Fatalf("bridge backup = %+v", res)
	}
	if len(d.lists) != 0 {
		t.Errorf("the bridge backup listed files for a deletion check: %v", d.lists)
	}
	if len(d.syncs) != 1 || syncMaxDelete(d.syncs[0]) != "" {
		t.Errorf("want one sync without --max-delete, got %v", d.syncs)
	}
}

// TestBackupRunForceOverridesRefusal: the API's force field reaches the run,
// and only for the brains it names.
func TestBackupRunForceOverridesRefusal(t *testing.T) {
	tb := newTestBridge(t, "")
	_ = os.MkdirAll(filepath.Dir(tb.backup.rcloneConf()), 0o700)
	_ = os.WriteFile(tb.backup.rcloneConf(), []byte("[brain-gdrive]\ntype = alias\nremote = /nowhere\n"), 0o600)
	addBackupBrain(t, tb, "notes")
	d := &deletionRunner{src: []string{}, cur: []string{"a", "b"}}
	tb.backup.runner = d.run

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
	_ = os.MkdirAll(filepath.Dir(tb.backup.rcloneConf()), 0o700)
	_ = os.WriteFile(tb.backup.rcloneConf(), []byte("[brain-gdrive]\ntype = alias\nremote = /nowhere\n"), 0o600)
	addBackupBrain(t, tb, "notes")
	d := &deletionRunner{src: []string{}, cur: []string{"a", "b"}}
	tb.backup.runner = d.run
	return tb, d
}

// scheduledRun lets the scheduler fire on the given day and waits for the
// backup of "notes".
func scheduledRun(t *testing.T, tb *testBridge, day int) *BackupResult {
	t.Helper()
	y, mo, d := time.Now().Date()
	tb.backup.tick(time.Date(y, mo, d+day, 12, 0, 0, 0, time.Local))
	return waitBackup(t, tb, "notes")
}

// TestScheduledBackupNeverForces: the nightly run keeps refusing a mass
// deletion, so a wiped brain is never mirrored without the owner.
func TestScheduledBackupNeverForces(t *testing.T) {
	tb, d := scheduledBackupBridge(t)
	now := time.Now()
	res := scheduledRun(t, tb, 0)
	d.mu.Lock()
	syncs := len(d.syncs)
	d.mu.Unlock()
	if res.OK || !res.Refused || res.Trigger != "scheduled" || syncs != 0 {
		t.Errorf("scheduled run = %+v with %d syncs, want a scheduled refusal and no sync", res, syncs)
	}
	tb.backup.mu.Lock()
	last := tb.backup.state.LastScheduledDay
	tb.backup.mu.Unlock()
	if last != now.Format("2006-01-02") {
		t.Errorf("last scheduled day = %q, want today", last)
	}
}

// TestScheduledRefusalIsToldOnWhatsApp: a scheduled run that refuses tells
// the brain's chat once; later refusals stay quiet until a backup of the
// brain succeeds, manual refusals are never sent (the owner is looking at
// the dashboard), and a notice that failed to send is tried next time.
func TestScheduledRefusalIsToldOnWhatsApp(t *testing.T) {
	tb, _ := scheduledBackupBridge(t)
	br := tb.brainMgr.Get("notes")
	jid := tb.replyChatFor(br, tb.cfgs.Get())
	if jid == "" {
		t.Fatal("the test brain has no chat")
	}
	sent := func() []devchat.Message { return tb.dev.Outbox() }
	told := func() bool {
		tb.backup.mu.Lock()
		defer tb.backup.mu.Unlock()
		_, ok := tb.backup.state.RefusalNotices["notes"]
		return ok
	}

	if res := scheduledRun(t, tb, 0); !res.Refused {
		t.Fatalf("first scheduled run = %+v, want a refusal", res)
	}
	out := sent()
	if len(out) != 1 || out[0].JID != jid || !strings.Contains(out[0].Text, "Backup of notes: "+massDeletionError) ||
		!strings.Contains(out[0].Text, "2 of 2 backed-up files are gone") {
		t.Fatalf("want one notice to %s, got %+v", jid, out)
	}
	if _, ok := NewBackupManager(tb.cfgs, tb.brainMgr).state.RefusalNotices["notes"]; !ok {
		t.Error("the notice is not saved, so a restart would send it again")
	}

	scheduledRun(t, tb, 1)
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
	send := tb.backup.notify
	tb.backup.notify = func(context.Context, BackupTarget, string) error { return fmt.Errorf("WhatsApp is not connected") }
	scheduledRun(t, tb, 2)
	if told() || len(sent()) != 1 {
		t.Errorf("a notice that failed to send must not count as told")
	}
	tb.backup.notify = send
	scheduledRun(t, tb, 3)
	if out := sent(); len(out) != 2 || !told() {
		t.Errorf("want the second streak told once it could be sent, got %+v", out)
	}
}

// TestRefusalNoticeGivesUp: the notice is sent from inside the backup job,
// so a send that hangs must not keep the job (and Back up now) busy. It
// gives up after refusalNoticeTimeout and counts as not told.
func TestRefusalNoticeGivesUp(t *testing.T) {
	tb, _ := scheduledBackupBridge(t)
	defer func(d time.Duration) { refusalNoticeTimeout = d }(refusalNoticeTimeout)
	refusalNoticeTimeout = 50 * time.Millisecond
	tb.backup.notify = func(ctx context.Context, _ BackupTarget, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		res := &BackupResult{Refused: true, Trigger: "scheduled", Error: massDeletionError}
		tb.backup.noticeRefusal(context.Background(), backupTarget(tb.brainMgr.Get("notes"), nil), res)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a send that hangs keeps the backup job busy")
	}
	tb.backup.mu.Lock()
	_, told := tb.backup.state.RefusalNotices["notes"]
	tb.backup.mu.Unlock()
	if told {
		t.Error("a notice that timed out must not count as told")
	}
}

// TestRefusalNoticesForgetRemovedBrains: the saved notices drop brains that
// are gone, so the list does not grow forever and a new brain that reuses an
// id is still told about its first refusal.
func TestRefusalNoticesForgetRemovedBrains(t *testing.T) {
	tb, _ := scheduledBackupBridge(t)
	tb.backup.mu.Lock()
	tb.backup.state.RefusalNotices = map[string]time.Time{"removed": time.Now(), "notes": time.Now()}
	tb.backup.mu.Unlock()
	if res := scheduledRun(t, tb, 0); !res.Refused {
		t.Fatalf("scheduled run = %+v, want a refusal", res)
	}
	saved := NewBackupManager(tb.cfgs, tb.brainMgr).state.RefusalNotices
	if _, ok := saved["removed"]; ok {
		t.Errorf("the notice of a removed brain is still saved: %v", saved)
	}
	if _, ok := saved["notes"]; !ok {
		t.Errorf("the notice of a brain that is still refused was dropped: %v", saved)
	}
	if out := tb.dev.Outbox(); len(out) != 0 {
		t.Errorf("a refusal already told was sent again: %+v", out)
	}
}

// TestSyncDirReportsMaxDelete: rclone stopping at --max-delete is a refusal,
// not an unexplained failure.
func TestSyncDirReportsMaxDelete(t *testing.T) {
	tb := newTestBridge(t, "")
	tb.backup.runner = func(_ context.Context, args []string, stderr io.Writer) ([]byte, error) {
		fmt.Fprintln(stderr, `{"level":"error","msg":"Got fatal error on delete: --max-delete threshold reached","object":"a"}`)
		fmt.Fprintln(stderr, `{"level":"error","msg":"Fatal error received - not attempting retries"}`)
		return nil, fmt.Errorf("exit status 7")
	}
	cases := []struct {
		name      string
		maxDelete int
		want      func(res *BackupResult) string
	}{
		// Unlike the check before the sync, this refusal comes after rclone
		// moved up to the limit out of current/; the message must say where.
		{"limit 1", 1, func(res *BackupResult) string {
			return "up to that many files were already moved from current/ to versions/" + res.Version
		}},
		// A partly readable brain folder is synced with no deletion allowed.
		{"limit 0", 0, func(*BackupResult) string { return "nothing was removed from current/" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := tb.backup.syncDir(context.Background(), t.TempDir(), notesBase, "", "manual", c.maxDelete)
			if res.OK || !res.Refused || !strings.HasPrefix(res.Error, massDeletionError) {
				t.Errorf("result = %+v", res)
			}
			if want := c.want(res); !strings.Contains(res.Error, want) {
				t.Errorf("error = %q, want it to say %q", res.Error, want)
			}
		})
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
			if res.OK || !res.Refused || !strings.HasPrefix(res.Error, massDeletionError) {
				t.Errorf("result = %+v, want a refusal", res)
			}
			if wipe == "memories" && !strings.Contains(res.Error, "in memories/ are gone") {
				t.Errorf("error = %q, want it to name memories/", res.Error)
			}
			if n := count(cur); n != backedUp {
				t.Errorf("current/ has %d files after a refused run, want all %d", n, backedUp)
			}
			body := tb.do("GET", "/api/backup/status", nil).Body.String()
			if !strings.Contains(body, massDeletionError) || !strings.Contains(body, `"refused":true`) {
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

// TestSyncDirMaxDeleteWithRealRclone pins the rclone log line that marks a
// sync stopped by --max-delete.
func TestSyncDirMaxDeleteWithRealRclone(t *testing.T) {
	tb, remoteDir := rcloneTestBridge(t, "")
	src := t.TempDir()
	for _, f := range []string{"a", "b", "c"} {
		_ = os.WriteFile(filepath.Join(src, f), []byte(f), 0o600)
	}
	if res := tb.backup.syncDir(context.Background(), src, notesBase, "", "manual", -1); !res.OK {
		t.Fatalf("first sync = %+v", res)
	}
	// Replacing files moves the old copies to versions/ too, but rclone does
	// not count that as a deletion.
	for _, f := range []string{"a", "b", "c"} {
		_ = os.WriteFile(filepath.Join(src, f), []byte(f+" changed"), 0o600)
	}
	if res := tb.backup.syncDir(context.Background(), src, notesBase, "", "manual", 0); !res.OK {
		t.Fatalf("overwriting every file with --max-delete 0 = %+v", res)
	}
	_ = os.Remove(filepath.Join(src, "a"))
	_ = os.Remove(filepath.Join(src, "b"))
	res := tb.backup.syncDir(context.Background(), src, notesBase, "", "manual", 1)
	if res.OK || !res.Refused {
		t.Errorf("result = %+v, want a refusal at the --max-delete limit", res)
	}
	entries, _ := os.ReadDir(filepath.Join(remoteDir, config.DefaultBackupRoot, "notes", "current"))
	if len(entries) < 2 {
		t.Errorf("current/ has %d files, rclone deleted more than the limit", len(entries))
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

// TestRcloneIsInterruptedWhenCancelled: a cancelled rclone gets an
// interrupt first, as from Ctrl-C, so that it can end its transfers and
// remove its partial files, and is killed only if it does not exit.
func TestRcloneIsInterruptedWhenCancelled(t *testing.T) {
	bin := t.TempDir()
	script := filepath.Join(bin, "rclone")
	fake := "#!/bin/sh\ndir=$(dirname \"$0\")\ntrap 'echo interrupted > \"$dir/signal\"; exit 1' INT\n" +
		"touch \"$dir/started\"\nwhile :; do sleep 0.05; done\n"
	if err := os.WriteFile(script, []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	tb := newTestBridge(t, `, "backup": {"rclone_path": "`+script+`"}`)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := tb.backup.runRclone(ctx, []string{"copy", "x:", "y"}, nil)
		done <- err
	}()
	testutil.WaitFor(t, 10*time.Second, "rclone to start", func() bool { return fsx.Exists(filepath.Join(bin, "started")) })
	cancel()
	select {
	case <-done:
	case <-time.After(rcloneStopGrace + 5*time.Second):
		t.Fatal("runRclone did not return after the cancel")
	}
	if data, err := os.ReadFile(filepath.Join(bin, "signal")); err != nil || strings.TrimSpace(string(data)) != "interrupted" {
		t.Errorf("rclone was not interrupted (%v), so it could not clean up", err)
	}
}

// TestBackupStopWaitsForTheRun: Stop cancels a running backup and waits
// for it, so its result is saved before the bridge closes down, and nothing
// starts after it.
func TestBackupStopWaitsForTheRun(t *testing.T) {
	tb := newTestBridge(t, "")
	m := tb.backup
	_ = os.MkdirAll(filepath.Dir(m.rcloneConf()), 0o700)
	_ = os.WriteFile(m.rcloneConf(), []byte("[brain-gdrive]\ntype = alias\nremote = /nowhere\n"), 0o600)
	addBackupBrain(t, tb, "notes")
	syncing := make(chan struct{})
	m.runner = func(ctx context.Context, args []string, stderr io.Writer) ([]byte, error) {
		if args[0] != "sync" {
			return []byte("[]"), nil
		}
		close(syncing)
		<-ctx.Done()
		time.Sleep(300 * time.Millisecond) // rclone winding down
		return nil, ctx.Err()
	}
	if err := m.StartRun([]string{"notes"}, false, "manual", true); err != nil {
		t.Fatal(err)
	}
	<-syncing
	m.Stop()
	m.mu.Lock()
	res, job := m.state.Brains["notes"], m.job
	m.mu.Unlock()
	if res == nil || res.Error != "cancelled" || job != nil {
		t.Errorf("after Stop: result %+v, job %+v; want the cancelled run recorded and done", res, job)
	}
	if err := m.StartRun([]string{"notes"}, false, "manual", true); !errors.Is(err, errBackupStopped) {
		t.Errorf("StartRun after Stop = %v, want errBackupStopped", err)
	}
}

// TestUnreadableBackupStateIsKept: a state file that cannot be parsed is
// kept as state.json.corrupt rather than overwritten by the next save.
func TestUnreadableBackupStateIsKept(t *testing.T) {
	tb := newTestBridge(t, "")
	path := tb.backup.statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"brains": {"notes": tru`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewBackupManager(tb.cfgs, tb.brainMgr)
	if data, err := os.ReadFile(path + ".corrupt"); err != nil || string(data) != `{"brains": {"notes": tru` {
		t.Errorf("state.json.corrupt = %q, %v; want the unreadable file", data, err)
	}
	if m.state.Brains == nil || len(m.state.Brains) != 0 {
		t.Errorf("state = %+v, want an empty one", m.state)
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
	tb.backup.runner = run
	if err := os.MkdirAll(filepath.Dir(tb.backup.rcloneConf()), 0o700); err != nil {
		t.Fatal(err)
	}
	return tb
}

// TestBackupConnectLeavesAWorkingLinkAlone: connecting starts by deleting
// the remote, so Connect is refused while Drive is connected, and while a
// backup or restore runs on it.
func TestBackupConnectLeavesAWorkingLinkAlone(t *testing.T) {
	cases := []struct {
		name      string
		connected bool
		job       bool
		want      string
	}{
		{"connected", true, false, "already connected"},
		{"a backup runs", false, true, "is running"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var calls [][]string
			tb := connectTestBridge(t, func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
				mu.Lock()
				calls = append(calls, args)
				mu.Unlock()
				return nil, nil
			})
			m := tb.backup
			conf := "[brain-gdrive]\ntype = drive\ntoken = \n"
			if tc.connected {
				conf = "[brain-gdrive]\ntype = drive\ntoken = {\"access_token\":\"x\"}\n"
			}
			_ = os.WriteFile(m.rcloneConf(), []byte(conf), 0o600)
			if tc.job {
				m.mu.Lock()
				m.job = &backupJob{cancel: func() {}}
				m.mu.Unlock()
				t.Cleanup(func() { m.mu.Lock(); m.job = nil; m.mu.Unlock() })
			}
			w := tb.do("POST", "/api/backup/connect", nil)
			if w.Code != 409 || !strings.Contains(w.Body.String(), tc.want) {
				t.Errorf("connect = %d %s, want 409 with %q", w.Code, w.Body.String(), tc.want)
			}
			time.Sleep(50 * time.Millisecond) // a flow started anyway would have called rclone by now
			mu.Lock()
			defer mu.Unlock()
			if len(calls) != 0 {
				t.Errorf("rclone ran %v", calls)
			}
			if data, _ := os.ReadFile(m.rcloneConf()); string(data) != conf {
				t.Errorf("rclone.conf changed to %q", data)
			}
		})
	}
}

// TestConnectDriveChecksEveryAnswer: each of rclone's answers is checked,
// the last one included, and a failure keeps what rclone said about it.
func TestConnectDriveChecksEveryAnswer(t *testing.T) {
	question := []byte(`{"State":"*again","Option":{"Name":"config_something","DefaultStr":"x"}}`)
	cases := []struct {
		name string
		run  func(args []string, stderr io.Writer) ([]byte, error)
		want string
	}{
		{"delete fails", func(args []string, _ io.Writer) ([]byte, error) {
			if args[1] == "delete" {
				return nil, errors.New("exit status 1")
			}
			return []byte(`{"State":""}`), nil
		}, "removing the unfinished Drive link"},
		{"the last answer fails", func(args []string, stderr io.Writer) ([]byte, error) {
			if args[1] == "update" {
				fmt.Fprintln(stderr, "ERROR : quota exceeded")
				return nil, errors.New("exit status 1")
			}
			return question, nil
		}, "quota exceeded"},
		{"output is not a question", func(args []string, stderr io.Writer) ([]byte, error) {
			if args[1] == "create" {
				fmt.Fprintln(stderr, "NOTICE: Config file not found")
				return []byte("Remote config\n"), nil
			}
			return nil, nil
		}, "Config file not found"},
		{"questions never end", func(args []string, _ io.Writer) ([]byte, error) {
			if args[1] == "delete" {
				return nil, nil
			}
			return question, nil
		}, "more than 10 questions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tb := connectTestBridge(t, func(_ context.Context, args []string, stderr io.Writer) ([]byte, error) {
				return tc.run(args, stderr)
			})
			err := tb.backup.connectDrive(context.Background(), &connectJob{State: "running"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("connectDrive = %v, want an error with %q", err, tc.want)
			}
		})
	}
}

// TestBackupDisconnectWaitsForTheSignIn: Disconnect cancels a sign-in that
// is still running and waits for it before deleting the remote, so a token
// rclone writes as it ends cannot leave Drive connected afterwards.
func TestBackupDisconnectWaitsForTheSignIn(t *testing.T) {
	var conf string
	authorising := make(chan struct{})
	tb := connectTestBridge(t, func(ctx context.Context, args []string, _ io.Writer) ([]byte, error) {
		switch {
		case args[1] == "delete":
			return nil, os.WriteFile(conf, nil, 0o600)
		case args[1] == "create":
			close(authorising)
			<-ctx.Done()
			// rclone writes the token it got as it is interrupted.
			time.Sleep(200 * time.Millisecond)
			_ = os.WriteFile(conf, []byte("[brain-gdrive]\ntype = drive\ntoken = {\"access_token\":\"x\"}\n"), 0o600)
			return nil, ctx.Err()
		}
		return nil, nil
	})
	conf = tb.backup.rcloneConf()
	if w := tb.do("POST", "/api/backup/connect", nil); w.Code != 202 {
		t.Fatalf("connect = %d %s", w.Code, w.Body.String())
	}
	<-authorising
	tb.backup.mu.Lock()
	job := tb.backup.connect
	tb.backup.mu.Unlock()
	if w := tb.do("POST", "/api/backup/disconnect", nil); w.Code != 200 {
		t.Fatalf("disconnect = %d %s", w.Code, w.Body.String())
	}
	// Only once the sign-in has ended can its token have been written.
	select {
	case <-job.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the sign-in never ended")
	}
	if tb.backup.connected() {
		t.Error("Drive is connected after Disconnect: the sign-in's token came after the delete")
	}
}

// TestBackupDisconnectReportsFailures: a delete that fails is an error, not
// "disconnected", and Disconnect is refused while a backup runs.
func TestBackupDisconnectReportsFailures(t *testing.T) {
	tb := connectTestBridge(t, func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
		if args[1] == "delete" {
			return nil, errors.New("permission denied")
		}
		return nil, nil
	})
	if w := tb.do("POST", "/api/backup/disconnect", nil); w.Code != 500 || !strings.Contains(w.Body.String(), "permission denied") {
		t.Errorf("disconnect = %d %s, want 500 with the error", w.Code, w.Body.String())
	}
	m := tb.backup
	m.mu.Lock()
	m.job = &backupJob{cancel: func() {}}
	m.mu.Unlock()
	t.Cleanup(func() { m.mu.Lock(); m.job = nil; m.mu.Unlock() })
	if w := tb.do("POST", "/api/backup/disconnect", nil); w.Code != 409 {
		t.Errorf("disconnect during a backup = %d %s, want 409", w.Code, w.Body.String())
	}
}

// TestBackupDisconnectHoldsOffWhatWouldUseTheLink: while Disconnect
// removes the Drive link, nothing starts that would use it: no backup,
// which would fail on the deleted remote, no restore, no sign-in next to
// the delete, and no second Disconnect. All of them can start again after.
func TestBackupDisconnectHoldsOffWhatWouldUseTheLink(t *testing.T) {
	const connected, abandoned = "[brain-gdrive]\ntype = drive\ntoken = {\"access_token\":\"x\"}\n", "[brain-gdrive]\ntype = drive\ntoken = \n"
	cases := []struct {
		name, conf, path string
		body             any
	}{
		{"a backup", connected, "/api/backup/run", map[string]any{}},
		{"a restore", connected, "/api/backup/restore", map[string]string{"brain": "notes", "target": "~/restored"}},
		{"a sign-in", abandoned, "/api/backup/connect", nil},
		{"another disconnect", connected, "/api/backup/disconnect", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The first delete, Disconnect's, waits for release.
			deleting, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			tb := connectTestBridge(t, func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
				if len(args) > 1 && args[1] == "delete" {
					first := false
					once.Do(func() { first = true; close(deleting) })
					if first {
						<-release
					}
				}
				return nil, nil
			})
			if err := os.WriteFile(tb.backup.rcloneConf(), []byte(tc.conf), 0o600); err != nil {
				t.Fatal(err)
			}
			disconnected := make(chan int)
			go func() { disconnected <- tb.do("POST", "/api/backup/disconnect", nil).Code }()
			<-deleting
			w := tb.do("POST", tc.path, tc.body)
			close(release)
			if w.Code != 409 || !strings.Contains(w.Body.String(), "being removed") {
				t.Errorf("%s during the disconnect = %d %s, want 409", tc.path, w.Code, w.Body.String())
			}
			if code := <-disconnected; code != 200 {
				t.Errorf("disconnect = %d", code)
			}
			if _, err := os.Stat(filepath.Join(tb.home, "restored")); !os.IsNotExist(err) {
				t.Errorf("the refused restore made its folder (%v)", err)
			}
			m := tb.backup
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.job != nil || m.connect != nil || m.disconnecting {
				t.Errorf("after the disconnect: job %+v, sign-in %+v, disconnecting %v; want none", m.job, m.connect, m.disconnecting)
			}
		})
	}
}

// TestBackupDisconnectKeepsASignInThatDidNotStop: a sign-in that does not
// end when Disconnect cancels it stays on the status page, and keeps
// another from starting next to it.
func TestBackupDisconnectKeepsASignInThatDidNotStop(t *testing.T) {
	prev := backupStopWait
	backupStopWait = 200 * time.Millisecond
	t.Cleanup(func() { backupStopWait = prev })
	authorising, stuck := make(chan struct{}), make(chan struct{})
	var once sync.Once
	tb := connectTestBridge(t, func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
		if args[1] == "create" {
			once.Do(func() { close(authorising) })
			<-stuck // deaf to the cancel
		}
		return nil, nil
	})
	t.Cleanup(func() { close(stuck) })
	if w := tb.do("POST", "/api/backup/connect", nil); w.Code != 202 {
		t.Fatalf("connect = %d %s", w.Code, w.Body.String())
	}
	<-authorising
	if w := tb.do("POST", "/api/backup/disconnect", nil); w.Code != 500 || !strings.Contains(w.Body.String(), "did not stop") {
		t.Errorf("disconnect = %d %s, want 500", w.Code, w.Body.String())
	}
	var st struct {
		Connect *connectJob `json:"connect"`
	}
	_ = json.Unmarshal(tb.do("GET", "/api/backup/status", nil).Body.Bytes(), &st)
	if st.Connect == nil || st.Connect.State != "running" {
		t.Errorf("status shows sign-in %+v, want the running one", st.Connect)
	}
	if w := tb.do("POST", "/api/backup/connect", nil); w.Code != 409 || !strings.Contains(w.Body.String(), "already connecting") {
		t.Errorf("second connect = %d %s, want 409", w.Code, w.Body.String())
	}
}

// TestDailyBackupWithoutDriveIsLogged: a daily backup with no Drive link
// to run on says so in the log, once a day rather than every minute.
func TestDailyBackupWithoutDriveIsLogged(t *testing.T) {
	tb := newTestBridge(t, `, "backup": {"enabled": true}`)
	logs := captureLog(t)
	day := time.Date(2026, 10, 18, 4, 0, 0, 0, time.Local)
	for i := range 3 {
		tb.backup.tick(day.Add(time.Duration(i) * time.Minute))
	}
	tb.backup.tick(day.AddDate(0, 0, 1))
	all := strings.Join(logs(), "\n")
	if n := strings.Count(all, "Google Drive is not connected"); n != 2 {
		t.Errorf("logged %d times over two days, want 2:\n%s", n, all)
	}
}

// TestBackupSummarySaysWhatRuns: the overview tells a restore that runs
// from a backup.
func TestBackupSummarySaysWhatRuns(t *testing.T) {
	cases := []struct {
		name               string
		job                *backupJob
		running, restoring bool
	}{
		{"nothing", nil, false, false},
		{"a backup", &backupJob{kind: jobBackup}, true, false},
		{"a restore", &backupJob{kind: jobRestore}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestBridge(t, "").backup
			m.mu.Lock()
			m.job = tc.job
			m.mu.Unlock()
			t.Cleanup(func() { m.mu.Lock(); m.job = nil; m.mu.Unlock() })
			if s := m.Summary(); s.Running != tc.running || s.Restoring != tc.restoring {
				t.Errorf("running %v, restoring %v; want %v, %v", s.Running, s.Restoring, tc.running, tc.restoring)
			}
		})
	}
}

// TestBackupSummaryShowsTheLatestFailure: with several brains failing, the
// overview shows the latest failure and whose it is, the same on every
// refresh, and a removed brain's failure neither shows nor stays saved.
func TestBackupSummaryShowsTheLatestFailure(t *testing.T) {
	tb := newTestBridge(t, `, "backup": {"include_bridge_config": false}`)
	m := tb.backup
	_ = os.MkdirAll(filepath.Dir(m.rcloneConf()), 0o700)
	_ = os.WriteFile(m.rcloneConf(), []byte("[brain-gdrive]\ntype = alias\nremote = /nowhere\n"), 0o600)
	addBackupBrain(t, tb, "a")
	addBackupBrain(t, tb, "b")
	at := time.Date(2026, 10, 18, 3, 30, 0, 0, time.Local)
	failed := func(min int, msg string) *BackupResult {
		return &BackupResult{Started: at, Finished: at.Add(time.Duration(min) * time.Minute), Error: msg}
	}
	m.mu.Lock()
	m.state.Brains["a"] = failed(1, "a failed")
	m.state.Brains["b"] = failed(2, "b failed")
	m.state.Brains["gone"] = failed(3, "gone failed") // a brain removed since
	m.state.Bridge = &BackupResult{OK: true, Finished: at.Add(4 * time.Minute)}
	m.mu.Unlock()
	for range 20 {
		s := m.Summary()
		if s.LastError != "b failed" || s.LastErrorBrain != "b" {
			t.Fatalf("summary error = %q of %q, want b's, the latest of the configured brains", s.LastError, s.LastErrorBrain)
		}
	}

	m.runner = (&deletionRunner{src: []string{"x"}, cur: []string{"x"}}).run
	if res := runBackup(t, tb, "a"); !res.OK {
		t.Fatalf("backup of a = %+v", res)
	}
	saved := NewBackupManager(tb.cfgs, tb.brainMgr).state.Brains
	if _, ok := saved["gone"]; ok || saved["a"] == nil || saved["b"] == nil {
		t.Errorf("saved results for %v, want a and b only", slices.Collect(maps.Keys(saved)))
	}
}

// TestBackupBridgeRefusesAnIncompleteStage: the bridge's settings are
// synced only when every file of them could be staged. A sync of what
// could would move the good copies in Drive to versions/ and report OK.
func TestBackupBridgeRefusesAnIncompleteStage(t *testing.T) {
	cases := []struct {
		name    string
		failing string // the staged file whose write fails; "" for none
	}{
		// What the last run staged cannot be cleared: its brains folder
		// became read-only.
		{"the last stage stays", ""},
		{"config.json", "config.json"},
		{"a brain's manifest", "notes.brain.json"},
		{"the README", "README.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tb := newTestBridge(t, "")
			addBackupBrain(t, tb, "notes")
			m := tb.backup
			var mu sync.Mutex
			var calls [][]string
			m.runner = func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
				mu.Lock()
				calls = append(calls, args)
				mu.Unlock()
				return nil, nil
			}
			if tc.failing == "" {
				if os.Geteuid() == 0 {
					t.Skip("root writes to read-only folders")
				}
				brains := filepath.Join(m.dir(), "_staging", "brains")
				if err := os.MkdirAll(brains, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(brains, "old.brain.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(brains, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(brains, 0o700) })
			} else {
				prev := writeStaged
				writeStaged = func(name string, data []byte, perm os.FileMode) error {
					if filepath.Base(name) == tc.failing {
						return errors.New("disk full")
					}
					return prev(name, data, perm)
				}
				t.Cleanup(func() { writeStaged = prev })
			}

			res := m.backupBridge(context.Background(), "manual")
			if res.OK || !strings.Contains(res.Error, "staging the bridge's settings failed") {
				t.Errorf("result = %+v, want a staging failure", res)
			}
			if tc.failing != "" && !strings.Contains(res.Error, "disk full") {
				t.Errorf("result = %+v, want the write's error", res)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(calls) != 0 {
				t.Errorf("rclone ran %v", calls)
			}
		})
	}
}

// connectAlias gives the bridge's rclone config a remote, so that the
// bridge counts Drive as connected.
func connectAlias(t *testing.T, tb *testBridge) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(tb.backup.rcloneConf()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tb.backup.rcloneConf(), []byte("[brain-gdrive]\ntype = alias\nremote = /nowhere\n"), 0o600); err != nil {
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
	tb.backup.runner = func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
		mu.Lock()
		calls = append(calls, args)
		mu.Unlock()
		return []byte("[]"), nil
	}
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
			tb.backup.runner = func(_ context.Context, args []string, stderr io.Writer) ([]byte, error) {
				src = args[1]
				<-release
				if tc.fail {
					fmt.Fprintln(stderr, `{"level":"error","msg":"directory not found"}`)
					return nil, errors.New("exit status 3")
				}
				fmt.Fprintln(stderr, `{"level":"notice","msg":"stats","stats":{"bytes":2048,"transfers":3}}`)
				return nil, nil
			}
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
				LastRestore *RestoreResult `json:"last_restore"`
			}
			_ = json.Unmarshal(tb.do("GET", "/api/backup/status", nil).Body.Bytes(), &st)
			if st.Job.Kind != "restore" || st.Job.Brain != "notes" || st.Job.Target != filepath.Join(tb.home, "restored") {
				t.Errorf("job = %+v, want the restore of notes", st.Job)
			}
			close(release)
			testutil.WaitFor(t, 10*time.Second, "the restore", func() bool {
				tb.backup.mu.Lock()
				defer tb.backup.mu.Unlock()
				return tb.backup.job == nil
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
	tb.backup.mu.Lock()
	defer tb.backup.mu.Unlock()
	if tb.backup.job != nil {
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
	tb.backup.runner = func(ctx context.Context, args []string, _ io.Writer) ([]byte, error) {
		if args[0] != "sync" {
			return []byte("[]"), nil
		}
		close(syncing)
		<-ctx.Done()
		return nil, ctx.Err()
	}
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
			filter := brainFilter(tc.excludes, tc.includeSecrets)
			lines := strings.Split(strings.TrimSpace(filter), "\n")
			for _, p := range backupSecretExcludes {
				if got := slices.Contains(lines, "- "+p); got == tc.includeSecrets {
					t.Errorf("filter has - %s: %v, want %v:\n%s", p, got, !tc.includeSecrets, filter)
				}
			}
			for _, l := range lines {
				if strings.HasPrefix(l, "+") {
					t.Errorf("filter includes %q", l)
				}
			}
			if !tc.includeSecrets && lines[0] != "- "+backupSecretExcludes[0] {
				t.Errorf("filter starts with %q, want the secret files first", lines[0])
			}
		})
	}

	t.Run("a run writes it", func(t *testing.T) {
		tb := newTestBridge(t, "")
		addBackupBrain(t, tb, "notes")
		var filters []string
		tb.backup.runner = func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
			if i := slices.Index(args, "--filter-from"); i >= 0 && args[0] == "sync" {
				data, _ := os.ReadFile(args[i+1])
				filters = append(filters, string(data))
			}
			return []byte("[]"), nil
		}
		tb.backup.backupBrain(t.Context(), backupTarget(tb.brainMgr.Get("notes"), nil), "manual", true)
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
	for _, p := range backupSecretExcludes {
		if !strings.Contains(string(doc), "`"+p+"`") {
			t.Errorf("docs/BACKUP.md does not name %s", p)
		}
	}
}

// TestSyncDirRetriesFilesThatChange: rclone fails a file whose size changes
// while it copies it ("source file is being updated"), and a brain's
// history gets a line whenever a message comes in. The sync asks rclone to
// try again after a pause, and the log says when it had to.
func TestSyncDirRetriesFilesThatChange(t *testing.T) {
	tb := newTestBridge(t, "")
	var got []string
	tb.backup.runner = func(_ context.Context, args []string, stderr io.Writer) ([]byte, error) {
		got = args
		fmt.Fprintln(stderr, `{"level":"error","msg":"conversation-history.jsonl: Failed to copy: can't copy - source file is being updated (size changed from 10 to 12)","object":"conversation-history.jsonl"}`)
		fmt.Fprintln(stderr, `{"level":"error","msg":"Attempt 1/3 failed with 1 errors and: can't copy - source file is being updated"}`)
		fmt.Fprintln(stderr, `{"level":"notice","msg":"Attempt 2/3 succeeded"}`)
		return nil, nil
	}
	logged := captureLog(t)

	res := tb.backup.syncDir(t.Context(), t.TempDir(), notesBase, "", "manual", -1)
	if !res.OK {
		t.Errorf("result = %+v, want OK", res)
	}
	if flagValue(got, "--retries") != "3" || flagValue(got, "--retries-sleep") != "10s" {
		t.Errorf("sync args %q, want --retries 3 --retries-sleep 10s", got)
	}
	if all := strings.Join(logged(), "\n"); !strings.Contains(all, "rclone tried 1 more time(s)") {
		t.Errorf("log = %q, want it to say rclone retried", all)
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
			tb.backup.idleWait = c.wait
			running := func() bool {
				br.mu.Lock()
				defer br.mu.Unlock()
				return br.current != nil
			}
			var syncedWhileRunning []bool
			tb.backup.runner = func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
				if args[0] == "sync" {
					syncedWhileRunning = append(syncedWhileRunning, running())
				}
				return []byte("[]"), nil
			}
			logged := captureLog(t)

			t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "1500")
			if _, _, err := br.Submit(Inbound{Inbound: chat.Inbound{JID: "15551234567@s.whatsapp.net", Sender: "15551234567", Text: "hello", Source: "whatsapp", At: time.Now()}}); err != nil {
				t.Fatal(err)
			}
			testutil.WaitFor(t, 10*time.Second, "the run to start", running)
			tb.backup.backupBrain(t.Context(), backupTarget(br, nil), "manual", true)
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
