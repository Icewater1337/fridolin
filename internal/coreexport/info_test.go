package coreexport

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

// TestExportInfo: an export writes its info beside the archive, with the
// data dir's device id and the move, readable without the keys; the
// manifest inside carries them too.
func TestExportInfo(t *testing.T) {
	st, id := newMemStore(), identity(t)
	dev, move := strings.Repeat("ab", 16), "m-20261024-1200-beef"
	res, err := Export(context.Background(), dataDir(t), []age.Recipient{id.Recipient()}, st, ExportOptions{DB: sqliteDB{}, Version: "v9", Hostname: "mini", DeviceID: dev, Move: move})
	if err != nil {
		t.Fatal(err)
	}
	info, ok, err := LatestInfo(context.Background(), st)
	if err != nil || !ok {
		t.Fatalf("LatestInfo: %v %v", ok, err)
	}
	if info.Key != res.Key || info.Device != dev || info.Move != move || info.Version != "v9" || info.Bytes != int64(len(st.objs[res.Key])) {
		t.Errorf("info %+v, archive %d bytes", info, len(st.objs[res.Key]))
	}
	if InfoKey(res.Key) != strings.TrimSuffix(res.Key, ".tar.gz.age")+".json" {
		t.Errorf("InfoKey %s", InfoKey(res.Key))
	}
	got, err := Import(context.Background(), st, []age.Identity{id}, filepath.Join(t.TempDir(), "s"), ImportOptions{DB: sqliteDB{}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Manifest.DeviceID != dev || got.Manifest.Move != move {
		t.Errorf("manifest %+v", got.Manifest)
	}

	// An export from before infos: the key alone.
	delete(st.objs, InfoKey(res.Key))
	if info, ok, err := LatestInfo(context.Background(), st); err != nil || ok || info.Key != res.Key {
		t.Errorf("without an info: %+v %v %v", info, ok, err)
	}
	// An info that names another export is refused.
	st.objs[InfoKey(res.Key)] = []byte(`{"key":"core/exports/other.tar.gz.age"}`)
	if _, _, err := LatestInfo(context.Background(), st); err == nil {
		t.Error("an info of another export was taken")
	}
}
