package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
)

// sandboxSetupTimeout bounds setup: the first build pulls about 1.5 GB.
const sandboxSetupTimeout = 45 * time.Minute

// findDocker is the docker CLI, "" for none: the PATH, then where Docker
// Desktop and Homebrew put it.
func findDocker() string {
	if p, err := exec.LookPath("docker"); err == nil {
		return p
	}
	for _, p := range []string{"/usr/local/bin/docker", "/opt/homebrew/bin/docker"} {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// nodeSandboxSetup runs `fridolin node sandbox setup`.
func nodeSandboxSetup(f sandboxFlags, dir, sbx string, stdout, stderr io.Writer) int {
	fail := func(format string, args ...any) int {
		fmt.Fprintf(stderr, "fridolin node sandbox setup: "+format+"\n", args...)
		return 1
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return fail("Docker Sandboxes run on macOS (and Linux with KVM), not on %s", runtime.GOOS)
	}
	if _, err := nodeagent.LoadConfig(dir); err != nil {
		return fail("%v", err)
	}
	if sbx == "" {
		return fail("sbx is not installed: install Docker Sandboxes (on a Mac: brew install --cask sbx), run sbx login, then this again")
	}
	docker := findDocker()
	if docker == "" {
		return fail("docker is not installed: setup builds the template with Docker (Docker Desktop or OrbStack)")
	}
	repo := f.repo
	if repo == "" {
		repo = "."
	}
	repo, err := filepath.Abs(repo)
	if err != nil {
		return fail("%v", err)
	}
	bin := f.bin
	if bin != "" {
		if bin, err = filepath.Abs(bin); err != nil {
			return fail("%v", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), sandboxSetupTimeout)
	defer cancel()
	o := nodeagent.SandboxSetupOptions{Dir: dir, Sbx: sbx, Docker: docker, Repo: repo, Bin: bin, Out: stdout}
	if err := nodeagent.SandboxSetup(ctx, o); err != nil {
		return fail("%v", err)
	}
	fmt.Fprintln(stdout, "Next: approve the capability on the core (!approve in your chat, or the Nodes page).")
	fmt.Fprintln(stdout, "For Codex in the sandboxes, sign in once: sbx secret set openai --oauth")
	return 0
}
