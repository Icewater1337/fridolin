package cli

import (
	"os"
	"path/filepath"
	"testing"

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
	_, folders := exportFolders(cfg, cfgPath)
	got := map[string]string{}
	for _, f := range folders {
		got[f.ID] = f.Configured
	}
	if got["personal"] != "~/Personal" || got["other"] != filepath.ToSlash(other) {
		t.Errorf("configured paths = %v", got)
	}
}
