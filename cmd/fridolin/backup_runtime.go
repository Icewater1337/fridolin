package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/sandbox"
)

// findRclone finds the rclone binary, given backup.rclone_path. The sandbox
// profile uses it too, to keep brains from replacing the binary (see
// bridgeProgramsOf).
//
// A configured path must work as it is: falling back would quietly run
// another rclone, maybe another version, than the one you named, and hide
// the typo. A name without a folder, and rclone itself when none is
// configured, is looked up only in the PATH folders helperPath keeps:
// rclone runs outside the sandbox. Only without a configured one does it
// look further, where launchd's PATH lacks Homebrew's folder and rclone's
// installer's.
func findRclone(p string) (string, error) {
	if p != "" {
		if !strings.Contains(p, "/") {
			if found := lookPathIn(p, helperPath()); found != "" {
				return found, nil
			}
			return "", fmt.Errorf("backup.rclone_path %s: %w", p, exec.ErrNotFound)
		}
		found, err := exec.LookPath(p)
		if err != nil {
			return "", fmt.Errorf("backup.rclone_path %s: %w", p, err)
		}
		return found, nil
	}
	if found := lookPathIn("rclone", helperPath()); found != "" {
		return found, nil
	}
	for _, c := range rcloneFallbacks() {
		// For an absolute path LookPath only checks that it is an
		// executable file: one that merely exists would fail to run.
		if found, err := exec.LookPath(c); err == nil {
			return found, nil
		}
	}
	return "", errRcloneMissing
}

// rcloneFallbacks are where findRclone looks when rclone is not in the
// folders of PATH it looks in first, a variable so tests can point it at
// their own files.
var rcloneFallbacks = func() []string {
	return []string{"/opt/homebrew/bin/rclone", "/usr/local/bin/rclone", filepath.Join(homeDir(), ".local/bin/rclone")}
}

// checkRestoreTarget reports why target, a folder by its real path, lies
// in what the sandbox keeps from brains ("is inside …", like
// checkBrainDir), or nil. The bridge writes the copy outside any sandbox,
// so where a brain folder may still live, inside ~/.ssh or a deny_paths
// entry, no restore needs to write. The folders of your own files are the
// exception, ~/Documents and the rest of sandboxUserFileFolders(): brains live
// there, and so may a restored one, as CheckBrainPath lets a new one.
func checkRestoreTarget(target string, cfg *config.Config, home string) error {
	var denied []sandbox.ProtectedPath
	for _, p := range sandboxPolicyOf(cfg).protected(home, ownLayout()) {
		if !slices.ContainsFunc(sandbox.UserFileFolders(), func(sub string) bool { return p.Path == filepath.Join(home, sub) }) {
			p.Inside = true
		}
		denied = append(denied, p)
	}
	for _, p := range cfg.Sandbox.DenyPaths {
		denied = append(denied, sandbox.ProtectedPath{Path: fsx.ExpandHome(p, home), Inside: true, What: "the deny_paths entry " + p})
	}
	if what, ok := protectedIn(target, denied); ok {
		return fmt.Errorf("%s, which the sandbox keeps from brains", what)
	}
	return nil
}
func rcloneInstallHint() string {
	switch {
	case fsx.Exists("/opt/homebrew/bin/brew") || fsx.Exists("/usr/local/bin/brew"):
		return "brew install rclone"
	case fsx.Exists("/usr/bin/apt-get"):
		return "sudo apt-get install rclone   (or: sudo -v ; curl https://rclone.org/install.sh | sudo bash)"
	default:
		return "See https://rclone.org/install/"
	}
}
