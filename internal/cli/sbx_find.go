package cli

import (
	"os"
	"os/exec"
	"runtime"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
)

// findSbx is the sbx CLI of Docker Sandboxes: on PATH, else where
// Homebrew puts it, which a launchd agent's short PATH lacks, or on
// Windows where winget links it. "" when it is not installed (on Windows
// the node's service then looks for the signed-in user's).
func findSbx() string {
	if runtime.GOOS == "windows" {
		if p, err := exec.LookPath("sbx"); err == nil {
			return p
		}
		return nodeagent.FindSbx(os.Environ())
	}
	return findProgram("sbx", "/opt/homebrew/bin/sbx", "/usr/local/bin/sbx")
}

// findDocker is the docker CLI, found the same way.
func findDocker() string {
	return findProgram("docker", "/usr/local/bin/docker", "/opt/homebrew/bin/docker")
}

// findProgram looks name up on PATH, then at each of places.
func findProgram(name string, places ...string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, p := range places {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}
