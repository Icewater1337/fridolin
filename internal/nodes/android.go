package nodes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// AndroidBuild has node id build the Android project in p with Gradle
// and writes the APK to w, chunk by chunk, checking its size and SHA-256
// against the node's. A build that failed on the node is a result with
// Error and the end of Gradle's output, and no error. The node's staged
// APK is removed after, read or not.
func (s *Service) AndroidBuild(ctx context.Context, id string, p nodeproto.AndroidBuildParams, w io.Writer) (nodeproto.AndroidBuildResult, error) {
	if err := p.Check(); err != nil {
		return nodeproto.AndroidBuildResult{}, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	var res nodeproto.AndroidBuildResult
	if err := s.Call(ctx, id, nodeproto.MethodAndroidBuild, p, &res); err != nil {
		return res, err
	}
	if res.APK == nil {
		if res.Error == "" {
			res.Error = "the node built no app"
		}
		return res, nil
	}
	tr := *res.APK
	defer func() {
		// A context of its own: the build's may be what ended.
		dctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		_ = s.Call(dctx, id, nodeproto.MethodAndroidDrop, nodeproto.FrideChunkParams{ID: tr.ID}, nil)
	}()
	h := sha256.New()
	var n int64
	for {
		var c nodeproto.FrideChunk
		if err := s.Call(ctx, id, nodeproto.MethodAndroidRead, nodeproto.FrideChunkParams{ID: tr.ID, Offset: n}, &c); err != nil {
			return res, err
		}
		if len(c.Data) > 0 {
			if _, err := w.Write(c.Data); err != nil {
				return res, err
			}
			h.Write(c.Data)
			n += int64(len(c.Data))
		}
		if c.EOF {
			break
		}
		if len(c.Data) == 0 {
			return res, fmt.Errorf("%w: node %s sent an empty chunk at %d of %d bytes", ErrTransfer, id, n, tr.Size)
		}
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if n != tr.Size || sum != tr.SHA256 {
		return res, fmt.Errorf("%w: %d bytes with SHA-256 %s, node %s staged %d with %s", ErrTransfer, n, sum, id, tr.Size, tr.SHA256)
	}
	return res, nil
}
