package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// backupFixture supplies only the backup service's owned inputs. It creates no
// bridge, runtime, HTTP server, config store, process, or ambient home lookup.
type backupFixture struct {
	manager *BackupManager
	options BackupOptions
	runner  BackupRunFunc // assigned before starting a job

	mu       sync.Mutex
	settings BackupSettings
	targets  []BackupTarget
}

func newBackupFixture(t *testing.T, configure func(*BackupSettings)) *backupFixture {
	t.Helper()
	f := &backupFixture{settings: BackupSettings{Remote: "brain-gdrive", Root: "fridolin-backup", DailyAt: "03:30", KeepVersionsDays: 30, IncludeBridgeConfig: true}}
	if configure != nil {
		configure(&f.settings)
	}
	dir := t.TempDir()
	f.options = BackupOptions{
		BackupDir: filepath.Join(dir, "backup"), RcloneConf: filepath.Join(dir, "rclone", "rclone.conf"),
		Settings: func() BackupSettings {
			f.mu.Lock()
			defer f.mu.Unlock()
			s := f.settings
			s.ConfiguredIDs = slices.Clone(s.ConfiguredIDs)
			return s
		},
		Targets: func() []BackupTarget {
			f.mu.Lock()
			defer f.mu.Unlock()
			return slices.Clone(f.targets)
		},
		BridgeFiles: func() (BackupBridgeFiles, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			files := BackupBridgeFiles{Config: []byte("{}\n")}
			for _, target := range f.targets {
				files.Manifests = append(files.Manifests, BackupManifestFile{ID: target.ID, Data: []byte("{}\n")})
			}
			return files, nil
		},
		FindRclone:     func(string) (string, error) { return "", errRcloneMissing },
		Environ:        func(context.Context) []string { return nil },
		DesktopContext: func(ctx context.Context) context.Context { return ctx },
		InstallHint:    func() string { return "install rclone" },
		RefusalText:    func(name, reason string) string { return name + ": " + reason },
		Runner: func(ctx context.Context, args []string, stderr io.Writer) ([]byte, error) {
			if f.runner == nil {
				return nil, fmt.Errorf("unexpected rclone call: %v", args)
			}
			return f.runner(ctx, args, stderr)
		},
	}
	f.manager = newBackupManager(f.options)
	t.Cleanup(f.manager.Stop)
	return f
}

func (f *backupFixture) update(fn func(*BackupSettings)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(&f.settings)
}

func (f *backupFixture) addTarget(t *testing.T, id string) BackupTarget {
	t.Helper()
	target := BackupTarget{ID: id, Dir: t.TempDir(), Settings: func() BackupTargetSettings { return BackupTargetSettings{Name: id, Enabled: true} }, WaitIdle: func(context.Context, time.Duration) bool { return true }}
	f.mu.Lock()
	f.targets = append(f.targets, target)
	f.settings.ConfiguredIDs = append(f.settings.ConfiguredIDs, id)
	f.mu.Unlock()
	return target
}

func (f *backupFixture) target(id string) BackupTarget {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, target := range f.targets {
		if target.ID == id {
			return target
		}
	}
	panic("missing test target " + id)
}

func (f *backupFixture) run(t *testing.T, id string) *BackupResult {
	t.Helper()
	if err := f.manager.StartRun([]string{id}, false, "manual", false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		f.manager.mu.Lock()
		res, running := f.manager.state.Brains[id], f.manager.job != nil
		f.manager.mu.Unlock()
		if !running && res != nil {
			return res
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("backup did not finish")
	return nil
}

func mustBackupJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type backupLogBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *backupLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func captureBackupLog(t *testing.T) func() []string {
	t.Helper()
	var buf backupLogBuffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return func() []string {
		buf.mu.Lock()
		defer buf.mu.Unlock()
		return strings.Split(buf.String(), "\n")
	}
}

// backupDeletionRunner plays rclone for the mass-deletion check: lsjson lists src
// for the brain folder and cur for current/ (nil means rclone reports the
// folder as not found), and sync and lsjson record their arguments.
type backupDeletionRunner struct {
	mu      sync.Mutex
	src     []string
	cur     []string
	srcErr  string // set: listing the brain folder fails with this message
	listErr string // set: listing current/ fails with this message
	syncErr string // set: the sync fails with this error in rclone's log
	syncs   [][]string
	lists   [][]string
}

func (d *backupDeletionRunner) run(_ context.Context, args []string, stderr io.Writer) ([]byte, error) {
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

// backupFixtureBase is where the default config backs up the brain "notes".
const backupFixtureBase = "brain-gdrive:fridolin-backup" + "/notes"

// backupFlagValue returns the value after an rclone flag, "" when it is not set.
func backupFlagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// backupMaxDelete returns a sync's --max-delete value, "" for none.
func backupMaxDelete(args []string) string { return backupFlagValue(args, "--max-delete") }
