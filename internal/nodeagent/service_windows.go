//go:build windows

package nodeagent

import (
	"errors"
	"fmt"

	"github.com/kardianos/service"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// queryService opens the service with no more rights than any user has:
// kardianos/service's Status also asks to start and stop it, which
// Windows grants a LocalSystem service's administrators only, so a
// normal PowerShell would hear "Access is denied". ok is false for a
// service that is not installed.
func queryService(name string, access uint32) (s *mgr.Service, ok bool, err error) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, false, err
	}
	defer windows.CloseServiceHandle(m)
	h, err := windows.OpenService(m, windows.StringToUTF16Ptr(name), access)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &mgr.Service{Handle: h, Name: name}, true, nil
}

func serviceStatus(cfg *service.Config) (string, error) {
	s, ok, err := queryService(cfg.Name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return "", err
	}
	if !ok {
		return "not installed", nil
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return "", err
	}
	switch st.State {
	case svc.Running, svc.StartPending:
		return "running", nil
	case svc.Stopped, svc.StopPending, svc.Paused, svc.PausePending, svc.ContinuePending:
		return "installed, stopped", nil
	}
	return fmt.Sprintf("unknown (%d)", st.State), nil
}

func serviceStateDir(cfg *service.Config) (string, error) {
	s, ok, err := queryService(cfg.Name, windows.SERVICE_QUERY_CONFIG)
	if err != nil || !ok {
		return "", err
	}
	defer s.Close()
	c, err := s.Config()
	if err != nil {
		return "", err
	}
	args, err := windows.DecomposeCommandLine(c.BinaryPathName)
	if err != nil {
		return "", err
	}
	return stateDirArg(args), nil
}
