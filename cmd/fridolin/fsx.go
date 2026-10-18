package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// File-system primitives that know nothing of the bridge. The home folder
// is always a parameter.

// writeFileAtomic writes data to a temp file in the same directory, syncs it
// and renames it over path, so a crash never leaves a half-written file. With
// backup, the previous version is kept as path.bak.
func writeFileAtomic(path string, data []byte, perm os.FileMode, backup bool) error {
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
			_ = os.WriteFile(path+".bak", prev, perm)
		}
	}
	return os.Rename(tmpName, path)
}

// expandHome rewrites a leading "~" to home, the user's home directory. It
// is the one ~ expansion: effective applies it to every path setting, so
// only code that works on the file layer, or on a path from a request,
// calls it itself.
func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

// canonicalDir cleans p and resolves its symlinks (on macOS /tmp is a
// symlink to /private/tmp, and the sandbox compares real paths). For an
// absolute path that does not exist yet, such as a folder the dashboard is
// about to create, it resolves the part that does: ~/link/new, with ~/link
// a symlink out of HOME, lies where the link points, and that is where
// MkdirAll would create it.
func canonicalDir(p string) string {
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if filepath.IsAbs(p) {
		return resolvePath(p)
	}
	return p
}

const maxSymlinks = 64

// resolvePath returns the real path of the absolute path p. Every symlink in
// the part that exists is resolved, including a dangling one (it resolves to
// its target), and the part that does not exist yet is kept as written, so
// the result still names a folder created there later. ".." is applied to
// the resolved path, as the kernel does. On a symlink loop p comes back as
// is.
func resolvePath(p string) string {
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

// isWithin reports whether p is dir or inside it.
func isWithin(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// tildePath writes paths under home, the user's home folder, as ~/…, so
// config.json and backups stay valid on a machine with a different user
// name.
func tildePath(p, home string) string {
	if home == "" {
		return p
	}
	home = canonicalDir(home)
	if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
		if rel == "." {
			return "~"
		}
		return "~/" + rel
	}
	return p
}

// fileExists reports whether p names a file or folder that exists (and can
// be looked at), following symlinks.
func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
