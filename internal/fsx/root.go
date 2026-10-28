package fsx

import (
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// ErrSymlink is why the bridge refuses a file that a brain could have
// replaced with a symlink.
var ErrSymlink = errors.New("it is a symlink, which the bridge does not follow")

// OpenInFolder opens the file name, relative to the brain folder root, for
// the bridge, which works on it outside the sandbox while the brain
// controls what is there: a brain may replace any file in its folder with
// a symlink to ~/.ssh, to another brain's history or to a file the sandbox
// denies it, and have the bridge read that into its prompt or append to
// it. os.Root refuses, at every step of the open, a path that leads out of
// the folder, through a symlinked .claude folder say. It refuses a symlink
// at the file itself too, even one that stays inside, and anything but a
// regular file, and the file it opens must be the one it looked at, so
// that nothing swapped in between gets through. The open does not block,
// so a FIFO cannot hang the bridge either. With os.O_CREATE it creates
// the file when there is none, never through a symlink.
func OpenInFolder(root *os.Root, name string, flag int, perm os.FileMode) (*os.File, error) {
	for tries := 0; ; tries++ {
		fi, err := root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) && flag&os.O_CREATE != 0 {
			// With O_EXCL, os.Root follows no symlink, not even one
			// planted since the Lstat; that makes the create fail.
			f, err := root.OpenFile(name, flag|os.O_EXCL|syscall.O_NONBLOCK, perm)
			if errors.Is(err, fs.ErrExist) && tries < 2 {
				continue // something appeared there meanwhile: look again
			}
			return f, err
		}
		if err != nil {
			return nil, err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil, ErrSymlink
		}
		if !fi.Mode().IsRegular() {
			return nil, errors.New("not a regular file")
		}
		f, err := root.OpenFile(name, flag&^os.O_CREATE|syscall.O_NONBLOCK, perm)
		if err != nil {
			return nil, err
		}
		if st, err := f.Stat(); err != nil || !os.SameFile(fi, st) {
			f.Close()
			if err == nil {
				err = errors.New("it was replaced while the bridge opened it")
			}
			return nil, err
		}
		return f, nil
	}
}

// WriteInRoot writes what r gives to name, relative to root, at once: into
// a new temporary file beside it that is then renamed over it, making its
// folder when it is not there. Every step goes through root, so a symlink a
// brain planted on the way cannot lead the write out of the folder, and
// the rename replaces a symlink at name itself instead of writing through it.
func WriteInRoot(root *os.Root, name string, r io.Reader, perm os.FileMode) error {
	dir := filepath.Dir(name)
	if err := root.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+filepath.Base(name)+"."+rand.Text()+".tmp")
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	_, err = io.Copy(f, r)
	if err == nil {
		err = f.Chmod(perm)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return root.Rename(tmp, name)
}
