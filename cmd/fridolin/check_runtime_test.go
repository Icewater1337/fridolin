package main

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/registry"
)

func TestCheckRoutesRetainsUnavailableClaimsAndProfilePaths(t *testing.T) {
	root := t.TempDir()
	active := manifest.Spec{Dir: t.TempDir(), Manifest: manifest.Manifest{ID: "active"}}
	missing := manifest.Spec{
		Dir: filepath.Join(root, "missing"), Path: filepath.Join(root, "configured"),
		Manifest: manifest.Manifest{ID: "missing", Routing: manifest.Routing{Default: true}},
	}
	issues := []registry.Issue{{ID: "held", Kind: "missing", AtLoad: true, Routing: manifest.Routing{HandlesDMs: true}}}
	routes, dirs, err := checkRoutes([]manifest.Spec{active, missing}, issues)
	if err != nil {
		t.Fatal(err)
	}
	if id := routes.For(testGroup, true); id != "" || routes.HeldFor(testGroup, true) != "missing" {
		t.Fatalf("group routed to %q, held by %q; want missing default reserved", id, routes.HeldFor(testGroup, true))
	}
	if id := routes.For(testDMChat, false); id != "" || routes.HeldFor(testDMChat, false) != "held" {
		t.Fatalf("DM routed to %q, held by %q; want unresolved DM reserved", id, routes.HeldFor(testDMChat, false))
	}
	if !slices.Equal(dirs["missing"], []string{missing.Path, missing.Dir}) || !slices.Equal(dirs["active"], []string{active.Dir}) {
		t.Fatalf("profile paths = %v; want active and both unavailable spellings", dirs)
	}
}

func TestCheckRoutesValidatesBeforeOpeningFolders(t *testing.T) {
	root := t.TempDir()
	_, _, err := checkRoutes([]manifest.Spec{
		{Dir: filepath.Join(root, "one"), Manifest: manifest.Manifest{ID: "same"}},
		{Dir: filepath.Join(root, "two"), Manifest: manifest.Manifest{ID: "same"}},
	}, nil)
	if err == nil {
		t.Fatal("duplicate IDs were accepted after unavailable folders were dropped")
	}
}
