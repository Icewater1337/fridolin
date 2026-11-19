package runsnap

import (
	"fmt"
	"io/fs"
	"os"
	"path"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// File is one file as a walk saw it: its size, modification time (Unix
// nanoseconds), permission bits and the hash of its bytes, under which
// the store keeps them.
type File struct {
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
	Mode  uint32 `json:"mode"`
	Hash  string `json:"hash"`
}

// same reports whether f and g have the same bytes and mode.
func (f File) same(g File) bool { return f.Hash == g.Hash && f.Mode == g.Mode }

// unchanged reports whether f, seen by an earlier walk, still describes
// what a walk now sees as g (no hash yet): the same size and
// modification time, so its hash need not be taken again.
func (f File) unchanged(g File) bool { return f.Hash != "" && f.Size == g.Size && f.MTime == g.MTime }

// state is the files of one root by their slash-separated path.
type state map[string]File

// Limits bound one walk of a run's folders together.
type Limits struct {
	// MaxFiles and MaxBytes are the most files and bytes the folders
	// may hold; a run over either keeps no record of its files.
	MaxFiles int
	MaxBytes int64
}

// TooLargeError is a walk over its Limits.
type TooLargeError struct{ Reason string }

func (e *TooLargeError) Error() string { return e.Reason }

// counter adds up what the walks of one run's roots saw.
type counter struct {
	files int
	bytes int64
	lim   Limits
}

func (c *counter) add(size int64) error {
	c.files++
	c.bytes += size
	switch {
	case c.lim.MaxFiles > 0 && c.files > c.lim.MaxFiles:
		return &TooLargeError{Reason: fmt.Sprintf("its folders hold more than %d files", c.lim.MaxFiles)}
	case c.lim.MaxBytes > 0 && c.bytes > c.lim.MaxBytes:
		return &TooLargeError{Reason: fmt.Sprintf("its folders hold more than %d MB", c.lim.MaxBytes>>20)}
	}
	return nil
}

// scan lists the regular files of root that are neither skipped nor
// ignored, without their hashes. With learn, it reads the .gitignore of
// each folder it enters into ig; without, it keeps to the rules ig
// holds. Symbolic links, devices and the like are not looked at: the
// bridge follows no link a brain may have planted, and restores files
// only.
func scan(root *os.Root, skip func(rel string, dir bool) bool, ig ignoreRules, learn bool, c *counter) (state, error) {
	out := state{}
	if learn {
		readIgnore(root, "", ig)
	}
	err := fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == "." {
				return err
			}
			// A folder that cannot be read is left out, as a vanished file.
			return nil
		}
		if p == "." {
			return nil
		}
		if d.IsDir() {
			if (skip != nil && skip(p, true)) || ig.ignored(p, true) {
				return fs.SkipDir
			}
			if learn {
				readIgnore(root, p, ig)
			}
			return nil
		}
		if !d.Type().IsRegular() || (skip != nil && skip(p, false)) || ig.ignored(p, false) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if err := c.add(info.Size()); err != nil {
			return err
		}
		out[p] = File{Size: info.Size(), MTime: info.ModTime().UnixNano(), Mode: uint32(info.Mode().Perm())}
		return nil
	})
	return out, err
}

// readIgnore reads the .gitignore of the folder dir of root into ig.
func readIgnore(root *os.Root, dir string, ig ignoreRules) {
	f, err := fsx.OpenInFolder(root, path.Join(dir, gitignoreName), os.O_RDONLY, 0)
	if err != nil {
		return
	}
	defer f.Close()
	data := make([]byte, maxIgnoreBytes)
	n, _ := f.Read(data)
	if rules := parseIgnore(data[:n]); len(rules) > 0 {
		ig[dir] = rules
	}
}

// hashAll fills in the hashes of cur: from known where a file's size and
// time are what they were and its bytes are in the store, otherwise by
// reading it into the store. A file that cannot be opened (it went away,
// became a symlink or something other than a file, or may not be read)
// is left out, as if scan had not seen it; only a store that cannot be
// written fails the walk.
func hashAll(root *os.Root, cur, known state, obj objects) error {
	for rel, f := range cur {
		if k, ok := known[rel]; ok && k.unchanged(f) && obj.has(k.Hash) {
			f.Hash = k.Hash
			cur[rel] = f
			continue
		}
		// Opened as the bridge opens a brain's files (fsx.OpenInFolder):
		// through no symlink, a regular file only, without blocking.
		file, err := fsx.OpenInFolder(root, rel, os.O_RDONLY, 0)
		if err != nil {
			delete(cur, rel)
			continue
		}
		sum, _, err := obj.put(file)
		file.Close()
		if err != nil {
			return fmt.Errorf("storing %s: %w", rel, err)
		}
		f.Hash = sum
		cur[rel] = f
	}
	return nil
}
