package cloud

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/seal"
)

func TestMigratingContract(t *testing.T) {
	nw, err := NewDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old, err := NewDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	storeContract(t, &Migrating{New: nw, Old: old})
}

// Old objects are read until they are copied; writes go to the new store
// (sealed); a delete reaches both.
func TestMigratingReadsOldWritesNew(t *testing.T) {
	ctx := context.Background()
	key, _ := seal.NewKey()
	nw, _ := newSealedDir(t, key)
	old, err := NewDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Put(ctx, "a", strings.NewReader("old a")); err != nil {
		t.Fatal(err)
	}
	if err := old.Put(ctx, "b", strings.NewReader("old b")); err != nil {
		t.Fatal(err)
	}
	m := &Migrating{New: nw, Old: old}
	if err := m.Put(ctx, "b", strings.NewReader("new b!")); err != nil {
		t.Fatal(err)
	}
	read := func(k string) string {
		rc, err := m.Get(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		b, _ := io.ReadAll(rc)
		return string(b)
	}
	if read("a") != "old a" || read("b") != "new b!" {
		t.Fatal("reads")
	}
	list, err := m.List(ctx, "")
	if err != nil || len(list) != 2 || list[1].Size != 6 {
		t.Fatalf("list %+v %v", list, err)
	}
	if _, err := old.Stat(ctx, "b"); err != nil {
		t.Fatal("the put touched the old store")
	}
	if err := m.Delete(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Stat(ctx, "b"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("delete left the old copy")
	}
	if _, err := m.Stat(ctx, "b"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("delete left a copy")
	}
}
