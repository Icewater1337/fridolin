package main

import (
	"os/exec"
)

// gitPath is git's full path: sandbox-exec runs a program by path.
func gitPath() string {
	if p, err := exec.LookPath("git"); err == nil {
		return p
	}
	return "/usr/bin/git"
}
