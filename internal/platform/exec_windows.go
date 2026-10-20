package platform

import (
	"errors"
	"os"
	"os/exec"
)

// Exec runs argv0 with argv and envv in place of the running process, as
// far as Windows allows: it has no exec(2), so the new process is a child
// that gets this one's console and standard files, and this one waits for
// it and then exits with its status. Each restart leaves one such waiting
// process behind. It returns only when the child could not start.
func Exec(argv0 string, argv, envv []string) error {
	cmd := exec.Command(argv0)
	cmd.Args = argv
	cmd.Env = envv
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	err := cmd.Wait()
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		os.Exit(exit.ExitCode())
	}
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
	return nil
}
