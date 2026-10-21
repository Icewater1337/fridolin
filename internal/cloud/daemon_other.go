//go:build !linux

package cloud

import "os/exec"

// setDaemonAttrs has nothing to set outside Linux, which alone can stop a
// child when its parent dies; a daemon a crash leaves behind idles on its
// own socket until it is killed.
func setDaemonAttrs(*exec.Cmd) {}
