package app

import (
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/version"
)

// A node whose sandboxes mount another fridolin than the core's is not
// ready until the core sent its own; one that names none is judged by
// its problem alone.
func TestSandboxNotReadyOnAnotherBin(t *testing.T) {
	t.Parallel()
	c := nodeproto.SandboxCap{Version: "v0.50.0", SignedIn: true, Daemon: true, Settings: true, Template: "fridolin-brain:x", Providers: []string{"claude"}}
	if why := sandboxNotReady(&c, "claude"); why != "" {
		t.Errorf("without bin: %q", why)
	}
	c.Bin = version.String()
	if why := sandboxNotReady(&c, "claude"); why != "" {
		t.Errorf("with the core's bin: %q", why)
	}
	c.Bin = "v0.0.1-other"
	if why := sandboxNotReady(&c, "claude"); !strings.Contains(why, "v0.0.1-other") {
		t.Errorf("with another bin: %q", why)
	}
}
