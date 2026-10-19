package main

import (
	"context"
	"encoding/json"
	"reflect"
	"runtime"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

func TestBackupSettingsProjectionPreservesStatusJSON(t *testing.T) {
	for _, raw := range []string{`{}`, `{"backup":{"enabled":true,"keep_versions_days":0,"include_bridge_config":false,"include_secret_files":true}}`} {
		var file config.Config
		if err := json.Unmarshal([]byte(raw), &file); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Resolve(&file, configPaths(), runtime.GOOS)
		if err != nil {
			t.Fatal(err)
		}
		before, err := json.Marshal(cfg.Backup)
		if err != nil {
			t.Fatal(err)
		}
		after, err := json.Marshal(backupSettings(cfg))
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatalf("status settings changed:\n%s\n%s", before, after)
		}
	}
}

func TestBackupTargetProjectionKeepsIdentityAndOwnsExcludes(t *testing.T) {
	br := &Brain{id: "notes", workDir: t.TempDir()}
	br.settings.Store(&brainSettings{name: "Before", manifest: manifest.Manifest{Backup: manifest.Backup{Enabled: true, Exclude: []string{"one"}}}})
	original := br
	var notified *Brain
	target := backupTarget(br, func(_ context.Context, got *Brain, _ string) error { notified = got; return nil })
	first := target.Settings()
	first.Exclude[0] = "changed by consumer"
	if !reflect.DeepEqual(target.Settings().Exclude, []string{"one"}) {
		t.Fatal("projection borrowed mutable excludes")
	}
	original.settings.Store(&brainSettings{name: "After", manifest: manifest.Manifest{Backup: manifest.Backup{Exclude: []string{"two"}}}})
	br = &Brain{id: "notes", workDir: t.TempDir()}
	if s := target.Settings(); s.Name != "After" || s.Enabled || !reflect.DeepEqual(s.Exclude, []string{"two"}) {
		t.Fatalf("target froze settings: %+v", s)
	}
	if err := target.Notify(context.Background(), "notice"); err != nil {
		t.Fatal(err)
	}
	if notified != original || notified == br || target.Dir != original.Dir() {
		t.Fatal("target followed a replacement runtime with the same ID")
	}
}
