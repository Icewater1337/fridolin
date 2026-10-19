package main

import (
	"os"
	"path/filepath"

	"github.com/Icewater1337/fridolin/internal/config"
)

// NewPlaudManager supplies settings and the paths captured for this bridge start.
func NewPlaudManager(cfgs *config.Store, brains *BrainManager) *PlaudManager {
	boot := cfgs.Boot()
	return newPlaudManager(PlaudOptions{
		PlaudDir: boot.PlaudDir(), Home: boot.Home,
		Settings: func() *PlaudSnapshot { return plaudSnapshotOf(runtimeConfig(cfgs)) },
	}, brains)
}

// plaudSnapshotOf projects one config snapshot; a poll retains it for its full pass.
func plaudSnapshotOf(c *config.Config) *PlaudSnapshot {
	return &PlaudSnapshot{
		Plaud: PlaudSettings(c.Plaud), DataDir: c.DataDir, MediaDir: c.MediaDir,
		QueueMaxPending: c.Queue.MaxPending, OwnerChat: ownerChat(c), Interval: c.Plaud.Interval(),
	}
}

// brainPlaudPATH is the PATH brains run with when plaud.brain_access lets
// them use Plaud's CLI, or "" to keep the bridge's own: the bridge's whole
// PATH, which brains run with anyway (unlike the bridge's own runs of the
// CLI, theirs are sandboxed), and the CLI's folder, since the bridge finds
// the CLI in nvm when it is not on its PATH, which under the service it
// often is not. That folder comes first only when it holds the node the
// CLI's #!/usr/bin/env node needs, as in plaudPATH. Any other comes last:
// brains find `plaud` there all the same, and nothing a brain put there
// runs in place of a program on the bridge's PATH, such as the claude that
// sandbox-exec starts for every brain's run. The sandbox also keeps brains
// from writing to every folder on that PATH (see bridgeProgramsOf).
func brainPlaudPATH(cfg *config.Config) string {
	if !cfg.Plaud.BrainAccess {
		return ""
	}
	bin, err := findPlaudCLI(cfg.Plaud.CLIPath)
	if err != nil {
		return ""
	}
	path := os.Getenv("PATH") + ":/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
	if dir := cliNodeDir(bin); dir != "" {
		return dir + ":" + path
	}
	return path + ":" + filepath.Dir(bin)
}
