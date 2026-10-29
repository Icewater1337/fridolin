package app

import (
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
)

// TestGoldenBwrapHostHome pins a brain's run in the container of a native
// install moved there (core migrate --from-native): its folder bound at
// the Mac's path on top of an empty HOME at the Mac's home folder, the
// credentials folder never bound, ~/.gitconfig read-only, the container's
// own ~/.local/bin (the image's tool links) read-only, the Plaud login
// with plaud.brain_access read-only but for tokens.json, and git's stored
// credentials masked.
func TestGoldenBwrapHostHome(t *testing.T) {
	t.Parallel()
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
	// Serial: InContainer and ContainerHostHome read the process's
	// environment.
	t.Setenv(HostHomeVar, "/Users/you")
	setenv(t, "HOME", "/Users/you")
	t.Setenv("FRIDOLIN_CONTAINER", "")
	if got := ContainerHostHome(); got != "" {
		t.Errorf("outside the container: %q", got)
	}
}

// The Plaud login is shared writable whichever file the CLI keeps it in.
func TestBwrapSharesEveryPlaudLoginFile(t *testing.T) {
	t.Parallel()
	sp := bwrapProfiler(t, config.SandboxConfig{}, map[string][]string{"main": {"/Users/you/Main"}},
		map[string]string{"/Users/you/.plaud/plaud-secrets": "file"})
	sp.hostHome = "/Users/you"
	sp.home = "/Users/you"
	sp.PlaudShared = true
	got, err := sp.Profile("main")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "--bind\n/Users/you/.plaud/plaud-secrets\n/Users/you/.plaud/plaud-secrets") {
		t.Errorf("plaud-secrets not bound:\n%s", got)
	}
}
