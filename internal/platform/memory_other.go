//go:build !darwin && !linux && !windows

package platform

import "errors"

// Memory is not known on this system.
func Memory() (total, available uint64, err error) {
	return 0, 0, errors.New("memory is not known on this system")
}
