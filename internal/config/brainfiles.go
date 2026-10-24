package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// stagedFile is a file of a brain's folder other than brain.json that a
// transaction writes, or removes: a scheduled agent's
// .brain/schedules/<name>.json.
type stagedFile struct {
	dir, rel string
	// data is what it writes; nil removes the file.
	data []byte
	root *os.Root

	// written says it is written or removed; existed and prev say
	// whether it was there before and what it held.
	written bool
	existed bool
	prev    []byte
}

// WriteBrainFile stages data as the file rel, a slash-separated path
// inside the brain folder dir. A later write or removal of the same file
// replaces this one.
func (tx *Tx) WriteBrainFile(dir, rel string, data []byte) error {
	return tx.stageFile(dir, rel, bytes.Clone(data))
}

// RemoveBrainFile stages the removal of rel in dir; one that is not
// there is fine.
func (tx *Tx) RemoveBrainFile(dir, rel string) error {
	return tx.stageFile(dir, rel, nil)
}

func (tx *Tx) stageFile(dir, rel string, data []byte) error {
	if !filepath.IsLocal(filepath.FromSlash(rel)) || rel == manifest.File {
		return fmt.Errorf("config: %q is no file a transaction may write in a brain folder", rel)
	}
	tx.checkBrains = true
	dir = filepath.Clean(dir)
	for _, f := range tx.files {
		if f.dir == dir && f.rel == rel {
			f.data = data
			return nil
		}
	}
	tx.files = append(tx.files, &stagedFile{dir: dir, rel: rel, data: data})
	return nil
}

// writeFiles writes and removes the staged files, in order, after
// reading what each held so rollbackFiles can put it back. A file that
// is no regular file, a symlink a brain put there say, is not replaced.
func (tx *Tx) writeFiles() error {
	for _, f := range tx.files {
		root, err := os.OpenRoot(f.dir)
		if err != nil {
			return err
		}
		f.root = root
		prev, err := fsx.ReadBrainFileRoot(root, f.rel)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			f.existed, f.prev = false, nil
		case err != nil:
			return fmt.Errorf("%s: %w", filepath.Join(f.dir, f.rel), err)
		default:
			f.existed, f.prev = true, prev
		}
		if f.data == nil {
			if f.existed {
				if err := root.Remove(f.rel); err != nil {
					return err
				}
			}
		} else if err := manifest.WriteInFolder(root, f.rel, f.data); err != nil {
			return err
		}
		f.written = true
	}
	return nil
}

// rollbackFiles puts back what the staged files held, the last first,
// and logs what it cannot.
func (tx *Tx) rollbackFiles() {
	for i := len(tx.files) - 1; i >= 0; i-- {
		f := tx.files[i]
		if !f.written {
			continue
		}
		f.written = false
		var err error
		if f.existed {
			err = manifest.WriteInFolder(f.root, f.rel, f.prev)
		} else if f.data != nil {
			err = f.root.Remove(f.rel)
			if err == nil {
				// The folder the write made, when it is empty now.
				_ = f.root.Remove(path.Dir(f.rel))
			}
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("config: putting back %s after the update failed: %v", filepath.Join(f.dir, f.rel), err)
		}
	}
}

func (tx *Tx) closeFiles() {
	for _, f := range tx.files {
		if f.root != nil {
			f.root.Close()
			f.root = nil
		}
	}
}
