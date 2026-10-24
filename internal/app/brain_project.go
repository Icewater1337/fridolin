package app

import (
	"context"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/platform"
	prompttext "github.com/Icewater1337/fridolin/internal/prompt"
)

// settingsFor is the current composition boundary. The manager's later Apply
// seam receives brainGlobals directly; configuration and host discovery stay
// here rather than becoming imports of the brain runtime.
func settingsFor(cfg *config.Settings, sp manifest.Spec) *brain.Settings {
	return brain.MergeSettings(brainGlobalsOf(cfg, defaultWaits().runTimeoutUnit), sp)
}

// brainGlobalsOf are the brains' settings from cfg, with timeoutUnit what
// one of run_timeout_minutes is.
func brainGlobalsOf(cfg *config.Settings, timeoutUnit time.Duration) brain.Globals {
	return brain.Globals{
		CodexModel: cfg.CodexModel, CodexEffort: cfg.CodexEffort, RunLimit: cfg.MaxConcurrentRuns,
		Defaults: brain.Settings{
			ClaudePath: cfg.ClaudePath, Provider: string(cfg.AgentProvider), CodexPath: cfg.CodexPath,
			Model: cfg.BrainModel, Effort: cfg.BrainEffort,
			DisallowedTools: append([]string(nil), cfg.DisallowedTools...),
			Dangerously:     cfg.DangerouslySkipPermissions, InheritUser: cfg.InheritClaudeUserConfig,
			OTPGuard: cfg.OTPGuard, OTPMarkers: append([]string(nil), cfg.OTPMarkers...),
			Viewer: cfg.Viewer, RunTimeout: runTimeout(cfg, timeoutUnit), QueueMode: string(cfg.Queue.Mode), MaxPending: cfg.Queue.MaxPending,
			CredsDir: CredentialsDir(cfg), MediaDir: cfg.MediaDir,
			EnvPassthrough: append([]string(nil), cfg.ChildEnvPassthrough...), ToolsDir: ownLayout().Tools,
			// Absolute: brains run in their own folder, where a relative data_dir
			// would name another one. R6 supplies these startup paths through Boot.
			DataDir: AbsPath(cfg.DataDir), DBPath: AbsPath(cfg.DBPath), PlaudPATH: brainPlaudPATH(cfg),
			Vault:      cfg.Vault.Enabled,
			FrideTools: cfg.NodesEnabled,
		},
	}
}

// dataEnv tells brains where the bridge keeps its data. The tools in
// tools/ read these until the bridge answered them (brainapi.go); compat:
// brains and their scripts rely on these names, as on the other BRIDGE_
// variables.
func dataEnv(s *brain.Settings) []string {
	return []string{"BRIDGE_DATA_DIR=" + s.DataDir, "BRIDGE_DB_PATH=" + s.DBPath}
}

// startViewer is NewTmuxViewer, a variable so tests need no tmux.
var startViewer = NewTmuxViewer

// brainViewerStart starts a brain's activity viewer with the part of the
// bridge's environment env that any program gets.
func brainViewerStart(env Environment) func(name, streamLog, cwd string, passthrough []string) (*platform.Viewer, error) {
	return func(name, streamLog, cwd string, passthrough []string) (*platform.Viewer, error) {
		return startViewer(name, streamLog, cwd, childEnv(env, anyChild, passthrough))
	}
}

func isBuildChat(jid string) bool { return brainOfBuildJID(jid) != "" }

func commandBrainInputRules(hooks *BrainHooks) brain.InputRules {
	return brain.InputRules{
		Chats:     func(m chat.Inbound) []string { return chatJIDs(brain.Inbound{Inbound: m}) },
		Observe:   func(mode string, m chat.Inbound) bool { return observeRuns(mode, brain.Inbound{Inbound: m}) },
		BuildChat: isBuildChat,
		Recall: func(r brain.RecallRequest) []prompttext.Recalled {
			if hooks == nil || hooks.Recall == nil {
				return nil
			}
			return hooks.Recall(r)
		},
		Hydrate: func(ctx context.Context, brainID string, paths []string) {
			if hooks == nil || hooks.Hydrate == nil {
				return
			}
			hooks.Hydrate(ctx, brainID, paths)
		},
	}
}

// claimBrainMedia retains the process migration lifecycle and resolves the
// working directory when the message is accepted, before history is written.
func claimBrainMedia(mediaDir, brainID string, paths []string) map[string]string {
	return mediaInboxAt(mediaDir).Claim(brainID, paths)
}
