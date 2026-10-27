package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
)

// lookAtSandbox is how Docker Sandboxes stand for the node with state
// dir dir, with the machine's sbx and host secret store. It changes
// nothing.
func lookAtSandbox(dir string) nodeagent.SandboxStatus {
	home, _ := os.UserHomeDir()
	store, _ := nodeagent.HostSecretStore(runtime.GOOS, os.Getenv, home)
	ctx, cancel := context.WithTimeout(context.Background(), sandboxLookTimeout)
	defer cancel()
	return nodeagent.LookAtSandbox(ctx, dir, findSbx(), store)
}

// sandboxLookTimeout bounds one look at sbx from the command line.
const sandboxLookTimeout = time.Minute

// nodeSandboxStatus runs `fridolin node sandbox status`.
func nodeSandboxStatus(dir, sbx string, stdout io.Writer) int {
	s := lookAtSandbox(dir)
	switch {
	case s.Sbx == "":
		fmt.Fprintln(stdout, "sbx is not installed here: no Docker Sandboxes.")
		return 0
	case s.Off:
		fmt.Fprintln(stdout, "Docker Sandboxes are turned off here (fridolin node sandbox on).")
		return 0
	case s.Err != nil:
		fmt.Fprintf(stdout, "sbx (%s) does not run: %v\n", sbx, s.Err)
		return 1
	}
	i := s.Info
	yes := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	fmt.Fprintf(stdout, "sbx:             %s (%s; tested %s)\n", i.Version, s.Sbx, nodeagent.TestedSbxVersion)
	fmt.Fprintf(stdout, "daemon:          %s\nsigned in:       %s\n", yes(i.Daemon), yes(i.SignedIn))
	fmt.Fprintf(stdout, "settings:        %s\n", yes(i.Settings))
	fmt.Fprintf(stdout, "template:        %s\n", noneIfEmpty(i.Template))
	fmt.Fprintf(stdout, "linux fridolin:  %s\n", noneIfEmpty(i.Bin))
	fmt.Fprintf(stdout, "logins:          %s\n", noneIfEmpty(strings.Join(i.Providers, ", ")))
	fmt.Fprintf(stdout, "free memory:     %d MB\n", i.FreeMemoryMB)
	for _, v := range i.Sandboxes {
		fmt.Fprintf(stdout, "sandbox:         %s (%s, %d MB)\n", v.Name, v.Status, v.MemoryMB)
	}
	if i.Problem != "" {
		fmt.Fprintf(stdout, "not ready:       %s\n", i.Problem)
		return 1
	}
	fmt.Fprintln(stdout, "Ready: the node offers Docker Sandboxes.")
	return 0
}

func noneIfEmpty(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
