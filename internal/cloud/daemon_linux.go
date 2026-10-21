package cloud

import (
	"os/exec"
	"syscall"
)

// setDaemonAttrs makes the kernel stop rclone rcd when the bridge dies,
// so a crash leaves no daemon behind.
func setDaemonAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
