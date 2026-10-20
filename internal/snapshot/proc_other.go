//go:build !unix

package snapshot

import "os"

// terminate stops p. Windows has no signal a process can be asked to stop
// with, so it is killed; exec's WaitDelay then has nothing left to do.
func terminate(p *os.Process) error {
	return p.Kill()
}
