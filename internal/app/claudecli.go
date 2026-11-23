package app

import (
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// ClaudeInfo finds the CLI and its version. The version is cached until the
// binary changes: `claude` on the PATH is a symlink the CLI moves to each
// new release it installs, and the dashboard must not show the old one for
// as long as the bridge runs. A `--version` run that failed is tried again
// after claudecli.FailedProbeRetry. A CLI that is not found is looked up
// again every time, so installing it shows at once. It runs the CLI with a
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

// AntigravityInfo is ClaudeInfo for agy, which a bare antigravity_path
// also finds in ~/.local/bin, where its installer puts it.
func AntigravityInfo(env Environment, path, home string, passthrough []string) claudecli.Status {
	return cliInfo(&cliVersions, env, agyPath(path, home), passthrough, antigravityChild)
}

// antigravityInfo is AntigravityInfo for the bridge.
func (b *App) antigravityInfo(cfg *config.Settings) claudecli.Status {
	return cliInfo(&b.cliVersions, b.env, agyPath(cfg.AntigravityPath, b.cfgs.Boot().Home), cfg.ChildEnvPassthrough, antigravityChild)
}

// agyPath is path, or, when it is a bare name the PATH does not have,
// the program of that name in home's ~/.local/bin, where agy's installer
// puts it and a service's PATH often misses it.
func agyPath(path, home string) string {
	if path == "" {
		path = "agy"
	}
	if _, err := exec.LookPath(path); err == nil || home == "" || filepath.Base(path) != path {
		return path
	}
	c := filepath.Join(home, ".local", "bin", path)
	if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
		return c
	}
	return path
}

// resolveAgentBinary is the absolute path of the agent CLI a run starts,
// found from the configured path on the bridge's own PATH (not the
// brain's, which a brain may change). The sandboxes need it absolute:
// bubblewrap binds and runs the program by its path, and sandbox-exec's
// profile allows it by path. A config that names the CLI bare ("claude",
// "codex") is the default, and what a native install moved into the
// container keeps once its Mac-only paths are dropped.
func resolveAgentBinary(provider manifest.Provider, path, home string) (string, error) {
	name, setting := "claude", "claude_path"
	switch provider {
	case manifest.ProviderCodex:
		name, setting = "codex", "codex_path"
	case manifest.ProviderAntigravity:
		name, setting = "agy", "antigravity_path"
		path = agyPath(cmp.Or(path, name), home)
	case manifest.ProviderClaude:
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
