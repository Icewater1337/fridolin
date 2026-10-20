//go:build windows

package platform

import "golang.org/x/sys/windows"

// FreeBytes returns the bytes free to the calling user on the volume that
// holds path, which must exist.
func FreeBytes(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, nil, nil); err != nil {
		return 0, err
	}
	return free, nil
}
