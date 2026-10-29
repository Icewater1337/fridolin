package plaud

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func loginHome(t *testing.T, files ...string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".plaud"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(home, ".plaud", f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// A CLI that keeps its login in plaud-secrets, not tokens.json, still has one.
func TestLoginFilesFindsPlaudSecrets(t *testing.T) {
	home := loginHome(t, "plaud-secrets", "device_id")
	got := loginFilesIn(home)
	if want := []string{filepath.Join(home, ".plaud", "plaud-secrets")}; !slices.Equal(got, want) {
		t.Errorf("login files = %v, want %v", got, want)
	}
	if len(loginFilesIn(loginHome(t, "device_id"))) != 0 {
		t.Error("device_id counted as a login")
	}
}

func TestCheckPlaudLoginRefusesLinkedPlaudSecrets(t *testing.T) {
	home := loginHome(t)
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, ".plaud", "plaud-secrets")); err != nil {
		t.Skip("no symlinks:", err)
	}
	if err := checkPlaudLogin(home); err == nil {
		t.Error("a symlinked plaud-secrets was accepted")
	}
}

func TestRemoveLoginRemovesEveryLoginFile(t *testing.T) {
	home := loginHome(t, "tokens.json", "plaud-secrets", "device_id")
	removeLogin(home)
	if left := loginFilesIn(home); len(left) != 0 {
		t.Errorf("left: %v", left)
	}
	if _, err := os.Stat(filepath.Join(home, ".plaud", "device_id")); err != nil {
		t.Errorf("device_id went too: %v", err)
	}
}
