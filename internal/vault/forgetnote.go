package vault

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// ForgetNoteName is the note in the data dir's core/ folder that lists the
// folders the vault forgets (Forget) when it next starts: the import of a
// core move writes it for each brain folder it brings home from the data
// dir, while no vault runs (docs/VAULT.md, "Forgetting a folder").
const ForgetNoteName = "vault-forget.json"

// ForgetNote is the content of the note.
type ForgetNote struct {
	Folders []ForgetFolder `json:"folders"`
}

// ForgetFolder is one folder to forget: a vault path, relative to the hot
// root (brains-folders/Personal), why, and since when.
type ForgetFolder struct {
	Prefix string    `json:"prefix"`
	Why    string    `json:"why,omitempty"`
	At     time.Time `json:"at"`
}

// ReadForgetNote reads the note at file; a missing one is empty.
func ReadForgetNote(file string) (ForgetNote, error) {
	var n ForgetNote
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return n, nil
	}
	if err != nil {
		return n, err
	}
	if err := json.Unmarshal(data, &n); err != nil {
		return n, fmt.Errorf("vault: %s: %w", file, err)
	}
	return n, nil
}

// AddToForgetNote adds folders to the note at file, each prefix once.
func AddToForgetNote(file string, folders ...ForgetFolder) error {
	n, err := ReadForgetNote(file)
	if err != nil {
		return err
	}
	for _, f := range folders {
		if _, err := cleanStrict(f.Prefix); err != nil {
			return err
		}
		if !slices.ContainsFunc(n.Folders, func(g ForgetFolder) bool { return g.Prefix == f.Prefix }) {
			n.Folders = append(n.Folders, f)
		}
	}
	return WriteForgetNote(file, n)
}

// WriteForgetNote writes the note at file, through a temporary file
// beside it; an empty note removes it.
func WriteForgetNote(file string, n ForgetNote) error {
	if len(n.Folders) == 0 {
		err := os.Remove(file)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	data, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
