// Package fsx holds file-system primitives that know nothing of the
// bridge: an atomic write, ~ expansion and its reverse, symlink-resolved
// paths and containment. The home folder is always a parameter; nothing
// here reads the process's environment.
package fsx

import (
	"bytes"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// WriteFileAtomic writes data to a temp file in the same directory, syncs it
// and renames it over path, so a crash never leaves a half-written file. With
// backup, the previous version is kept as path.bak.
func WriteFileAtomic(path string, data []byte, perm os.FileMode, backup bool) error {
	return writeFileAtomic(path, data, perm, backup, false)
}

// CreateFileAtomic publishes a complete file only if path is still absent.
// Existing files and symlinks are never replaced; callers can use errors.Is
// with fs.ErrExist to recognize another creator winning the race.
func CreateFileAtomic(path string, data []byte, perm os.FileMode) error {
	return writeFileAtomic(path, data, perm, false, true)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode, backup, exclusive bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if backup {
		if prev, err := os.ReadFile(path); err == nil && !bytes.Equal(prev, data) {
			// Rename a complete backup instead of following a .bak symlink
			// or exposing a truncated previous version to another reader.
			if err := writeFileAtomic(path+".bak", prev, perm, false, false); err != nil {
				return fmt.Errorf("backing up %s: %w", path, err)
			}
		}
	}
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer parent.Close()
	if exclusive {
		err = os.Link(tmpName, path)
	} else {
		err = os.Rename(tmpName, path)
	}
	if err != nil {
		return err
	}
	// Publication has committed. Report a durability warning, not a failed
	// transaction: callers must not roll back companion manifests while the
	// new config is already visible at path. Windows cannot sync a
	// directory (Access is denied) and needs none: NTFS journals the rename.
	if runtime.GOOS == "windows" {
		return nil
	}
	if err := parent.Sync(); err != nil {
		log.Printf("warning: syncing directory after publishing %s: %v", path, err)
	}
	return nil
}

// ExpandHome rewrites a leading "~" to home, the user's home directory. It
// is the bridge's one ~ expansion: loading the config applies it to every
// path setting, so only code that works on the file layer, or on a path
// from a request, calls it itself.
func ExpandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

// Canonical cleans p and resolves its symlinks (on macOS /tmp is a
// symlink to /private/tmp, and the sandbox compares real paths). For an
// absolute path that does not exist yet, such as a folder the dashboard is
// about to create, it resolves the part that does: ~/link/new, with ~/link
// a symlink out of HOME, lies where the link points, and that is where
// MkdirAll would create it.
func Canonical(p string) string {
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if filepath.IsAbs(p) {
		return Resolve(p)
	}
	return p
}

const maxSymlinks = 64

// Resolve returns the real path of the absolute path p. Every symlink in
// the part that exists is resolved, including a dangling one (it resolves to
// its target), and the part that does not exist yet is kept as written, so
// the result still names a folder created there later. ".." is applied to
// the resolved path, as the kernel does. On a symlink loop p comes back as
// is.
func Resolve(p string) string {
	resolved := string(filepath.Separator)
	rest := strings.Split(p, string(filepath.Separator))
	links := 0
	for len(rest) > 0 {
		name := rest[0]
		rest = rest[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}
		next := filepath.Join(resolved, name)
		fi, err := os.Lstat(next)
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			// A real entry, or one that does not exist (yet): nothing to resolve.
			resolved = next
			continue
		}
		target, err := os.Readlink(next)
		if links++; err != nil || links > maxSymlinks {
			return filepath.Clean(p)
		}
		if !filepath.IsAbs(target) {
			target = resolved + string(filepath.Separator) + target
		}
		// Walk the target from the root: it may hold symlinks and ".." too.
		rest = append(strings.Split(target, string(filepath.Separator)), rest...)
		resolved = string(filepath.Separator)
	}
	return resolved
}

// Within reports whether p is dir or inside it.
func Within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// Tilde writes paths under home, the user's home folder, as ~/…, so
// config.json and backups stay valid on a machine with a different user
// name.
func Tilde(p, home string) string {
	if home == "" {
		return p
	}
	home = Canonical(home)
	if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
		if rel == "." {
			return "~"
		}
		return "~/" + rel
	}
	return p
}

// Exists reports whether p names a file or folder that exists (and can
// be looked at), following symlinks.
func Exists(p string) bool { _, err := os.Stat(p); return err == nil }
