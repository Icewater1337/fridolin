package cli

import (
	"fmt"
	"io"

	"github.com/Icewater1337/fridolin/container"
)

const boxUsage = `fridolin box — a node in a box (docs/NODES.md, "Node in a box")

Usage:
  fridolin box compose   print the box's compose file of this version, which
                         box.sh and box.ps1 write beside their .env
`

// boxCmd runs `fridolin box` and returns its exit status.
func boxCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, boxUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	switch args[0] {
	case "compose":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "fridolin box compose takes no arguments")
			return 2
		}
		_, _ = stdout.Write(container.Box)
		return 0
	default:
		fmt.Fprintf(stderr, "fridolin box: unknown command %q\n\n%s", args[0], boxUsage)
		return 2
	}
}
