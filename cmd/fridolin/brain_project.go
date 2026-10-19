package main

import (
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// settingsFor is the current composition boundary. The manager's later Apply
// seam receives brainGlobals directly; configuration and host discovery stay
// here rather than becoming imports of the brain runtime.
func settingsFor(cfg *config.Config, sp manifest.Spec) *brainSettings {
	return mergeBrainSettings(brainGlobalsOf(cfg), sp)
}

func brainGlobalsOf(cfg *config.Config) brainGlobals {
	return brainGlobals{
		codexModel: cfg.CodexModel, codexEffort: cfg.CodexEffort, runLimit: cfg.RunLimit(),
		defaults: brainSettings{
			claudePath: cfg.ClaudePath, provider: cfg.AgentProvider, codexPath: cfg.CodexPath,
			model: cfg.BrainModel, effort: cfg.BrainEffort,
			disallowedTools: append([]string(nil), cfg.DisallowedTools...),
			dangerously:     cfg.DangerouslySkipPermissions, inheritUser: cfg.InheritClaudeUserConfig,
			otpGuard: cfg.OTPGuard, otpMarkers: append([]string(nil), cfg.OTPMarkers...),
			viewer: cfg.Viewer, runTimeout: runTimeout(cfg), queueMode: cfg.Queue.Mode, maxPending: cfg.Queue.MaxPending,
			credsDir: credentialsDirOf(cfg), mediaDir: cfg.MediaDir,
			envPassthrough: append([]string(nil), cfg.ChildEnvPassthrough...), toolsDir: ownLayout().Tools,
			// Absolute: brains run in their own folder, where a relative data_dir
			// would name another one. R6 supplies these startup paths through Boot.
			dataDir: absPath(cfg.DataDir), dbPath: absPath(cfg.DBPath), plaudPATH: brainPlaudPATH(cfg),
		},
	}
}

// dataEnv tells brains where the bridge keeps its data. The tools in
// tools/ read these until the bridge answered them (brainapi.go); compat:
// brains and their scripts rely on these names, as on the other BRIDGE_
// variables.
func dataEnv(s *brainSettings) []string {
	return []string{"BRIDGE_DATA_DIR=" + s.dataDir, "BRIDGE_DB_PATH=" + s.dbPath}
}

// credentialsDirOf is where per-brain credential files live.
func credentialsDirOf(cfg *config.Config) string {
	if cfg.Sandbox.CredentialsDir != "" {
		return cfg.Sandbox.CredentialsDir
	}
	return cfg.DataDir
}

// startViewer is NewTmuxViewer, a variable so tests need no tmux.
var startViewer = NewTmuxViewer

func startBrainViewer(name, streamLog, cwd string, passthrough []string) (*platform.Viewer, error) {
	return startViewer(name, streamLog, cwd, childEnv(anyChild, passthrough))
}

func isBuildChat(jid string) bool { return brainOfBuildJID(jid) != "" }

func commandBrainInputRules() brainInputRules {
	return brainInputRules{
		Chats:     func(m chat.Inbound) []string { return chatJIDs(Inbound{Inbound: m}) },
		Observe:   func(mode string, m chat.Inbound) bool { return observeRuns(mode, Inbound{Inbound: m}) },
		BuildChat: isBuildChat,
	}
}

// claimBrainMedia retains the process migration lifecycle and resolves the
// working directory when the message is accepted, before history is written.
func claimBrainMedia(mediaDir, brainID string, paths []string) map[string]string {
	return mediaInboxAt(mediaDir).Claim(brainID, paths)
}
