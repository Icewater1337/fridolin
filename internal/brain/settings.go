package brain

import (
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// brainQueueInterrupt is the runtime protocol value. Composition pins it to
// the independently owned configuration enum without a runtime config import.
const QueueInterrupt = "interrupt"

// brainInputRules supplies content-only decisions from command composition.
// The runtime does not depend on transport identity or pipeline filtering.
type InputRules struct {
	Chats     func(chat.Inbound) []string
	Observe   func(string, chat.Inbound) bool
	BuildChat func(string) bool
}

// brainGlobals is the runtime-owned snapshot projected by the command. Its
// defaults contain no per-brain manifest or prompt selection. The two Codex
// values remain distinct because that provider ignores Claude overrides.
type Globals struct {
	Defaults                Settings
	CodexModel, CodexEffort string
	RunLimit                int
}

// mergeBrainSettings applies only the manifest's run overrides. It reads no
// process state and owns every mutable settings slice, as settingsFor did.
func MergeSettings(g Globals, sp manifest.Spec) *Settings {
	m := sp.Manifest
	s := g.Defaults
	s.Name, s.Manifest, s.PromptFile, s.Observe = m.Name, m, sp.PromptFile, m.Observe
	if m.Model != "" {
		s.Model = m.Model
	}
	if m.Effort != "" {
		s.Effort = m.Effort
	}
	tools := g.Defaults.DisallowedTools
	if m.DisallowedTools != nil {
		tools = *m.DisallowedTools
	}
	s.DisallowedTools = append([]string(nil), tools...)
	s.OTPMarkers = append([]string(nil), g.Defaults.OTPMarkers...)
	s.EnvPassthrough = append([]string(nil), g.Defaults.EnvPassthrough...)
	if s.Provider == "codex" {
		s.Model, s.Effort = g.CodexModel, g.CodexEffort
	}
	return &s
}
