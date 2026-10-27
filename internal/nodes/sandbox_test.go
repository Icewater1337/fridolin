package nodes

import (
	"slices"
	"testing"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// A node that starts to offer sandbox asks for the commands approval
// again, like fride; once approved, it is covered whatever sbx reports
// later, and the plain sentence says what it means.
func TestSandboxNeedsTheCommandsApproval(t *testing.T) {
	r, err := OpenRegistry(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := r.NewJoinCode()
	h := hello(t, code, nodeproto.Capabilities{Host: true})
	if _, err := r.Hello(h, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ApproveDevice(h.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ApproveCommands(h.DeviceID); err != nil {
		t.Fatal(err)
	}
	h.JoinCode = ""
	h.Capabilities.Sandbox = &nodeproto.SandboxCap{Version: "v0.50.0", SignedIn: true, Daemon: true, Settings: true, Providers: []string{"claude"}}
	n, err := r.Hello(h, "")
	if err != nil || n.Commands() != nodeproto.CommandsPending || n.Effective().Sandbox != nil || n.Effective().Allows(nodeproto.MethodSandboxRun) {
		t.Fatalf("sandbox offered: %+v %v", n, err)
	}
	if !slices.Contains(n.Declared.Plain(), "run brains in Docker Sandboxes on this machine") {
		t.Errorf("the approval does not say what sandbox means: %q", n.Declared.Plain())
	}
	if n, err = r.ApproveCommands(h.DeviceID); err != nil || n.Commands() != nodeproto.CommandsApproved || !n.Effective().Allows(nodeproto.MethodSandboxRun) {
		t.Fatalf("approved: %+v %v", n, err)
	}
	// sbx's state changes: still approved, and the core sees the new state.
	h.Capabilities.Sandbox = &nodeproto.SandboxCap{Version: "v0.51.0", Problem: "sbx is not signed in"}
	if n, err = r.Hello(h, ""); err != nil || n.Commands() != nodeproto.CommandsApproved || n.Effective().Sandbox.Problem == "" {
		t.Fatalf("sbx's state changed: %+v %v", n, err)
	}
	if got := n.Effective().Summary(); got != "host, sandbox" {
		t.Errorf("summary %q", got)
	}
}

// The core refuses a call it can tell is wrong before sending it.
func TestSandboxCallsAreCheckedBeforeSending(t *testing.T) {
	s := &Service{}
	if _, err := s.SandboxRun(t.Context(), "n1", nodeproto.SandboxRunParams{Brain: "a"}); err == nil {
		t.Error("a run without a run id")
	}
	if _, err := s.SandboxEnsure(t.Context(), "n1", nodeproto.SandboxEnsureParams{Brain: "a", Folder: "rel"}); err == nil {
		t.Error("an ensure with a relative folder")
	}
	if _, err := s.SandboxRead(t.Context(), "n1", nodeproto.SandboxReadParams{Run: "r", WaitMS: 60000}); err == nil {
		t.Error("a read that waits a minute")
	}
	if err := s.SandboxStop(t.Context(), "n1", nodeproto.SandboxStopParams{Run: "r/1"}); err == nil {
		t.Error("a stop of a run id with a slash")
	}
	if err := s.SandboxRemove(t.Context(), "n1", "A B"); err == nil {
		t.Error("a remove of a brain that is no id")
	}
}
