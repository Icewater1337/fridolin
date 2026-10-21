package cloud

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/seal"
)

// countingStore counts the listings a Mirror makes.
type countingStore struct {
	ObjectStore
	lists int
}

func (c *countingStore) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	c.lists++
	return c.ObjectStore.List(ctx, prefix)
}

func TestMirrorUploadsChangesAndBatchesDeletes(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	write := func(rel, data string) {
		p := filepath.Join(src, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	key, _ := seal.NewKey()
	sealed, raw := newSealedDir(t, key)
	store := &countingStore{ObjectStore: sealed}
	m := &Mirror{Dir: src, Store: store, DeleteEvery: time.Hour}
	write("bridge.db/ltx/0/a.ltx", "aaa")
	write("bridge.db/ltx/0/b.ltx", "bbbb")
	write("bridge.db/ltx/0/c.ltx.tmp", "partial")
	now := time.Now()
	st, err := m.Sync(ctx, now)
	if err != nil || st.Uploaded != 2 || st.Files != 2 {
		t.Fatalf("first sync: %+v %v", st, err)
	}
	if st, err = m.Sync(ctx, now.Add(time.Minute)); err != nil || st.Uploaded != 0 {
		t.Fatalf("nothing changed: %+v %v", st, err)
	}
	write("bridge.db/ltx/1/d.ltx", "dd")
	if err := os.Remove(filepath.Join(src, "bridge.db/ltx/0/a.ltx")); err != nil {
		t.Fatal(err)
	}
	st, err = m.Sync(ctx, now.Add(2*time.Minute))
	if err != nil || st.Uploaded != 1 || st.Deleted != 0 {
		t.Fatalf("delete waits for its batch: %+v %v", st, err)
	}
	st, err = m.Sync(ctx, now.Add(2*time.Hour))
	if err != nil || st.Deleted != 1 {
		t.Fatalf("the batch: %+v %v", st, err)
	}
	if store.lists != 1 {
		t.Errorf("%d listings, want the first one only", store.lists)
	}
	if raw, _ := os.ReadFile(filepath.Join(raw.Root, "bridge.db/ltx/0/b.ltx")); string(raw[:8]) != "FRDSEAL1" {
		t.Error("the copy is not sealed")
	}
	out := filepath.Join(t.TempDir(), "back")
	n, err := Fetch(ctx, sealed, out)
	if err != nil || n != 2 {
		t.Fatalf("fetch: %d %v", n, err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "bridge.db/ltx/1/d.ltx")); string(b) != "dd" {
		t.Errorf("fetched %q", b)
	}
}
