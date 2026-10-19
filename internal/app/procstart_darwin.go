package app

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// procStart says when process pid started, as kern.proc.pid reports it:
// what tells it from a later process given the same PID. A process that
// does not exist is syscall.ESRCH.
func procStart(pid int) (string, error) {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return "", syscall.ESRCH
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if int(kp.Proc.P_pid) != pid {
		return "", syscall.ESRCH // it ended in between
	}
	tv := kp.Proc.P_starttime
	return fmt.Sprintf("%d.%06d", tv.Sec, tv.Usec), nil
}

// bootID names this boot of the machine: no process outlives it. It is the
// boot session's UUID. kern.boottime moves when the wall clock is stepped,
// by NTP say, and a crash then a step would leave its runs unreaped.
func bootID() (string, error) {
	return unix.Sysctl("kern.bootsessionuuid")
}
