package cloud

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Mirror keeps Store a copy of the files in Dir: what is new or changed
// is uploaded, what is gone is deleted. It is how Litestream's replicas
// reach B2 with two layers (docs/VAULT.md, "Encryption"): Litestream
// writes them to a local folder, and Mirror uploads that through a sealed
// store over rclone crypt. It lists Store once, at its first Sync, and
// then remembers what it put there, so a sync that uploads costs no
// listing (uploads are free on B2); deletes are gathered and sent in one
// batch at most every DeleteEvery, each batch one listing.
type Mirror struct {
	Dir   string
	Store ObjectStore
	// DeleteEvery spaces the batches of deletes; 0 deletes at every Sync.
	DeleteEvery time.Duration
	// Skip leaves files out by their key (Litestream's temporary files).
	Skip func(key string) bool

	mu         sync.Mutex
	remote     map[string]int64 // key → size in Store
	listed     bool
	lastDelete time.Time
	last       MirrorStatus
}

// MirrorStatus is what the last Sync did.
type MirrorStatus struct {
	At       time.Time `json:"at,omitzero"`
	Uploaded int       `json:"uploaded"`
	Deleted  int       `json:"deleted"`
	Files    int       `json:"files"`
	Error    string    `json:"error,omitempty"`
}

// Status returns what the last Sync did.
func (m *Mirror) Status() MirrorStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last
}

// local lists Dir's files by key with their sizes.
func (m *Mirror) local() (map[string]int64, error) {
	out := map[string]int64{}
	err := filepath.WalkDir(m.Dir, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(m.Dir, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if strings.HasSuffix(key, ".tmp") || (m.Skip != nil && m.Skip(key)) {
			return nil
		}
		info, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil // gone since the listing
		}
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			out[key] = info.Size()
		}
		return nil
	})
	return out, err
}

// Sync uploads what changed and, when due, deletes what is gone.
func (m *Mirror) Sync(ctx context.Context, now time.Time) (MirrorStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, err := m.sync(ctx, now)
	st.At = now
	if err != nil {
		st.Error = err.Error()
	}
	m.last = st
	return st, err
}

func (m *Mirror) sync(ctx context.Context, now time.Time) (MirrorStatus, error) {
	var st MirrorStatus
	if !m.listed {
		objs, err := m.Store.List(ctx, "")
		if err != nil {
			return st, err
		}
		m.remote = make(map[string]int64, len(objs))
		for _, o := range objs {
			m.remote[o.Key] = o.Size
		}
		m.listed = true
	}
	files, err := m.local()
	if err != nil {
		return st, err
	}
	st.Files = len(files)
	for key, size := range files {
		if have, ok := m.remote[key]; ok && have == size {
			continue
		}
		if err := ctx.Err(); err != nil {
			return st, err
		}
		f, err := os.Open(filepath.Join(m.Dir, filepath.FromSlash(key)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return st, err
		}
		err = m.Store.Put(ctx, key, f)
		_ = f.Close()
		if err != nil {
			return st, err
		}
		m.remote[key] = size
		st.Uploaded++
	}
	if m.DeleteEvery > 0 && now.Sub(m.lastDelete) < m.DeleteEvery {
		return st, nil
	}
	var gone []string
	for key := range m.remote {
		if _, ok := files[key]; !ok {
			gone = append(gone, key)
		}
	}
	if len(gone) > 0 {
		if err := DeleteKeys(ctx, m.Store, gone); err != nil {
			return st, err
		}
		for _, k := range gone {
			delete(m.remote, k)
		}
		st.Deleted = len(gone)
	}
	m.lastDelete = now
	return st, nil
}

// Fetch copies every object of store into dir, which must not exist yet:
// the way back for a restore.
func Fetch(ctx context.Context, store ObjectStore, dir string) (int, error) {
	if _, err := os.Lstat(dir); err == nil {
		return 0, errors.New("cloud: " + dir + " exists already")
	}
	objs, err := store.List(ctx, "")
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	for _, o := range objs {
		if err := ValidKey(o.Key); err != nil {
			return 0, err
		}
		p := filepath.Join(dir, filepath.FromSlash(o.Key))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return 0, err
		}
		if err := fetchOne(ctx, store, o.Key, p); err != nil {
			return 0, err
		}
	}
	return len(objs), nil
}

func fetchOne(ctx context.Context, store ObjectStore, key, p string) error {
	rc, err := store.Get(ctx, key)
	if err != nil {
		return err
	}
	defer rc.Close()
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.ReadFrom(readerWithContext(ctx, rc))
	return errors.Join(err, f.Close())
}
