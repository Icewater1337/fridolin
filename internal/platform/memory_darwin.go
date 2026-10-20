//go:build darwin

package platform

import "golang.org/x/sys/unix"

// Memory returns the bytes of memory the machine has and, roughly, how
// many are free: the free and speculative pages, which macOS hands out
// first.
func Memory() (total, available uint64, err error) {
	total, err = unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0, 0, err
	}
	page, err := unix.SysctlUint32("hw.pagesize")
	if err != nil {
		return total, 0, nil
	}
	free, _ := unix.SysctlUint32("vm.page_free_count")
	spec, _ := unix.SysctlUint32("vm.page_speculative_count")
	return total, (uint64(free) + uint64(spec)) * uint64(page), nil
}
