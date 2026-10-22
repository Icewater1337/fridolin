package coreexport

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/Icewater1337/fridolin/internal/seal"
)

func sealKey(t *testing.T) []byte {
	t.Helper()
	s, err := seal.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	k, err := seal.ParseKey(s)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// An export to a post-quantum identity with the seal layer imports with
// both keys, and with neither one alone.
func TestSealedHybridExport(t *testing.T) {
	ctx := context.Background()
	id, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	key := sealKey(t)
	st := newMemStore()
	res, err := Export(ctx, dataDir(t), []age.Recipient{id.Recipient()}, st, ExportOptions{DB: sqliteDB{}, SealKey: key})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(st.objs[res.Key][:200], []byte("mlkem768x25519")) {
		t.Fatalf("the export's age header names no ML-KEM recipient: %q", st.objs[res.Key][:120])
	}
	if _, err := Import(ctx, st, []age.Identity{id}, t.TempDir(), ImportOptions{DB: sqliteDB{}, SealKey: key}); err != nil {
		t.Fatalf("import with both keys: %v", err)
	}
	if _, err := Import(ctx, st, []age.Identity{id}, t.TempDir(), ImportOptions{DB: sqliteDB{}}); err == nil || !strings.Contains(err.Error(), "export key") {
		t.Fatalf("import without the seal key: %v", err)
	}
	if _, err := Import(ctx, st, []age.Identity{id}, t.TempDir(), ImportOptions{DB: sqliteDB{}, SealKey: sealKey(t)}); err == nil {
		t.Fatal("import with another seal key worked")
	}
	if _, err := Import(ctx, st, []age.Identity{identity(t)}, t.TempDir(), ImportOptions{DB: sqliteDB{}, SealKey: key}); err == nil {
		t.Fatal("import with another identity worked")
	}
}

// An export from before the seal layer (age alone, X25519) still imports,
// with the old identity beside the new one, and a seal key given.
func TestOldExportStillImports(t *testing.T) {
	ctx := context.Background()
	old := identity(t)
	pq, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	st := newMemStore()
	if _, err := Export(ctx, dataDir(t), []age.Recipient{old.Recipient()}, st, ExportOptions{DB: sqliteDB{}}); err != nil {
		t.Fatal(err)
	}
	var notes []string
	logf := func(f string, a ...any) { notes = append(notes, f) }
	if _, err := Import(ctx, st, []age.Identity{pq, old}, t.TempDir(), ImportOptions{DB: sqliteDB{}, SealKey: sealKey(t), Logf: logf}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "one layer") {
		t.Fatalf("no note about the single layer: %q", notes)
	}
}
