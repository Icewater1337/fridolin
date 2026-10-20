//go:build unix

package app

import (
	"os"
	"syscall"
)

// ownedByOther reports whether path exists and belongs to a user other than
// the one the bridge runs as.
func ownedByOther(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	return ok && int(sys.Uid) != os.Geteuid()
}

// openToOthers returns the permission bits of dir and whether they give
// group or other users any access.
func openToOthers(dir string) (os.FileMode, bool) {
	st, err := os.Stat(dir)
	if err != nil {
		return 0, false
	}
	return st.Mode().Perm(), st.Mode().Perm()&0o077 != 0
}
