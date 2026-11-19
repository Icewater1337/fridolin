package runsnap

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
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

// open opens the object sum, checking that it still is what its name
// says as it is read (verifyingReader).
func (o objects) open(sum string) (io.ReadCloser, error) {
	if !hashRe.MatchString(sum) {
		return nil, fmt.Errorf("%q is not an object's name", sum)
	}
	f, err := os.Open(o.path(sum))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("the stored copy %s… is gone: %w", sum[:12], ErrObjectMissing)
	}
	if err != nil {
		return nil, err
	}
	return &verifyingReader{f: f, h: sha256.New(), want: sum}, nil
}

// ErrObjectMissing is a stored copy that is not in the store any more.
var ErrObjectMissing = errors.New("missing from the store")

// verifyingReader reads an object and fails at its end if its bytes do
// not hash to its name: a store damaged on disk restores nothing wrong.
type verifyingReader struct {
	f    *os.File
	h    hash.Hash
	want string
}

func (v *verifyingReader) Read(p []byte) (int, error) {
	n, err := v.f.Read(p)
	if n > 0 {
		_, _ = v.h.Write(p[:n])
	}
	if errors.Is(err, io.EOF) && hex.EncodeToString(v.h.Sum(nil)) != v.want {
		return n, fmt.Errorf("the stored copy %s… is damaged", v.want[:12])
	}
	return n, err
}

func (v *verifyingReader) Close() error { return v.f.Close() }

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
