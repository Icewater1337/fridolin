package cloud

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Icewater1337/fridolin/internal/seal"
)

// Sealed is an ObjectStore that seals every object (internal/seal,
// XChaCha20-Poly1305 under Key) before it reaches Inner, and opens it on
// the way back: the inner layer of a store whose outer layer is Inner's
// own encryption (an rclone crypt remote), with a key of its own
// (docs/VAULT.md, "Encryption"). Stat and List give the plaintext's
// sizes, so a caller that compares a cold object's size with its file's
// sees no difference. An object that is not sealed, or sealed under
// another key, fails to open: Get returns seal.ErrNotSealed or
// seal.ErrWrongKey, and a damaged one fails on Read.
type Sealed struct {
	Inner ObjectStore
	Key   []byte
}

// NewSealed returns a Sealed store over inner with the key in text form
// (seal.ParseKey).
func NewSealed(inner ObjectStore, key string) (*Sealed, error) {
	k, err := seal.ParseKey(key)
	if err != nil {
		return nil, err
	}
	return &Sealed{Inner: inner, Key: k}, nil
}

// Put implements ObjectStore.
func (s *Sealed) Put(ctx context.Context, key string, r io.Reader) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	pr, pw := io.Pipe()
	go func() {
		w, err := seal.Encrypt(pw, s.Key)
		if err == nil {
			_, err = io.Copy(w, r)
		}
		if err == nil {
			err = w.Close()
		}
		pw.CloseWithError(err)
	}()
	err := s.Inner.Put(ctx, key, pr)
	pr.CloseWithError(errors.New("cloud: sealed upload ended"))
	return err
}

// Get implements ObjectStore.
func (s *Sealed) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, err := s.Inner.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	r, err := seal.Decrypt(rc, s.Key)
	if err != nil {
		_ = rc.Close()
		return nil, fmt.Errorf("cloud: opening %s: %w", key, err)
	}
	return struct {
		io.Reader
		io.Closer
	}{r, rc}, nil
}

// plain turns a sealed object's info into the plaintext's.
func plain(info ObjectInfo) (ObjectInfo, error) {
	n, err := seal.PlainSize(info.Size)
	if err != nil {
		return info, fmt.Errorf("cloud: %s has %d bytes, which no sealed object has: %w", info.Key, info.Size, err)
	}
	info.Size = n
	return info, nil
}

// Stat implements ObjectStore.
func (s *Sealed) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	info, err := s.Inner.Stat(ctx, key)
	if err != nil {
		return info, err
	}
	return plain(info)
}

// List implements ObjectStore. An object whose size no sealed object has
// is listed with its stored size less the header, so that a size check
// against the plaintext fails for it rather than the whole listing.
func (s *Sealed) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	list, err := s.Inner.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if p, err := plain(list[i]); err == nil {
			list[i] = p
		} else {
			list[i].Size = -1
		}
	}
	return list, nil
}

// Delete implements ObjectStore.
func (s *Sealed) Delete(ctx context.Context, key string) error { return s.Inner.Delete(ctx, key) }

// DeleteKeys implements BatchDeleter when Inner does.
func (s *Sealed) DeleteKeys(ctx context.Context, keys []string) error {
	return DeleteKeys(ctx, s.Inner, keys)
}

// BatchDeleter deletes many objects at once: rclone lists the folder once
// instead of looking each object up (a Class B transaction on B2) before
// deleting it.
type BatchDeleter interface {
	DeleteKeys(ctx context.Context, keys []string) error
}

// DeleteKeys deletes keys from st, in one batch when st can.
func DeleteKeys(ctx context.Context, st ObjectStore, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	if b, ok := st.(BatchDeleter); ok {
		return b.DeleteKeys(ctx, keys)
	}
	for _, k := range keys {
		if err := st.Delete(ctx, k); err != nil {
			return err
		}
	}
	return nil
}
