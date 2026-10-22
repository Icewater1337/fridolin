package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/snapshot"
)

// fridolin vault migrate-encryption (POST /api/vault/migrate-encryption)
// moves what is stored with one layer of encryption to the stores with
// two (docs/VAULT.md, "Encryption"): it copies the cold tier's files into
// the sealed folder, the restic snapshots into the repositories behind
// rclone crypt (restic copy), and checks that the Litestream mirror is
// current. A dry run only counts. With delete_old, once a store's copy is
// verified, its old folder is deleted and the store marked retired, so
// the vault stops reading it.

// encryptionMigration is the last run's record in the status.
type encryptionMigration struct {
	At        time.Time        `json:"at"`
	DryRun    bool             `json:"dry_run"`
	DeleteOld bool             `json:"delete_old"`
	Running   bool             `json:"running,omitempty"`
	Stores    []migrationStore `json:"stores"`
	OK        bool             `json:"ok"`
}

// migrationStore is one store's part of it.
type migrationStore struct {
	Store string `json:"store"`
	// Action is what was (or, in a dry run, would be) done, in words.
	Action string `json:"action"`
	// Pending is how much the old store holds that the new one lacks;
	// Copied how much this run copied (files, or snapshots for restic).
	Pending int    `json:"pending"`
	Copied  int    `json:"copied"`
	Retired bool   `json:"retired,omitempty"`
	Error   string `json:"error,omitempty"`
}

// MigrateEncryption starts the job.
func (s *vaultService) MigrateEncryption(dryRun, deleteOld bool) error {
	if dryRun && deleteOld {
		return errors.New("a dry run deletes nothing: drop --delete-old, or use --run")
	}
	return s.startJob("migrate-encryption", func(ctx context.Context, run *vaultRun) error {
		rec := &encryptionMigration{At: time.Now().UTC(), DryRun: dryRun, DeleteOld: deleteOld, Running: true}
		s.setMigration(rec)
		m := migrator{s: s, run: run, dryRun: dryRun, deleteOld: deleteOld}
		rec.Stores = append(rec.Stores, m.cold(ctx))
		if vs := run.snaps; vs != nil && run.b.restic != nil {
			vs.mu.Lock()
			rec.Stores = append(rec.Stores, m.restic(ctx, vs, "restic", func() (*snapshot.Restic, *snapshot.Restic, error) {
				return run.b.resticOld, run.b.restic, vs.ensureRepo(ctx)
			}))
			rec.Stores = append(rec.Stores, m.restic(ctx, vs, "drive", func() (*snapshot.Restic, *snapshot.Restic, error) {
				return m.drive(ctx, run)
			}))
			vs.mu.Unlock()
		}
		rec.Stores = append(rec.Stores, m.litestream(ctx))
		rec.Running, rec.OK = false, true
		for _, st := range rec.Stores {
			rec.OK = rec.OK && st.Error == ""
		}
		s.setMigration(rec)
		return nil
	})
}

func (s *vaultService) setMigration(rec *encryptionMigration) {
	cp := *rec
	cp.Stores = append([]migrationStore(nil), rec.Stores...)
	s.mu.Lock()
	s.status.EncryptionMigration = &cp
	s.mu.Unlock()
}

type migrator struct {
	s                 *vaultService
	run               *vaultRun
	dryRun, deleteOld bool
}

// retire marks store retired in core/encryption.json.
func (m migrator) retire(store string) error {
	core := m.s.cfgs.Get().Boot.Layout().Core()
	st, err := readEncryptionState(core)
	if err != nil {
		return err
	}
	if st.Retired == nil {
		st.Retired = map[string]time.Time{}
	}
	st.Retired[store] = time.Now().UTC()
	return writeEncryptionState(core, st)
}

// verified notes that store's copy was checked.
func (m migrator) verified(store string) {
	core := m.s.cfgs.Get().Boot.Layout().Core()
	st, err := readEncryptionState(core)
	if err == nil {
		if st.Migrated == nil {
			st.Migrated = map[string]time.Time{}
		}
		st.Migrated[store] = time.Now().UTC()
		err = writeEncryptionState(core, st)
	}
	if err != nil {
		m.s.log.Warn("vault: noting the verified copy", "store", store, "err", err)
	}
}

func (st *migrationStore) fail(err error) migrationStore {
	if err != nil {
		st.Error = err.Error()
	}
	return *st
}

// cold copies the old cold folder's files that the sealed one lacks.
func (m migrator) cold(ctx context.Context) migrationStore {
	st := migrationStore{Store: "cold"}
	mig, ok := m.run.b.cold.(*cloud.Migrating)
	if !ok {
		if _, sealed := m.run.b.cold.(*cloud.Sealed); sealed {
			st.Action, st.Retired = "two layers; the single-layer folder is retired", true
		} else {
			st.Action = "one layer: the seal key (fridolin.vault.seal.key) is missing, so there is nothing to copy to"
		}
		return st
	}
	old, err := mig.Old.List(ctx, "")
	if err != nil {
		return st.fail(fmt.Errorf("listing the single-layer cold tier: %w", err))
	}
	nw, err := mig.New.List(ctx, "")
	if err != nil {
		return st.fail(fmt.Errorf("listing the sealed cold tier: %w", err))
	}
	have := make(map[string]int64, len(nw))
	for _, o := range nw {
		have[o.Key] = o.Size
	}
	var pending []cloud.ObjectInfo
	for _, o := range old {
		if size, ok := have[o.Key]; !ok || size != o.Size {
			pending = append(pending, o)
		}
	}
	st.Pending = len(pending)
	if m.dryRun {
		st.Action = fmt.Sprintf("would copy %d of %d files into the sealed folder", len(pending), len(old))
		if len(old) == 0 {
			st.Action = "the single-layer folder is empty: --run --delete-old retires it"
		}
		return st
	}
	for _, o := range pending {
		if err := copyObject(ctx, mig.Old, mig.New, o); err != nil {
			st.Action = fmt.Sprintf("copied %d of %d files", st.Copied, len(pending))
			return st.fail(err)
		}
		st.Copied++
	}
	st.Pending = 0
	m.verified("cold")
	st.Action = fmt.Sprintf("copied %d files; all %d are in the sealed folder", st.Copied, len(old))
	if !m.deleteOld {
		return st
	}
	keys := make([]string, 0, len(old))
	for _, o := range old {
		keys = append(keys, o.Key)
	}
	if err := cloud.DeleteKeys(ctx, mig.Old, keys); err != nil {
		return st.fail(fmt.Errorf("deleting the single-layer cold tier: %w", err))
	}
	if err := m.retire("cold"); err != nil {
		return st.fail(err)
	}
	st.Retired = true
	st.Action += fmt.Sprintf("; deleted the %d single-layer files (the vault reads the sealed folder alone from its next start)", len(old))
	return st
}

// copyObject copies o from one store to another and checks the size.
func copyObject(ctx context.Context, from, to cloud.ObjectStore, o cloud.ObjectInfo) error {
	rc, err := from.Get(ctx, o.Key)
	if err != nil {
		return fmt.Errorf("reading %s: %w", o.Key, err)
	}
	err = to.Put(ctx, o.Key, rc)
	_ = rc.Close()
	if err != nil {
		return fmt.Errorf("writing %s: %w", o.Key, err)
	}
	info, err := to.Stat(ctx, o.Key)
	if err != nil {
		return fmt.Errorf("checking %s: %w", o.Key, err)
	}
	if info.Size != o.Size {
		return fmt.Errorf("%s has %d bytes after the copy, %d before", o.Key, info.Size, o.Size)
	}
	return nil
}

// drive opens both Drive repositories, making the new one when needed.
func (m migrator) drive(ctx context.Context, run *vaultRun) (*snapshot.Restic, *snapshot.Restic, error) {
	if run.b.drive == nil || run.b.driveOld == nil {
		return nil, nil, nil
	}
	old, err := run.b.driveOld()
	if err != nil {
		return nil, nil, err
	}
	nw, err := run.b.drive()
	if err != nil {
		return nil, nil, err
	}
	if m.dryRun {
		return old, nw, nil
	}
	ok, err := nw.Exists(ctx)
	if err == nil && !ok {
		var from *snapshot.Restic
		if found, _ := old.Exists(ctx); found {
			from = old
		}
		err = nw.Init(ctx, from)
	}
	return old, nw, err
}

// sameSnapshot is what a copied snapshot keeps of its original.
func sameSnapshot(a, b snapshot.Snapshot) bool {
	return a.Time.Equal(b.Time) && a.Hostname == b.Hostname && strings.Join(a.Paths, "\x00") == strings.Join(b.Paths, "\x00")
}

// missing returns old's snapshots that nw has no copy of.
func missing(old, nw []snapshot.Snapshot) []snapshot.Snapshot {
	var out []snapshot.Snapshot
	for _, o := range old {
		found := false
		for _, n := range nw {
			if sameSnapshot(o, n) {
				found = true
				break
			}
		}
		if !found {
			out = append(out, o)
		}
	}
	return out
}

// restic copies a single-layer repository's snapshots into the one behind
// rclone crypt; the caller holds vs.mu.
func (m migrator) restic(ctx context.Context, vs *vaultSnapshots, store string, open func() (*snapshot.Restic, *snapshot.Restic, error)) migrationStore {
	st := migrationStore{Store: store}
	enc, _ := readEncryptionState(m.s.cfgs.Get().Boot.Layout().Core())
	if enc.retired(store) {
		st.Action, st.Retired = "two layers; the single-layer repository is retired", true
		return st
	}
	old, nw, err := open()
	switch {
	case errors.Is(err, errDriveNotConnected):
		st.Action = "Google Drive is not connected: nothing to copy"
		return st
	case err != nil:
		return st.fail(err)
	case old == nil:
		st.Action = "one layer: the snapshots' crypt password (fridolin.snapshots.crypt.password) is missing, so there is nothing to copy to"
		if store == "drive" && m.run.b.drive == nil {
			st.Action = "no Drive repository"
		}
		return st
	}
	found, err := old.Exists(ctx)
	if errors.Is(err, snapshot.ErrRepoMissing) {
		found, err = false, nil
	}
	if err != nil {
		return st.fail(fmt.Errorf("opening the single-layer repository: %w", err))
	}
	var oldSnaps []snapshot.Snapshot
	if found {
		if oldSnaps, err = old.Snapshots(ctx, nil); err != nil {
			return st.fail(fmt.Errorf("listing the single-layer repository: %w", err))
		}
	}
	var newSnaps []snapshot.Snapshot
	if ok, err := nw.Exists(ctx); err == nil && ok {
		if newSnaps, err = nw.Snapshots(ctx, nil); err != nil {
			return st.fail(fmt.Errorf("listing the repository behind rclone crypt: %w", err))
		}
	}
	todo := missing(oldSnaps, newSnaps)
	st.Pending = len(todo)
	if m.dryRun {
		st.Action = fmt.Sprintf("would copy %d of %d snapshots with restic copy", len(todo), len(oldSnaps))
		if !found {
			st.Action = "there is no single-layer repository: --run --delete-old retires it"
		}
		return st
	}
	if len(todo) > 0 {
		vs.charge("copy")
		if err := nw.Copy(ctx, old); err != nil {
			return st.fail(fmt.Errorf("restic copy: %w", err))
		}
		if newSnaps, err = nw.Snapshots(ctx, nil); err != nil {
			return st.fail(err)
		}
		if left := missing(oldSnaps, newSnaps); len(left) > 0 {
			return st.fail(fmt.Errorf("%d snapshots are still missing after restic copy", len(left)))
		}
	}
	st.Copied, st.Pending = len(todo), 0
	m.verified(store)
	st.Action = fmt.Sprintf("copied %d snapshots; all %d are in the repository behind rclone crypt", len(todo), len(oldSnaps))
	if !m.deleteOld {
		return st
	}
	return m.purge(ctx, st, store, "repository")
}

// purge deletes store's single-layer folder and retires it.
func (m migrator) purge(ctx context.Context, st migrationStore, store, what string) migrationStore {
	purge := m.run.b.oldPurge[store]
	if purge == nil {
		return st.fail(fmt.Errorf("no way to delete the single-layer %s here", what))
	}
	if err := purge(ctx); err != nil {
		return st.fail(fmt.Errorf("deleting the single-layer %s: %w", what, err))
	}
	if err := m.retire(store); err != nil {
		return st.fail(err)
	}
	st.Retired = true
	st.Action += "; deleted the single-layer " + what
	return st
}

// litestream checks that the mirror uploads, then deletes the replicas
// Litestream wrote to B2 itself before.
func (m migrator) litestream(ctx context.Context) migrationStore {
	st := migrationStore{Store: "litestream"}
	mirror := m.run.b.liteMirror
	switch {
	case m.run.b.lite == nil:
		st.Action = "Litestream does not run here"
		return st
	case mirror == nil:
		st.Action = "no layer of fridolin's: the snapshots' crypt password or the Litestream seal key is missing"
		return st
	}
	enc, _ := readEncryptionState(m.s.cfgs.Get().Boot.Layout().Core())
	if enc.retired("litestream") {
		st.Action, st.Retired = "two layers; the old replicas are deleted", true
		return st
	}
	if m.dryRun {
		st.Action = "the mirror uploads the replicas with two layers; --run --delete-old deletes the old ones (litestream/ in the snapshots bucket)"
		return st
	}
	res, err := mirror.Sync(ctx, time.Now())
	if err != nil {
		return st.fail(fmt.Errorf("the mirror's upload: %w", err))
	}
	st.Copied = res.Uploaded
	st.Action = fmt.Sprintf("the mirror is current (%d files)", res.Files)
	m.verified("litestream")
	if !m.deleteOld {
		return st
	}
	return m.purge(ctx, st, "litestream", "replicas")
}
