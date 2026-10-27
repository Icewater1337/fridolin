package cli

import (
	"fmt"
	"io"

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
