package route

import (
	"slices"
	"testing"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

func routeSpec(id string, routing manifest.Routing) manifest.Spec {
	return manifest.Spec{Manifest: manifest.Manifest{ID: id, Name: id, Routing: routing}, Dir: "/brains/" + id}
}

func TestRouteValidationPreservesErrorsAndOrder(t *testing.T) {
	a := routeSpec("a", manifest.Routing{})
	b := routeSpec("b", manifest.Routing{})
	for _, tc := range []struct {
		name string
		edit func([]manifest.Spec)
		want string
	}{
		{"duplicate id", func(s []manifest.Spec) { s[1].Manifest.ID = "a" }, `two brains use the id "a" (/brains/a and /brains/b)`},
		{"duplicate group", func(s []manifest.Spec) {
			s[0].Manifest.Routing.Groups, s[1].Manifest.Routing.Groups = []string{"group@g.us"}, []string{"group@g.us"}
		}, `group group@g.us is claimed by both "a" and "b"`},
		{"two DM brains", func(s []manifest.Spec) {
			s[0].Manifest.Routing.HandlesDMs, s[1].Manifest.Routing.HandlesDMs = true, true
		}, `brains "a" and "b" both handle DMs`},
		{"two defaults", func(s []manifest.Spec) {
			s[0].Manifest.Routing.Default, s[1].Manifest.Routing.Default = true, true
		}, `brains "a" and "b" are both marked default`},
		{"same folder", func(s []manifest.Spec) { s[1].Dir = s[0].Dir }, `brains "a" and "b" use the same folder /brains/a`},
		{"nested folder", func(s []manifest.Spec) { s[1].Dir = "/brains/a/b" }, `brain folder /brains/a/b is inside brain folder /brains/a`},
		{"identity before folder", func(s []manifest.Spec) {
			s[1].Manifest.ID, s[1].Dir = "a", s[0].Dir
		}, `two brains use the id "a" (/brains/a and /brains/a)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			specs := []manifest.Spec{a, b}
			tc.edit(specs)
			if err := Validate(specs); err == nil || err.Error() != tc.want {
				t.Fatalf("validation = %v, want %q", err, tc.want)
			}
			if table, err := Build(specs); table != nil || err == nil || err.Error() != tc.want {
				t.Fatalf("build = %+v, %v", table, err)
			}
		})
	}
}

func TestRouteTableSelectionAndOwnedValues(t *testing.T) {
	const group = "120363000000000000@g.us"
	const owner = "15551234567@s.whatsapp.net"
	specs := []manifest.Spec{
		routeSpec("first", manifest.Routing{}),
		routeSpec("dm", manifest.Routing{HandlesDMs: true, Groups: []string{group}}),
		routeSpec("fallback", manifest.Routing{Default: true}),
	}
	table, err := Build(specs)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		jid   string
		group bool
		want  string
	}{{group, true, "dm"}, {"unclaimed@g.us", true, "fallback"}, {owner, false, "dm"}} {
		if got := table.For(tc.jid, tc.group); got != tc.want {
			t.Errorf("For(%q,%v) = %q, want %q", tc.jid, tc.group, got, tc.want)
		}
	}
	if !table.Has("dm") || table.Has("missing") || table.Default() != "fallback" {
		t.Fatal("membership or default changed")
	}
	dirs := table.Dirs()
	if !slices.Equal(dirs, []string{"/brains/first", "/brains/dm", "/brains/fallback"}) {
		t.Fatalf("folders = %v", dirs)
	}
	dirs[0] = "changed"
	specs[1].Manifest.Routing.Groups[0] = "changed"
	if table.Dirs()[0] != "/brains/first" || table.For(group, true) != "dm" || table.ReplyChat("dm", "") != group {
		t.Fatal("table shares mutable construction data")
	}
	if table.ReplyChat("dm", owner) != owner || table.ReplyChat("missing", owner) != "" {
		t.Fatal("DM reply priority or unknown brain changed")
	}
}

func TestRouteReplyChatAndFallbackCases(t *testing.T) {
	const dm = "15551234567@s.whatsapp.net"
	const group = "120363000000000000@g.us"
	for _, tc := range []struct {
		name        string
		routing     manifest.Routing
		reply       string
		owner       string
		precedingDM bool
		want        string
	}{
		{"DM before group", manifest.Routing{HandlesDMs: true, Groups: []string{group}}, "", dm, false, dm},
		{"default before group", manifest.Routing{Default: true, Groups: []string{group}}, "", dm, false, dm},
		{"first fallback before group", manifest.Routing{Groups: []string{group}}, "", dm, false, dm},
		{"group brain", manifest.Routing{Groups: []string{group}}, "", dm, true, group},
		{"no chat", manifest.Routing{}, "", dm, true, ""},
		{"no owner", manifest.Routing{Groups: []string{group}}, "", "", false, group},
		{"explicit reply", manifest.Routing{HandlesDMs: true}, group, dm, false, group},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := routeSpec("a", tc.routing)
			spec.Manifest.ReplyChat = tc.reply
			var specs []manifest.Spec
			if tc.precedingDM {
				specs = append(specs, routeSpec("owner", manifest.Routing{HandlesDMs: true}))
			}
			specs = append(specs, spec)
			table, err := Build(specs)
			if err != nil {
				t.Fatal(err)
			}
			if got := table.ReplyChat("a", tc.owner); got != tc.want {
				t.Fatalf("reply chat = %q, want %q", got, tc.want)
			}
		})
	}
	empty, err := Build(nil)
	if err != nil || empty.For("", false) != "" || empty.For(group, true) != "" || empty.Default() != "" || len(empty.Dirs()) != 0 {
		t.Fatalf("empty table = %+v, %v", empty, err)
	}
}
