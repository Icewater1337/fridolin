package backup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/apperr"
)

// fakeOAuth stands in for the bridge's Google sign-in: it hands out an
// auth URL per sign-in and lets the test finish the last one, as Google's
// redirect would.
type fakeOAuth struct {
	mu     sync.Mutex
	client *OAuthClient // the Google sign-in client; nil for none
	starts []OAuthClient
	scopes [][]string
	done   func(*OAuthToken, error)
}

const (
	testClientID     = "1234-drive.apps.googleusercontent.com"
	testClientSecret = "DRIVE-CLIENT-SECRET"
	testPastedJSON   = `{"web":{"client_id":"5678-pasted.apps.googleusercontent.com","client_secret":"PASTED-SECRET"}}`
)

func newFakeOAuth(t *testing.T, m *Manager) *fakeOAuth {
	t.Helper()
	f := &fakeOAuth{client: &OAuthClient{Type: "installed", ClientID: testClientID, ClientSecret: testClientSecret}}
	m.opts.DriveClientPath = filepath.Join(t.TempDir(), "google", "drive_oauth_client.json")
	m.opts.OAuth = OAuth{
		SignInClient: func() *OAuthClient {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.client == nil {
				return nil
			}
			c := *f.client
			return &c
		},
		ParseClient: func(data []byte) (*OAuthClient, error) {
			var file map[string]struct {
				ClientID     string `json:"client_id"`
				ClientSecret string `json:"client_secret"`
			}
			if err := json.Unmarshal(data, &file); err != nil {
				return nil, err
			}
			for typ, c := range file {
				return &OAuthClient{Type: typ, ClientID: c.ClientID, ClientSecret: c.ClientSecret}, nil
			}
			return nil, errors.New("no client")
		},
		RedirectURI: func() string { return "http://127.0.0.1:8765/" },
		Start: func(c OAuthClient, scopes []string, done func(*OAuthToken, error)) (string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.starts = append(f.starts, c)
			f.scopes = append(f.scopes, scopes)
			f.done = done
			return "https://accounts.example.com/o/oauth2/v2/auth?state=s" + string(rune('0'+len(f.starts))), nil
		},
	}
	return f
}

// finish ends the last sign-in as Google's redirect would.
func (f *fakeOAuth) finish(tok *OAuthToken, err error) {
	f.mu.Lock()
	done := f.done
	f.mu.Unlock()
	done(tok, err)
}

var testToken = &OAuthToken{AccessToken: "SECRET-ACCESS", RefreshToken: "SECRET-REFRESH", Expiry: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)}

// driveFake is rclone on a Drive: the folders at the top that the trial
// remote's client can see, by name and id.
type driveFake struct {
	mu      sync.Mutex
	folders map[string]string
	calls   [][]string
	fail    string // a command (lsjson, mkdir) that fails
	block   chan struct{}
}

func (d *driveFake) run(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
	d.mu.Lock()
	d.calls = append(d.calls, args)
	fail, block := d.fail, d.block
	d.mu.Unlock()
	if block != nil && args[0] == "lsjson" {
		<-block // deaf to a cancel
	}
	if args[0] == fail {
		return nil, errors.New("googleapi: Error 403: insufficientPermissions")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	switch args[0] {
	case "lsjson":
		type entry struct {
			Name, ID string
			IsDir    bool
		}
		var out []entry
		for name, id := range d.folders {
			out = append(out, entry{name, id, true})
		}
		return json.Marshal(out)
	case "mkdir":
		if strings.HasSuffix(args[1], ":"+DriveFolder) {
			if d.folders == nil {
				d.folders = map[string]string{}
			}
			d.folders[DriveFolder] = "FOLDER-ID-NEW"
		}
	case "config":
		return nil, nil
	}
	return nil, nil
}

func (d *driveFake) commands() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, c := range d.calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

const sharedClientConf = "[brain-gdrive]\ntype = drive\nscope = drive.file\ntoken = {\"access_token\":\"OLD\"}\n"

func waitConnect(t *testing.T, m *Manager) *connectJob {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		job := copyOf(m.connect)
		m.mu.Unlock()
		if job != nil && job.State != "running" {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the sign-in never ended")
	return nil
}

func readConf(t *testing.T, m *Manager) string {
	t.Helper()
	data, err := os.ReadFile(m.rcloneConf())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

// TestConnectDriveWithOwnClient: the sign-in asks for drive.file with the
// Google sign-in client, and once Google answers the link goes into the
// remote with the client's id and secret, rclone's form of the token and
// a Drive folder of its own (root_folder_id), made by the new client; the
// backup folder is made in it. The trial remote is gone afterwards and no
// secret reaches the status.
func TestConnectDriveWithOwnClient(t *testing.T) {
	d := &driveFake{}
	fx := connectBackupFixture(t, d.run)
	m := fx.manager
	if err := os.WriteFile(m.rcloneConf(), []byte("[other]\ntype = alias\nremote = /x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	url, err := m.Connect(false)
	if err != nil || !strings.HasPrefix(url, "https://accounts.example.com/") {
		t.Fatalf("connect = %q, %v", url, err)
	}
	if got := fx.oauth.scopes[0]; !slices.Equal(got, []string{DriveScope}) || fx.oauth.starts[0].ClientID != testClientID {
		t.Fatalf("sign-in with %+v for %v", fx.oauth.starts[0], got)
	}
	st := m.Status()
	if st.Connect == nil || st.Connect.Step != stepSignIn || st.Connect.AuthURL != url {
		t.Fatalf("status while signing in: %+v", st.Connect)
	}
	fx.oauth.finish(testToken, nil)
	if job := waitConnect(t, m); job.State != "done" {
		t.Fatalf("sign-in ended %+v", job)
	}
	conf := readConf(t, m)
	sec := RcloneSection(conf, "brain-gdrive")
	if sec["type"] != "drive" || sec["client_id"] != testClientID || sec["client_secret"] != testClientSecret || sec["scope"] != "drive.file" || sec["root_folder_id"] != "FOLDER-ID-NEW" {
		t.Fatalf("remote = %v", sec)
	}
	var tok map[string]string
	if err := json.Unmarshal([]byte(sec["token"]), &tok); err != nil || tok["access_token"] != "SECRET-ACCESS" || tok["refresh_token"] != "SECRET-REFRESH" || tok["token_type"] != "Bearer" || tok["expiry"] != "2026-10-21T12:00:00Z" {
		t.Fatalf("token line %q (%v)", sec["token"], err)
	}
	if RcloneSection(conf, "brain-gdrive"+connectingSuffix) != nil || RcloneSection(conf, "other")["remote"] != "/x" {
		t.Errorf("rclone.conf afterwards:\n%s", conf)
	}
	want := []string{
		"lsjson --dirs-only --no-modtime --no-mimetype brain-gdrive-connecting:",
		"mkdir brain-gdrive-connecting:" + DriveFolder,
		"lsjson --dirs-only --no-modtime --no-mimetype brain-gdrive-connecting:",
		"mkdir brain-gdrive-connecting:fridolin-backup",
	}
	if got := d.commands(); !slices.Equal(got, want) {
		t.Errorf("rclone ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	st = m.Status()
	if !st.Connected || st.SharedClient || st.DriveFolder != DriveFolder || st.Client == nil || st.Client.Source != "google" || st.Client.ClientIDHint != "1234-…" {
		t.Errorf("status = connected %v, shared %v, folder %q, client %+v", st.Connected, st.SharedClient, st.DriveFolder, st.Client)
	}
	body := string(mustBackupJSON(t, st))
	for _, secret := range []string{"SECRET-ACCESS", "SECRET-REFRESH", testClientSecret, testClientID} {
		if strings.Contains(body, secret) {
			t.Errorf("status shows %s", secret)
		}
	}
}

// TestConnectDriveUsesItsOwnFolderAgain: a Drive folder the client can
// already see is one it made, and is used again rather than made twice.
func TestConnectDriveUsesItsOwnFolderAgain(t *testing.T) {
	d := &driveFake{folders: map[string]string{DriveFolder: "FOLDER-ID-OLD"}}
	fx := connectBackupFixture(t, d.run)
	if _, err := fx.manager.Connect(false); err != nil {
		t.Fatal(err)
	}
	fx.oauth.finish(testToken, nil)
	if job := waitConnect(t, fx.manager); job.State != "done" {
		t.Fatalf("sign-in ended %+v", job)
	}
	if id := RcloneSection(readConf(t, fx.manager), "brain-gdrive")["root_folder_id"]; id != "FOLDER-ID-OLD" {
		t.Errorf("root_folder_id = %q", id)
	}
	if slices.ContainsFunc(d.commands(), func(c string) bool { return strings.HasSuffix(c, ":"+DriveFolder) }) {
		t.Errorf("made the folder again: %v", d.commands())
	}
}

// TestConnectDrivePrefersAPastedClient: a client pasted for Drive comes
// before the Google sign-in one, and removing it goes back to that.
func TestConnectDrivePrefersAPastedClient(t *testing.T) {
	fx := connectBackupFixture(t, (&driveFake{}).run)
	m := fx.manager
	if _, err := m.SetDriveClient([]byte("not json")); err == nil {
		t.Error("a broken client was kept")
	}
	if _, err := m.SetDriveClient([]byte(testPastedJSON)); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(m.opts.DriveClientPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("client file: %v %v", fi, err)
	}
	if st := m.Status(); st.Client == nil || st.Client.Source != "drive" || st.Client.Type != "web" || st.Client.ClientIDHint != "5678-…" || st.RedirectURI != "http://127.0.0.1:8765/" {
		t.Fatalf("client status %+v, redirect %q", st.Client, st.RedirectURI)
	}
	if _, err := m.Connect(false); err != nil {
		t.Fatal(err)
	}
	if c := fx.oauth.starts[0]; c.ClientID != "5678-pasted.apps.googleusercontent.com" || c.ClientSecret != "PASTED-SECRET" {
		t.Errorf("signed in with %+v", c)
	}
	if err := m.DeleteDriveClient(); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(); st.Client == nil || st.Client.Source != "google" {
		t.Errorf("after removing it: %+v", st.Client)
	}
	fx.oauth.mu.Lock()
	fx.oauth.client = nil
	fx.oauth.mu.Unlock()
	if _, err := m.Connect(false); apperr.KindOf(err) != apperr.Precondition || !strings.Contains(err.Error(), "Google OAuth client") {
		t.Errorf("connect without a client = %v", err)
	}
}

// TestBackupConnectLeavesAWorkingLinkAlone: without reconnect, Connect is
// refused while Drive is connected, and it is refused while a backup or
// restore runs; neither starts a sign-in or touches the config.
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
			d := &driveFake{}
			fx := connectBackupFixture(t, d.run)
			m := fx.manager
			conf := "[brain-gdrive]\ntype = drive\ntoken = \n"
			if tc.connected {
				conf = sharedClientConf
			}
			_ = os.WriteFile(m.rcloneConf(), []byte(conf), 0o600)
			if tc.job {
				m.mu.Lock()
				m.job = &backupJob{cancel: func() {}}
				m.mu.Unlock()
				t.Cleanup(func() { m.mu.Lock(); m.job = nil; m.mu.Unlock() })
			}
			_, err := m.Connect(false)
			if apperr.KindOf(err) != apperr.Conflict || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("connect = %v, want conflict with %q", err, tc.want)
			}
			if len(fx.oauth.starts) != 0 || len(d.commands()) != 0 {
				t.Errorf("sign-ins %v, rclone %v", fx.oauth.starts, d.commands())
			}
			if got := readConf(t, m); got != conf {
				t.Errorf("rclone.conf changed to %q", got)
			}
		})
	}
}

// TestReconnectKeepsTheOldLinkUntilTheNewOneWorks: switching a link through
// rclone's shared client to the owner's own leaves the old remote in place,
// backing up, until the new one has its folder; a sign-in that fails or a
// Drive that refuses leaves it for good, and no trial remote behind.
func TestReconnectKeepsTheOldLinkUntilTheNewOneWorks(t *testing.T) {
	cases := []struct {
		name    string
		fail    string // the rclone command that fails
		signErr error
		want    string // in the job's error; "" for done
	}{
		{"switched", "", nil, ""},
		{"sign-in refused", "", errors.New("the Google sign-in was cancelled"), "cancelled"},
		{"Drive refuses the listing", "lsjson", nil, "insufficientPermissions"},
		{"Drive refuses the folder", "mkdir", nil, "insufficientPermissions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &driveFake{fail: tc.fail}
			fx := connectBackupFixture(t, d.run)
			m := fx.manager
			if err := os.WriteFile(m.rcloneConf(), []byte(sharedClientConf), 0o600); err != nil {
				t.Fatal(err)
			}
			if st := m.Status(); !st.Connected || !st.SharedClient {
				t.Fatalf("before: connected %v, shared %v", st.Connected, st.SharedClient)
			}
			if _, err := m.Connect(true); err != nil {
				t.Fatal(err)
			}
			if st := m.Status(); !st.Connected || st.Connect == nil || !st.Connect.Reconnect {
				t.Errorf("while signing in: connected %v, %+v", st.Connected, st.Connect)
			}
			if tc.signErr != nil {
				fx.oauth.finish(nil, tc.signErr)
			} else {
				fx.oauth.finish(testToken, nil)
			}
			job := waitConnect(t, m)
			conf := readConf(t, m)
			if RcloneSection(conf, "brain-gdrive"+connectingSuffix) != nil {
				t.Errorf("trial remote left behind:\n%s", conf)
			}
			if tc.want == "" {
				if job.State != "done" || RcloneSection(conf, "brain-gdrive")["client_id"] != testClientID || m.Status().SharedClient {
					t.Errorf("job %+v, conf:\n%s", job, conf)
				}
				return
			}
			if job.State != "error" || !strings.Contains(job.Error, tc.want) {
				t.Errorf("job = %+v, want an error with %q", job, tc.want)
			}
			if conf != sharedClientConf {
				t.Errorf("the old link changed:\n%s", conf)
			}
		})
	}
}

// TestReconnectGivesWayToABackup: a backup that starts while the new link
// is set up keeps the old one; the sign-in says to connect again.
func TestReconnectGivesWayToABackup(t *testing.T) {
	d := &driveFake{}
	var m *Manager
	fx := connectBackupFixture(t, func(ctx context.Context, args []string, w io.Writer) ([]byte, error) {
		if args[0] == "mkdir" && strings.HasSuffix(args[1], ":fridolin-backup") {
			m.mu.Lock()
			m.job = &backupJob{cancel: func() {}}
			m.mu.Unlock()
		}
		return d.run(ctx, args, w)
	})
	m = fx.manager
	t.Cleanup(func() { m.mu.Lock(); m.job = nil; m.mu.Unlock() })
	if err := os.WriteFile(m.rcloneConf(), []byte(sharedClientConf), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Connect(true); err != nil {
		t.Fatal(err)
	}
	fx.oauth.finish(testToken, nil)
	if job := waitConnect(t, m); job.State != "error" || !strings.Contains(job.Error, "connect again") {
		t.Errorf("job = %+v", job)
	}
	if conf := readConf(t, m); conf != sharedClientConf {
		t.Errorf("rclone.conf:\n%s", conf)
	}
}

// TestConnectReplacesASignInStillWaiting: clicking Connect again while the
// first sign-in still waits for Google starts a new one; the first one's
// answer, should it still come, changes nothing.
func TestConnectReplacesASignInStillWaiting(t *testing.T) {
	d := &driveFake{}
	fx := connectBackupFixture(t, d.run)
	m := fx.manager
	if _, err := m.Connect(false); err != nil {
		t.Fatal(err)
	}
	fx.oauth.mu.Lock()
	first := fx.oauth.done
	fx.oauth.mu.Unlock()
	m.mu.Lock()
	firstJob := m.connect
	m.mu.Unlock()
	second, err := m.Connect(false)
	if err != nil || !strings.HasSuffix(second, "s2") {
		t.Fatalf("second connect = %q, %v", second, err)
	}
	<-firstJob.done
	first(testToken, nil)
	time.Sleep(50 * time.Millisecond)
	if len(d.commands()) != 0 {
		t.Errorf("the first sign-in's answer ran rclone: %v", d.commands())
	}
	if st := m.Status(); st.Connect == nil || st.Connect.State != "running" || st.Connect.AuthURL != second {
		t.Errorf("status shows %+v", st.Connect)
	}
}

// TestBackupDisconnectWaitsForTheSignIn: Disconnect cancels a sign-in that
// is still running and waits for it before deleting the remote, so that a
// link it puts in place as it ends cannot leave Drive connected afterwards.
func TestBackupDisconnectWaitsForTheSignIn(t *testing.T) {
	release := make(chan struct{})
	d := &driveFake{block: release}
	var conf string
	fx := connectBackupFixture(t, func(ctx context.Context, args []string, w io.Writer) ([]byte, error) {
		if args[0] == "config" && args[1] == "delete" {
			data, _ := os.ReadFile(conf)
			s, _ := setRcloneSection(string(data), args[2], nil)
			return nil, os.WriteFile(conf, []byte(s), 0o600)
		}
		return d.run(ctx, args, w)
	})
	conf = fx.manager.rcloneConf()
	if _, err := fx.manager.Connect(false); err != nil {
		t.Fatal(err)
	}
	fx.oauth.finish(testToken, nil)
	go func() { time.Sleep(200 * time.Millisecond); close(release) }()
	if err := fx.manager.Disconnect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if fx.manager.connected() {
		t.Errorf("Drive is connected after Disconnect:\n%s", readConf(t, fx.manager))
	}
}

// TestBackupDisconnectReportsFailures: a delete that fails is an error, not
// "disconnected", and Disconnect is refused while a backup runs.
func TestBackupDisconnectReportsFailures(t *testing.T) {
	fx := connectBackupFixture(t, func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
		if args[1] == "delete" {
			return nil, errors.New("permission denied")
		}
		return nil, nil
	})
	if err := fx.manager.Disconnect(t.Context()); err == nil || apperr.KindOf(err) != apperr.Unknown || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("disconnect = %v, want delete failure", err)
	}
	m := fx.manager
	m.mu.Lock()
	m.job = &backupJob{cancel: func() {}}
	m.mu.Unlock()
	t.Cleanup(func() { m.mu.Lock(); m.job = nil; m.mu.Unlock() })
	if err := fx.manager.Disconnect(t.Context()); apperr.KindOf(err) != apperr.Conflict {
		t.Errorf("disconnect during backup = %v, want conflict", err)
	}
}

// TestBackupDisconnectHoldsOffWhatWouldUseTheLink: while Disconnect
// removes the Drive link, nothing starts that would use it: no backup,
// which would fail on the deleted remote, no restore, no sign-in next to
// the delete, and no second Disconnect. All of them can start again after.
func TestBackupDisconnectHoldsOffWhatWouldUseTheLink(t *testing.T) {
	const connected, abandoned = "[brain-gdrive]\ntype = drive\ntoken = {\"access_token\":\"x\"}\n", "[brain-gdrive]\ntype = drive\ntoken = \n"
	for _, name := range []string{"a backup", "a restore", "a sign-in", "another disconnect"} {
		t.Run(name, func(t *testing.T) {
			deleting, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			fx := connectBackupFixture(t, func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
				if len(args) > 1 && args[1] == "delete" {
					first := false
					once.Do(func() { first = true; close(deleting) })
					if first {
						<-release
					}
				}
				return nil, nil
			})
			conf := connected
			if name == "a sign-in" {
				conf = abandoned
			}
			if err := os.WriteFile(fx.manager.rcloneConf(), []byte(conf), 0600); err != nil {
				t.Fatal(err)
			}
			disconnected := make(chan error, 1)
			go func() { disconnected <- fx.manager.Disconnect(t.Context()) }()
			<-deleting
			target := filepath.Join(t.TempDir(), "restored")
			var err error
			switch name {
			case "a backup":
				err = fx.manager.StartRun(nil, false, "manual", false)
			case "a restore":
				_, err = fx.manager.Restore(RestoreRequest{Brain: "notes", Target: target}, func(s string) (string, error) { return s, nil })
			case "a sign-in":
				_, err = fx.manager.Connect(false)
			case "another disconnect":
				err = fx.manager.Disconnect(t.Context())
			}
			close(release)
			if !errors.Is(err, errDisconnecting) {
				t.Errorf("%s during disconnect = %v, want errDisconnecting", name, err)
			}
			if err := <-disconnected; err != nil {
				t.Errorf("disconnect = %v", err)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Errorf("refused restore made its folder (%v)", err)
			}
			m := fx.manager
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.job != nil || m.connect != nil || m.disconnecting {
				t.Errorf("after disconnect: job %+v, sign-in %+v, disconnecting %v; want none", m.job, m.connect, m.disconnecting)
			}
		})
	}
}

// TestBackupDisconnectKeepsASignInThatDidNotStop: a sign-in that does not
// end when Disconnect cancels it (an rclone call that hangs while the
// folder is set up) stays on the status page, and keeps another from
// starting next to it.
func TestBackupDisconnectKeepsASignInThatDidNotStop(t *testing.T) {
	prev := backupStopWait
	backupStopWait = 200 * time.Millisecond
	t.Cleanup(func() { backupStopWait = prev })
	stuck := make(chan struct{})
	d := &driveFake{block: stuck}
	fx := connectBackupFixture(t, d.run)
	t.Cleanup(func() { close(stuck) })
	if _, err := fx.manager.Connect(false); err != nil {
		t.Fatal(err)
	}
	fx.oauth.finish(testToken, nil)
	testWaitFor(t, func() bool { return len(d.commands()) > 0 })
	if err := fx.manager.Disconnect(t.Context()); err == nil || apperr.KindOf(err) != apperr.Unknown || !strings.Contains(err.Error(), "did not stop") {
		t.Errorf("disconnect = %v, want sign-in timeout", err)
	}
	var st struct {
		Connect *connectJob `json:"connect"`
	}
	_ = json.Unmarshal(mustBackupJSON(t, fx.manager.Status()), &st)
	if st.Connect == nil || st.Connect.State != "running" || st.Connect.Step != stepFolder {
		t.Errorf("status shows sign-in %+v, want the running one", st.Connect)
	}
	if _, err := fx.manager.Connect(false); apperr.KindOf(err) != apperr.Conflict || !strings.Contains(err.Error(), "already connecting") {
		t.Errorf("second connect = %v, want conflict", err)
	}
}

func testWaitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("timed out")
}

// TestSetRcloneSection: the writer replaces or adds one section and keeps
// every other line; nil removes it; a value with a line break is refused.
func TestSetRcloneSection(t *testing.T) {
	conf := "# mine\n[a]\ntype = alias\n\n[brain-gdrive]\ntype = drive\ntoken = {}\n\n[b]\ntype = local\n"
	got, err := setRcloneSection(conf, "brain-gdrive", []rcloneKV{{"type", "drive"}, {"client_id", "id"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "# mine\n[a]\ntype = alias\n\n[b]\ntype = local\n\n[brain-gdrive]\ntype = drive\nclient_id = id\n"; got != want {
		t.Errorf("replaced:\n%q\nwant\n%q", got, want)
	}
	got, _ = setRcloneSection(got, "brain-gdrive", nil)
	if want := "# mine\n[a]\ntype = alias\n\n[b]\ntype = local\n"; got != want {
		t.Errorf("removed: %q", got)
	}
	if got, _ := setRcloneSection("", "x", []rcloneKV{{"type", "drive"}}); got != "[x]\ntype = drive\n" {
		t.Errorf("new file: %q", got)
	}
	if _, err := setRcloneSection("", "x", []rcloneKV{{"token", "a\n[evil]"}}); err == nil {
		t.Error("a line break went into the config")
	}
}

// TestSharedClientRemotes: a Drive remote without a client_id uses
// rclone's shared client; one with it, and other kinds, do not.
func TestSharedClientRemotes(t *testing.T) {
	conf := "[old]\ntype = drive\ntoken = {}\n\n[own]\ntype = drive\nclient_id = x\n\n[alias]\ntype = alias\n\n[old2]\ntype = drive\nclient_id =\n"
	if got := SharedClientRemotes(conf); !slices.Equal(got, []string{"old", "old2"}) {
		t.Errorf("shared = %v", got)
	}
}
