package brain

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
			g := Globals{Defaults: Settings{Provider: tc.provider, Model: "sonnet", Effort: "low", MediaDir: "/media", DataDir: "/data"}, CodexModel: "codex-model", CodexEffort: "high"}
			sp := manifest.Spec{Manifest: manifest.Manifest{ID: "a", Name: "Example", Model: tc.model, Effort: tc.effort, Observe: "all"}, PromptFile: "prompt.md"}
			s := MergeSettings(g, sp)
			if s.Model != tc.wantModel || s.Effort != tc.wantEffort {
				t.Fatalf("provider settings = %q/%q, want %q/%q", s.Model, s.Effort, tc.wantModel, tc.wantEffort)
			}
			if s.Name != "Example" || s.PromptFile != "prompt.md" || s.Observe != "all" || !reflect.DeepEqual(s.Manifest, sp.Manifest) || s.MediaDir != "/media" || s.DataDir != "/data" {
				t.Fatalf("projection lost manifest or startup values: %+v", s)
			}
		})
	}
}

func TestMergeBrainSettingsOwnsMutableRunSlices(t *testing.T) {
	g := Globals{Defaults: Settings{
		DisallowedTools: []string{"global-tool"}, OTPMarkers: []string{"marker"}, EnvPassthrough: []string{"EXAMPLE_ENV"},
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
			s := MergeSettings(g, sp)
			if !reflect.DeepEqual(s.DisallowedTools, tc.want) {
				t.Fatalf("tools = %v, want %v", s.DisallowedTools, tc.want)
			}
			if len(s.DisallowedTools) != 0 {
				s.DisallowedTools[0] = "changed"
			}
			s.OTPMarkers[0], s.EnvPassthrough[0] = "changed", "CHANGED"
			if g.Defaults.DisallowedTools[0] != "global-tool" || manifestTools[0] != "manifest-tool" || g.Defaults.OTPMarkers[0] != "marker" || g.Defaults.EnvPassthrough[0] != "EXAMPLE_ENV" {
				t.Fatal("one run changed its global or manifest settings")
			}
			next := MergeSettings(g, sp)
			if !reflect.DeepEqual(next.DisallowedTools, tc.want) || next.OTPMarkers[0] != "marker" || next.EnvPassthrough[0] != "EXAMPLE_ENV" {
				t.Fatal("one run changed the next run's settings")
			}
		})
	}
}
