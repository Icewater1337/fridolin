package nodeagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
)

// SandboxSetup sets this machine's sbx up for fridolin's sandboxes: sbx's
// global settings, the template, the linux fridolin when one is given,
// and state.json. It is safe to run again; a template already in sbx is
// not built again.
func SandboxSetup(ctx context.Context, o SandboxSetupOptions) error {
	a := &Agent{Dir: o.Dir, SandboxSbx: o.Sbx, Logf: func(string, ...any) {}}
	d := sbxCLI{path: o.Sbx, env: o.env}
	out, stderr, err := d.run(ctx, nil, nil, "version")
	if err != nil {
		return fmt.Errorf("sbx does not run: %w", sbxError(err, stderr))
	}
	if v := parseSbxVersion(out); v != TestedSbxVersion {
		fmt.Fprintf(o.Out, "Note: sbx is %s; fridolin is tested with %s.\n", orUnknown(v), TestedSbxVersion)
	}
	diag, list, problem := sbxState(ctx, d)
	switch {
	case !diag.Daemon:
		return fmt.Errorf("%s", problem)
	case !diag.SignedIn:
		return errSbxSignedOut
	}

	changed, restart, err := sbxEnforce(ctx, d, true)
	for _, c := range changed {
		fmt.Fprintf(o.Out, "Set sbx's %s.\n", c)
	}
	if err != nil {
		return err
	}
	if restart {
		if slices.ContainsFunc(list, func(s sbxSandbox) bool { return s.Status == "running" }) {
			fmt.Fprintln(o.Out, "A sandbox runs: restart sbx's daemon (sbx daemon restart) once none does, for the SSH agent setting to apply to all.")
		} else if _, stderr, err := d.run(ctx, nil, nil, "daemon", "restart"); err != nil {
			return fmt.Errorf("sbx daemon restart: %w", sbxError(err, stderr))
		} else {
			fmt.Fprintln(o.Out, "Restarted sbx's daemon.")
		}
	}
	fmt.Fprintln(o.Out, "sbx's global settings are fridolin's: network policy deny-all, SSH agent forwarding and the browser off.")

	tag, err := SandboxTemplateTag(o.Repo)
	if err != nil {
		return err
	}
	if sbxHasTemplate(ctx, d, tag) {
		fmt.Fprintf(o.Out, "sbx has the template %s already.\n", tag)
	} else if err := o.buildTemplate(ctx, d, tag); err != nil {
		return err
	}

	if o.Bin != "" {
		v, err := a.putSandboxBinFile(o.Bin)
		if err != nil {
			return err
		}
		fmt.Fprintf(o.Out, "The sandboxes run the linux fridolin %s from %s.\n", v, o.Bin)
	}

	st, err := a.loadSandboxState()
	if err != nil {
		return err
	}
	st.Template = tag
	if err := a.saveSandboxState(st); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "Set up: the node offers Docker Sandboxes with the template %s.\n", tag)
	return nil
}

// buildTemplate builds the template's image with Docker and loads it
// into sbx's own image store.
func (o SandboxSetupOptions) buildTemplate(ctx context.Context, d sbxCLI, tag string) error {
	fmt.Fprintf(o.Out, "Building the template %s with Docker (a few minutes the first time)…\n", tag)
	dockerfile := filepath.Join(o.Repo, "container", "sandbox", "Dockerfile")
	if err := o.run(ctx, o.Docker, "build", "-f", dockerfile, "-t", tag, o.Repo); err != nil {
		return err
	}
	dir := filepath.Join(o.Dir, sandboxDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tar := filepath.Join(dir, "template.tar")
	defer os.Remove(tar)
	if err := o.run(ctx, o.Docker, "save", tag, "-o", tar); err != nil {
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
	if err := o.run(ctx, o.Docker, "image", "rm", tag); err != nil {
		fmt.Fprintf(o.Out, "Note: could not remove Docker's copy of %s: %v\n", tag, err)
	}
	return nil
}

// putSandboxBinFile puts the linux fridolin at path in place for the
// sandboxes, and returns its version.
func (a *Agent) putSandboxBinFile(path string) (string, error) {
	arch := runtime.GOARCH
	check := a.checkSandboxBin
	if check == nil {
		check = checkLinuxBinary
	}
	v, err := check(path, arch)
	if err != nil {
		return "", err
	}
	if v == "" {
		return "", fmt.Errorf("%s has no version stamped in (build it with make, or GOOS=linux make build)", path)
	}
	dir := a.sandboxBinHostDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, sum, err := copyToTemp(path, dir)
	if err != nil {
		return "", err
	}
	if err := a.installSandboxBin(tmp, sandboxBinInfo{Version: v, Arch: arch, SHA256: sum}); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return v, nil
}
