package hostsecrets

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Icewater1337/fridolin/internal/seal"
)

// Sealed keeps each secret in a file of mode 0600 in Dir, sealed under
// Key (internal/seal), with its name inside, so that a file renamed to
// another secret's name does not open. It is a node in a box's store
// (docs/NODES.md, "core-host"): the box has no keychain, and its volume
// holds the secrets encrypted at rest under a key the box derives from
// its device key.
type Sealed struct {
	Dir string
	Key []byte
}

func (s Sealed) path(name string) (string, error) {
	if err := checkName(name); err != nil {
		return "", err
	}
	if len(s.Key) != seal.KeySize {
		return "", errors.New("the sealed secret store has no key")
	}
	return filepath.Join(s.Dir, name+".sealed"), nil
}

func (s Sealed) Get(name string) (string, error) {
	p, err := s.path(name)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	if err != nil {
		return "", err
	}
	plain, err := seal.Open(data, s.Key)
	if err != nil {
		return "", fmt.Errorf("%s does not open with this machine's key: %w", name, err)
	}
	got, value, ok := bytes.Cut(plain, []byte{0})
	if !ok || string(got) != name {
		clear(plain)
		return "", fmt.Errorf("%s holds another secret", p)
	}
	v := string(value)
	clear(plain)
	return v, nil
}

func (s Sealed) Has(name string) (bool, error) {
	_, err := s.Get(name)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (s Sealed) Set(name, value string) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(s.Dir, 0o700); err != nil {
		return err
	}
	plain := append([]byte(name+"\x00"), value...)
	sealed, err := seal.Seal(plain, s.Key)
	clear(plain)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, "."+name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(sealed); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

func (s Sealed) Delete(name string) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
