package main

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// errSymlink is why the bridge refuses a file that a brain could have
// replaced with a symlink.
var errSymlink = errors.New("it is a symlink, which the bridge does not follow")

// openInFolder opens the file name, relative to the brain folder root, for
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
func openInFolder(root *os.Root, name string, flag int, perm os.FileMode) (*os.File, error) {
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
			return nil, errSymlink
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
