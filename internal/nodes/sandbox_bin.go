package nodes

import (
	"context"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// The linux fridolin of the core's version, which every brain's sandbox
// on a node mounts read-only and runs as `fridolin mcp --run <id>`. The
// core sends it from its binaries folder, signed, as it sends updates
// (sandbox.bin_write, sandbox.bin), whenever a node whose sandbox is
// approved declares another version (SandboxCap.Bin): after the node's
// setup, and after each update of the core.

// sandboxBinRetry is how long the core waits before it tries again to
// send a node the binary, after a try that failed or is under way.
const sandboxBinRetry = 10 * time.Minute

func init() {
	slowCalls[nodeproto.MethodSandboxBin] = 2 * time.Minute
}

// SandboxBinary sends node id the linux build of the core's version for
// its sandboxes, for the node's architecture (a VM runs the host's).
func (s *Service) SandboxBinary(ctx context.Context, id string) error {
	n, err := s.reg.Get(id)
	if err != nil {
		return err
	}
	bin, path, err := s.Binary("linux", n.Arch)
	if err != nil {
		return err
	}
	key, err := s.updateKey()
	if err != nil {
		return err
	}
	tr, sum, err := s.sendFile(ctx, id, nodeproto.MethodSandboxBinWrite, path)
	if err != nil {
		return err
	}
	p := nodeproto.UpdateApplyParams{
		ID: tr.ID, Version: s.version, OS: "linux", Arch: n.Arch, SHA256: bin.SHA256, Size: bin.Size,
		TransferSHA256: sum, Signature: nodeproto.SignUpdate(key, s.version, "linux", n.Arch, bin.SHA256),
	}
	return s.Call(ctx, id, nodeproto.MethodSandboxBin, p, nil)
}

// maybeSandboxBin sends node id the binary when its approved sandbox,
// set up, declares another version than the core's; at most once per
// sandboxBinRetry.
func (s *Service) maybeSandboxBin(id string) {
	n, err := s.reg.Get(id)
	if err != nil {
		return
	}
	sb := n.Effective().Sandbox
	if sb == nil || sb.Template == "" || sb.Bin == s.version {
		return
	}
	s.mu.Lock()
	if t, ok := s.sandboxBin[id]; ok && time.Since(t) < sandboxBinRetry {
		s.mu.Unlock()
		return
	}
	s.sandboxBin[id] = time.Now()
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), sandboxBinRetry)
	defer cancel()
	if err := s.SandboxBinary(ctx, id); err != nil {
		s.logf("nodes: sending %s (%s) the linux fridolin %s for its sandboxes: %v", n.Name, id, s.version, err)
		return
	}
	s.mu.Lock()
	delete(s.sandboxBin, id)
	s.mu.Unlock()
	s.logf("nodes: %s (%s) has the linux fridolin %s for its sandboxes", n.Name, id, s.version)
}
