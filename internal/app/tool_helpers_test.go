package app

import (
	"bytes"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/toolcli"
)

// runTool runs a tool subcommand in process, in dir, with env as its whole
// environment, and returns its exit status, stdout and stderr.
func runTool(t *testing.T, cmd func([]string, toolcli.IO) int, dir string, env map[string]string, now time.Time, args ...string) (int, string, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := cmd(args, toolcli.IO{Stdout: &out, Stderr: &errs, Getenv: func(k string) string { return env[k] }, Dir: dir, Now: now})
	return code, out.String(), errs.String()
}
