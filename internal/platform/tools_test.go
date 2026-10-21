package platform

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLookTool(t *testing.T) {
	found := func(p string) func(string) (string, error) {
		return func(string) (string, error) { return p, nil }
	}
	missing := func(string) (string, error) { return "", errors.New("not found") }
	abs := filepath.Join(t.TempDir(), "git")
	if p, err := lookTool("git", found(abs), "linux"); err != nil || p != abs {
		t.Errorf("in the PATH: %q %v", p, err)
	}
	if _, err := lookTool("../git", found(abs), "linux"); err == nil {
		t.Error("a path passed as a name")
	}
	if _, err := lookTool("git", found("git"), "windows"); err == nil {
		t.Error("a relative path passed")
	}
	if _, err := lookTool("no-such-tool-anywhere", missing, "linux"); err == nil {
		t.Error("a missing tool was found")
	}
	if _, err := os.Stat("/bin/sh"); err == nil {
		if p, err := lookTool("sh", missing, "linux"); err != nil || !filepath.IsAbs(p) {
			t.Errorf("outside the PATH: %q %v", p, err)
		}
	}
}

func TestEnvWith(t *testing.T) {
	got := envWith([]string{"A=1", "GIT_TERMINAL_PROMPT=1", "AB=2"}, "GIT_TERMINAL_PROMPT=0", "A=3")
	if want := []string{"AB=2", "GIT_TERMINAL_PROMPT=0", "A=3"}; !slices.Equal(got, want) {
		t.Errorf("%q, want %q", got, want)
	}
}

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for in, want := range map[string]string{"~": home, "~/repos": filepath.Join(home, "repos"), "/abs": "/abs", "~other/x": "~other/x", "rel": "rel"} {
		if got := ExpandHome(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
