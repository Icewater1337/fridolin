//go:build !windows

package nodeagent

import (
	"context"
	"os"
	"os/exec"
	"runtime"
)

// sbxCommand is sbx at path with args, and the environment it starts
// from: the agent's own, which is the user's whose sbx it is. done
// releases nothing here.
func sbxCommand(ctx context.Context, path string, args []string) (*exec.Cmd, []string, func(), error) {
	c := exec.CommandContext(ctx, path, args...)
	return c, c.Environ(), func() {}, nil
}

// sbxInSession reports whether sbx runs in another user's session than
// the agent's: only a Windows service's does.
func sbxInSession() bool { return false }

func (sbxCLI) unavailable() string { return "" }

// hostFolder: the agent is the user who runs sbx; the folder is its
// alone.
func (sbxCLI) hostFolder(dir string) error { return os.Chmod(dir, 0o700) }

// secretCommand is the host command sbx runs to read the token file.
func (sbxCLI) secretCommand(file string) (string, error) {
	return sbxSecretCommandFor(runtime.GOOS, "", file)
}
