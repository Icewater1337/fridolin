package nodeproto

import (
	"slices"
	"strings"
	"testing"
)

// The sandbox capability allows its methods, needs approval once, and
// says what is missing before a brain can run in it.
func TestSandboxCap(t *testing.T) {
	t.Parallel()
	ready := &SandboxCap{Version: "0.50.0", SignedIn: true, Daemon: true, Settings: true, Template: "fridolin-brain:v1", Providers: []string{"claude"}}
	c := Capabilities{Sandbox: ready}
	for _, m := range SandboxMethods {
		if !c.Allows(m) || (Capabilities{}).Allows(m) {
			t.Errorf("%s: allowed %v with the capability, %v without", m, c.Allows(m), (Capabilities{}).Allows(m))
		}
	}
	if c.Empty() || Covers(Capabilities{}, c) || !Covers(c, c) {
		t.Error("sandbox needs the owner's approval, once")
	}
	if got := Intersect(c, c).Sandbox; got == nil || !slices.Equal(got.Providers, ready.Providers) {
		t.Errorf("Intersect lost the sandbox: %+v", got)
	}
	if !slices.Contains(c.Plain(), SandboxApproval) || c.Summary() != "sandbox" {
		t.Errorf("plain %q, summary %q", c.Plain(), c.Summary())
	}
	if why := ready.Ready("claude"); why != "" {
		t.Errorf("ready for claude: %s", why)
	}
	if why := ready.Ready("codex"); !strings.Contains(why, "codex has no login") {
		t.Errorf("codex: %q", why)
	}
	down := *ready
	down.Daemon = false
	if why := down.Ready("claude"); !strings.Contains(why, "daemon") || (Capabilities{Sandbox: &down}).Summary() != "sandbox (not ready)" {
		t.Errorf("daemon down: %q", why)
	}
	if err := (Capabilities{Sandbox: &SandboxCap{Problem: strings.Repeat("x", 2000)}}).Check(); err == nil {
		t.Error("a long problem passed the check")
	}
	if SandboxName("main") != "fridolin-main" {
		t.Error(SandboxName("main"))
	}
}
