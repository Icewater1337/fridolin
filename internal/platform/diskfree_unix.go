//go:build unix

package platform

import "golang.org/x/sys/unix"

// FreeBytes returns the bytes free to an unprivileged user on the file
// system that holds path, which must exist.
func FreeBytes(path string) (uint64, error) {
	// Bavail and Bsize have other types on each system.
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
