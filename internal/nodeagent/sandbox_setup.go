package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// `fridolin node sandbox setup` (docs/NODES.md, "Docker Sandboxes"): it
// makes sbx's global settings fridolin's, builds the template with
// Docker from a checkout and loads it into sbx, and records it in
// state.json. Only then does the agent declare sandbox.

// TestedSbxVersion is the sbx release fridolin's sandboxes were tested
// with; doctor warns on another.
const TestedSbxVersion = "v0.50.0"

// sandboxTemplateRepo is the name of fridolin's template image; its tag
// is a hash of what it is built from.
const sandboxTemplateRepo = "fridolin-brain"

// SandboxSetupOptions are what setup works with.
type SandboxSetupOptions struct {
	// Dir is the node's state dir.
	Dir string
	// Sbx and Docker are the programs.
	Sbx, Docker string
	// Repo is a fridolin checkout: container/sandbox/Dockerfile, tools/.
	Repo string
	// Bin is a linux fridolin to put in place for the sandboxes; "" keeps
	// the one there, which the core sends once sandbox is approved.
	Bin string
	// Out gets what setup does, for the person who runs it.
	Out io.Writer
	// env is added to each program's environment (tests: the fakes').
	env []string
}

// SandboxTemplateTag is the template's name for the checkout at repo: a
// hash of its Dockerfile and tools/*.py.
func SandboxTemplateTag(repo string) (string, error) {
	h := sha256.New()
	files, err := filepath.Glob(filepath.Join(repo, "tools", "*.py"))
	if err != nil {
		return "", err
	}
	slices.Sort(files)
	for _, f := range append([]string{filepath.Join(repo, "container", "sandbox", "Dockerfile")}, files...) {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", fmt.Errorf("%s is not a fridolin checkout: %w", repo, err)
		}
		fmt.Fprintf(h, "%s %d\n", filepath.Base(f), len(b))
		h.Write(b)
	}
	return sandboxTemplateRepo + ":" + hex.EncodeToString(h.Sum(nil))[:12], nil
}

// run runs a program of setup, its output to Out.
func (o SandboxSetupOptions) run(ctx context.Context, prog string, args ...string) error {
	c := exec.CommandContext(ctx, prog, args...)
	c.Env = slices.Concat(sbxEnv(c.Environ()), o.env)
	c.Stdin = strings.NewReader("")
	c.Stdout, c.Stderr = o.Out, o.Out
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", filepath.Base(prog), args[0], err)
	}
	return nil
}

// errSbxSignedOut is sbx without Docker's sign-in.
var errSbxSignedOut = errors.New("sbx is not signed in to Docker: run sbx login, then fridolin node sandbox setup again")
