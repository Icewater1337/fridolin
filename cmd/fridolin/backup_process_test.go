package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

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

// TestSyncDirMaxDeleteWithRealRclone pins the rclone log line that marks a
// sync stopped by --max-delete.
func TestSyncDirMaxDeleteWithRealRclone(t *testing.T) {
	fx, remoteDir := rcloneBackupFixture(t)
	src := t.TempDir()
	for _, f := range []string{"a", "b", "c"} {
		_ = os.WriteFile(filepath.Join(src, f), []byte(f), 0o600)
	}
	if res := fx.manager.syncDir(context.Background(), src, backupFixtureBase, "", "manual", -1); !res.OK {
		t.Fatalf("first sync = %+v", res)
	}
	// Replacing files moves the old copies to versions/ too, but rclone does
	// not count that as a deletion.
	for _, f := range []string{"a", "b", "c"} {
		_ = os.WriteFile(filepath.Join(src, f), []byte(f+" changed"), 0o600)
	}
	if res := fx.manager.syncDir(context.Background(), src, backupFixtureBase, "", "manual", 0); !res.OK {
		t.Fatalf("overwriting every file with --max-delete 0 = %+v", res)
	}
	_ = os.Remove(filepath.Join(src, "a"))
	_ = os.Remove(filepath.Join(src, "b"))
	res := fx.manager.syncDir(context.Background(), src, backupFixtureBase, "", "manual", 1)
	if res.OK || !res.Refused {
		t.Errorf("result = %+v, want a refusal at the --max-delete limit", res)
	}
	entries, _ := os.ReadDir(filepath.Join(remoteDir, "fridolin-backup", "notes", "current"))
	if len(entries) < 2 {
		t.Errorf("current/ has %d files, rclone deleted more than the limit", len(entries))
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
	fx := processBackupFixture(t, script)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := fx.manager.runRclone(ctx, []string{"copy", "x:", "y"}, nil)
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

func processBackupFixture(t *testing.T, bin string) *backupFixture {
	t.Helper()
	f := newBackupFixture(t, nil)
	opts := f.options
	opts.Runner = nil
	opts.FindRclone = func(string) (string, error) { return bin, nil }
	home := t.TempDir()
	opts.Environ = func(context.Context) []string {
		return []string{"PATH=/usr/bin:/bin", "HOME=" + home, "RCLONE_CONFIG=" + opts.RcloneConf}
	}
	f.manager = newBackupManager(opts)
	t.Cleanup(f.manager.Stop)
	return f
}

func rcloneBackupFixture(t *testing.T) (*backupFixture, string) {
	t.Helper()
	bin, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone not installed")
	}
	f := processBackupFixture(t, bin)
	remote := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(f.options.RcloneConf), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.options.RcloneConf, []byte("[brain-gdrive]\ntype = alias\nremote = "+remote+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return f, remote
}
