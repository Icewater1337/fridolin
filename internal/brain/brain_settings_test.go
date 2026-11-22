package brain

import (
	"cmp"
	"reflect"
	"testing"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

func TestMergeBrainSettingsProviderOverrides(t *testing.T) {
	for _, tc := range []struct {
		name, provider, model, effort, wantModel, wantEffort string
		own, wantProvider                                    manifest.Provider
	}{
		{"Claude defaults", "claude", "", "", "sonnet", "low", "", ""},
		{"Claude manifest", "claude", "opus", "max", "opus", "max", "", ""},
		{"Claude model only", "claude", "opus", "", "opus", "low", "", ""},
		{"Codex ignores Claude manifest", "codex", "opus", "max", "codex-model", "high", "", ""},
		{"Codex defaults", "codex", "", "", "codex-model", "high", "", ""},
		{"Antigravity ignores Claude manifest", "antigravity", "opus", "max", "agy-model", "", "", ""},
		{"brain on Antigravity, bridge on Claude", "claude", "opus", "max", "agy-model", "", "antigravity", "antigravity"},
		{"brain on Claude, bridge on Codex", "codex", "opus", "max", "opus", "max", "claude", "claude"},
		{"brain on Codex, bridge on Antigravity", "antigravity", "", "", "codex-model", "high", "codex", "codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := Globals{Defaults: Settings{Provider: manifest.Provider(tc.provider), Model: "sonnet", Effort: "low", MediaDir: "/media", DataDir: "/data"}, CodexModel: "codex-model", CodexEffort: "high", AntigravityModel: "agy-model"}
			sp := manifest.Spec{Manifest: manifest.Manifest{ID: "a", Name: "Example", Model: tc.model, Effort: tc.effort, Observe: "all", Provider: tc.own}, PromptFile: "prompt.md"}
			s := MergeSettings(g, sp)
			if want := cmp.Or(tc.wantProvider, manifest.Provider(tc.provider)); s.Provider != want {
				t.Errorf("provider = %q, want %q", s.Provider, want)
			}
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
