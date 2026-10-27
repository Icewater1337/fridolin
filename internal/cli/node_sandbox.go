package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
)

const nodeSandboxUsage = `Usage: fridolin node sandbox setup [--repo DIR] [--bin PATH] | status | on | off

Docker Sandboxes (sbx) on this machine, where the core may run brains'
runs in microVMs (docs/NODES.md, "Docker Sandboxes"). The node offers
them once setup has run, unless they are turned off here:

  setup   make sbx's global settings fridolin's (network policy
          deny-all, SSH agent forwarding and the browser off), build the
          template with Docker from the checkout in --repo (default: the
          current folder) and load it into sbx. --bin puts a linux
          fridolin in place for the sandboxes; without it the core sends
          its own once the capability is approved. Safe to run again.
  status  what sbx says, and what is missing
  off     stop offering them (a file sandbox-off in the state dir)
  on      offer them again

The running node agent sees a change within minutes. The core asks the
owner to approve the capability before it uses it.
`

// sandboxFlags are the flags of fridolin node sandbox setup.
type sandboxFlags struct {
	repo, bin string
}

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
func nodeSandbox(pos []string, f sandboxFlags, dir string, pkg appPackage, stdout, stderr io.Writer) int {
	if len(pos) == 0 || pos[0] == "help" {
		fmt.Fprint(stdout, nodeSandboxUsage)
		return 0
	}
	if len(pos) != 1 || !slices.Contains([]string{"setup", "status", "on", "off"}, pos[0]) || (f.repo != "" || f.bin != "") && pos[0] != "setup" {
		fmt.Fprintf(stderr, "fridolin node sandbox: unknown command or wrong arguments\n\n%s", nodeSandboxUsage)
		return 2
	}
	switch pos[0] {
	case "setup":
		return nodeSandboxSetup(f, dir, findSbx(), stdout, stderr)
	case "status":
		return nodeSandboxStatus(dir, findSbx(), stdout)
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
		fmt.Fprintln(stdout, "Docker Sandboxes are on again: the node offers them once set up (fridolin node sandbox setup).")
	}
	return 0
}
