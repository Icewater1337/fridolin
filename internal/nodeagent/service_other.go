//go:build !windows

package nodeagent

import (
	"errors"
	"fmt"

	"github.com/kardianos/service"
)

func serviceStatus(cfg *service.Config) (string, error) {
	s, err := service.New(&program{}, cfg)
	if err != nil {
		return "", err
	}
	st, err := s.Status()
	if errors.Is(err, service.ErrNotInstalled) {
		return "not installed", nil
	}
	if err != nil {
		return "", err
	}
	switch st {
	case service.StatusRunning:
		return "running", nil
	case service.StatusStopped:
		return "installed, stopped", nil
	case service.StatusUnknown:
		fallthrough
	default:
		return fmt.Sprintf("unknown (%d)", st), nil
	}
}

// serviceStateDir is not read back here: the launchd agent and the
// systemd unit run as the user who installed them, who sees what they see.
func serviceStateDir(*service.Config) (string, error) { return "", nil }
