//go:build unix

package google

import (
	"os"
	"syscall"
)

// openNoFollow opens path for reading, refusing a symlink at its end with
// ELOOP. The open does not block, so a FIFO cannot hang the dashboard.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
