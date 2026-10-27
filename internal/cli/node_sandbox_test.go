package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
)

// TestNodeSandbox turns Docker Sandboxes off and on in the state dir.
func TestNodeSandbox(t *testing.T) {
	dir, _ := joinedState(t, "mini")
	run := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := nodeCmd(append(append([]string{"sandbox"}, args...), "--state-dir", dir), &out, &errOut)
		return code, out.String(), errOut.String()
	}
	if code, out, _ := run(); code != 0 || !strings.Contains(out, "Docker Sandboxes") {
		t.Fatalf("help: %d %q", code, out)
	}
	if nodeagent.SandboxOff(dir) {
		t.Fatal("off before anything")
	}
	if code, out, e := run("off"); code != 0 || !strings.Contains(out, "turned off") || !nodeagent.SandboxOff(dir) {
		t.Fatalf("off: %d %q %q", code, out, e)
	}
	if code, out, e := run("on"); code != 0 || !strings.Contains(out, "on again") || nodeagent.SandboxOff(dir) {
		t.Fatalf("on: %d %q %q", code, out, e)
	}
	if code, _, _ := run("on"); code != 0 {
		t.Error("on twice")
	}
	if code, _, _ := run("sideways"); code != 2 {
		t.Error("an unknown command")
	}
	if code, _, e := run("off"); code != 0 {
		t.Fatal(e)
	}
	empty := t.TempDir()
	var out, errOut bytes.Buffer
	if code := nodeCmd([]string{"sandbox", "off", "--state-dir", empty}, &out, &errOut); code != 1 {
		t.Errorf("a folder that is no node's: %d %q", code, errOut.String())
	}
}
