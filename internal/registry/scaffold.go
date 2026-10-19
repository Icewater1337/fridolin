package registry

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

// ScaffoldTx retains the directory handles and identities of its creations
// until registration commits. A failed add must not follow a replaced parent
// or remove a file that appeared in place of one of its templates.
type ScaffoldTx struct {
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

func newScaffold(dir string) (*ScaffoldTx, error) {
	s := &ScaffoldTx{}
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
func (s *ScaffoldTx) mkdirAll(root *os.Root, rel string) error {
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

func (s *ScaffoldTx) write(rel, content string) error {
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

// Rollback removes only unchanged entries this transaction created.
func (s *ScaffoldTx) Rollback() {
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

// Close releases the retained directory handles after commit or rollback.
func (s *ScaffoldTx) Close() {
	for i := len(s.roots) - 1; i >= 0; i-- {
		s.roots[i].Close()
	}
}

// Created returns the template paths this transaction created, in name order.
func (s *ScaffoldTx) Created() []string { return s.created }

// Scaffold prepares a folder for a new brain: the folder itself, a
// starter prompt file, the memory layout and brain.json. It never replaces
// a file that already exists, so adopting a folder with content is safe. It
// returns the files it created.
func Scaffold(dir string, m *manifest.Manifest, templates Templates) ([]string, error) {
	created, err := scaffoldFolder(dir, m, templates)
	if err != nil {
		return created, err
	}
	if err := manifest.Write(dir, m, false); err == nil {
		created = append(created, manifest.File)
	} else if !errors.Is(err, manifest.ErrExists) {
		return created, err
	}
	sort.Strings(created)
	return created, nil
}

// scaffoldFolder is Scaffold without brain.json, which the dashboard
// writes with the update that adds the brain (in the registration transaction).
func scaffoldFolder(dir string, m *manifest.Manifest, templates Templates) ([]string, error) {
	s, err := ScaffoldFolder(dir, m, templates)
	// ScaffoldFolder always returns its transaction, including partial failure.
	s.Close()
	return s.created, err
}

// ScaffoldFolder writes templates while retaining ownership for a later rollback.
// The transaction is always non-nil; the caller closes it even on failure.
func ScaffoldFolder(dir string, m *manifest.Manifest, templates Templates) (*ScaffoldTx, error) {
	s, err := newScaffold(dir)
	if err != nil {
		return s, err
	}

	prompt := renderTemplate(templates.Prompt, m)
	files := []struct{ rel, content string }{
		{m.Normalized().PromptFile, prompt},
		{templates.IdentityMapPath, identityMapTemplate},
		{"memories/README.md", "# Memories\n\nOne markdown file per topic. The brain reads and updates these between conversations.\n"},
		{"skills/README.md", "# Skills\n\nReusable how-tos and scripts this brain has written for itself.\n"},
	}
	for _, sk := range templates.Skills {
		files = append(files, struct{ rel, content string }{"skills/" + sk.Name, renderTemplate(sk.Content, m)})
	}
	for _, f := range files {
		if err := s.write(f.rel, f.content); err != nil {
			return s, err
		}
	}
	sort.Strings(s.created)
	return s, nil
}

const identityMapTemplate = `# WhatsApp identity map

Who is who in this brain's chats. The bridge reads this table to show names
instead of raw numbers in the chat history it gives the brain. Write numbers
with the country code, any way you like: +1 555 123-4567 works.

| WhatsApp Number | Person | Notes |
| --------------- | ------ | ----- |
`

// renderTemplate fills a brain template, the starter prompt or an example
// skill, for the new brain m. The templates name the bridge's commands by
// $FRIDOLIN_BIN and its helpers by $FRIDOLIN_TOOLS_DIR, which every run
// sets, and never by a path: a brain folder keeps
// working when it or the bridge moves.
func renderTemplate(tpl string, m *manifest.Manifest) string {
	return strings.NewReplacer("{{BRAIN_NAME}}", m.Name).Replace(tpl)
}
