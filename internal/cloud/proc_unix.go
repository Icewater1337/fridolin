//go:build unix

package cloud

import (
	"os"
	"syscall"
)

// terminateProcess asks p to stop with SIGTERM, on which rclone finishes
// cleanly.
func terminateProcess(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
