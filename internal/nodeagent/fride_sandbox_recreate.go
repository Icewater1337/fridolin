package nodeagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// carryDir is the folder of what update carries into a sandbox it makes
// anew. It stays while that is not done, so that update can finish.
func (o FrideSandboxOptions) carryDir() string {
	return filepath.Join(o.Dir, filepath.FromSlash(frideSandboxCarryDir))
}

// carrying reports whether an update left a carry to finish with.
func (o FrideSandboxOptions) carrying() bool {
	_, err := os.Stat(o.carryDir())
	return err == nil
}

// recreate makes the sandbox anew from the template tag, with FrIDE at
// commit, and carries over what lives only on the old VM's disk: the
// history, the workspaces, the Claude login, Claude Code's and git's
// settings (frideSandboxCarry). The new template is built first; the old
// sandbox goes only once its things are on the host. status is sbx's
// status of the old sandbox, "" when an earlier update removed it
// already and left its things in the carry folder.
func (o FrideSandboxOptions) recreate(ctx context.Context, st *FrideSandboxState, status, tag, commit string) error {
	config, err := companionConfig(o.Repo, st.Base)
	if err != nil {
		return err
	}
	if err := o.ensureTemplate(ctx, tag); err != nil {
		return err
	}
	// FrIDE stays stopped until the new sandbox is made; whether it ran
	// waits in the carry folder for an update that finishes this one.
	carry := o.carryDir()
	runMark := filepath.Join(carry, ".run")
	wasRun := st.Run
	if _, err := os.Stat(runMark); err == nil {
		wasRun = true
	}
	if wasRun {
		if err := writeFile(runMark, nil, 0o600); err != nil {
			return err
		}
	}
	if st.Run {
		st.Run = false
		if err := saveFrideSandbox(o.Dir, *st); err != nil {
			return err
		}
	}
	if status != "" {
		if status == "running" {
			if err := o.exec(ctx, nil, frideSandboxStop); err != nil {
				return err
			}
		}
		n, err := o.saveVM(ctx, carry, frideSandboxCarry)
		if err != nil {
			st.Run = wasRun
			_ = saveFrideSandbox(o.Dir, *st)
			return fmt.Errorf("saving the sandbox's things: %w; the sandbox stays as it is", err)
		}
		fmt.Fprintf(o.Out, "Saved %d folders and files of the sandbox (workspaces, sessions, FrIDE's tasks, the login) to %s.\n", n, carry)
		if _, stderr, err := o.sbx().run(ctx, nil, nil, "rm", "-f", o.sandbox()); err != nil {
			return fmt.Errorf("sbx could not remove %s: %w", o.sandbox(), sbxError(err, stderr))
		}
	}
	old := st.Template
	st.Template = tag
	if st.DockerDisk == "" {
		st.DockerDisk = DefaultFrideSandboxDockerDisk
	}
	if err := o.newSandbox(ctx, *st); err != nil {
		return fmt.Errorf("%w; what it held is in %s: fridolin node fride sandbox update puts it back", err, carry)
	}
	if err := o.makeInside(ctx, st, config, commit, carry, frideSandboxCarry); err != nil {
		_, _, _ = o.sbx().run(context.WithoutCancel(ctx), nil, nil, "rm", "-f", o.sandbox())
		return fmt.Errorf("%w; the new sandbox is removed again, and what the old one held is in %s: fridolin node fride sandbox update puts it back", err, carry)
	}
	st.Run = wasRun
	if err := saveFrideSandbox(o.Dir, *st); err != nil {
		return err
	}
	if err := os.RemoveAll(carry); err != nil {
		fmt.Fprintf(o.Out, "Note: %s stays (%v); remove it.\n", carry, err)
	}
	fmt.Fprintf(o.Out, "Made the sandbox anew from %s, with its workspaces, sessions, FrIDE's tasks and login. The old template stays in sbx: sbx template rm %s\n", tag, old)
	return nil
}
