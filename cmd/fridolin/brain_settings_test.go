package main

import (
	"reflect"
	"testing"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

func TestMergeBrainSettingsProviderOverrides(t *testing.T) {
	for _, tc := range []struct {
		name, provider, model, effort, wantModel, wantEffort string
	}{
		{"Claude defaults", "claude", "", "", "sonnet", "low"},
		{"Claude manifest", "claude", "opus", "max", "opus", "max"},
		{"Claude model only", "claude", "opus", "", "opus", "low"},
		{"Codex ignores Claude manifest", "codex", "opus", "max", "codex-model", "high"},
		{"Codex defaults", "codex", "", "", "codex-model", "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := brainGlobals{defaults: brainSettings{provider: tc.provider, model: "sonnet", effort: "low", mediaDir: "/media", dataDir: "/data"}, codexModel: "codex-model", codexEffort: "high"}
			sp := manifest.Spec{Manifest: manifest.Manifest{ID: "a", Name: "Example", Model: tc.model, Effort: tc.effort, Observe: "all"}, PromptFile: "prompt.md"}
			s := mergeBrainSettings(g, sp)
			if s.model != tc.wantModel || s.effort != tc.wantEffort {
				t.Fatalf("provider settings = %q/%q, want %q/%q", s.model, s.effort, tc.wantModel, tc.wantEffort)
			}
			if s.name != "Example" || s.promptFile != "prompt.md" || s.observe != "all" || !reflect.DeepEqual(s.manifest, sp.Manifest) || s.mediaDir != "/media" || s.dataDir != "/data" {
				t.Fatalf("projection lost manifest or startup values: %+v", s)
			}
		})
	}
}

func TestMergeBrainSettingsOwnsMutableRunSlices(t *testing.T) {
	g := brainGlobals{defaults: brainSettings{
		disallowedTools: []string{"global-tool"}, otpMarkers: []string{"marker"}, envPassthrough: []string{"EXAMPLE_ENV"},
	}}
	manifestTools := []string{"manifest-tool"}
	for _, tc := range []struct {
		name  string
		tools *[]string
		want  []string
	}{
		{"inherit", nil, []string{"global-tool"}},
		{"replace", &manifestTools, []string{"manifest-tool"}},
		{"clear", new([]string), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp := manifest.Spec{Manifest: manifest.Manifest{DisallowedTools: tc.tools}}
			s := mergeBrainSettings(g, sp)
			if !reflect.DeepEqual(s.disallowedTools, tc.want) {
				t.Fatalf("tools = %v, want %v", s.disallowedTools, tc.want)
			}
			if len(s.disallowedTools) != 0 {
				s.disallowedTools[0] = "changed"
			}
			s.otpMarkers[0], s.envPassthrough[0] = "changed", "CHANGED"
			if g.defaults.disallowedTools[0] != "global-tool" || manifestTools[0] != "manifest-tool" || g.defaults.otpMarkers[0] != "marker" || g.defaults.envPassthrough[0] != "EXAMPLE_ENV" {
				t.Fatal("one run changed its global or manifest settings")
			}
			next := mergeBrainSettings(g, sp)
			if !reflect.DeepEqual(next.disallowedTools, tc.want) || next.otpMarkers[0] != "marker" || next.envPassthrough[0] != "EXAMPLE_ENV" {
				t.Fatal("one run changed the next run's settings")
			}
		})
	}
}
