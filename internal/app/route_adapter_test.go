package app

import (
	"testing"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/route"
)

func TestReplyChatKeepsHeldDMAndRetiredBrainIdentity(t *testing.T) {
	const group = "120363000000000000@g.us"
	const owner = "15551234567@s.whatsapp.net"

	cfg := &config.File{AllowedNumbers: []string{"15551234567"}}
	for _, tc := range []struct {
		name       string
		heldDM     string
		retired    bool
		handlesDMs bool
		want       string
	}{
		{"current fallback", "", false, false, owner},
		{"held DM blocks fallback", "missing", false, false, group},
		{"retired pointer is not the replacement", "", true, false, group},
		{"retired explicit DM setting remains", "", true, true, owner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core := brain.NewManager(brain.Options{})
			t.Cleanup(core.Stop)
			makeSpec := func(dm bool) manifest.Spec {
				return manifest.Spec{Dir: t.TempDir(), Manifest: manifest.Manifest{ID: "a", Routing: manifest.Routing{HandlesDMs: dm, Groups: []string{group}}}}
			}
			var selected *brain.Brain
			if tc.retired {
				if _, err := core.Apply([]manifest.Spec{makeSpec(tc.handlesDMs)}, brain.Globals{}, brain.ApplyOptions{}); err != nil {
					t.Fatal(err)
				}
				selected = core.Get("a")
				if _, err := core.Apply(nil, brain.Globals{}, brain.ApplyOptions{}); err != nil {
					t.Fatal(err)
				}
			}

			var held []route.Held
			if tc.heldDM != "" {
				held = []route.Held{{ID: tc.heldDM, Routing: manifest.Routing{HandlesDMs: true}}}
			}
			if _, err := core.Apply([]manifest.Spec{makeSpec(false)}, brain.Globals{}, brain.ApplyOptions{Held: held}); err != nil {
				t.Fatal(err)
			}
			if selected == nil {
				selected = core.Get("a")
			}
			mgr := &BrainManager{Manager: core}
			b := &App{brainMgr: mgr}
			if got := b.replyChatFor(selected, cfg); got != tc.want {
				t.Fatalf("reply chat = %q, want %q", got, tc.want)
			}
		})
	}
}
