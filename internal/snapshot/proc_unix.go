//go:build unix

package snapshot

import (
	"os"
	"syscall"
)

// terminate asks p to stop: SIGTERM, on which restic and litestream finish
// what they write and remove their locks before they exit.
func terminate(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
