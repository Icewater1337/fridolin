//go:build unix

package app

import (
	"errors"
	"syscall"
	"time"
)

// runIsOurs reports whether rec's process group is still the run's. With
// its leader alive, it is when the leader started when the run's did: a
// PID given to another process since would have started later. With the
// leader gone, the group lives on while any of its members does (commands
// the run started), and the system gives its ID to no other process or
// group while it does. That leaves a group whose ID was reused after the
// run's had ended, by a process that made a group of its own and then
// ended too, all between the crash and this start: not worth a guard.
func runIsOurs(rec runRecord) bool {
	start, err := procStart(rec.PGID)
	switch {
	case err == nil:
		return start == rec.Start
	case errors.Is(err, syscall.ESRCH):
		// EPERM: there is a group, but another user's.
		return syscall.Kill(-rec.PGID, 0) == nil
	}
	return false
}

// stopGroup ends the process group pgid: SIGTERM, then SIGKILL for what is
// left after reapGrace.
func stopGroup(pgid int) {
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
		return // gone already
	}
	deadline := time.Now().Add(reapGrace)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pgid, 0) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}
