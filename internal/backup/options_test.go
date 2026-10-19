package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The current target filter and backup destination are read after waiting idle.
func TestBackupOptionsReadSettingsAfterIdle(t *testing.T) {
	dir := t.TempDir()
	settings := Settings{Remote: "remote", Root: "before"}
	targetSettings := TargetSettings{Name: "Before", Enabled: true, Exclude: []string{"before"}}
	var sources, destinations, filters []string
	opts := Options{BackupDir: dir, RcloneConf: filepath.Join(dir, "rclone.conf"), Settings: func() Settings { return settings }, Runner: func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
		sources = append(sources, args[1])
		destinations = append(destinations, args[2])
		for i, a := range args {
			if a == "--filter-from" {
				data, err := os.ReadFile(args[i+1])
				if err != nil {
					t.Fatal(err)
				}
				filters = append(filters, string(data))
			}
		}
		return nil, nil
	}}
	target := Target{ID: "notes", Dir: filepath.Join(dir, "selected"), Settings: func() TargetSettings { return targetSettings }, WaitIdle: func(context.Context, time.Duration) bool {
		targetSettings.Exclude = []string{"after"}
		settings.Root = "after"
		return true
	}}
	m := New(opts)
	if res := m.backupBrain(context.Background(), target, "manual", true); !res.OK {
		t.Fatalf("backup = %+v", res)
	}
	if !reflect.DeepEqual(sources, []string{target.Dir + "/"}) || !reflect.DeepEqual(destinations, []string{"remote:after/notes/current"}) || len(filters) != 1 || !strings.HasSuffix(filters[0], "- after\n") || strings.Contains(filters[0], "- before\n") {
		t.Fatalf("sources %v, destinations %v, filters %q", sources, destinations, filters)
	}
}

// Staging preserves ordered errors and still attempts every later file. No
// ConfigStore or runtime is needed to exercise the backup write boundary.
func TestBackupOptionsStageOrderedFilesAndErrors(t *testing.T) {
	dir := t.TempDir()
	stage := filepath.Join(dir, "stage")
	if err := os.MkdirAll(stage, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "stale"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	marshalErr := errors.New("manifest encoding failed")
	m := New(Options{BackupDir: dir, BridgeFiles: func() (BridgeFiles, error) {
		if _, err := os.Stat(filepath.Join(stage, "stale")); !os.IsNotExist(err) {
			t.Fatalf("files read before old staging cleared: %v", err)
		}
		return BridgeFiles{Config: []byte("{}"), Manifests: []ManifestFile{{ID: "first", Data: []byte("first")}, {ID: "second", Err: marshalErr}, {ID: "third", Data: []byte("third")}}}, nil
	}})
	old := writeStaged
	t.Cleanup(func() { writeStaged = old })
	var writes []string
	writeStaged = func(path string, data []byte, mode os.FileMode) error {
		writes = append(writes, filepath.Base(path))
		if filepath.Base(path) == "config.json" {
			return errors.New("config write failed")
		}
		return old(path, data, mode)
	}
	err := m.stageBridge(stage)
	if err == nil || err.Error() != "config write failed\nmanifest encoding failed" || !errors.Is(err, marshalErr) {
		t.Fatalf("stage = %v", err)
	}
	if !reflect.DeepEqual(writes, []string{"config.json", "first.brain.json", "third.brain.json", "README.txt"}) {
		t.Fatalf("writes %v", writes)
	}
}
