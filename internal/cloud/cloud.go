// Package cloud is the vault's door to object storage. Code talks to the
// ObjectStore interface; Rclone implements it by running the rclone binary
// against a remote configured only through environment variables (never a
// config file holding secrets), and Dir implements it on a local folder for
// tests and dev mode.
package cloud

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ErrNotFound is returned (wrapped) by Get and Stat for a key the store
// does not hold.
var ErrNotFound = errors.New("cloud: object not found")

// ObjectInfo describes one stored object. Key is the object's key as passed
// to Put: a slash-separated relative path.
type ObjectInfo struct {
	Key     string
	Size    int64
	ModTime time.Time
}

// ObjectStore is a flat key → bytes store. Keys are slash-separated
// relative paths (ValidKey); a crypt remote encrypts them on the wire, so
// the vault keeps keys equal to the hot tier's relative paths.
type ObjectStore interface {
	// Put stores r's bytes under key, replacing any object there.
	Put(ctx context.Context, key string, r io.Reader) error
	// Get opens the object under key. The caller closes the reader; a
	// transfer that fails part way reports the error from Read or Close.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Stat describes the object under key, or returns ErrNotFound.
	Stat(ctx context.Context, key string) (ObjectInfo, error)
	// List returns every object whose key is below the directory prefix
	// ("" for all), with full keys, sorted by key. A prefix that holds
	// nothing is an empty list, not an error.
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)
	// Delete removes the object under key. Deleting a missing key is not
	// an error.
	Delete(ctx context.Context, key string) error
}

// ValidKey reports whether key is a clean slash-separated relative path:
// not empty, no leading or trailing slash, no empty, "." or ".." segment
// and no backslash or NUL.
func ValidKey(key string) error {
	if key == "" {
		return errors.New("cloud: empty key")
	}
	if strings.ContainsAny(key, "\\\x00") {
		return fmt.Errorf("cloud: key %q has a backslash or NUL", key)
	}
	for seg := range strings.SplitSeq(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("cloud: key %q is not a clean relative path", key)
		}
	}
	return nil
}

// validPrefix accepts "" (everything) or a valid key used as a directory.
func validPrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	return ValidKey(prefix)
}
