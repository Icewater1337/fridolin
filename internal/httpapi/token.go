package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// Token is a secret kept in a file of the data dir: the
// dashboard's, or the one scripts send to /send and /ingest.
type Token struct {
	path string
	v    atomic.Value // string
}

// minTokenLen is the shortest token LoadToken accepts; Rotate writes 48
// hex digits.
const minTokenLen = 32

// LoadDashboardToken reads <dataDir>/dashboard.token, creating it on first
// run (see LoadToken).
func LoadDashboardToken(dataDir string) (*Token, error) {
	return LoadToken(filepath.Join(dataDir, "dashboard.token"))
}

// LoadToken reads the token in path, creating the file when there is
// none. Only a missing file makes a new token: one that cannot be read or
// holds something too short to be a token is an error, since quietly
// replacing it would lock out every dashboard and script that uses it, and
// hide whatever broke the file.
func LoadToken(path string) (*Token, error) {
	t := &Token{path: path}
	data, err := os.ReadFile(t.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := t.Rotate(); err != nil {
			return nil, fmt.Errorf("creating token %s: %w", path, err)
		}
		return t, nil
	case err != nil:
		return nil, fmt.Errorf("reading token: %w", err)
	}
	tok := strings.TrimSpace(string(data))
	if len(tok) < minTokenLen || strings.ContainsAny(tok, " \t\r\n") {
		return nil, fmt.Errorf("%s is not a token (delete the file and the bridge makes a new one)", t.path)
	}
	t.v.Store(tok)
	return t, nil
}

// Value returns the token.
func (t *Token) Value() string { return t.v.Load().(string) }

// Rotate replaces the token and its file: whoever sends the old one is
// refused from then on. apiRotateToken also ends every dashboard session.
func (t *Token) Rotate() error {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	tok := hex.EncodeToString(buf)
	if err := fsx.WriteFileAtomic(t.path, []byte(tok+"\n"), 0o600, false); err != nil {
		return err
	}
	t.v.Store(tok)
	return nil
}

// Matches compares in constant time.
func (t *Token) Matches(s string) bool {
	return subtle.ConstantTimeCompare([]byte(s), []byte(t.Value())) == 1
}

// ReadToken reads the existing dashboard token without creating it.
func ReadToken(dataDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, "dashboard.token"))
	return strings.TrimSpace(string(data)), err
}
