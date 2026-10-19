package main

import (
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// brainQueueInterrupt is the runtime protocol value. Composition pins it to
// the independently owned configuration enum without a runtime config import.
const brainQueueInterrupt = "interrupt"

// brainInputRules supplies content-only decisions from command composition.
// The runtime does not depend on transport identity or pipeline filtering.
type brainInputRules struct {
	Chats     func(chat.Inbound) []string
	Observe   func(string, chat.Inbound) bool
	BuildChat func(string) bool
}

// brainGlobals is the runtime-owned snapshot projected by the command. Its
// defaults contain no per-brain manifest or prompt selection. The two Codex
// values remain distinct because that provider ignores Claude overrides.
type brainGlobals struct {
	defaults                brainSettings
	codexModel, codexEffort string
	runLimit                int
}

// mergeBrainSettings applies only the manifest's run overrides. It reads no
// process state and owns every mutable settings slice, as settingsFor did.
func mergeBrainSettings(g brainGlobals, sp manifest.Spec) *brainSettings {
	m := sp.Manifest
	s := g.defaults
	s.name, s.manifest, s.promptFile, s.observe = m.Name, m, sp.PromptFile, m.Observe
	if m.Model != "" {
		s.model = m.Model
	}
	if m.Effort != "" {
		s.effort = m.Effort
	}
	tools := g.defaults.disallowedTools
	if m.DisallowedTools != nil {
		tools = *m.DisallowedTools
	}
	s.disallowedTools = append([]string(nil), tools...)
	s.otpMarkers = append([]string(nil), g.defaults.otpMarkers...)
	s.envPassthrough = append([]string(nil), g.defaults.envPassthrough...)
	if s.provider == "codex" {
		s.model, s.effort = g.codexModel, g.codexEffort
	}
	return &s
}
