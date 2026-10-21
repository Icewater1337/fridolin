//go:build windows

package nodeagent

import (
	"os"
	"os/exec"
)

// reexec starts exe with the same arguments in this console and ends
// this process: Windows has no exec in place.
func reexec(exe string) error {
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
