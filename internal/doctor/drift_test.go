package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// TestBrainDrift (#189): doctor names the brain whose brain.json on disk
// differs from the approved manifest, and says that core export refuses
// while it keeps more home.
func TestBrainDrift(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "Personal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	approved := &manifest.Manifest{ID: "personal", Name: "Personal", Backup: manifest.Backup{Enabled: true, Exclude: []string{}}}
	cfg := &config.Settings{}
	cfg.BrainDirs = []config.BrainRef{{Path: dir, Approved: approved}, {Path: filepath.Join(home, "New")}}
	env := &Env{Home: home, Config: cfg}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "brain.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"id":"personal","name":"Personal","backup":{"enabled":true,"exclude":[]}}`)
	if r := checkBrainDrift(context.Background(), env); r.Status != OK {
		t.Errorf("as approved: %+v", r)
	}
	write(`{"id":"personal","name":"Personal","backup":{"enabled":true,"exclude":["notes/work/"]}}`)
	r := checkBrainDrift(context.Background(), env)
	if r.Status != Warn || !strings.Contains(r.Detail, "personal: brain.json on disk differs") || !strings.Contains(r.Detail, "notes/work/") || !strings.Contains(r.Fix, "--use-approved") {
		t.Errorf("wider exclude: %+v", r)
	}
}
