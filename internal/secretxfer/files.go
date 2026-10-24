package secretxfer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// fileName are the secret files a move carries from the core's
// credentials folder (sandbox.credentials_dir, under ~): the
// Google OAuth web client, the dashboard's users, and each brain's
// calendar client and token. Nothing else in that folder goes (#187).
var fileName = regexp.MustCompile(`^(google_oauth_web_credentials|dashboard-users|google_calendar_(credentials|token)_[a-z0-9][a-z0-9_-]{0,63})\.json$`)

const (
	maxFiles     = 8
	maxFileBytes = 16 << 10
)

// filesDir is the credentials folder as config names it under the core's
// home: "~/" and plain path components, never "..".
var filesDir = regexp.MustCompile(`^~(/[A-Za-z0-9._-]{1,64}){1,4}$`)

// CheckFilesDir reports what is wrong with a credentials folder a move
// names for its secret files.
func CheckFilesDir(dir string) error {
	if !filesDir.MatchString(dir) {
		return fmt.Errorf("the secret files' folder %q is not a folder under ~", dir)
	}
	for _, c := range strings.Split(dir, "/")[1:] {
		if c == "." || c == ".." {
			return fmt.Errorf("the secret files' folder %q is not a folder under ~", dir)
		}
	}
	return nil
}

// FileName reports whether name is a secret file a move carries.
func FileName(name string) bool { return fileName.MatchString(name) }

func checkFiles(files map[string][]byte) error {
	if len(files) > maxFiles {
		return fmt.Errorf("the bundle holds %d secret files, more than %d", len(files), maxFiles)
	}
	for name, data := range files {
		if !FileName(name) {
			return fmt.Errorf("the bundle names the file %q, which a move does not carry", name)
		}
		if len(data) == 0 || len(data) > maxFileBytes {
			return fmt.Errorf("the secret file %s is empty or larger than %d bytes", name, maxFileBytes)
		}
	}
	return nil
}

// ReadFiles reads the secret files directly in dir: regular files only
// (never a link), by FileNames. A missing dir holds none.
func ReadFiles(dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string][]byte{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, e := range entries {
		if !FileName(e.Name()) || !e.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out[e.Name()] = data
	}
	return out, checkFiles(out)
}

// StoreFiles writes files into dir (made 0700), each 0600, and never
// replaces one already there: those are kept, so the folder of a machine
// that ran the core before keeps what it has. Stored were written.
func StoreFiles(dir string, files map[string][]byte) (stored, kept []string, err error) {
	if err := checkFiles(files); err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, nil, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := filepath.Join(dir, n)
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			kept = append(kept, n)
			continue
		}
		if err != nil {
			return stored, kept, err
		}
		_, werr := f.Write(files[n])
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = os.Remove(p)
			return stored, kept, werr
		}
		stored = append(stored, n)
	}
	return slices.Clip(stored), slices.Clip(kept), nil
}
