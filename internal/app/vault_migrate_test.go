package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/snapshot"
)

// TestVaultMigrateEncryption: a vault that ran with one layer (a cold
// tier in cold/, a restic repository in restic/) moves to two with
// fridolin vault migrate-encryption: a dry run counts, a run copies and
// verifies, --delete-old deletes the old folders and retires them.
func TestVaultMigrateEncryption(t *testing.T) {
	if testing.Short() {
		t.Skip("runs restic")
	}
	bin := findVaultTool("restic")
	if bin == "" {
		t.Skip("restic is not installed")
	}
	cfg := vaultConfigRclone(t)
	if cfg == "" {
		t.Skip("rclone is not installed")
	}
	ctx := context.Background()
	local := t.TempDir()
	t.Setenv(vaultLocalEnv, local)
	t.Setenv("RESTIC_PASSWORD", "test-password")

	// Before: the single-layer cold tier and repository.
	oldCold, err := cloud.NewDir(filepath.Join(local, "cold"))
	if err != nil {
		t.Fatal(err)
	}
	if err := oldCold.Put(ctx, "vault/documents/main/old.pdf", strings.NewReader("%PDF an old file")); err != nil {
		t.Fatal(err)
	}
	old := &snapshot.Restic{Binary: bin, Repo: filepath.Join(local, "restic"), Env: append(vaultBaseEnv(), "RESTIC_PASSWORD=test-password")}
	if err := old.Init(ctx, nil); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "note.md"), "an old note")
	if _, err := old.Backup(ctx, snapshot.BackupOptions{Paths: []string{src}, Tags: []string{"fridolin", "nightly"}, Host: "fridolin-core"}); err != nil {
		t.Fatal(err)
	}

	h := newHarness(t, cfg)
	h.startVault()
	st := h.vaultStatus()
	layers := map[string]storeLayers{}
	for _, l := range st.Encryption {
		layers[l.Store] = l
	}
	if l := layers["cold"]; len(l.Layers) != 2 || l.Old != "copying" {
		t.Errorf("cold: %+v", l)
	}
	if l := layers["restic"]; len(l.Layers) != 2 || l.Old != "copying" {
		t.Errorf("restic: %+v", l)
	}
	if l := layers["exports"]; !l.PQ {
		t.Errorf("exports: %+v", l)
	}

	byStore := func(m *encryptionMigration) map[string]migrationStore {
		out := map[string]migrationStore{}
		for _, s := range m.Stores {
			out[s.Store] = s
		}
		return out
	}
	st = h.vaultJob("/api/vault/migrate-encryption", map[string]any{"dry_run": true})
	dry := byStore(st.EncryptionMigration)
	if dry["cold"].Pending != 1 || dry["restic"].Pending != 1 || dry["cold"].Copied != 0 {
		t.Fatalf("dry run: %+v", st.EncryptionMigration)
	}
	if w := h.Do("POST", "/api/vault/migrate-encryption", map[string]any{"dry_run": true, "delete_old": true}); w.Code != 400 {
		t.Errorf("a dry run that deletes: %d", w.Code)
	}

	st = h.vaultJob("/api/vault/migrate-encryption", map[string]any{"delete_old": true})
	got := byStore(st.EncryptionMigration)
	if !st.EncryptionMigration.OK || got["cold"].Copied != 1 || !got["cold"].Retired || got["restic"].Copied != 1 || !got["restic"].Retired || !got["litestream"].Retired {
		t.Fatalf("run: %+v", st.EncryptionMigration)
	}
	if objs, _ := oldCold.List(ctx, ""); len(objs) != 0 {
		t.Errorf("the old cold tier keeps %v", objs)
	}
	if _, err := os.Stat(filepath.Join(local, "restic")); !os.IsNotExist(err) {
		t.Errorf("the old repository stays: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(local, "cold-sealed", "vault", "documents", "main", "old.pdf"))
	if err != nil || !strings.HasPrefix(string(raw), "FRDSEAL1") {
		t.Errorf("the copy is not sealed: %v", err)
	}
	state, err := readEncryptionState(h.b.cfgs.Get().Boot.Layout().Core())
	if err != nil || !state.retired("cold") || !state.retired("restic") {
		t.Errorf("state: %+v %v", state, err)
	}
	// The repository behind rclone crypt has the old snapshot.
	_, run, err := h.b.vault.vault()
	if err != nil {
		t.Fatal(err)
	}
	repo := run.b.restic
	snaps, err := repo.Snapshots(ctx, nil)
	if err != nil || len(snaps) == 0 {
		t.Fatalf("snapshots after the copy: %v %v", snaps, err)
	}
}
