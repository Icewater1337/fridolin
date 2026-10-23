//go:build windows

package nodeagent

import (
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// The state dir comes back from the command line the service manager
// keeps, which kardianos/service writes with syscall.EscapeArg (through
// golang.org/x/sys/windows/svc/mgr), quotes and all.
func TestStateDirFromTheServiceCommandLine(t *testing.T) {
	for _, dir := range []string{`C:\Users\you\AppData\Local\fridolin-node`, `D:\state dirs\fridolin node`, `C:\Users\you\"odd"\x`} {
		line := syscall.EscapeArg(`C:\Program Files\fridolin\fridolin.exe`)
		for _, a := range ServiceConfig("windows", "", dir).Arguments {
			line += " " + syscall.EscapeArg(a)
		}
		args, err := windows.DecomposeCommandLine(line)
		if err != nil {
			t.Fatal(err)
		}
		if got := stateDirArg(args); got != dir {
			t.Errorf("%s: %q, want %q", line, got, dir)
		}
	}
}

// Asking after a service needs no administrator, and one that is not
// installed says so.
func TestServiceThatIsNotInstalled(t *testing.T) {
	cfg := ServiceConfig("windows", `C:\Program Files\fridolin\fridolin.exe`, `C:\Users\you\AppData\Local\fridolin-node`)
	cfg.Name = "fridolin-node-test-not-installed"
	if st, err := ServiceStatus(cfg); err != nil || st != "not installed" {
		t.Errorf("status: %q, %v", st, err)
	}
	if dir, err := ServiceStateDir(cfg); err != nil || dir != "" {
		t.Errorf("state dir: %q, %v", dir, err)
	}
}
