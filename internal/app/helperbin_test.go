package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// shadowingPrograms are the programs the bridge runs outside the sandbox,
// or that those run by name: what a brain would plant in a PATH folder to
// have it run there.
var shadowingPrograms = []string{"tmux", "rclone", "plaud", "node", "osascript", "open", "sh", "tail"}

// plantPrograms puts an executable stub of each of shadowingPrograms in
// dir. A stub that runs leaves <name>.ran next to itself.
func plantPrograms(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range shadowingPrograms {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n: > '"+p+".ran'\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

// plantedOutsideHome gives the test a HOME of its own and puts a folder
// outside it first on PATH, holding a stub of each of shadowingPrograms,
// as a brain could plant them in /opt/homebrew/opt/go/bin. It returns that
// folder and the HOME.
func plantedOutsideHome(t *testing.T) (planted, home string) {
	t.Helper()
	home, planted = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	plantPrograms(t, planted)
	t.Setenv("PATH", planted+string(filepath.ListSeparator)+os.Getenv("PATH"))
	return planted, home
}

// forgetTmux makes findTmux look tmux up again, now and after the test, so
// that neither this test nor a later one gets what another one found.
func forgetTmux(t *testing.T) {
	t.Helper()
	old := tmuxPath
	tmuxOnce, tmuxPath = sync.Once{}, ""
	t.Cleanup(func() { tmuxOnce, tmuxPath = sync.Once{}, old })
}

// TestHelperPath: of the bridge's PATH, helpers are looked up only in the
// folders under HOME and in helperDirs, in PATH's order. A folder outside
// both is left out wherever PATH has it, and so is one that is merely a
// symlink to a folder of helperDirs; one that resolves into HOME counts as
// under HOME, as for the sandbox.
func TestHelperPath(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
	outside := fsx.Canonical(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	intoHome, toUsrBin := filepath.Join(outside, "into-home"), filepath.Join(outside, "to-usr-bin")
	for link, target := range map[string]string{intoHome: filepath.Join(home, "bin"), toUsrBin: "/usr/bin"} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	join := func(dirs ...string) string { return strings.Join(dirs, string(filepath.ListSeparator)) }
	cases := []struct {
		name, path, want string
		dirs             []string // helperDirs, nil for the default
	}{
		{name: "the service's PATH",
			path: join("/opt/homebrew/opt/go/bin", "/opt/homebrew/bin", home+"/.local/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin", "/opt/local/bin"),
			want: join("/opt/homebrew/bin", home+"/.local/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin", "/opt/local/bin")},
		{name: "a folder outside HOME first", path: join(outside, home+"/bin", "/usr/bin"), want: join(home+"/bin", "/usr/bin")},
		{name: "HOME's order is PATH's", path: join("/usr/bin", home+"/bin", "/bin"), want: join("/usr/bin", home+"/bin", "/bin")},
		{name: "relative, empty and repeated entries", path: join("bin", "", ".", "/usr/bin/", "/usr/bin", home+"/bin/../bin"), want: join("/usr/bin", home+"/bin")},
		{name: "the folder above HOME", path: join(filepath.Dir(home), "/usr/bin"), want: "/usr/bin"},
		{name: "a symlink into HOME", path: join(intoHome, "/usr/bin"), want: join(intoHome, "/usr/bin")},
		{name: "a symlink to a system folder", path: join(toUsrBin, "/bin"), want: "/bin"},
		{name: "helperDirs decides outside HOME", path: join("/usr/bin", outside, "/bin"), dirs: []string{outside}, want: outside},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", home)
			t.Setenv("PATH", tc.path)
			if tc.dirs != nil {
				old := helperDirs
				helperDirs = tc.dirs
				t.Cleanup(func() { helperDirs = old })
			}
			if got := helperPath(); got != tc.want {
				t.Errorf("helperPath() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestHelpersAreNotFoundInFoldersBrainsCanWrite: the bridge runs tmux,
// rclone and the Plaud CLI outside the sandbox, so it never takes them from
// a PATH folder outside HOME and outside helperDirs, however early PATH
// names it: a brain could have planted them there. One in a PATH folder
// under HOME, where the sandbox keeps brains from planting it, is still
// found, ahead of the folder outside.
func TestHelpersAreNotFoundInFoldersBrainsCanWrite(t *testing.T) {
	cases := []struct {
		name string
		find func(t *testing.T) string
	}{
		{"tmux", func(t *testing.T) string { forgetTmux(t); return findTmux() }},
		{"rclone", func(t *testing.T) string { p, _ := findRclone(""); return p }},
		{"rclone named in rclone_path", func(t *testing.T) string { p, _ := findRclone("rclone"); return p }},
		{"plaud", func(t *testing.T) string { p, _ := findPlaudCLI(""); return p }},
	}
	for _, tc := range cases {
		program := strings.Fields(tc.name)[0]
		t.Run(tc.name, func(t *testing.T) {
			t.Run("planted outside HOME", func(t *testing.T) {
				planted, _ := plantedOutsideHome(t)
				if got := tc.find(t); strings.HasPrefix(got, planted+string(filepath.Separator)) {
					t.Errorf("found %s, the one planted on PATH", got)
				}
			})
			t.Run("in a PATH folder under HOME", func(t *testing.T) {
				planted, home := plantedOutsideHome(t)
				bin := filepath.Join(home, ".local", "bin")
				plantPrograms(t, bin)
				t.Setenv("PATH", bin+string(filepath.ListSeparator)+os.Getenv("PATH"))
				if got, want := tc.find(t), filepath.Join(bin, program); got != want {
					t.Errorf("found %q, want %s (the stub outside HOME is in %s)", got, want, planted)
				}
			})
		})
	}
}
