package nodeagent

import (
	"context"
	"os"
	"runtime"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// sandboxBinWrite is sandbox.bin_write: a chunk of the binary, kept with
// the update transfers.
func (a *Agent) sandboxBinWrite(p nodeproto.FrideChunkParams) (any, *nodeproto.Error) {
	if a.Dir == "" {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", errNoStateDir)
	}
	if p.ID == "" {
		if p.Offset != 0 {
			return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "a new transfer starts at offset 0")
		}
		p.ID = newTransferID()
	}
	path, e := a.updateTransferPath(p.ID)
	if e != nil {
		return nil, e
	}
	if p.Offset+int64(len(p.Data)) > nodeproto.MaxUpdateTransferBytes {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "a binary is at most %d bytes", nodeproto.MaxUpdateTransferBytes)
	}
	tr, e := appendChunk(path, p)
	if e != nil {
		return nil, e
	}
	return tr, nil
}

// sandboxBinApply is sandbox.bin: the sent binary replaces the one the
// sandboxes mount, once its signature with the core's update key, its
// platform and its SHA-256 check out. A run under way keeps the file it
// started with.
func (a *Agent) sandboxBinApply(ctx context.Context, p nodeproto.UpdateApplyParams) (any, *nodeproto.Error) {
	if a.Dir == "" {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", errNoStateDir)
	}
	if err := p.Check(); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	path, e := a.updateTransferPath(p.ID)
	if e != nil {
		return nil, e
	}
	defer os.Remove(path)
	if p.OS != "linux" || p.Arch != runtime.GOARCH {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "a binary for %s/%s; this node's sandboxes are linux/%s", p.OS, p.Arch, runtime.GOARCH)
	}
	key, e := a.trustedCoreKey()
	if e != nil {
		return nil, e
	}
	if !nodeproto.VerifyUpdate(key, p) {
		return nil, nodeproto.Errorf(nodeproto.CodeDenied, "the binary's signature does not verify with the core's update key %s", nodeproto.KeyFingerprint(key))
	}
	if sum, err := fileSHA256(path); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeNotFound, "no transfer %s", p.ID)
	} else if sum != p.TransferSHA256 {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "the transfer's SHA-256 is %s, the core sent %s: it changed on the way", sum, p.TransferSHA256)
	}
	if err := a.placeSandboxBin(path, p); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	a.logf("node: the sandboxes' linux fridolin is %s now", p.Version)
	if a.sandboxCheck(ctx, true) {
		a.declChanged()
	}
	return struct{}{}, nil
}
