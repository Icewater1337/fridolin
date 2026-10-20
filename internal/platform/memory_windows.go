//go:build windows

package platform

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// memoryStatusEx is MEMORYSTATUSEX.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// Memory returns the bytes of memory the machine has and how many are
// available (GlobalMemoryStatusEx).
func Memory() (total, available uint64, err error) {
	st := memoryStatusEx{}
	st.Length = uint32(unsafe.Sizeof(st))
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	if err := proc.Find(); err != nil {
		return 0, 0, err
	}
	if r, _, err := proc.Call(uintptr(unsafe.Pointer(&st))); r == 0 {
		return 0, 0, err
	}
	return st.TotalPhys, st.AvailPhys, nil
}
