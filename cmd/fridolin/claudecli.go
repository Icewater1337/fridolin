package main

import (
	"log"
	"os/exec"
	"time"
)

// claudeInfo finds the CLI and its version. The version is cached until the
// binary changes: `claude` on the PATH is a symlink the CLI moves to each
// new release it installs, and the dashboard must not show the old one for
// as long as the bridge runs. A CLI that is not found is looked up again
// every time, so installing it shows at once. It runs the CLI with a
// brain's environment: a claude_path wrapper may run what a brain can
// change.
func claudeInfo(path string, passthrough []string) claudeStatus {
	return cliInfo(path, passthrough, claudeChild)
}

func codexInfo(path string, passthrough []string) claudeStatus {
	return cliInfo(path, passthrough, codexChild)
}

var cliVersions cliProber

func cliInfo(path string, passthrough []string, scope childScope) claudeStatus {
	resolved, err := exec.LookPath(path)
	if err != nil {
		return claudeStatus{Path: path}
	}
	return cliVersions.Info(path, resolved, childEnv(scope, passthrough))
}

// logClaudeCLI says at startup which CLI brains run and warns when it is
// missing or too old, so a log excerpt shows it next to the bridge's own
// version.
func logClaudeCLI(path string, passthrough []string) {
	st := claudeInfo(path, passthrough)
	switch {
	case !st.Found:
		log.Printf("WARNING: the Claude Code CLI (%s) was not found, so brains cannot run. Set its path under Settings → Claude.", path)
	case st.Version == "":
		log.Printf("Claude Code CLI: %s (`--version` gave no answer)", st.Path)
	default:
		log.Printf("Claude Code CLI: %s, %s", st.Path, st.Version)
	}
	if w := claudeVersionWarning(st); w != "" {
		log.Printf("WARNING: %s", w)
	}
}

// runWaitDelay is how long Wait waits, once the CLI has exited, for the
// copies of its input and output to end, a variable so tests need not
// wait for it.
var runWaitDelay = 5 * time.Second
