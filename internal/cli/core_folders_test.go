package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/Icewater1337/fridolin/internal/app"
)

// TestExportFoldersKeepPathsAsTyped (#182): the folder a move takes along
// keeps its path as config.json has it, ~/Personal, not the expanded one,
// so moved_from and the brain's path after the way back are what the
// owner typed.
func TestExportFoldersKeepPathsAsTyped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "Personal"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "Personal", "brain.json"), []byte(`{"id":"personal","name":"Personal"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(home, "elsewhere", "Other")
	cfgPath := filepath.Join(home, "config.json")
	cfgJSON := `{"data_dir": "` + filepath.ToSlash(filepath.Join(home, "data")) + `", "brain_dirs": [
		{"path": "~/Personal", "approved": {"id": "personal", "name": "Personal"}},
		{"path": "` + filepath.ToSlash(other) + `", "approved": {"id": "other", "name": "Other"}}]}`
	if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := app.LoadExistingConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	_, folders, _ := exportFolders(cfg, cfgPath, false)
	got := map[string]string{}
	for _, f := range folders {
		got[f.ID] = f.Configured
	}
	if got["personal"] != "~/Personal" || got["other"] != filepath.ToSlash(other) {
		t.Errorf("configured paths = %v", got)
	}
}

// driftFixture is a home with brain personal, whose brain.json on disk
// has backup.exclude disk while config.json approved approved; it
// returns a runner of fridolin core with the export's keys and a store.
func driftFixture(t *testing.T, disk, approved string) (run func(args ...string) (int, string, string), store string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	makeFiles(t, home, "Personal/notes/work/plan.md", "Personal/notes/diary.md", "data/dashboard.token")
	brain := func(exclude string) string {
		return `{"id":"personal","name":"Personal","backup":{"enabled":true,"exclude":` + exclude + `}}`
	}
	if err := os.WriteFile(filepath.Join(home, "Personal", "brain.json"), []byte(brain(disk)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := writeConfig(t, `{"data_dir": "`+filepath.ToSlash(filepath.Join(home, "data"))+`", "brain_dirs": [
		{"path": "~/Personal", "approved": `+brain(approved)+`}]}`)
	id, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	store = t.TempDir()
	return func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		env := coreEnv{environ: []string{"FRIDOLIN_AGE_IDENTITY_PQ=" + id.String(), "FRIDOLIN_EXPORT_KEY=" + testLayerKey}, stdout: &stdout, stderr: &stderr}
		code := runCore(append(args, "--store", store, "--config", cfg), env)
		return code, stdout.String(), stderr.String()
	}, store
}

// TestExportRefusesAWiderUnapprovedExclude (#189): brain.json on disk
// keeps notes/work/ home and the approved manifest does not. The export
// says so and refuses, until the owner approves it or passes
// --use-approved, which takes the folder along by the approved manifest.
func TestExportRefusesAWiderUnapprovedExclude(t *testing.T) {
	run, store := driftFixture(t, `["notes/work/"]`, `[]`)
	code, _, errOut := run("export")
	for _, want := range []string{"warning: brain personal", "excludes notes/work/ (backup.exclude), which the approved one does not", "nothing was exported", "approve it in the dashboard", "--use-approved"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("export without --use-approved lacks %q:\n%s", want, errOut)
		}
	}
	if code != 1 {
		t.Errorf("export without --use-approved: exit %d", code)
	}
	if entries, _ := os.ReadDir(store); len(entries) > 0 {
		t.Errorf("the refused export wrote %v", entries)
	}
	if code, _, errOut := run("export", "--drift"); code != 1 || !strings.Contains(errOut, "the export would refuse") {
		t.Errorf("export --drift: exit %d: %s", code, errOut)
	}
	// The list says what the export takes once it goes ahead.
	if code, out, errOut := run("export", "--list-folders"); code != 0 || strings.Contains(out, "notes/work/plan.md") || !strings.Contains(out, "personal/notes/diary.md") || !strings.Contains(errOut, "warning: brain personal") {
		t.Errorf("--list-folders: exit %d\n%s\n%s", code, out, errOut)
	}
	if code, out, errOut := run("export", "--list-folders", "--use-approved"); code != 0 || !strings.Contains(out, "personal/notes/work/plan.md") || !strings.Contains(errOut, "With --use-approved") {
		t.Errorf("--list-folders --use-approved: exit %d\n%s\n%s", code, out, errOut)
	}
	if code, out, errOut := run("export", "--use-approved"); code != 0 || !strings.Contains(out, "core/exports/") || !strings.Contains(errOut, "With --use-approved the export follows the approved manifest") {
		t.Errorf("export --use-approved: exit %d\n%s\n%s", code, out, errOut)
	}
}

// TestExportWarnsAboutANarrowerUnapprovedExclude (#189): brain.json on
// disk no longer excludes notes/work/; the approved manifest still does,
// and the export follows it, saying so.
func TestExportWarnsAboutANarrowerUnapprovedExclude(t *testing.T) {
	run, _ := driftFixture(t, `[]`, `["notes/work/"]`)
	code, out, errOut := run("export")
	if code != 0 || !strings.Contains(out, "core/exports/") {
		t.Fatalf("export: exit %d\n%s\n%s", code, out, errOut)
	}
	for _, want := range []string{"warning: brain personal", "no longer excludes notes/work/ (backup.exclude), which the approved one does", "The export follows the approved manifest"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("export lacks %q:\n%s", want, errOut)
		}
	}
	if code, out, _ := run("export", "--list-folders"); code != 0 || strings.Contains(out, "notes/work/plan.md") {
		t.Errorf("--list-folders took notes/work along: exit %d\n%s", code, out)
	}
}

// TestExportWithoutDrift says nothing about drift.
func TestExportWithoutDrift(t *testing.T) {
	run, _ := driftFixture(t, `["notes/work/"]`, `["notes/work/"]`)
	if code, out, errOut := run("export"); code != 0 || strings.Contains(errOut, "warning:") {
		t.Errorf("export: exit %d\n%s\n%s", code, out, errOut)
	}
	if code, out, errOut := run("export", "--drift"); code != 0 || !strings.Contains(out, "Every brain folder is as you approved it.") {
		t.Errorf("export --drift: exit %d\n%s\n%s", code, out, errOut)
	}
}

// TestCoreSendPassesTheCoresDriftOn: core send asks the running core
// (core export --drift) and shows its warnings before the transfer.
func TestCoreSendPassesTheCoresDriftOn(t *testing.T) {
	d, ran := hostFixture(t, mapSecrets{})
	d.run = func(_ context.Context, name string, args, _ []string, stdout, _ io.Writer) error {
		*ran = append(*ran, ranCmd{name: name, args: args})
		fmt.Fprintln(stdout, "warning: brain personal (/Users/you/Personal): brain.json on disk differs")
		return errors.New("exit status 1")
	}
	var stderr bytes.Buffer
	sendDriftCheck(context.Background(), coreEnv{stderr: &stderr}, d)
	if len(*ran) != 1 || strings.Join((*ran)[0].args, " ") != "exec fridolin-core fridolin core export --drift" {
		t.Errorf("ran %v", *ran)
	}
	if s := stderr.String(); !strings.Contains(s, "Before the move, the core says:\nwarning: brain personal") || !strings.Contains(s, "--use-approved") {
		t.Errorf("stderr:\n%s", s)
	}
	d.run = func(context.Context, string, []string, []string, io.Writer, io.Writer) error {
		return errors.New("no such container")
	}
	stderr.Reset()
	sendDriftCheck(context.Background(), coreEnv{stderr: &stderr}, d)
	if stderr.Len() > 0 {
		t.Errorf("without a running core: %s", stderr.String())
	}
}
