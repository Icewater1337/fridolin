//go:build !windows

package nodeagent

import (
	"os"
	"syscall"
)

// reexec replaces this process with exe and the same arguments: an agent
// run from a terminal restarts in place after an update.
func reexec(exe string) error {
	return syscall.Exec(exe, append([]string{exe}, os.Args[1:]...), os.Environ())
}
