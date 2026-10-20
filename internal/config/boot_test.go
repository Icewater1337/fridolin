package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestStoreBootSurvivesSavesAndIsAValueCopy(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	opts := Options{Paths: Paths{Home: home, DefaultDataDir: filepath.Join(home, "running")}, GOOS: "linux"}
	cs, err := Open(filepath.Join(home, "config.json"), opts)
	if err != nil {
		t.Fatal(err)
	}
	before := cs.Boot()
	copy := before
	copy.DataDir = "changed"
	if cs.Boot() != before {
		t.Fatal("modifying a returned Boot changed the store")
	}
	restart, err := cs.Update("", func(c *File) error {
		c.HTTPListen = "127.0.0.1:9876"
		c.DataDir = "~/next"
		c.DBPath = "~/next/custom.db"
		c.MediaDir = "~/next/inbox"
		c.LogLevel = "debug"
		c.Dashboard.Disabled = true
		c.Dashboard.RequireToken = new(false)
		c.Nodes.Enabled = true
		c.Nodes.Listen = "127.0.0.1:8799"
		c.AgentProvider = "codex"
		c.CodexModel = "live-model"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(restart, RestartFieldNames()) {
		t.Fatalf("restart = %v", restart)
	}
	if cs.Boot() != before {
		t.Fatalf("save changed Boot: %#v -> %#v", before, cs.Boot())
	}
	if cs.Get().DataDir != filepath.Join(home, "next") || cs.Get().CodexModel != "live-model" || cs.FileCopy().DataDir != "~/next" {
		t.Fatal("save lost the next-start or live values")
	}
	for _, dir := range []string{before.DataDir, cs.Get().DataDir} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("save created runtime dir %q: %v", dir, err)
		}
	}
	for name, got := range map[string]string{"dashboard.token": before.TokenPath(), "bridge.lock": before.LockPath(), "backup": before.BackupDir(), "rclone/rclone.conf": before.RcloneConf(), "google/oauth_client.json": before.GoogleClientPath(), "plaud": before.PlaudDir(), "otp": before.OTPDir()} {
		if want := filepath.Join(before.DataDir, filepath.FromSlash(name)); got != want {
			t.Errorf("Boot path = %q, want %q", got, want)
		}
	}
	if before.Home != home || before.HTTPListen != "127.0.0.1:8765" || before.DashboardDisabled || !before.DashboardRequireToken {
		t.Fatalf("startup values = %#v", before)
	}
	restarted, err := Open(cs.Path(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Boot().DataDir != cs.Get().DataDir || restarted.Boot().DashboardRequireToken {
		t.Fatalf("next open did not apply saved Boot values: %#v", restarted.Boot())
	}
}
