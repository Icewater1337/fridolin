package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

// The registry check sees derived values while the update owns the lock, and
// can refuse the whole transaction before either manifest or config is written.
func TestStoreChecksBrainsBeforeWriting(t *testing.T) {
	home := t.TempDir()
	t.Parallel()
	path := filepath.Join(home, "config.json")
	before := []byte(`{"log_level":"info"}`)
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "brain")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	refused := errors.New("brain set refused")
	var cs *Store
	called := 0
	check := func(old, next *Settings) error {
		called++
		if cs.mu.TryLock() {
			cs.mu.Unlock()
			t.Error("brain-set validation ran outside the commit lock")
		}
		if old.LogLevel != "info" || next.LogLevel != "debug" || next.DataDir != filepath.Join(home, "chosen") {
			t.Errorf("validation received the wrong effective snapshots: %q, %q, %q", old.LogLevel, next.LogLevel, next.DataDir)
		}
		if _, err := os.Stat(filepath.Join(dir, manifest.File)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("manifest written before validation: %v", err)
		}
		return refused
	}
	var err error
	cs, err = Open(path, Options{Paths: Paths{Home: home, DefaultDataDir: filepath.Join(home, DataDirName)}, GOOS: "linux", CheckBrains: check})
	if err != nil {
		t.Fatal(err)
	}
	_, err = cs.UpdateTx("", func(c *File, tx *Tx) error {
		c.LogLevel, c.DataDir = "debug", "~/chosen"
		return tx.CreateManifest(dir, &manifest.Manifest{Version: manifest.Version, ID: "main", Name: "Main"})
	})
	if !errors.Is(err, refused) || called != 1 {
		t.Fatalf("UpdateTx: %v, checks %d; want refusal from one check", err, called)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != string(before) {
		t.Errorf("refused transaction changed config: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dir, manifest.File)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("refused transaction wrote manifest: %v", err)
	}
	if cs.Get().LogLevel != "info" {
		t.Error("refused transaction published a snapshot")
	}
	if _, err := cs.Update("", func(c *File) error { c.Comment = "after refusal"; return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRefusesUncheckedBrainUpdates(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	path := filepath.Join(t.TempDir(), "config.json")
	cs, err := Open(path, Options{Paths: Paths{Home: home, DefaultDataDir: filepath.Join(home, DataDirName)}, GOOS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = cs.UpdateTx("", func(c *File, tx *Tx) error {
		c.Comment = "refused"
		tx.CheckBrains()
		return nil
	})
	if err == nil || err.Error() != "config: brain-set validation is not configured" {
		t.Fatalf("unchecked update: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unchecked update wrote config: %v", err)
	}
}
