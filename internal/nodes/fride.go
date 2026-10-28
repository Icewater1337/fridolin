package nodes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// slowCalls are methods that may take longer than callTimeout: an export
// stops a task's agents and stack and writes its bundle, an import checks
// it out again, a close writes the rationale and stops the stack.
var slowCalls = map[string]time.Duration{
	nodeproto.MethodFrideExport: 15 * time.Minute,
	nodeproto.MethodFrideImport: 15 * time.Minute,
	nodeproto.MethodFrideClose:  5 * time.Minute,
	nodeproto.MethodFrideDelete: 5 * time.Minute,
	// A clone fetches the whole history; a new repository may be pushed
	// to GitHub.
	nodeproto.MethodFrideRepoClone: 15 * time.Minute,
	nodeproto.MethodFrideRepoNew:   5 * time.Minute,
	// An instance's start may make its sandbox from the template, boot
	// it and clone its repositories; a stop waits for its VM, a remove
	// saves its history first.
	nodeproto.MethodFrideInstanceStart:  20 * time.Minute,
	nodeproto.MethodFrideInstanceStop:   5 * time.Minute,
	nodeproto.MethodFrideInstanceRemove: 10 * time.Minute,
	// A Gradle build: the node's own timeout, and time to stage the APK.
	nodeproto.MethodAndroidBuild: time.Duration(nodeproto.AndroidBuildTimeoutSeconds)*time.Second + 2*time.Minute,
	// core.check runs setup-machine --core, which asks Docker, and reads
	// the core's status and secret files through one-off containers.
	nodeproto.MethodCoreCheck: 5 * time.Minute,
	// A host that never ran this version pulls the whole image (GBs; the
	// agent gives it an hour), and a rehearsal imports the latest export.
	nodeproto.MethodCoreImagePull: 45 * time.Minute,
	nodeproto.MethodCoreRehearse:  30 * time.Minute,
	// The secret files go through a one-off container on each side.
	nodeproto.MethodCoreSecretsSend:    6 * time.Minute,
	nodeproto.MethodCoreSecretsReceive: 6 * time.Minute,
	// Forgetting reads the lease and removes the data dir and the secret
	// files through one-off containers.
	nodeproto.MethodCoreSecretsForget: 10 * time.Minute,
}

// FrideTask hands the FrIDE of node id a message.
func (s *Service) FrideTask(ctx context.Context, id string, p nodeproto.FrideTaskParams) (nodeproto.FrideThread, error) {
	var out nodeproto.FrideThread
	return out, s.Call(ctx, id, nodeproto.MethodFrideTask, p, &out)
}

// FrideStatus reads a thread of node id's FrIDE, with its task.
func (s *Service) FrideStatus(ctx context.Context, id string, thread int) (nodeproto.FrideStatus, error) {
	return s.FrideStatusOn(ctx, id, "", thread)
}

// FrideStatusOn reads a thread of instance's FrIDE on node id ("" for the
// node's own FrIDE), with its task.
func (s *Service) FrideStatusOn(ctx context.Context, id, instance string, thread int) (nodeproto.FrideStatus, error) {
	var out nodeproto.FrideStatus
	return out, s.Call(ctx, id, nodeproto.MethodFrideStatus, nodeproto.FrideStatusParams{FrideOn: on(instance), ThreadID: thread}, &out)
}

// FrideReply answers a thread of node id's FrIDE.
func (s *Service) FrideReply(ctx context.Context, id string, thread int, text string) (nodeproto.FrideThread, error) {
	return s.FrideReplyOn(ctx, id, "", thread, text)
}

// FrideReplyOn answers a thread of instance's FrIDE on node id.
func (s *Service) FrideReplyOn(ctx context.Context, id, instance string, thread int, text string) (nodeproto.FrideThread, error) {
	var out nodeproto.FrideThread
	return out, s.Call(ctx, id, nodeproto.MethodFrideReply, nodeproto.FrideReplyParams{FrideOn: on(instance), ThreadID: thread, Text: text}, &out)
}

// FrideTaskInfo reads a task of node id's FrIDE.
func (s *Service) FrideTaskInfo(ctx context.Context, id, task string) (nodeproto.FrideTask, error) {
	return s.FrideTaskInfoOn(ctx, id, "", task)
}

// FrideTaskInfoOn reads a task of instance's FrIDE on node id.
func (s *Service) FrideTaskInfoOn(ctx context.Context, id, instance, task string) (nodeproto.FrideTask, error) {
	var out nodeproto.FrideTask
	return out, s.Call(ctx, id, nodeproto.MethodFrideTaskInfo, nodeproto.FrideTaskRef{FrideOn: on(instance), TaskID: task}, &out)
}

// FrideClose closes a task of node id's FrIDE.
func (s *Service) FrideClose(ctx context.Context, id, task string) (nodeproto.FrideTask, error) {
	return s.FrideCloseOn(ctx, id, "", task)
}

// FrideCloseOn closes a task of instance's FrIDE on node id.
func (s *Service) FrideCloseOn(ctx context.Context, id, instance, task string) (nodeproto.FrideTask, error) {
	var out nodeproto.FrideTask
	return out, s.Call(ctx, id, nodeproto.MethodFrideClose, nodeproto.FrideTaskRef{FrideOn: on(instance), TaskID: task}, &out)
}

// FrideIdentities hands node id all the git identities of its FrIDE,
// with their tokens (docs/NODES.md, "Git identities for FrIDE").
func (s *Service) FrideIdentities(ctx context.Context, id string, p nodeproto.FrideIdentities) (nodeproto.FrideIdentitiesResult, error) {
	var out nodeproto.FrideIdentitiesResult
	return out, s.Call(ctx, id, nodeproto.MethodFrideIdentities, p, &out)
}

// FrideMem asks node id's FrIDE whether a stack may start now.
func (s *Service) FrideMem(ctx context.Context, id string) (nodeproto.FrideMem, error) {
	var out nodeproto.FrideMem
	return out, s.Call(ctx, id, nodeproto.MethodFrideMem, struct{}{}, &out)
}

// FrideDelete removes a task's workspace on node id; force drops what is not
// committed, for a task an archive holds.
func (s *Service) FrideDelete(ctx context.Context, id, task string, force bool) error {
	return s.FrideDeleteOn(ctx, id, "", task, force)
}

// FrideDeleteOn removes a task's workspace in instance's FrIDE on node id.
func (s *Service) FrideDeleteOn(ctx context.Context, id, instance, task string, force bool) error {
	return s.Call(ctx, id, nodeproto.MethodFrideDelete, nodeproto.FrideDeleteParams{FrideOn: on(instance), TaskID: task, Force: force}, nil)
}

// FrideRepoClone clones p.URL as p.Name into the base folder of node id's
// FrIDE.
func (s *Service) FrideRepoClone(ctx context.Context, id string, p nodeproto.FrideRepoParams) (nodeproto.FrideRepo, error) {
	var out nodeproto.FrideRepo
	return out, s.Call(ctx, id, nodeproto.MethodFrideRepoClone, p, &out)
}

// FrideRepoNew makes the repository p.Name in the base folder of node id's
// FrIDE.
func (s *Service) FrideRepoNew(ctx context.Context, id string, p nodeproto.FrideRepoParams) (nodeproto.FrideRepo, error) {
	var out nodeproto.FrideRepo
	return out, s.Call(ctx, id, nodeproto.MethodFrideRepoNew, p, &out)
}

// ErrTransfer is a bundle that did not arrive whole.
var ErrTransfer = errors.New("the bundle did not arrive whole")

// FrideExport exports a task of node id's FrIDE (p's instance's) into w,
// chunk by chunk, and returns its size and SHA-256, which it checks
// against the node's. The node's staged copy is removed after.
func (s *Service) FrideExport(ctx context.Context, id string, p nodeproto.FrideExportParams, w io.Writer) (int64, string, error) {
	var tr nodeproto.FrideTransfer
	if err := s.Call(ctx, id, nodeproto.MethodFrideExport, p, &tr); err != nil {
		return 0, "", err
	}
	defer func() {
		// A context of its own: the export's may be what ended.
		dctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		_ = s.Call(dctx, id, nodeproto.MethodFrideDrop, nodeproto.FrideChunkParams{FrideOn: p.FrideOn, ID: tr.ID}, nil)
	}()
	h := sha256.New()
	var n int64
	for {
		var c nodeproto.FrideChunk
		if err := s.Call(ctx, id, nodeproto.MethodFrideRead, nodeproto.FrideChunkParams{FrideOn: p.FrideOn, ID: tr.ID, Offset: n}, &c); err != nil {
			return n, "", err
		}
		if len(c.Data) > 0 {
			if _, err := w.Write(c.Data); err != nil {
				return n, "", err
			}
			h.Write(c.Data)
			n += int64(len(c.Data))
		}
		if c.EOF {
			break
		}
		if len(c.Data) == 0 {
			return n, "", fmt.Errorf("%w: node %s sent an empty chunk at %d of %d bytes", ErrTransfer, id, n, tr.Size)
		}
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if n != tr.Size || tr.SHA256 != "" && sum != tr.SHA256 {
		return n, sum, fmt.Errorf("%w: %d bytes with SHA-256 %s, node %s staged %d with %s", ErrTransfer, n, sum, id, tr.Size, tr.SHA256)
	}
	return n, sum, nil
}

// FrideImport sends r to node id chunk by chunk and imports it there as a
// task. The node's staged copy is removed after.
func (s *Service) FrideImport(ctx context.Context, id string, r io.Reader, as string, startAgent bool) (nodeproto.FrideTask, error) {
	return s.FrideImportOn(ctx, id, "", r, as, startAgent)
}

// FrideImportOn is FrideImport into instance's FrIDE on node id.
func (s *Service) FrideImportOn(ctx context.Context, id, instance string, r io.Reader, as string, startAgent bool) (nodeproto.FrideTask, error) {
	fo := on(instance)
	var tr nodeproto.FrideTransfer
	buf := make([]byte, nodeproto.FrideChunkBytes)
	for {
		k, rerr := io.ReadFull(r, buf)
		if k > 0 || tr.ID == "" {
			if err := s.Call(ctx, id, nodeproto.MethodFrideWrite, nodeproto.FrideChunkParams{FrideOn: fo, ID: tr.ID, Offset: tr.Size, Data: buf[:k]}, &tr); err != nil {
				return nodeproto.FrideTask{}, err
			}
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return nodeproto.FrideTask{}, rerr
		}
	}
	defer func() {
		dctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		_ = s.Call(dctx, id, nodeproto.MethodFrideDrop, nodeproto.FrideChunkParams{FrideOn: fo, ID: tr.ID}, nil)
	}()
	var out nodeproto.FrideTask
	err := s.Call(ctx, id, nodeproto.MethodFrideImport, nodeproto.FrideImportParams{FrideOn: fo, ID: tr.ID, As: as, StartAgent: startAgent}, &out)
	return out, err
}
