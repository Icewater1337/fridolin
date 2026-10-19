package fsx

import (
	"errors"
	"io/fs"
	"log"
	"os"
)

// RestrictToOwner takes group and other permissions away from an existing
// file or directory, and logs when it had to. It never adds permissions,
// keeps the setuid, setgid and sticky bits, and leaves a path that does not
// exist alone.
func RestrictToOwner(path string) error {
	st, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	old := st.Mode()
	if old.Perm()&0o077 == 0 {
		return nil
	}
	// Chmod sets the special bits too, so pass them on or they are lost:
	// a setgid folder would stop handing its group to new files.
	mode := old & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky) &^ 0o077
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	log.Printf("%s was open to other users (mode %04o); changed it to %04o", path, uint32(old.Perm()), uint32(mode.Perm()))
	return nil
}
