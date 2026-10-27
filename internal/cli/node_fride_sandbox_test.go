package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
)

// The commands' arguments, and what they refuse before they run sbx: the
// tests never run the machine's sbx.
func TestNodeFrideSandbox(t *testing.T) {
	t.Parallel()
	dir, _ := joinedState(t, "mini")
	run := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := nodeCmd(append([]string{"fride", "sandbox"}, args...), &out, &errOut)
		return code, out.String(), errOut.String()
	}
	if code, out, _ := run(); code != 0 || !strings.Contains(out, "Docker Sandbox") || !strings.Contains(out, "17850") {
		t.Fatalf("help: %d %q", code, out)
	}
	if code, _, _ := run("sideways"); code != 2 {
		t.Error("an unknown command")
	}
	if code, _, _ := run("start", "extra", "--state-dir", dir); code != 2 {
		t.Error("start with an argument")
	}
	if code, _, e := run("join", "--state-dir", dir, "--core", "core:8766"); code != 2 {
		t.Errorf("join without a code: %d %q", code, e)
	}
	if code, _, e := run("join", "CODE", "--state-dir", dir, "--core", "core:8766"); code != 1 || !strings.Contains(e, "holds a node already") {
		t.Errorf("join into a node's state dir: %d %q", code, e)
	}
	call := func(sub string, f frideSandboxFlags, sbx string) (int, string) {
		var out, errOut bytes.Buffer
		code := frideSandbox(sub, f, dir, sbx, &out, &errOut)
		return code, errOut.String()
	}
	if code, e := call("status", frideSandboxFlags{}, ""); code != 1 || !strings.Contains(e, "sbx is not installed") {
		t.Errorf("status without sbx: %d %q", code, e)
	}
	if code, e := call("login", frideSandboxFlags{}, "/nonexistent/sbx"); code != 1 || !strings.Contains(e, "create first") {
		t.Errorf("login without a sandbox: %d %q", code, e)
	}
	if code, e := call("github", frideSandboxFlags{tokenStdin: true}, "/nonexistent/sbx"); code != 1 || !strings.Contains(e, "create first") {
		t.Errorf("github without a sandbox: %d %q", code, e)
	}
	if code, _, e := run("github", "--remove", "--token-stdin", "--state-dir", dir, "extra"); code != 2 {
		t.Errorf("github with an argument: %d %q", code, e)
	}
	if code, e := call("rm", frideSandboxFlags{}, "/nonexistent/sbx"); code != 1 || !strings.Contains(e, "--yes") {
		t.Errorf("rm without --yes and no terminal: %d %q", code, e)
	}
}

// join copies the running fridolin into the state dir, which the second
// node's service runs.
func TestCopyOwnBinary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := copyOwnBinary(dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(nodeagent.OwnBinary(dir))
	if err != nil || fi.Mode().Perm() != 0o755 || fi.Size() == 0 {
		t.Fatalf("%v %v", fi, err)
	}
}
