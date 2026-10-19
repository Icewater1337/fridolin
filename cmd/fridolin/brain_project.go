package main

import (
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/manifest"
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
