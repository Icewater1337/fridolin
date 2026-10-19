package main

import (
	"context"
	"encoding/json"
	"reflect"
	"runtime"
	"testing"

	"github.com/Icewater1337/fridolin/internal/brain"
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
	mgr := brain.NewManager(brain.Options{})
	t.Cleanup(mgr.Stop)
	sp := manifest.Spec{Dir: t.TempDir(), Manifest: manifest.Manifest{ID: "notes", Name: "Before", Backup: manifest.Backup{Enabled: true, Exclude: []string{"one"}}}}
	apply := func() {
		t.Helper()
		if failures, err := mgr.Apply([]manifest.Spec{sp}, brain.Globals{}, brain.ApplyOptions{}); err != nil || len(failures) != 0 {
			t.Fatalf("apply: %v %v", failures, err)
		}
	}
	apply()
	br := mgr.Get("notes")
	original := br
	var notified *brain.Brain
	target := backupTarget(br, func(_ context.Context, got *brain.Brain, _ string) error { notified = got; return nil })
	first := target.Settings()
	first.Exclude[0] = "changed by consumer"
	if !reflect.DeepEqual(target.Settings().Exclude, []string{"one"}) {
		t.Fatal("projection borrowed mutable excludes")
	}
	sp.Manifest.Name = "After"
	sp.Manifest.Backup = manifest.Backup{Exclude: []string{"two"}}
	apply()
	if mgr.Get("notes") != original {
		t.Fatal("settings reload replaced the runtime")
	}
	sp.Dir = t.TempDir()
	apply()
	br = mgr.Get("notes")
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
