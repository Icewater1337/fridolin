package app

import (
	"os/exec"
	"time"

	"github.com/Icewater1337/fridolin/internal/claudecli"
)

// claudeInfo finds the CLI and its version. The version is cached until the
// binary changes: `claude` on the PATH is a symlink the CLI moves to each
// new release it installs, and the dashboard must not show the old one for
// as long as the bridge runs. A CLI that is not found is looked up again
// every time, so installing it shows at once. It runs the CLI with a
// brain's environment: a claude_path wrapper may run what a brain can
// change.
func ClaudeInfo(path string, passthrough []string) claudecli.Status {
	return cliInfo(path, passthrough, claudeChild)
}

func CodexInfo(path string, passthrough []string) claudecli.Status {
	return cliInfo(path, passthrough, codexChild)
}

var cliVersions claudecli.Prober

func cliInfo(path string, passthrough []string, scope childScope) claudecli.Status {
	resolved, err := exec.LookPath(path)
	if err != nil {
		return claudecli.Status{Path: path}
	}
	return cliVersions.Info(path, resolved, childEnv(scope, passthrough))
}

// runWaitDelay is how long Wait waits, once the CLI has exited, for the
// copies of its input and output to end, a variable so tests need not
// wait for it.
var runWaitDelay = 5 * time.Second
