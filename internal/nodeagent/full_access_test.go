package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// fullAccessNode is a node that declares exec (hostname only) and
// terminal in root, and full-access when declare says so, approved as
// approve says.
func fullAccessNode(t *testing.T, root string, declare, approve bool) *Agent {
	t.Helper()
	caps := func(full bool) nodeproto.Capabilities {
		return nodeproto.Capabilities{
			Exec:       &nodeproto.ExecCap{Allow: []nodeproto.ExecRule{{Name: "hostname", Args: []string{}}}},
			Terminal:   &nodeproto.WorkstationCap{Roots: []string{root}},
			FullAccess: full,
		}
	}
	a := &Agent{Dir: t.TempDir(), Config: Config{Core: "x", Name: "n", Capabilities: caps(declare)}, Logf: t.Logf}
	a.setState(nodeproto.State{Device: nodeproto.DeviceApproved, Commands: nodeproto.CommandsApproved, Approved: caps(approve)})
	return a
}

func TestFullAccessNeedsTheNodesWordAndTheOwnersApproval(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("runs /bin/sh")
	}
	shell := `{"argv":["/bin/sh","-c","echo full"],"full":true}`
	for _, c := range []struct {
		name              string
		declare, approve  bool
		params, wantError string
	}{
		{"the default stays restricted", false, false, `{"argv":["/bin/sh","-c","echo full"]}`, nodeproto.CodeDenied},
		{"full asked, nothing declared or approved", false, false, shell, nodeproto.CodeDenied},
		{"approved by a core, not declared by the node", false, true, shell, nodeproto.CodeDenied},
		{"declared by the node, not approved", true, false, shell, nodeproto.CodeDenied},
		{"full access, but the call does not ask for it", true, true, `{"argv":["/bin/sh","-c","echo full"]}`, nodeproto.CodeDenied},
		{"both", true, true, shell, ""},
	} {
		a := fullAccessNode(t, t.TempDir(), c.declare, c.approve)
		res := a.handle(context.Background(), nodeproto.Request{Method: nodeproto.MethodExec, Params: []byte(c.params)})
		switch {
		case c.wantError != "" && (res.Error == nil || res.Error.Code != c.wantError):
			t.Errorf("%s: %+v, want %s", c.name, res.Error, c.wantError)
		case c.wantError == "" && res.Error != nil:
			t.Errorf("%s: %v", c.name, res.Error)
		case c.wantError == "":
			var out nodeproto.ExecResult
			if err := json.Unmarshal(res.Result, &out); err != nil || out.Stdout != "full\n" {
				t.Errorf("%s: %s, %v", c.name, res.Result, err)
			}
		}
	}
}

func TestAFullTerminalRunsWithoutTheSandbox(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("runs /bin/sh")
	}
	root := t.TempDir()
	open := func(a *Agent, full bool) nodeproto.Response {
		a.WorkSandbox = nodeproto.SandboxExec // what the Mac has; a full terminal leaves it out
		a.TerminalShell = []string{"/bin/sh"}
		raw, _ := json.Marshal(nodeproto.TerminalOpenParams{Folder: root, Full: full})
		return a.handle(context.Background(), nodeproto.Request{Method: nodeproto.MethodTerminalOpen, Params: raw})
	}
	if res := open(fullAccessNode(t, root, true, false), true); res.Error == nil || res.Error.Code != nodeproto.CodeDenied {
		t.Fatalf("a full terminal without the owner's approval: %+v", res.Error)
	}
	a := fullAccessNode(t, root, true, true)
	res := open(a, true)
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	var info nodeproto.TerminalInfo
	if err := json.Unmarshal(res.Result, &info); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = a.terminalClose(info.ID) })
	if info.Sandbox != nodeproto.SandboxNone {
		t.Errorf("a full terminal runs in %q, want none", info.Sandbox)
	}
}

func TestLookAgentFindsTheCLIBelowTheHomeFolder(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the home folders are looked in outside Windows")
	}
	home := t.TempDir()
	notFound := func(string) (string, error) { return "", exec.ErrNotFound }
	if _, err := lookAgentIn("claude", home, notFound); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("nothing installed: %v", err)
	}
	cli := filepath.Join(home, ".local", "bin", "claude")
	if err := os.MkdirAll(filepath.Dir(cli), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cli, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := lookAgentIn("claude", home, notFound); err != nil || got != cli {
		t.Fatalf("~/.local/bin/claude: %q, %v", got, err)
	}
	if got, _ := lookAgentIn("claude", home, func(string) (string, error) { return "/usr/bin/claude", nil }); got != "/usr/bin/claude" {
		t.Errorf("the PATH's comes first: %q", got)
	}
}
