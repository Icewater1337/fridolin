package app

import (
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
)

// TestGoldenBwrapHostHome pins a brain's run in the container of a native
// install moved there (core migrate --from-native): its folder bound at
// the Mac's path on top of an empty HOME at the Mac's home folder, the
// credentials folder never bound, ~/.gitconfig read-only, the Plaud login
// with plaud.brain_access read-only but for tokens.json, and git's stored
// credentials masked.
func TestGoldenBwrapHostHome(t *testing.T) {
	sb := config.SandboxConfig{CredentialsDir: "/Users/you/.secrets", AllowPaths: []string{"/Users/you/shared"}}
	sp := bwrapProfiler(t, sb, map[string][]string{
		"main":   {"/Users/you/Main"},
		"family": {"/Users/you/Family"},
	}, map[string]string{"/Users/you/.plaud/tokens.json": "file", "/Users/you/.config/git": "dir"})
	sp.hostHome = "/Users/you"
	sp.home = "/Users/you"
	sp.PlaudShared = true
	got, err := sp.Profile("main")
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "sandbox/bwrap-host-home.args", []byte(got))
}

func TestContainerHostHome(t *testing.T) {
	t.Setenv(HostHomeVar, "/Users/you")
	t.Setenv("HOME", "/Users/you")
	t.Setenv("FRIDOLIN_CONTAINER", "")
	if got := ContainerHostHome(); got != "" {
		t.Errorf("outside the container: %q", got)
	}
}
