package site

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// emptyDir fails t when dir holds anything.
func emptyDir(t *testing.T, dir string) {
	t.Helper()
	if es, _ := os.ReadDir(dir); len(es) > 0 {
		t.Errorf("the bridge wrote into %s: %v", dir, es)
	}
}

func TestBuilderSetupWritesNothingThroughASymlink(t *testing.T) {
	for _, link := range []string{".fridolin-site", ".fridolin-site/plugin", ".fridolin-site/plugin/skills"} {
		t.Run(link, func(t *testing.T) {
			m, brainDir := newTestBuilderSite(t)
			out := t.TempDir()
			p := filepath.Join(brainDir, filepath.FromSlash(link))
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(out, p); err != nil {
				t.Fatal(err)
			}
			if err := m.ensureBuilder(context.Background(), "web"); err == nil {
				t.Error("the builder was set up through a symlink out of the brain's folder")
			}
			emptyDir(t, out)
		})
	}
}

func TestBuilderMCPConfigReplacesASymlink(t *testing.T) {
	m, brainDir := newTestBuilderSite(t)
	outside := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(outside, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(brainDir, ".fridolin-site")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "mcp.json")); err != nil {
		t.Fatal(err)
	}
	if err := m.writeMCPConfig("web"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(outside); string(data) != "mine" {
		t.Errorf("the file the link named became %q", data)
	}
	if fi, err := os.Lstat(filepath.Join(base, "mcp.json")); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("mcp.json is not a file of its own: %v %v", fi, err)
	}
}

func TestBuilderShellCopyStaysInTheFolder(t *testing.T) {
	m, brainDir := newTestBuilderSite(t)
	shell := t.TempDir()
	for name, data := range map[string]string{".fridolin-shell": "v1", "server/check.ts": "x", "node_modules/a/bin.js": "y"} {
		p := filepath.Join(shell, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(shell, "node_modules", ".bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../a/bin.js", filepath.Join(shell, "node_modules", ".bin", "a")); err != nil {
		t.Fatal(err)
	}
	if err := m.copyShell(shell, "web"); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(brainDir, ".fridolin-site", "shell")
	if data, err := os.ReadFile(filepath.Join(dst, "node_modules", ".bin", "a")); err != nil || string(data) != "y" {
		t.Errorf("the copied link reads %q, %v", data, err)
	}
	// The brain swaps server for a link out; the next copy refuses it.
	out := t.TempDir()
	if err := os.RemoveAll(filepath.Join(dst, "server")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(dst, "server")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dst, ".fridolin-shell")); err != nil {
		t.Fatal(err)
	}
	if err := m.copyShell(shell, "web"); err == nil {
		t.Error("the shell was copied through a symlink out of the brain's folder")
	}
	emptyDir(t, out)
}
