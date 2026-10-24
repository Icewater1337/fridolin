package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/plaud"
)

// NewPlaudManager supplies settings and the paths captured for this bridge start.
func NewPlaudManager(cfgs *config.Store, brains *BrainManager,
	submit func(*brain.Brain, string, string, string, []string, func(error)) error,
	notify func(context.Context, string, string) error, siteURL func() string, fleet plaudFleet) *plaud.Manager {
	return plaud.New(plaudOptions(cfgs, brains, submit, notify, siteURL, fleet))
}

// plaudOptions projects command policy and host dependencies into the service.
func plaudOptions(cfgs *config.Store, brains *BrainManager,
	submit func(*brain.Brain, string, string, string, []string, func(error)) error,
	notify func(context.Context, string, string) error, siteURL func() string, fleet plaudFleet) plaud.Options {
	boot := cfgs.Boot()
	return plaud.Options{
		PlaudDir: boot.PlaudDir(), SiteDevices: boot.Layout().SiteDevices(), Home: boot.Home,
		Settings:   func() *plaud.Snapshot { return plaudSnapshotOf(RuntimeSettings(cfgs)) },
		Target:     func(id string) *plaud.Target { return plaudTargetOf(brains, id, submit) },
		HeldTarget: func() string { return brains.HeldFor("", false) },
		Notify:     notify,
		FindCLI:    findPlaudCLI, CLIVersion: plaudCLIVersion,
		Environ: plaudEnvironment, DesktopContext: withDesktop,
		Inbox: mediaInboxAt,
		RunInterrupted: func(err error) bool {
			return errors.Is(err, claudecli.ErrInterrupted) || errors.Is(err, brain.ErrDropped) || errors.Is(err, context.Canceled)
		},
		SiteURL: siteURL, AppSource: plaudAppSource, AndroidToolchain: androidToolchain,
		AppEnviron: func(ctx context.Context, javaHome, sdk string) []string {
			return childEnv(helperScope(ctx), nil, "JAVA_HOME="+javaHome, "ANDROID_HOME="+sdk)
		},
		AppBuilders: fleet.builders, BuildOnNode: fleet.build,
	}
}

// plaudSnapshotOf projects one config snapshot; a poll retains it for its full pass.
func plaudSnapshotOf(c config.Settings) *plaud.Snapshot {
	wire := config.EffectiveFile(&c)
	return &plaud.Snapshot{
		Plaud: plaud.Settings(wire.Plaud), DataDir: c.Boot.DataDir, MediaDir: c.Boot.MediaDir,
		QueueMaxPending: c.Live.Queue.MaxPending, OwnerChat: ownerChatNumbers(c.Live.AllowedNumbers), Interval: c.Live.Plaud.Interval,
		Zone: c.Live.Location(),
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
func brainPlaudPATH(cfg *config.Settings) string {
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

// plaudTargetOf captures the selected runtime, rather than looking it up again
// by ID when the transcript is finally submitted after filesystem work.
func plaudTargetOf(brains *BrainManager, id string, submit func(*brain.Brain, string, string, string, []string, func(error)) error) *plaud.Target {
	var br *brain.Brain
	if id != "" {
		br = brains.Get(id)
	} else {
		br = brains.BrainFor("", false)
	}
	if br == nil {
		return nil
	}
	return &plaud.Target{
		BrainID:   br.ID(),
		ReplyChat: func() string { return br.Manifest().ReplyChat },
		OtherOwner: func(jid string) string {
			if owner := brains.BrainFor(jid, false); owner != nil && owner != br {
				return owner.ID()
			}
			return ""
		},
		Submit: func(jid, sender, text string, media []string, done func(error)) error {
			return submit(br, jid, sender, text, media, done)
		},
	}
}

// plaudEnvironment resolves host variables at the original process boundary.
func plaudEnvironment(ctx context.Context, bin, home string) []string {
	return childEnv(helperScope(ctx), nil, "HOME="+home, "PATH="+plaudPATH(bin),
		"DO_NOT_TRACK=1", "PLAUD_TELEMETRY_DISABLED=1", "PLAUD_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1")
}
