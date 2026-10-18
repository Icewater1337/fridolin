package main

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
)

// scaffoldTx retains the directory handles and identities of its creations
// until registration commits. A failed add must not follow a replaced parent
// or remove a file that appeared in place of one of its templates.
type scaffoldTx struct {
	root    *os.Root
	roots   []*os.Root
	entries []scaffoldEntry
	created []string
}

type scaffoldEntry struct {
	parent *os.Root
	name   string
	info   fs.FileInfo
}

func newScaffold(dir string) (*scaffoldTx, error) {
	s := &scaffoldTx{}
	if dir == "" {
		return s, &os.PathError{Op: "mkdir", Path: dir, Err: fs.ErrInvalid}
	}
	// The selected folder may itself be a symlink. Pin that selection before
	// using confined relative operations for its contents; existing parents
	// and the selected folder are not ours to remove during rollback.
	if root, err := os.OpenRoot(dir); err == nil {
		s.root, s.roots = root, []*os.Root{root}
		return s, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return s, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return s, err
	}
	parent, rel := filepath.Dir(abs), filepath.Base(abs)
	var root *os.Root
	for {
		root, err = os.OpenRoot(parent)
		if !errors.Is(err, fs.ErrNotExist) || filepath.Dir(parent) == parent {
			break
		}
		rel = filepath.Join(filepath.Base(parent), rel)
		parent = filepath.Dir(parent)
	}
	if err != nil {
		return s, err
	}
	s.roots = append(s.roots, root)
	if err := s.mkdirAll(root, rel); err != nil {
		return s, err
	}
	s.root, err = root.OpenRoot(rel)
	if err == nil {
		s.roots = append(s.roots, s.root)
	}
	return s, err
}

// mkdirAll remembers only successful mkdir calls, never directories already
// present. Each retained parent keeps rollback anchored if a directory moves.
func (s *scaffoldTx) mkdirAll(root *os.Root, rel string) error {
	if rel == "." {
		return nil
	}
	if !filepath.IsLocal(rel) {
		return &os.PathError{Op: "mkdir", Path: rel, Err: fs.ErrInvalid}
	}
	if err := s.mkdirAll(root, filepath.Dir(rel)); err != nil {
		return err
	}
	parent, err := root.OpenRoot(filepath.Dir(rel))
	if err != nil {
		return err
	}
	name := filepath.Base(rel)
	if err := parent.Mkdir(name, 0o700); err != nil {
		parent.Close()
		if errors.Is(err, fs.ErrExist) {
			return nil // OpenRoot at the next step verifies it is a directory.
		}
		return err
	}
	s.roots = append(s.roots, parent)
	info, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	s.entries = append(s.entries, scaffoldEntry{parent: parent, name: name, info: info})
	return nil
}

func (s *scaffoldTx) write(rel, content string) error {
	if !filepath.IsLocal(rel) {
		return &os.PathError{Op: "create", Path: rel, Err: fs.ErrInvalid}
	}
	if err := s.mkdirAll(s.root, filepath.Dir(rel)); err != nil {
		return err
	}
	parent, err := s.root.OpenRoot(filepath.Dir(rel))
	if err != nil {
		return err
	}
	name := filepath.Base(rel)
	f, err := parent.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		parent.Close()
		if errors.Is(err, fs.ErrExist) {
			return nil
		}
		return err
	}
	s.roots = append(s.roots, parent)
	_, writeErr := f.WriteString(content)
	info, statErr := f.Stat()
	closeErr := f.Close()
	if statErr == nil {
		s.entries = append(s.entries, scaffoldEntry{parent: parent, name: name, info: info})
	}
	if err := errors.Join(writeErr, statErr, closeErr); err != nil {
		return err
	}
	s.created = append(s.created, rel)
	return nil
}

func (s *scaffoldTx) rollback() {
	for i := len(s.entries) - 1; i >= 0; i-- {
		e := s.entries[i]
		info, err := e.parent.Lstat(e.name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err == nil && (!os.SameFile(e.info, info) || e.info.Mode() != info.Mode() ||
			!info.IsDir() && (e.info.Size() != info.Size() || !e.info.ModTime().Equal(info.ModTime()))) {
			continue // A replacement or edited file belongs to whoever changed it.
		}
		if err == nil {
			err = e.parent.Remove(e.name)
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("removing %s after the brain was not added: %v", filepath.Join(e.parent.Name(), e.name), err)
		}
	}
}

func (s *scaffoldTx) close() {
	for i := len(s.roots) - 1; i >= 0; i-- {
		s.roots[i].Close()
	}
}
