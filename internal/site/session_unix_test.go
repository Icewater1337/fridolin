//go:build unix

package site

import (
	"fmt"
	"os"
	"syscall"
)

// fakeSession prints the process's pid and the session it is in: a process
// that leads a session of its own prints the same number twice.
func fakeSession() int {
	// syscall has no Getsid on Linux.
	sid, _, errno := syscall.Syscall(syscall.SYS_GETSID, 0, 0, 0)
	if errno != 0 {
		fmt.Println("getsid:", errno)
		return 1
	}
	fmt.Printf("pid=%d sid=%d\n", os.Getpid(), sid)
	return 0
}
