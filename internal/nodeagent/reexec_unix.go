//go:build !windows

package nodeagent

import "syscall"

// reexec replaces this process with exe and the same arguments: an agent
// run from a terminal restarts in place after an update.
func reexec(exe string, args, env []string) error {
	return syscall.Exec(exe, append([]string{exe}, args...), env)
}
