package runsnap

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// objects is the content store: each file's bytes once, under the hex
// SHA-256 of them, objects/<first two>/<rest>. Every run of every brain
// shares it, so a file no run changed is stored once however many runs
// saw it.
type objects struct{ dir string }

// hashRe is a hash as the store names objects by.
var hashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// path is where the object hash lies.
func (o objects) path(sum string) string {
	return filepath.Join(o.dir, sum[:2], sum[2:])
}

// has reports whether the object sum is in the store.
func (o objects) has(sum string) bool {
	if !hashRe.MatchString(sum) {
		return false
	}
	fi, err := os.Lstat(o.path(sum))
	return err == nil && fi.Mode().IsRegular()
}

// put stores what r gives and returns its hash and size. It is written
// to a temporary file first and renamed into place, so an object in the
// store is always whole; one that is there already stays as it was.
func (o objects) put(r io.Reader) (string, int64, error) {
	tmpDir := filepath.Join(o.dir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return "", 0, err
	}
	tmp, err := os.CreateTemp(tmpDir, "put-*")
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if o.has(sum) {
		return sum, n, nil
	}
	dst := o.path(sum)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", 0, err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", 0, err
	}
	return sum, n, nil
}

// walk calls fn with the hash, size and path of every object in the
// store.
func (o objects) walk(fn func(sum string, size int64, path string)) error {
	tops, err := os.ReadDir(o.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, top := range tops {
		if !top.IsDir() || len(top.Name()) != 2 {
			continue
		}
		dir := filepath.Join(o.dir, top.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			sum := top.Name() + e.Name()
			if !hashRe.MatchString(sum) || !e.Type().IsRegular() {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			fn(sum, fi.Size(), filepath.Join(dir, e.Name()))
		}
	}
	return nil
}
