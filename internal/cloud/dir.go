package cloud

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
)

// dirStaging is the folder inside a Dir's root where Put writes before it
// renames into place; List skips it.
const dirStaging = ".cloud-staging~"

// Dir is an ObjectStore on a local folder: the object under key "a/b" is
// the file Root/a/b. It is the fake for tests and dev mode, and behaves as
// the rclone store does: Put replaces atomically, Delete of a missing key
// succeeds, List of a missing prefix is empty.
type Dir struct {
	Root string
	// Fail, when set, is asked before each operation ("put", "get",
	// "stat", "list", "delete") and its error is returned instead — the
	// tests' way to make an upload or a check fail.
	Fail func(op, key string) error

	mu sync.Mutex // serialises Put's rename with Delete and List
}

// NewDir returns a Dir on root, creating the folder.
func NewDir(root string) (*Dir, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &Dir{Root: root}, nil
}

func (d *Dir) fail(op, key string) error {
	if d.Fail == nil {
		return nil
	}
	return d.Fail(op, key)
}

func (d *Dir) file(key string) string {
	return filepath.Join(d.Root, filepath.FromSlash(key))
}

// Put implements ObjectStore.
func (d *Dir) Put(ctx context.Context, key string, r io.Reader) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	if err := d.fail("put", key); err != nil {
		return err
	}
	staging := filepath.Join(d.Root, dirStaging)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(staging, "put-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := io.Copy(tmp, readerWithContext(ctx, r)); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("cloud: put %s: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	dst := d.file(key)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// Get implements ObjectStore.
func (d *Dir) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ValidKey(key); err != nil {
		return nil, err
	}
	if err := d.fail("get", key); err != nil {
		return nil, err
	}
	f, err := os.Open(d.file(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("cloud: get %s: %w", key, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{readerWithContext(ctx, f), f}, nil
}

// Stat implements ObjectStore.
func (d *Dir) Stat(_ context.Context, key string) (ObjectInfo, error) {
	if err := ValidKey(key); err != nil {
		return ObjectInfo{}, err
	}
	if err := d.fail("stat", key); err != nil {
		return ObjectInfo{}, err
	}
	fi, err := os.Stat(d.file(key))
	if errors.Is(err, fs.ErrNotExist) || (err == nil && !fi.Mode().IsRegular()) {
		return ObjectInfo{}, fmt.Errorf("cloud: stat %s: %w", key, ErrNotFound)
	}
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Key: key, Size: fi.Size(), ModTime: fi.ModTime()}, nil
}

// List implements ObjectStore.
func (d *Dir) List(_ context.Context, prefix string) ([]ObjectInfo, error) {
	if err := validPrefix(prefix); err != nil {
		return nil, err
	}
	if err := d.fail("list", prefix); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	base := d.Root
	if prefix != "" {
		base = d.file(prefix)
	}
	var out []ObjectInfo
	err := filepath.WalkDir(base, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if e.IsDir() {
			if e.Name() == dirStaging && filepath.Dir(p) == d.Root {
				return filepath.SkipDir
			}
			return nil
		}
		if !e.Type().IsRegular() {
			return nil
		}
		fi, err := e.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(d.Root, p)
		if err != nil {
			return err
		}
		out = append(out, ObjectInfo{Key: filepath.ToSlash(rel), Size: fi.Size(), ModTime: fi.ModTime()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Delete implements ObjectStore.
func (d *Dir) Delete(_ context.Context, key string) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	if err := d.fail("delete", key); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	err := os.Remove(d.file(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	// Drop folders the delete left empty, as an object store has none.
	for dir := path.Dir(key); dir != "."; dir = path.Dir(dir) {
		if os.Remove(d.file(dir)) != nil {
			break
		}
	}
	return nil
}

// readerWithContext stops a copy once ctx is done.
func readerWithContext(ctx context.Context, r io.Reader) io.Reader {
	return ctxReader{ctx, r}
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
