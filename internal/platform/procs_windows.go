package platform

import (
	"errors"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processes walks a Toolhelp snapshot, and opens each process with the
// least access that reads its times and memory. Windows has no process
// groups or sessions in the Unix sense, and its command lines are read
// only through another process's memory, which housekeeping does not do:
// PGID, SID and Args stay empty.
func processes() ([]Proc, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var out []Proc
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		p := Proc{PID: int(e.ProcessID), PPID: int(e.ParentProcessID), UID: -1, Name: windows.UTF16ToString(e.ExeFile[:])}
		if p.PID == 0 {
			continue
		}
		readTimes(&p)
		out = append(out, p)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return out, nil
}

// processMemoryCounters is PROCESS_MEMORY_COUNTERS (psapi.h).
type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

func readTimes(p *Proc) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(p.PID))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &created, &exited, &kernel, &user) == nil {
		p.Start = time.Unix(0, created.Nanoseconds())
		p.CPU = time.Duration(fileTimeTicks(kernel)+fileTimeTicks(user)) * 100
	}
	var mc processMemoryCounters
	mc.CB = uint32(unsafe.Sizeof(mc))
	getMemoryInfo := windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")
	if r, _, _ := getMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&mc)), uintptr(mc.CB)); r != 0 {
		p.RSS = uint64(mc.WorkingSetSize)
	}
}

// fileTimeTicks is a FILETIME duration in 100 ns ticks.
func fileTimeTicks(ft windows.Filetime) int64 {
	return int64(ft.HighDateTime)<<32 | int64(ft.LowDateTime)
}

func processEnv(int) ([]string, error) {
	return nil, errors.New("reading another process's environment is not available on Windows")
}

// stillActive is GetExitCodeProcess's code for a running process.
const stillActive = 259

func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(h)
	var code uint32
	if windows.GetExitCodeProcess(h, &code) != nil {
		return true
	}
	return code == stillActive
}

// signalProcess ends pid at once: Windows has no SIGTERM to give a
// console-less process.
func signalProcess(pid int, _ bool) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil // gone
		}
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}
