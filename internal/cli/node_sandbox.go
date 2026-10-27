package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
)

const nodeSandboxUsage = `Usage: fridolin node sandbox on | off

Docker Sandboxes (sbx) on this machine, where the core may run brains'
runs in microVMs. The node offers it where it finds sbx, unless it is
turned off here:

  off   stop offering it (a file sandbox-off in the state dir)
  on    offer it again

The running node agent sees the change within minutes. The core asks the
owner to approve the capability before it uses it.
`

// findSbx is where `fridolin node run` looks for sbx: the PATH, then
// where Homebrew puts it (a launchd agent's PATH is plain). "" for
// nowhere. The agent is told sbx's path only from here
// (nodeagent.Agent.SandboxSbx), so that agents made in tests never run
// the machine's sbx.
func findSbx() string {
	if p, err := exec.LookPath("sbx"); err == nil {
		return p
	}
	for _, p := range []string{"/opt/homebrew/bin/sbx", "/usr/local/bin/sbx"} {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// nodeSandbox runs `fridolin node sandbox`.
func nodeSandbox(pos []string, dir string, pkg appPackage, stdout, stderr io.Writer) int {
	if len(pos) == 0 || pos[0] == "help" {
		fmt.Fprint(stdout, nodeSandboxUsage)
		return 0
	}
	if len(pos) != 1 || pos[0] != "on" && pos[0] != "off" {
		fmt.Fprintf(stderr, "fridolin node sandbox: unknown command or wrong arguments\n\n%s", nodeSandboxUsage)
		return 2
	}
	if _, err := nodeagent.LoadConfig(dir); err != nil {
		fmt.Fprintf(stderr, "fridolin node sandbox %s: %v\n", pos[0], err)
		return 1
	}
	if err := pkg.writeError(dir); err != nil {
		fmt.Fprintf(stderr, "fridolin node sandbox %s: %v\n", pos[0], err)
		return 1
	}
	off := pos[0] == "off"
	if err := nodeagent.SetSandboxOff(dir, off); err != nil {
		fmt.Fprintf(stderr, "fridolin node sandbox %s: %v\n", pos[0], err)
		return 1
	}
	if off {
		fmt.Fprintln(stdout, "Docker Sandboxes are turned off on this node.")
	} else {
		fmt.Fprintln(stdout, "Docker Sandboxes are on again: the node offers them while it finds sbx.")
	}
	return 0
}
