package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// The bridge runs a few helpers outside the sandbox: tmux for the activity
// viewer, rclone for backups, and the Plaud CLI with the node it needs.
// The service's PATH starts with the installer's own, which may name
// folders outside HOME that brains can write to (on Apple Silicon
// /opt/homebrew is yours, and /opt/homebrew/opt/go/bin comes before
// /usr/bin there). The sandbox only keeps brains from planting programs in
// PATH folders under HOME, so a tmux or rclone put in such a folder would
// run outside the sandbox the next time the bridge looked it up on PATH.
// helperPath leaves those folders out.

// helperDirs are the folders outside HOME that the bridge still looks up
// its helpers in when PATH names them: the system's own, which brains
// cannot write to, and then the usual prefixes of /usr/local, Homebrew and
// MacPorts, where these helpers get installed. A brain may be able to
// write to the latter and replace a helper there in place; only a sandbox
// that denies writes by default can stop that. A variable so tests can
// change it.
var helperDirs = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin", "/usr/local/bin", "/opt/homebrew/bin", "/opt/local/bin"}

// helperPath is the bridge's PATH with only the folders it looks up its
// helpers in, in PATH's order: those under HOME, where the sandbox keeps
// brains from planting the programs the bridge runs by name (see
// plantablePrograms, which judges "under HOME" the same way), and those in
// helperDirs. Every other folder is left out, however early PATH names it.
func helperPath() string {
	var homes []string
	// A HOME of "" or / would put every folder under it.
	if home := config.HomeDir(); home != "" && filepath.Dir(home) != home {
		homes = []string{home, fsx.Resolve(home)}
	}
	var out []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		// exec.LookPath skips relative entries.
		if !filepath.IsAbs(dir) {
			continue
		}
		dir = filepath.Clean(dir)
		if slices.Contains(out, dir) {
			continue
		}
		// helperDirs by name only: a folder that merely resolves to one
		// may be a symlink a brain can point elsewhere.
		if slices.Contains(helperDirs, dir) || withinAny(dir, homes) || withinAny(fsx.Resolve(dir), homes) {
			out = append(out, dir)
		}
	}
	return strings.Join(out, string(filepath.ListSeparator))
}
