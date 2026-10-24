package app

import (
	"fmt"
	"os/exec"
	"time"

	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/config"
)

// ClaudeInfo finds the CLI and its version. The version is cached until the
// binary changes: `claude` on the PATH is a symlink the CLI moves to each
// new release it installs, and the dashboard must not show the old one for
// as long as the bridge runs. A CLI that is not found is looked up again
// every time, so installing it shows at once. It runs the CLI with a
// brain's environment, taken from env: a claude_path wrapper may run what
// a brain can change.
func ClaudeInfo(env Environment, path string, passthrough []string) claudecli.Status {
	return cliInfo(&cliVersions, env, path, passthrough, claudeChild)
}

func CodexInfo(env Environment, path string, passthrough []string) claudecli.Status {
	return cliInfo(&cliVersions, env, path, passthrough, codexChild)
}

// cliVersions caches the versions ClaudeInfo and CodexInfo found; a bridge
// keeps its own (App.cliVersions).
var cliVersions claudecli.Prober

// cliInfo is ClaudeInfo for the CLI of scope, with the versions cached in
// versions.
func cliInfo(versions *claudecli.Prober, env Environment, path string, passthrough []string, scope childScope) claudecli.Status {
	resolved, err := exec.LookPath(path)
	if err != nil {
		return claudecli.Status{Path: path}
	}
	// Absolute, as runs start it (resolveAgentBinary): a PATH entry may be
	// relative.
	return versions.Info(path, AbsPath(resolved), childEnv(env, scope, passthrough))
}

// claudeInfo is ClaudeInfo for the bridge, with its environment and its own
// cache.
func (b *App) claudeInfo(cfg *config.Settings) claudecli.Status {
	return cliInfo(&b.cliVersions, b.env, cfg.ClaudePath, cfg.ChildEnvPassthrough, claudeChild)
}

// codexInfo is CodexInfo for the bridge.
func (b *App) codexInfo(cfg *config.Settings) claudecli.Status {
	return cliInfo(&b.cliVersions, b.env, cfg.CodexPath, cfg.ChildEnvPassthrough, codexChild)
}

// resolveAgentBinary is the absolute path of the agent CLI a run starts,
// found from the configured path on the bridge's own PATH (not the
// brain's, which a brain may change). The sandboxes need it absolute:
// bubblewrap binds and runs the program by its path, and sandbox-exec's
// profile allows it by path. A config that names the CLI bare ("claude",
// "codex") is the default, and what a native install moved into the
// container keeps once its Mac-only paths are dropped.
func resolveAgentBinary(provider, path string) (string, error) {
	name, setting := "claude", "claude_path"
	if provider == "codex" {
		name, setting = "codex", "codex_path"
	}
	if path == "" {
		path = name
	}
	found, err := exec.LookPath(path)
	if err != nil {
		return "", fmt.Errorf("the %s CLI %q was not found on the bridge's PATH; install it or set %s to its absolute path: %w", name, path, setting, err)
	}
	return AbsPath(found), nil
}

// runWaitDelay is how long Wait waits, once the CLI has exited, for the
// copies of its input and output to end, a variable so tests need not
// wait for it.
var runWaitDelay = 5 * time.Second
