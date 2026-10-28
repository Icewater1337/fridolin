package nodeagent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// runProg runs a program of create (Docker), its output to Out.
func (o FrideSandboxOptions) runProg(ctx context.Context, prog string, args ...string) error {
	c := exec.CommandContext(ctx, prog, args...)
	c.Env = slices.Concat(sbxEnv(c.Environ()), o.env)
	c.Stdin = strings.NewReader("")
	c.Stdout, c.Stderr = o.Out, o.Out
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", filepath.Base(prog), args[0], err)
	}
	return nil
}

// buildTemplate builds the template's image from dockerfile with Docker
// and loads it into sbx's own image store.
func (o FrideSandboxOptions) buildTemplate(ctx context.Context, d sbxCLI, tag, dockerfile string) error {
	fmt.Fprintf(o.Out, "Building the template %s with Docker (a few minutes the first time)…\n", tag)
	if err := o.runProg(ctx, o.Docker, "build", "-f", dockerfile, "-t", tag, o.Repo); err != nil {
		return err
	}
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return err
	}
	tar := filepath.Join(o.Dir, "template.tar")
	defer os.Remove(tar)
	if err := o.runProg(ctx, o.Docker, "save", tag, "-o", tar); err != nil {
		return err
	}
	fmt.Fprintln(o.Out, "Loading it into sbx…")
	if _, stderr, err := d.run(ctx, nil, nil, "template", "load", tar); err != nil {
		return fmt.Errorf("sbx template load: %w", sbxError(err, stderr))
	}
	if !sbxHasTemplate(ctx, d, tag) {
		return fmt.Errorf("sbx loaded the image, but does not list the template %s", tag)
	}
	// sbx keeps its own copy; Docker's is not needed (several GB).
	if err := o.runProg(ctx, o.Docker, "image", "rm", tag); err != nil {
		fmt.Fprintf(o.Out, "Note: could not remove Docker's copy of %s: %v\n", tag, err)
	}
	return nil
}
