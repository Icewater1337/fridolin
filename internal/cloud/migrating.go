package cloud

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"sort"
)

// Migrating is a store on its way from Old to New: what is written goes
// to New, and what New does not have yet is read from Old, until
// everything is copied over and Old is retired (fridolin vault
// migrate-encryption, docs/VAULT.md "Encryption"). New wins where both
// hold a key.
type Migrating struct {
	New, Old ObjectStore
}

// Put implements ObjectStore: into New only.
func (m *Migrating) Put(ctx context.Context, key string, r io.Reader) error {
	return m.New.Put(ctx, key, r)
}

// Get implements ObjectStore.
func (m *Migrating) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, err := m.New.Get(ctx, key)
	if errors.Is(err, fs.ErrNotExist) {
		return m.Old.Get(ctx, key)
	}
	return rc, err
}

// Stat implements ObjectStore.
func (m *Migrating) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	info, err := m.New.Stat(ctx, key)
	if errors.Is(err, fs.ErrNotExist) {
		return m.Old.Stat(ctx, key)
	}
	return info, err
}

// List implements ObjectStore: both, New's entry where both have a key.
func (m *Migrating) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	nw, err := m.New.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	old, err := m.Old.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(nw))
	for _, o := range nw {
		seen[o.Key] = true
	}
	for _, o := range old {
		if !seen[o.Key] {
			nw = append(nw, o)
		}
	}
	sort.Slice(nw, func(i, j int) bool { return nw[i].Key < nw[j].Key })
	return nw, nil
}

// Delete implements ObjectStore: from both.
func (m *Migrating) Delete(ctx context.Context, key string) error {
	return errors.Join(ignoreNotFound(m.New.Delete(ctx, key)), ignoreNotFound(m.Old.Delete(ctx, key)))
}

// DeleteKeys implements BatchDeleter: from both.
func (m *Migrating) DeleteKeys(ctx context.Context, keys []string) error {
	return errors.Join(DeleteKeys(ctx, m.New, keys), DeleteKeys(ctx, m.Old, keys))
}

func ignoreNotFound(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
