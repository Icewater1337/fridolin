package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
)

// TestGoldenNodeUnit pins the systemd user unit that `fridolin node
// install` writes on Linux, through kardianos/service as it runs there,
// with a systemctl that only answers. It must be wanted by
// default.target: a user's systemd never reaches multi-user.target, so a
// node enabled there stays off after a reboot (issue #171).
func TestGoldenNodeUnit(t *testing.T) {
	// kardianos/service picks systemd only where systemd runs.
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		t.Skip("not a systemd system")
	}
	home, bin := t.TempDir(), t.TempDir()
	calls := filepath.Join(bin, "calls")
	script := "#!/bin/sh\necho \"$*\" >> " + calls + "\n[ \"$1\" = --version ] && echo 'systemd 255 (255.4-1ubuntu8)'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	svc := nodeagent.ServiceConfig("linux", "/home/you/.local/bin/fridolin", "/home/you/.local/state/fridolin-node")
	if err := nodeagent.ControlService(svc, "install"); err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(nodeagent.LinuxUnitPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), "\nWantedBy=default.target\n") || strings.Contains(string(unit), "multi-user.target") {
		t.Errorf("the unit is not wanted by default.target:\n%s", unit)
	}
	if got, _ := os.ReadFile(calls); !strings.Contains(string(got), "enable --user fridolin-node.service") {
		t.Errorf("systemctl calls:\n%s", got)
	}
	assertGolden(t, "service/fridolin-node.service", unit)
}
