package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/apperr"
)

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
			fx := connectBackupFixture(t, func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
				mu.Lock()
				calls = append(calls, args)
				mu.Unlock()
				return nil, nil
			})
			m := fx.manager
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
			err := m.Connect()
			if apperr.KindOf(err) != apperr.Conflict || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("connect = %v, want conflict with %q", err, tc.want)
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
			fx := connectBackupFixture(t, func(_ context.Context, args []string, stderr io.Writer) ([]byte, error) {
				return tc.run(args, stderr)
			})
			err := fx.manager.connectDrive(context.Background(), &connectJob{State: "running"})
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
	fx := connectBackupFixture(t, func(ctx context.Context, args []string, _ io.Writer) ([]byte, error) {
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
	conf = fx.manager.rcloneConf()
	if err := fx.manager.Connect(); err != nil {
		t.Fatal(err)
	}
	<-authorising
	fx.manager.mu.Lock()
	job := fx.manager.connect
	fx.manager.mu.Unlock()
	if err := fx.manager.Disconnect(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Only once the sign-in has ended can its token have been written.
	select {
	case <-job.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the sign-in never ended")
	}
	if fx.manager.connected() {
		t.Error("Drive is connected after Disconnect: the sign-in's token came after the delete")
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
				_, err = fx.manager.Restore(BackupRestoreRequest{Brain: "notes", Target: target}, func(s string) (string, error) { return s, nil })
			case "a sign-in":
				err = fx.manager.Connect()
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
// end when Disconnect cancels it stays on the status page, and keeps
// another from starting next to it.
func TestBackupDisconnectKeepsASignInThatDidNotStop(t *testing.T) {
	prev := backupStopWait
	backupStopWait = 200 * time.Millisecond
	t.Cleanup(func() { backupStopWait = prev })
	authorising, stuck := make(chan struct{}), make(chan struct{})
	var once sync.Once
	fx := connectBackupFixture(t, func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
		if args[1] == "create" {
			once.Do(func() { close(authorising) })
			<-stuck // deaf to the cancel
		}
		return nil, nil
	})
	t.Cleanup(func() { close(stuck) })
	if err := fx.manager.Connect(); err != nil {
		t.Fatal(err)
	}
	<-authorising
	if err := fx.manager.Disconnect(t.Context()); err == nil || apperr.KindOf(err) != apperr.Unknown || !strings.Contains(err.Error(), "did not stop") {
		t.Errorf("disconnect = %v, want sign-in timeout", err)
	}
	var st struct {
		Connect *connectJob `json:"connect"`
	}
	_ = json.Unmarshal(mustBackupJSON(t, fx.manager.Status()), &st)
	if st.Connect == nil || st.Connect.State != "running" {
		t.Errorf("status shows sign-in %+v, want the running one", st.Connect)
	}
	if err := fx.manager.Connect(); apperr.KindOf(err) != apperr.Conflict || !strings.Contains(err.Error(), "already connecting") {
		t.Errorf("second connect = %v, want conflict", err)
	}
}
