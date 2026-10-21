package sandbox

import (
	"slices"
	"testing"
)

// A native install moved into the container: the run's HOME is the host's
// home folder, an empty tmpfs mounted before the binds that lie in it.
func TestBwrapArgsHostHome(t *testing.T) {
	p := BwrapPolicy{Brain: "a", Home: "/data/brains/a", Passwd: "/p", Group: "/g", HostHome: "/Users/you/",
		System: []string{"/usr"}, Write: []string{"/Users/you/brain", "/data/brains/a"}, Kind: func(string) string { return "dir" }}
	args, err := BwrapArgs(p, "/usr/local/bin/codex", nil)
	if err != nil {
		t.Fatal(err)
	}
	tmpfs := slices.Index(args, "/Users/you") - 1
	bind := slices.Index(args, "/Users/you/brain") - 1
	if tmpfs < 0 || args[tmpfs] != "--tmpfs" || bind < tmpfs || args[bind] != "--bind" {
		t.Errorf("the home tmpfs is not before the brain's bind: %q", args)
	}
	if i := slices.Index(args, "HOME"); i < 0 || args[i+1] != "/Users/you" {
		t.Errorf("HOME: %q", args)
	}
	if p.RunHome() != "/Users/you" || (BwrapPolicy{Home: "/h"}).RunHome() != "/h" {
		t.Error("RunHome")
	}
	p.HostHome = "relative"
	if _, err := BwrapArgs(p, "/x", nil); err == nil {
		t.Error("a relative host home was taken")
	}
}
