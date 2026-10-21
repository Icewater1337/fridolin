//go:build !linux && !darwin && !windows

package platform

import (
	"errors"
	"fmt"
)

// errNoProcessList: the bridge cannot list processes on this system.
func errNoProcessList() error {
	return fmt.Errorf("listing processes on this system: %w", errors.ErrUnsupported)
}

func processes() ([]Proc, error) { return nil, errNoProcessList() }

func processEnv(int) ([]string, error) { return nil, errNoProcessList() }
