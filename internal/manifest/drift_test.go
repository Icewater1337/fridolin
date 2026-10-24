package manifest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestDriftOf (#189): what brain.json and the scheduled agents on disk
// change against the approved manifest, and whether that keeps more of
// the folder home.
func TestDriftOf(t *testing.T) {
	approved := Manifest{ID: "personal", Name: "Personal", Backup: Backup{Enabled: true, Exclude: []string{"*.log"}}}
	for _, tc := range []struct {
		name, disk string
		any, backup, more bool
		describe          string
	}{
		{name: "as approved", disk: `{"id":"personal","name":"Personal","backup":{"enabled":true,"exclude":["*.log"]}}`},
		{name: "wider exclude", disk: `{"id":"personal","name":"Personal","backup":{"enabled":true,"exclude":["*.log","notes/work/"]}}`,
			any: true, backup: true, more: true, describe: "it excludes notes/work/ (backup.exclude), which the approved one does not"},
		{name: "node_only counts", disk: `{"id":"personal","name":"Personal","backup":{"enabled":true,"exclude":["*.log"],"node_only":["notes/work/"]}}`,
			any: true, backup: true, more: true, describe: "it excludes notes/work/"},
		{name: "narrower exclude", disk: `{"id":"personal","name":"Personal","backup":{"enabled":true,"exclude":[]}}`,
			any: true, backup: true, describe: "it no longer excludes *.log (backup.exclude), which the approved one does"},
		{name: "backup off", disk: `{"id":"personal","name":"Personal","backup":{"enabled":false,"exclude":["*.log"]}}`,
			any: true, backup: true, more: true, describe: "turns backup.enabled off"},
		{name: "cloud off", disk: `{"id":"personal","name":"Personal","backup":{"enabled":true,"exclude":["*.log"],"targets":{"cloud":false}}}`,
			any: true, backup: true, more: true, describe: "turns backup.targets.cloud off"},
		{name: "not the backup", disk: `{"id":"personal","name":"Personal Brain","backup":{"enabled":true,"exclude":["*.log"]}}`,
			any: true, describe: "(not in its backup settings)"},
		{name: "unreadable", disk: `{nope`, any: true, describe: "brain.json on disk cannot be read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, File), []byte(tc.disk), 0o600); err != nil {
				t.Fatal(err)
			}
			d := DriftOf(dir, approved)
			if d.Any() != tc.any || d.Backup() != tc.backup || d.KeepsMoreHome() != tc.more {
				t.Errorf("any %v backup %v more %v: %+v", d.Any(), d.Backup(), d.KeepsMoreHome(), d)
			}
			if !strings.Contains(d.Describe(), tc.describe) || (tc.describe == "") != (d.Describe() == "") {
				t.Errorf("Describe = %q, want %q", d.Describe(), tc.describe)
			}
		})
	}
}

// TestDriftOfSchedules: a scheduled agent's file waiting for approval is
// drift too, and so is a folder that does not exist not.
func TestDriftOfSchedules(t *testing.T) {
	dir := t.TempDir()
	m := Manifest{ID: "personal", Name: "Personal"}
	if err := os.WriteFile(filepath.Join(dir, File), []byte(`{"id":"personal","name":"Personal"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if d := DriftOf(dir, m); d.Any() {
		t.Fatalf("as approved: %+v", d)
	}
	if err := os.MkdirAll(filepath.Join(dir, ScheduleDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ScheduleFile("digest")), []byte(`{"cron":"0 8 * * *","prompt":"sum up"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := DriftOf(dir, m)
	if !d.Any() || d.BrainJSON || d.KeepsMoreHome() || !slices.Equal(d.Schedules, []string{"digest"}) || !strings.Contains(d.Describe(), "scheduled agents waiting for approval in .brain/schedules: digest") {
		t.Errorf("with a new agent: %+v %q", d, d.Describe())
	}
	if d := DriftOf(filepath.Join(dir, "gone"), m); d.Any() {
		t.Errorf("a missing folder: %+v", d)
	}
}
