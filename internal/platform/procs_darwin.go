package platform

import (
	"bytes"
	"encoding/binary"
	"errors"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// sZomb is p_stat of a zombie (<sys/proc.h>).
const sZomb = 5

// processes lists kern.proc.all, and reads the command line
// (kern.procargs2) and the memory and processor time (proc_pidinfo) of
// the processes this user may read: no ps, no cgo.
func processes() ([]Proc, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	out := make([]Proc, 0, len(kps))
	for i := range kps {
		kp := &kps[i]
		pid := int(kp.Proc.P_pid)
		if pid <= 0 {
			continue
		}
		p := Proc{
			PID: pid, PPID: int(kp.Eproc.Ppid), PGID: int(kp.Eproc.Pgid), UID: int(kp.Eproc.Ucred.Uid),
			Name:   cString(kp.Proc.P_comm[:]),
			Start:  time.Unix(int64(kp.Proc.P_starttime.Sec), int64(kp.Proc.P_starttime.Usec)*1000),
			Zombie: kp.Proc.P_stat == sZomb,
		}
		if sid, err := unix.Getsid(pid); err == nil {
			p.SID = sid
		}
		if !p.Zombie {
			if args, _, err := procArgs(pid); err == nil {
				p.Args = args
			}
			if ti, err := taskInfo(pid); err == nil {
				p.RSS = ti.Resident
				p.CPU = machTime(ti.TotalUser + ti.TotalSystem)
			}
		}
		out = append(out, p)
	}
	return out, nil
}

func cString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// procArgs reads kern.procargs2: argc, the executable's path, padding,
// then argc arguments and the environment, each NUL-terminated.
func procArgs(pid int) (args, env []string, err error) {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, nil, err
	}
	if len(buf) < 4 {
		return nil, nil, errors.New("kern.procargs2: too short")
	}
	argc := int(binary.LittleEndian.Uint32(buf))
	rest := buf[4:]
	// The executable's path, then the NULs that pad it.
	i := bytes.IndexByte(rest, 0)
	if i < 0 {
		return nil, nil, errors.New("kern.procargs2: no executable path")
	}
	rest = rest[i:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			i = len(rest)
		}
		s := string(rest[:i])
		if i < len(rest) {
			rest = rest[i+1:]
		} else {
			rest = nil
		}
		if len(args) < argc {
			args = append(args, s)
			continue
		}
		if s == "" {
			break
		}
		env = append(env, s)
	}
	return args, env, nil
}

func processEnv(pid int) ([]string, error) {
	_, env, err := procArgs(pid)
	return env, err
}

// procTaskInfo is struct proc_taskinfo (<sys/proc_info.h>).
type procTaskInfo struct {
	Virtual, Resident, TotalUser, TotalSystem, ThreadsUser, ThreadsSystem uint64
	Policy, Faults, Pageins, CowFaults, MessagesSent, MessagesReceived    int32
	SyscallsMach, SyscallsUnix, Csw, ThreadNum, NumRunning, Priority      int32
}

// The proc_info system call: PROC_INFO_CALL_PIDINFO with PROC_PIDTASKINFO.
const (
	sysProcInfo          = 336
	procInfoCallPIDInfo  = 2
	procPIDTaskInfo      = 4
	procTaskInfoSize     = int(unsafe.Sizeof(procTaskInfo{}))
	procTaskInfoExpected = 96
)

// taskInfo asks proc_pidinfo for pid's memory and processor time, as
// libproc does, by its system call.
func taskInfo(pid int) (procTaskInfo, error) {
	var ti procTaskInfo
	if procTaskInfoSize != procTaskInfoExpected {
		return ti, errors.New("proc_taskinfo has an unexpected size")
	}
	n, _, errno := syscall.Syscall6(sysProcInfo, procInfoCallPIDInfo, uintptr(pid), procPIDTaskInfo, 0,
		uintptr(unsafe.Pointer(&ti)), uintptr(procTaskInfoSize))
	if errno != 0 {
		return ti, errno
	}
	if int(n) != procTaskInfoSize {
		return ti, errors.New("proc_pidinfo: short answer")
	}
	return ti, nil
}

// machTime converts proc_taskinfo's times, in Mach absolute time units,
// to a duration: nanoseconds on Intel, 125/3 ns ticks (24 MHz) on Apple
// silicon, which is what mach_timebase_info says there.
func machTime(t uint64) time.Duration {
	if runtime.GOARCH == "arm64" {
		return time.Duration(t * 125 / 3)
	}
	return time.Duration(t)
}
