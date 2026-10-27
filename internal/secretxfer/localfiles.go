package secretxfer

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// The local files: what stays on the machines (internal/gitident), in
// the data dir's LocalDir. A move carries them beside the credentials
// folder's files, named with LocalPrefix.

func localPath(dataDir, name string) string {
	return filepath.Join(dataDir, filepath.FromSlash(LocalDir), strings.TrimPrefix(name, LocalPrefix))
}

// ReadLocalFiles reads the local files of dataDir, named with
// LocalPrefix: regular files only (never a link). A missing folder holds
// none.
func ReadLocalFiles(dataDir string) (map[string][]byte, error) {
	dir := filepath.Join(dataDir, filepath.FromSlash(LocalDir))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string][]byte{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, e := range entries {
		name := LocalPrefix + e.Name()
		if !LocalFileName(name) || !e.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out[name] = data
	}
	return out, checkFiles(out)
}

// StoreLocalFiles writes the local files into dataDir's LocalDir (made
// 0700), each 0600. Unlike the credentials folder's, one there is
// replaced: the moving core's is the newer, and an import keeps core/,
// so the old one would stay in use. Stored were not there; replaced were
// and differed.
func StoreLocalFiles(dataDir string, files map[string][]byte) (stored, replaced []string, err error) {
	if err := checkFiles(files); err != nil {
		return nil, nil, err
	}
	names := make([]string, 0, len(files))
	for n := range files {
		if !LocalFileName(n) {
			return nil, nil, fmt.Errorf("%s is no local file", n)
		}
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := localPath(dataDir, n)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return stored, replaced, err
		}
		if st, err := os.Lstat(p); err == nil && !st.Mode().IsRegular() {
			return stored, replaced, fmt.Errorf("%s is not a regular file: left as it is", p)
		}
		old, rerr := os.ReadFile(p)
		had := rerr == nil
		if had && bytes.Equal(old, files[n]) {
			continue
		}
		if err := writeFileAtomic(p, files[n]); err != nil {
			return stored, replaced, err
		}
		if had {
			replaced = append(replaced, n)
		} else {
			stored = append(stored, n)
		}
	}
	return slices.Clip(stored), slices.Clip(replaced), nil
}

// RemoveLocalFiles removes the named local files of dataDir, regular
// files only, and returns those it removed as LocalDir/name; one not
// there is skipped.
func RemoveLocalFiles(dataDir string, names []string) ([]string, error) {
	removed := []string{}
	for _, n := range names {
		if !LocalFileName(n) {
			return removed, fmt.Errorf("%q is no local file a move carries", n)
		}
		p := localPath(dataDir, n)
		st, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return removed, err
		}
		if !st.Mode().IsRegular() {
			continue
		}
		if err := os.Remove(p); err != nil {
			return removed, err
		}
		removed = append(removed, LocalDir+"/"+strings.TrimPrefix(n, LocalPrefix))
	}
	return removed, nil
}

// writeFileAtomic writes data to path, 0600, through a temporary file in
// the same folder.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
