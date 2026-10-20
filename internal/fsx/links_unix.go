//go:build unix

package fsx

import (
	"io/fs"
	"syscall"
)

// LinkCount returns how many names the file at path has, which fi (from
// a Stat or Lstat of path) holds, and false when that is not known.
func LinkCount(_ string, fi fs.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Nlink), true
}
