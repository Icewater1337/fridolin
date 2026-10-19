package main

import (
	"testing"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

func TestReplyChatKeepsHeldDMAndRetiredBrainIdentity(t *testing.T) {
	const group = "120363000000000000@g.us"
	const owner = "15551234567@s.whatsapp.net"
	brain := func(id string, handlesDMs bool) *Brain {
		br := &Brain{id: id}
		br.settings.Store(&brainSettings{manifest: manifest.Manifest{ID: id, Routing: manifest.Routing{HandlesDMs: handlesDMs, Groups: []string{group}}}})
		return br
	}
	cfg := &Config{AllowedNumbers: []string{"15551234567"}}
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
			current := brain("a", false)
			selected := current
			if tc.retired {
				selected = brain("a", tc.handlesDMs)
			}
			mgr := &BrainManager{fallback: current, held: heldRoutes{dms: tc.heldDM}}
			b := &Bridge{brainMgr: mgr}
			if got := b.replyChatFor(selected, cfg); got != tc.want {
				t.Fatalf("reply chat = %q, want %q", got, tc.want)
			}
		})
	}
}
