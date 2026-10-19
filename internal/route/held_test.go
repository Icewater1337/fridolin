package route

import (
	"testing"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

func TestHeldRoutesPreserveExplicitAndImplicitFallback(t *testing.T) {
	const group = "120363000000000000@g.us"
	for _, tc := range []struct {
		name     string
		active   manifest.Routing
		held     manifest.Routing
		jid      string
		isGroup  bool
		wantRun  string
		wantHeld string
	}{
		{"held DM blocks implicit fallback", manifest.Routing{}, manifest.Routing{HandlesDMs: true}, "dm", false, "", "held"},
		{"held DM blocks explicit fallback", manifest.Routing{Default: true}, manifest.Routing{HandlesDMs: true}, "dm", false, "", "held"},
		{"held DM leaves groups alone", manifest.Routing{}, manifest.Routing{HandlesDMs: true}, group, true, "active", ""},
		{"held group blocks fallback", manifest.Routing{}, manifest.Routing{Groups: []string{group}}, group, true, "", "held"},
		{"held group leaves other groups alone", manifest.Routing{}, manifest.Routing{Groups: []string{group}}, "other", true, "active", ""},
		{"held group leaves DMs alone", manifest.Routing{}, manifest.Routing{Groups: []string{group}}, "dm", false, "active", ""},
		{"held default blocks implicit fallback", manifest.Routing{}, manifest.Routing{Default: true}, "dm", false, "", "held"},
		{"held default blocks unclaimed group", manifest.Routing{}, manifest.Routing{Default: true}, "other", true, "", "held"},
		{"active explicit default wins", manifest.Routing{Default: true}, manifest.Routing{Default: true}, "dm", false, "active", ""},
		{"active DM wins", manifest.Routing{HandlesDMs: true}, manifest.Routing{HandlesDMs: true, Default: true}, "dm", false, "active", ""},
		{"active group wins", manifest.Routing{Groups: []string{group}}, manifest.Routing{Groups: []string{group}, Default: true}, group, true, "active", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			table, err := BuildWithHeld([]manifest.Spec{routeSpec("active", tc.active)}, []Held{{ID: "held", Routing: tc.held}})
			if err != nil {
				t.Fatal(err)
			}
			if run, held := table.For(tc.jid, tc.isGroup), table.HeldFor(tc.jid, tc.isGroup); run != tc.wantRun || held != tc.wantHeld {
				t.Fatalf("route = (%q, %q), want (%q, %q)", run, held, tc.wantRun, tc.wantHeld)
			}
		})
	}
}

func TestHeldRoutesOwnValuesAndKeepFirstClaim(t *testing.T) {
	const group = "120363000000000000@g.us"
	routing := manifest.Routing{HandlesDMs: true, Default: true, Groups: []string{group}}
	held := []Held{{ID: "first", Routing: routing}, {ID: "second", Routing: routing}}
	table, err := BuildWithHeld(nil, held)
	if err != nil {
		t.Fatal(err)
	}
	held[0].ID = "mutated"
	held[0].Routing.Groups[0] = "mutated"
	for _, tc := range []struct {
		jid   string
		group bool
	}{{group, true}, {"other", true}, {"dm", false}} {
		if got := table.HeldFor(tc.jid, tc.group); got != "first" || table.For(tc.jid, tc.group) != "" {
			t.Fatalf("held route = %q; first unavailable claim was not preserved", got)
		}
	}
	// A fresh snapshot can restore the same ID without changing the held one.
	restored, err := Build([]manifest.Spec{routeSpec("first", manifest.Routing{HandlesDMs: true})})
	if err != nil {
		t.Fatal(err)
	}
	if restored.For("dm", false) != "first" || restored.HeldFor("dm", false) != "" || table.HeldFor("dm", false) != "first" {
		t.Fatal("restoring an ID changed an earlier routing snapshot")
	}
}
