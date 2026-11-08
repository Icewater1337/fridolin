package app

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/cloud"
)

// hangStore's Put returns only when its context ends.
type hangStore struct {
	cloud.ObjectStore
	ended chan error
}

func (h *hangStore) List(context.Context, string) ([]cloud.ObjectInfo, error) { return nil, nil }

func (h *hangStore) Put(ctx context.Context, _ string, _ io.Reader) error {
	<-ctx.Done()
	h.ended <- ctx.Err()
	return ctx.Err()
}

func TestRunLiteMirrorEndsAHungSync(t *testing.T) {
	// Serial: shortens the package-level liteMirrorEvery and liteSyncTimeout.
	oldEvery, oldTimeout := liteMirrorEvery, liteSyncTimeout
	liteMirrorEvery, liteSyncTimeout = 10*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { liteMirrorEvery, liteSyncTimeout = oldEvery, oldTimeout })
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &hangStore{ended: make(chan error, 10)}
	s := &vaultService{log: slog.New(slog.DiscardHandler)}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.runLiteMirror(ctx, &cloud.Mirror{Dir: dir, Store: store}); close(done) }()
	select {
	case err := <-store.ended:
		if err != context.DeadlineExceeded {
			t.Fatalf("hung sync ended with %v, want the timeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a hung upload was never ended")
	}
	stop()
	<-done
}
