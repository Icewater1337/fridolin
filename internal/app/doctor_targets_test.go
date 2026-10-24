package app

import (
	"context"
	"encoding/json"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
)

// TestDoctorVaultTargets: a target without a snapshot within its window
// (its node off, its disk unplugged) warns.
func TestDoctorVaultTargets(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	cfg := &config.Settings{}
	cfg.DataDir = "/data"
	cfg.Vault.Targets = []config.VaultTarget{{Name: "nas", Kind: "node", Node: "a1b2c3d4e5f6", Folder: "/b"}, {Name: "usb", Kind: "node", Node: "a1b2c3d4e5f6", Folder: "/c", WindowHours: 200}}
	files := map[string]targetsFile{}
	env := &DoctorEnv{Config: cfg, Now: func() time.Time { return now }, ReadFile: func(p string) ([]byte, error) {
		f, ok := files[p]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return json.Marshal(f)
	}}
	if !targetsOn(env) {
		t.Fatal("applies with targets")
	}
	res := checkVaultTargets(context.Background(), env)
	if res.Status != DoctorWarn || !strings.Contains(res.Detail, "nas (no snapshot yet)") {
		t.Fatalf("no snapshot yet: %+v", res)
	}
	old := &snapshotRecord{At: now.Add(-30 * time.Hour), ID: "abc", Kind: snapshotNightly}
	failed := &snapshotRecord{At: now.Add(-time.Hour), Kind: "scheduled", Error: "the node nas is offline"}
	files[filepath.Join("/data", "core", vaultTargetsFile)] = targetsFile{Targets: map[string]*targetStatus{
		"nas": {LastNightly: old, Last: failed},
		"usb": {LastNightly: old, Last: old},
	}}
	res = checkVaultTargets(context.Background(), env)
	if res.Status != DoctorWarn || !strings.Contains(res.Detail, "1 of 2") || !strings.Contains(res.Detail, "offline") {
		t.Fatalf("one late: %+v", res)
	}
	files[filepath.Join("/data", "core", vaultTargetsFile)].Targets["nas"].LastNightly = &snapshotRecord{At: now.Add(-time.Hour), ID: "def", Kind: snapshotNightly}
	if res = checkVaultTargets(context.Background(), env); res.Status != DoctorOK {
		t.Fatalf("both within their windows: %+v", res)
	}
}
