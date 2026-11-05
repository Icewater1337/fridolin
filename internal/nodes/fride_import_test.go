package nodes

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// fakeFrideNode answers the node's fride.write calls and lists the ids of
// the fride.drop calls it hears; onWrite sees each write (1-based) before
// it is answered and may refuse it.
func fakeFrideNode(t *testing.T, onWrite func(n int) *nodeproto.Error) (*Service, string, func() []string) {
	t.Helper()
	var svc *Service
	var conns <-chan string
	svc = serveNodes(t, func(s *Service) { conns = connected(s) })
	code, _, err := svc.NewJoinCode()
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := nodeproto.DeviceIDFor(key.Public().(ed25519.PublicKey))
	ws := dialAsNodeWith(t, svc.Addr(), key, code, nodeproto.Capabilities{Fride: &nodeproto.FrideCap{URL: "http://127.0.0.1:17840"}})
	waitConnected(t, conns, id)
	if _, err := svc.ApproveDevice(id); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApproveCommands(id); err != nil {
		t.Fatal(err)
	}
	ws.SetReadLimit(64 << 20)
	var mu sync.Mutex
	var drops []string
	go func() {
		writes := 0
		for {
			env, err := readEnvelope(context.Background(), ws)
			if err != nil {
				return
			}
			if env.Type != nodeproto.TypeRequest {
				continue
			}
			req, err := nodeproto.DecodeBody[nodeproto.Request](env)
			if err != nil {
				return
			}
			var p nodeproto.FrideChunkParams
			_ = json.Unmarshal(req.Params, &p)
			resp := nodeproto.Response{Result: json.RawMessage(`{}`)}
			switch req.Method {
			case nodeproto.MethodFrideWrite:
				writes++
				if perr := onWrite(writes); perr != nil {
					resp = nodeproto.Response{Error: perr}
				} else {
					resp.Result, _ = json.Marshal(nodeproto.FrideTransfer{ID: "staged-1", Size: p.Offset + int64(len(p.Data))})
				}
			case nodeproto.MethodFrideDrop:
				mu.Lock()
				drops = append(drops, p.ID)
				mu.Unlock()
			}
			data, err := nodeproto.Encode(nodeproto.TypeResponse, env.ID, resp)
			if err == nil {
				err = ws.Write(context.Background(), websocket.MessageText, data)
			}
			if err != nil {
				return
			}
		}
	}()
	return svc, id, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), drops...)
	}
}

// An import that fails after the node staged its first chunk drops the
// staged copy, whatever ended it (#502).
func TestFrideImportDropsTheStagedCopyWhenItFails(t *testing.T) {
	big := bytes.Repeat([]byte{1}, 2*nodeproto.FrideChunkBytes+1)
	for _, tc := range []struct {
		name string
		src  func(cancel context.CancelFunc) io.Reader
		fail func(n int, cancel context.CancelFunc) *nodeproto.Error
	}{
		{"a write that fails part-way", func(context.CancelFunc) io.Reader { return bytes.NewReader(big) },
			func(n int, _ context.CancelFunc) *nodeproto.Error {
				if n == 2 {
					return nodeproto.Errorf(nodeproto.CodeFailed, "disk full")
				}
				return nil
			}},
		{"a source that fails part-way", func(context.CancelFunc) io.Reader {
			return io.MultiReader(bytes.NewReader(big[:nodeproto.FrideChunkBytes]), errReader{})
		}, func(int, context.CancelFunc) *nodeproto.Error { return nil }},
		{"a context cancelled part-way", func(context.CancelFunc) io.Reader { return bytes.NewReader(big) },
			func(n int, cancel context.CancelFunc) *nodeproto.Error {
				if n == 2 {
					cancel()
				}
				return nil
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			svc, id, drops := fakeFrideNode(t, func(n int) *nodeproto.Error { return tc.fail(n, cancel) })
			_, err := svc.FrideImport(ctx, id, tc.src(cancel), "task", false)
			if err == nil {
				t.Fatal("the import succeeded")
			}
			deadline := time.Now().Add(5 * time.Second)
			for len(drops()) == 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if got := drops(); len(got) != 1 || got[0] != "staged-1" {
				t.Fatalf("fride.drop for %v, want [staged-1]", got)
			}
		})
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("source broke") }
