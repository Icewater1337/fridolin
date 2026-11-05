package httpapi

import (
	"context"
	"errors"
	"testing"

	"github.com/Icewater1337/fridolin/internal/files"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

type readFails struct {
	err    error
	writes int
}

func (f *readFails) List(context.Context, string, string) ([]files.Entry, error) { return nil, nil }
func (f *readFails) Read(context.Context, string, string, int64) ([]byte, int64, bool, error) {
	return nil, 0, false, f.err
}
func (f *readFails) Write(context.Context, string, string, []byte) error { f.writes++; return nil }

func TestNodeCreateWritesOnlyWhenNotFound(t *testing.T) {
	t.Parallel()
	root := files.Root{Kind: files.KindNode, Node: "n", Path: "/data", Writable: true, Online: true}
	s := files.Scope{Write: true}
	for _, tc := range []struct {
		name  string
		err   error
		write bool
	}{
		{"not found", nodeproto.Errorf(nodeproto.CodeNotFound, "no such file"), true},
		{"timeout", nodeproto.Errorf(nodeproto.CodeTimeout, "slow"), false},
		{"denied", nodeproto.Errorf(nodeproto.CodeDenied, "no"), false},
		{"plain error", errors.New("boom"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n := &readFails{err: tc.err}
			_, err := (&FilesAPI{Nodes: n}).nodeWrite(t.Context(), root, "a.md", []byte("x"), "", s)
			if tc.write != (n.writes == 1) || tc.write != (err == nil) {
				t.Fatalf("writes=%d err=%v", n.writes, err)
			}
		})
	}
}
