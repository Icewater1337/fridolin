//go:build unix

package platform

import "syscall"

// Exec replaces the running process with argv0, run with argv and
// envv. It returns only when that fails.
func Exec(argv0 string, argv, envv []string) error {
	return syscall.Exec(argv0, argv, envv)
}
