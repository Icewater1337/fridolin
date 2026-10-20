package platform

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNodeStateDir(t *testing.T) {
	var env map[string]string
	getenv := func(k string) string { return env[k] }
	home := func() (string, error) { return filepath.FromSlash("/home/you"), nil }
	noHome := func() (string, error) { return "", errors.New("no home") }
	for _, c := range []struct {
		goos string
		env  map[string]string
		home func() (string, error)
		want string
	}{
		{"linux", nil, home, "/home/you/.local/state/fridolin-node"},
		{"linux", map[string]string{"XDG_STATE_HOME": filepath.FromSlash("/state")}, home, "/state/fridolin-node"},
		{"linux", map[string]string{"XDG_STATE_HOME": "relative"}, home, "/home/you/.local/state/fridolin-node"},
		{"darwin", nil, home, "/home/you/Library/Application Support/fridolin-node"},
		{"windows", map[string]string{"LOCALAPPDATA": filepath.FromSlash("/appdata")}, noHome, "/appdata/fridolin-node"},
		{"windows", nil, home, "/home/you/AppData/Local/fridolin-node"},
	} {
		// Paths follow the host's rules (filepath), so a case for the
		// other family of systems cannot hold here.
		if (c.goos == "windows") != (runtime.GOOS == "windows") {
			continue
		}
		env = c.env
		got, err := nodeStateDir(c.goos, getenv, c.home)
		if err != nil {
			t.Errorf("%s %v: %v", c.goos, c.env, err)
			continue
		}
		if got != filepath.FromSlash(c.want) {
			t.Errorf("%s %v: %s, want %s", c.goos, c.env, got, filepath.FromSlash(c.want))
		}
	}
	env = nil
	for _, goos := range []string{"linux", "darwin", "windows"} {
		if _, err := nodeStateDir(goos, getenv, noHome); err == nil {
			t.Errorf("%s without a home: no error", goos)
		}
	}
}

func TestMemory(t *testing.T) {
	total, avail, err := Memory()
	if err != nil {
		t.Skipf("memory not known here: %v", err)
	}
	if total == 0 || avail > total {
		t.Errorf("total %d, available %d", total, avail)
	}
}
