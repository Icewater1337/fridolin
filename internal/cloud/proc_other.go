//go:build !unix

package cloud

import "os"

// terminateProcess stops p. Windows has no signal to ask a process to
// stop with, so it is killed.
func terminateProcess(p *os.Process) error {
	return p.Kill()
}
