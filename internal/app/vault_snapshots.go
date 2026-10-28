package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/snapshot"
	"github.com/Icewater1337/fridolin/internal/store"
)

// The snapshots of the hot tier (docs/PLAN.md, Workstream D): restic to
// the snapshots bucket, a full one each night and one of the brain folders
// and audit/ each hour, kept 24 hourly, 14 daily, 8 weekly and 12 monthly;
// `restic check` each week and a check that reads 5% of the data each
// month; a weekly `restic copy` to a second repository on Google Drive.
// No live SQLite file is snapshotted: bridge.db and vault.db go in as
// VACUUM INTO copies in core/snapshot-staging/, and Litestream replicates
// them continuously besides.

// snapshotHost is the host restic records, the same on every machine the
// core runs on, so that retention spans a move.
const snapshotHost = "fridolin-core"

// snapshotTag marks every snapshot fridolin takes; the kinds are tags too.
const (
	snapshotTag     = "fridolin"
	snapshotHourly  = "hourly"
	snapshotNightly = "nightly"
)

// snapshotRetention is what `restic forget --prune` keeps.
var snapshotRetention = snapshot.Retention{Hourly: 24, Daily: 14, Weekly: 8, Monthly: 12}

// The schedule: the nightly snapshot after snapshotNightAt local time, and
// the intervals of the rest.
var (
	snapshotNightAt     = 3 // hour, local time
	snapshotHourlyEvery = time.Hour
	snapshotCheckEvery  = 7 * 24 * time.Hour
	snapshotReadEvery   = 30 * 24 * time.Hour
	snapshotCopyEvery   = 7 * 24 * time.Hour
)

// snapshotReadSubset is how much of the data the monthly check reads.
const snapshotReadSubset = "5%"

type snapshotRecord struct {
	At         time.Time `json:"at,omitzero"`
	Kind       string    `json:"kind"`
	ID         string    `json:"id,omitempty"`
	Files      int       `json:"files"`
	BytesAdded int64     `json:"bytes_added"`
	Error      string    `json:"error,omitempty"`
}

type checkRecord struct {
	At             time.Time `json:"at,omitzero"`
	ReadDataSubset string    `json:"read_data_subset,omitempty"`
	OK             bool      `json:"ok"`
	Error          string    `json:"error,omitempty"`
}

// deletionHold is a snapshot that found most files gone: pruning waits
// until the person allows it (POST /api/vault/prune/allow), so the
// snapshots that still hold them are not thinned out meanwhile.
type deletionHold struct {
	Since     time.Time `json:"since"`
	Snapshot  string    `json:"snapshot"`
	Kind      string    `json:"kind"`
	PrevFiles int       `json:"prev_files"`
	Files     int       `json:"files"`
}

type driveStatus struct {
	Enabled       bool         `json:"enabled"`
	Repo          string       `json:"repo,omitempty"`
	CompletedOnce bool         `json:"completed_once"`
	LastCopy      *vaultRecord `json:"last_copy,omitempty"`
}

type snapshotsStatus struct {
	Enabled      bool            `json:"enabled"`
	Repo         string          `json:"repo,omitempty"`
	Error        string          `json:"error,omitempty"`
	Last         *snapshotRecord `json:"last,omitempty"`
	LastHourly   *snapshotRecord `json:"last_hourly,omitempty"`
	LastNightly  *snapshotRecord `json:"last_nightly,omitempty"`
	LastCheck    *checkRecord    `json:"last_check,omitempty"`
	LastReadData *checkRecord    `json:"last_read_data,omitempty"`
	LastPrune    *vaultRecord    `json:"last_prune,omitempty"`
	DeletionHold *deletionHold   `json:"deletion_hold,omitempty"`
	Drive        driveStatus     `json:"drive"`
	// SkippedBrains are the brain folders the snapshots and the cold tier
	// leave out whole, and why: backup.enabled off, or a brain.json that
	// cannot be read (brainBackup).
	SkippedBrains []brainBackup `json:"skipped_brains,omitempty"`
}

type litestreamView struct {
	Enabled bool `json:"enabled"`
	snapshot.LitestreamStatus
	DBs []string `json:"dbs,omitempty"`
	// Mirror is the last upload of the local replicas, when they reach the
	// cloud through the mirror (two layers).
	Mirror *cloud.MirrorStatus `json:"mirror,omitempty"`
	// At is when the vault last copied Litestream's live state in; before
	// the vault's StartedAt it is not known yet.
	At time.Time `json:"at,omitzero"`
}

type drillCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type drillRecord struct {
	At       time.Time    `json:"at,omitzero"`
	OK       bool         `json:"ok"`
	Snapshot string       `json:"snapshot,omitempty"`
	Detail   string       `json:"detail,omitempty"`
	Checks   []drillCheck `json:"checks,omitempty"`
}

// vaultSnapshots runs the snapshots of one vault run.
type vaultSnapshots struct {
	s   *vaultService
	run *vaultRun
	cfg *config.Settings
	// mu keeps restic runs one at a time: the scheduler skips a round
	// while a manual one runs, a manual one waits.
	mu     sync.Mutex
	inited bool
}

func newVaultSnapshots(s *vaultService, run *vaultRun, cfg *config.Settings) *vaultSnapshots {
	return &vaultSnapshots{s: s, run: run, cfg: cfg}
}

func (vs *vaultSnapshots) restic() *snapshot.Restic { return vs.run.b.restic }

// snapshotState returns a copy of the snapshot part of the status.
func (vs *vaultSnapshots) state() snapshotsStatus {
	vs.s.mu.Lock()
	defer vs.s.mu.Unlock()
	return vs.s.status.Snapshots
}

func (vs *vaultSnapshots) update(fn func(st *snapshotsStatus)) {
	vs.s.mu.Lock()
	defer vs.s.mu.Unlock()
	fn(&vs.s.status.Snapshots)
	vs.s.saveLocked()
}

// due reports which scheduled jobs are due at now.
func (vs *vaultSnapshots) due(now time.Time) (hourly, nightly, check, read, copyDrive bool) {
	st := vs.state()
	if r := st.LastHourly; r == nil || now.Sub(r.At) >= snapshotHourlyEvery {
		hourly = true
	}
	// The night is the owner's (timezone), not the container's.
	local := now.In(vs.cfg.Location())
	night := time.Date(local.Year(), local.Month(), local.Day(), snapshotNightAt, 0, 0, 0, local.Location())
	if r := st.LastNightly; r == nil || (r.At.Before(night) && !now.Before(night)) || now.Sub(r.At) >= 26*time.Hour {
		nightly = true
	}
	if r := st.LastCheck; r == nil || now.Sub(r.At) >= snapshotCheckEvery {
		check = true
	}
	if r := st.LastReadData; r == nil || now.Sub(r.At) >= snapshotReadEvery {
		read = true
	}
	if vs.run.b.drive != nil && st.LastNightly != nil && st.LastNightly.Error == "" {
		if r := st.Drive.LastCopy; r == nil || now.Sub(r.At) >= snapshotCopyEvery {
			copyDrive = true
		}
	}
	return
}

// tick runs what is due, in the background, unless a restic job runs.
func (vs *vaultSnapshots) tick(ctx context.Context, now time.Time) {
	if vs.restic() == nil {
		return
	}
	hourly, nightly, check, read, copyDrive := vs.due(now)
	if !hourly && !nightly && !check && !read && !copyDrive {
		return
	}
	if !vs.mu.TryLock() {
		return
	}
	go func() {
		defer vs.mu.Unlock()
		if nightly {
			vs.snapshotLocked(ctx, snapshotNightly)
		} else if hourly {
			vs.snapshotLocked(ctx, snapshotHourly)
		}
		if check {
			vs.checkLocked(ctx, "")
		}
		if read {
			vs.checkLocked(ctx, snapshotReadSubset)
		}
		if copyDrive {
			vs.copyDriveLocked(ctx)
		}
	}()
}

// ensureRepo makes the repository the first time.
func (vs *vaultSnapshots) ensureRepo(ctx context.Context) error {
	if vs.inited {
		return nil
	}
	ok, err := vs.restic().Exists(ctx)
	vs.charge("exists")
	if err != nil {
		return err
	}
	if !ok {
		vs.charge("init")
		// A repository that takes over from a single-layer one shares its
		// chunker, so that migrate-encryption's copy deduplicates.
		var from *snapshot.Restic
		if old := vs.run.b.resticOld; old != nil {
			if found, err := old.Exists(ctx); err == nil && found {
				from = old
			}
		}
		if err := vs.restic().Init(ctx, from); err != nil {
			return err
		}
		vs.s.log.Info("vault: made the restic repository", "repo", vs.restic().Repo)
	}
	vs.inited = true
	return nil
}

// stagingDir holds the VACUUM INTO copies of the databases.
func (vs *vaultSnapshots) stagingDir() string {
	return filepath.Join(vs.cfg.Boot.Layout().Core(), "snapshot-staging")
}

// databases are the live SQLite files the snapshots copy, by name.
func (vs *vaultSnapshots) databases() map[string]string {
	dbs := map[string]string{"vault.db": vs.cfg.Boot.Layout().VaultDB()}
	if vs.run.b.bridgeDB != "" {
		dbs["bridge.db"] = vs.run.b.bridgeDB
	}
	return dbs
}

// stage makes fresh copies of the databases in the staging folder.
func (vs *vaultSnapshots) stage(ctx context.Context) error {
	return stageDatabases(ctx, vs.stagingDir(), vs.databases())
}

// stageDatabases makes fresh VACUUM INTO copies of dbs (name → live file)
// in dir.
func stageDatabases(ctx context.Context, dir string, dbs map[string]string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if _, err := config.EnsureDir(dir); err != nil {
		return err
	}
	for name, src := range dbs {
		if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := store.VacuumInto(ctx, src, filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// paths are what a snapshot of kind holds, and what it leaves out. Each
// brain folder goes by its backup settings (brainBackup), as the Drive
// backup does: one whose backup is off or whose brain.json cannot be read
// is left out whole, of the others what their backup.exclude names. The
// excludes are absolute, so one brain's patterns never touch another's
// files, and they hold for the folders in the data dir as well, which the
// nightly snapshot takes with it.
func (vs *vaultSnapshots) paths(kind string) (paths, excludes []string) {
	l := vs.s.currentLayout()
	data := l.root
	var outside []string
	dirs := slices.Sorted(maps.Keys(l.backups))
	for _, dir := range dirs {
		excludes = append(excludes, l.backups[dir].resticExcludes()...)
	}
	excludes = append(excludes, l.secretExcludes()...)
	ids := make([]string, 0, len(l.absBrainDirs))
	for id := range l.absBrainDirs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		dir := l.absBrainDirs[id]
		if b, ok := l.backups[dir]; !ok || b.Skip != "" {
			continue
		}
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		if kind == snapshotHourly {
			paths = append(paths, dir)
		} else if _, in := l.rel(dir); !in {
			outside = append(outside, dir)
		}
	}
	if kind == snapshotHourly {
		if audit := filepath.Join(data, "audit"); exists(audit) {
			paths = append(paths, audit)
		}
	} else {
		paths = append(append(paths, data), outside...)
	}
	return paths, append(excludes, dataDirExcludes(data, vs.databases())...)
}

// dataDirExcludes are what no snapshot of the data dir takes: the live
// databases (their copies are in a staging folder), lock files,
// Litestream's folders, and what is rebuilt.
func dataDirExcludes(data string, dbs map[string]string) []string {
	var excludes []string
	for _, db := range dbs {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			excludes = append(excludes, db+suffix)
		}
	}
	slices.Sort(excludes)
	return append(excludes,
		"*.lock", ".*-litestream", "node_modules", ".DS_Store",
		filepath.Join(data, runTokensDirName),
		filepath.Join(data, brainStatesDirName, "*", "tmp"),
		filepath.Join(data, brainStatesDirName, "*", "cache"),
		filepath.Join(data, "core", "restic-cache"),
		filepath.Join(data, "core", "restic-cache-*"),
		filepath.Join(data, "core", "target-staging"),
		filepath.Join(data, "core", "import"),
		filepath.Join(data, "core", "replaced-*"),
		filepath.Join(data, "core", "drill-*"),
		// What a restore of the secret folders wrote (core/replaced-*
		// holds what a forced one replaced): they go to B2 only.
		filepath.Join(data, "core", "restored-secrets-*"),
		// What stays on the machines, such as the GitLab connection
		// (internal/gitident): never to the cloud, so never in a
		// snapshot, whose copies go to B2, Drive and the node targets.
		config.Layout{Root: data}.LocalSecrets(),
	)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Snapshot takes one now (POST /api/vault/snapshot).
func (s *vaultService) Snapshot(kind string) error {
	if kind == "" {
		kind = snapshotNightly
	}
	if kind != snapshotNightly && kind != snapshotHourly {
		return fmt.Errorf("kind is %q or %q", snapshotNightly, snapshotHourly)
	}
	return s.snapshotJob("snapshot", func(ctx context.Context, vs *vaultSnapshots) { vs.snapshotLocked(ctx, kind) })
}

// Check checks the repository now (POST /api/vault/check).
func (s *vaultService) Check(subset string) error {
	return s.snapshotJob("check", func(ctx context.Context, vs *vaultSnapshots) { vs.checkLocked(ctx, subset) })
}

// RestoreDrill restores the latest snapshot into an empty folder and
// checks it (POST /api/vault/restore-drill, fridolin vault restore-drill).
func (s *vaultService) RestoreDrill() error {
	return s.snapshotJob("restore-drill", func(ctx context.Context, vs *vaultSnapshots) { vs.drillLocked(ctx) })
}

// AllowPrune lifts a deletion hold (POST /api/vault/prune/allow).
func (s *vaultService) AllowPrune() error {
	if _, _, err := s.vault(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.Snapshots.DeletionHold != nil {
		s.log.Info("vault: pruning allowed again after a deletion hold", "snapshot", s.status.Snapshots.DeletionHold.Snapshot)
	}
	s.status.Snapshots.DeletionHold = nil
	if sf := s.status.SecretFolders; sf != nil && sf.PruneHold != nil {
		s.log.Info("vault: pruning the secret folders' versions allowed again after a deletion hold", "version", sf.PruneHold.Snapshot)
		sf.PruneHold = nil
	}
	s.saveLocked()
	return nil
}

func (s *vaultService) snapshotJob(name string, fn func(context.Context, *vaultSnapshots)) error {
	return s.startJob(name, func(ctx context.Context, run *vaultRun) error {
		vs := run.snaps
		if vs == nil || vs.restic() == nil {
			why := "snapshots are off"
			if run.b.snapErr != "" {
				why += ": " + run.b.snapErr
			}
			return errors.New(why)
		}
		vs.mu.Lock()
		defer vs.mu.Unlock()
		fn(ctx, vs)
		return nil
	})
}

// snapshotLocked takes a snapshot of kind; the caller holds vs.mu.
func (vs *vaultSnapshots) snapshotLocked(ctx context.Context, kind string) {
	rec := &snapshotRecord{At: time.Now().UTC(), Kind: kind}
	err := vs.ensureRepo(ctx)
	if err == nil && kind == snapshotNightly {
		err = vs.stage(ctx)
	}
	var sum snapshot.BackupSummary
	vs.s.refreshLayout() // a brain.json may have changed since the tick
	paths, excludes := vs.paths(kind)
	if err == nil && len(paths) == 0 {
		err = errors.New("nothing to snapshot yet: no brain folder and no audit/")
	}
	if err == nil {
		sum, err = vs.restic().Backup(ctx, snapshot.BackupOptions{Paths: paths, Excludes: excludes, Tags: []string{snapshotTag, kind}, Host: snapshotHost})
		vs.charge("backup")
		if errors.Is(err, snapshot.ErrIncomplete) && sum.SnapshotID != "" {
			rec.Error = err.Error()
			err = nil
		}
	}
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		rec.Error = err.Error()
		vs.s.log.Warn("vault: snapshot failed", "kind", kind, "err", err)
	} else {
		rec.ID, rec.Files, rec.BytesAdded = shortID(sum.SnapshotID), sum.TotalFilesProcessed, sum.DataAdded
	}
	var prev *snapshotRecord
	vs.update(func(st *snapshotsStatus) {
		if kind == snapshotHourly {
			prev, st.LastHourly = st.LastHourly, rec
		} else {
			prev, st.LastNightly = st.LastNightly, rec
		}
		st.Last = rec
		// The mass-deletion guard of the Drive backup, for snapshots: a
		// snapshot that lost most of the files of the one before holds
		// pruning until the person says the deletion was meant.
		if err == nil && prev != nil && prev.Error == "" && snapshot.MassDeletion(prev.Files, rec.Files) && st.DeletionHold == nil {
			st.DeletionHold = &deletionHold{Since: rec.At, Snapshot: rec.ID, Kind: kind, PrevFiles: prev.Files, Files: rec.Files}
			vs.s.log.Warn("vault: a snapshot lost most of the files of the one before; pruning waits until you allow it on the Vault page",
				"kind", kind, "files", rec.Files, "before", prev.Files)
		}
	})
	if err != nil || kind != snapshotNightly {
		return
	}
	if vs.state().DeletionHold != nil {
		return
	}
	prune := &vaultRecord{At: time.Now().UTC()}
	vs.charge("forget-prune")
	if err := vs.restic().Forget(ctx, snapshotRetention, []string{snapshotTag}, true); err != nil {
		if ctx.Err() != nil {
			return
		}
		prune.Error = err.Error()
		vs.s.log.Warn("vault: pruning snapshots failed", "err", err)
	}
	vs.update(func(st *snapshotsStatus) { st.LastPrune = prune })
}

// resticCosts are what restic's runs spend on B2, measured against a
// counting S3 proxy with restic 0.19 on a small repository and its cache,
// plus the two calls (b2_authorize_account, b2_list_buckets) its native B2
// backend makes first (docs/VAULT.md, "B2 transactions"). A check that
// reads data, a copy and a restore download packs too, which grows with
// the repository; the numbers are a floor for those.
var resticCosts = map[string]cloud.Cost{
	"exists":          {B: 3, C: 6},
	"init":            {A: 2, B: 1, C: 6},
	"backup":          {A: 6, B: 3, C: 8},
	"forget-prune":    {A: 11, B: 3, C: 10},
	"check":           {A: 2, B: 9, C: 9},
	"check-read-data": {B: 50},
	"copy":            {B: 20, C: 10},
	"snapshots":       {B: 3, C: 6},
	"restore":         {B: 50, C: 8},
	// The housekeeping job's look at the locks: a listing, then one
	// restic run per lock, and unlock, each logging in like "exists".
	"list-locks": {B: 3, C: 7},
	"cat-lock":   {B: 3, C: 6},
	"unlock":     {B: 3, C: 7},
}

// withRestic runs fn with the snapshot repositories (b2, and drive when
// Google Drive is connected) while no restic job of the vault runs, which
// it keeps from starting meanwhile; charge counts a restic run of fn's on
// B2. It reports false, without running fn, when the vault runs no
// snapshots, and an error when a restic job runs now.
func (s *vaultService) withRestic(fn func(repos map[string]*snapshot.Restic, charge func(op string))) (bool, error) {
	_, run, err := s.vault()
	if err != nil || run.snaps == nil || run.snaps.restic() == nil {
		return false, nil
	}
	vs := run.snaps
	if !vs.mu.TryLock() {
		return false, errors.New("a restic job of the vault runs; its locks are looked at next time")
	}
	defer vs.mu.Unlock()
	repos := map[string]*snapshot.Restic{"b2": vs.restic()}
	if run.b.drive != nil {
		if d, err := run.b.drive(); err == nil {
			repos["drive"] = d
		}
	}
	fn(repos, vs.charge)
	return true, nil
}

// charge counts one restic run of op.
func (vs *vaultSnapshots) charge(op string) {
	vs.s.chargeB2(vs.run, "restic:"+op, resticCosts[op])
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// checkLocked runs restic check; the caller holds vs.mu.
func (vs *vaultSnapshots) checkLocked(ctx context.Context, subset string) {
	rec := &checkRecord{At: time.Now().UTC(), ReadDataSubset: subset}
	err := vs.ensureRepo(ctx)
	if err == nil {
		err = vs.restic().Check(ctx, subset)
		vs.charge("check")
		if subset != "" {
			vs.charge("check-read-data")
		}
	}
	if ctx.Err() != nil {
		return
	}
	rec.OK = err == nil
	if err != nil {
		rec.Error = err.Error()
		vs.s.log.Warn("vault: restic check failed", "subset", subset, "err", err)
	}
	vs.update(func(st *snapshotsStatus) {
		if subset != "" {
			st.LastReadData = rec
		}
		st.LastCheck = rec
	})
}

// copyDriveLocked copies the snapshots to the repository on Drive; the
// caller holds vs.mu. The first copy that completes retires the old Drive
// backup's scheduled runs (backup.Options.Superseded).
func (vs *vaultSnapshots) copyDriveLocked(ctx context.Context) {
	rec := &vaultRecord{At: time.Now().UTC()}
	drive, err := vs.run.b.drive()
	var repo string
	if err == nil {
		repo = drive.Repo
		var ok bool
		if ok, err = drive.Exists(ctx); err == nil && !ok {
			err = drive.Init(ctx, vs.restic())
		}
	}
	if err == nil {
		err = drive.Copy(ctx, vs.restic())
		vs.charge("copy")
	}
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		rec.Error = err.Error()
		if !errors.Is(err, errDriveNotConnected) {
			vs.s.log.Warn("vault: copying the snapshots to Google Drive failed", "err", err)
		}
	}
	vs.update(func(st *snapshotsStatus) {
		st.Drive.Enabled, st.Drive.LastCopy = true, rec
		if repo != "" {
			st.Drive.Repo = repo
		}
		if err == nil {
			if !st.Drive.CompletedOnce {
				vs.s.log.Info("vault: the snapshots are on Google Drive; the old Drive backup's daily runs stop now")
			}
			st.Drive.CompletedOnce = true
		}
	})
}

// drillLocked restores the latest snapshot into an empty folder and checks
// that a brain folder came back and that the database copies pass
// SQLite's integrity check; the caller holds vs.mu.
func (vs *vaultSnapshots) drillLocked(ctx context.Context) {
	rec := &drillRecord{At: time.Now().UTC()}
	defer func() {
		if ctx.Err() != nil {
			return
		}
		vs.s.mu.Lock()
		vs.s.status.LastDrill = rec
		vs.s.saveLocked()
		vs.s.mu.Unlock()
	}()
	fail := func(format string, a ...any) {
		rec.Detail = fmt.Sprintf(format, a...)
		vs.s.log.Warn("vault: restore drill failed", "detail", rec.Detail)
	}
	if err := vs.ensureRepo(ctx); err != nil {
		fail("%v", err)
		return
	}
	vs.charge("snapshots")
	snaps, err := vs.restic().Snapshots(ctx, []string{snapshotTag, snapshotNightly})
	if err == nil && len(snaps) == 0 {
		snaps, err = vs.restic().Snapshots(ctx, []string{snapshotTag})
	}
	if err != nil {
		fail("listing the snapshots: %v", err)
		return
	}
	if len(snaps) == 0 {
		fail("there is no snapshot yet")
		return
	}
	latest := snaps[len(snaps)-1]
	rec.Snapshot = latest.ShortID
	tmp, err := os.MkdirTemp("", "fridolin-drill-")
	if err != nil {
		fail("%v", err)
		return
	}
	defer os.RemoveAll(tmp)
	target := filepath.Join(tmp, "restore")
	if err := os.Mkdir(target, 0o700); err != nil {
		fail("%v", err)
		return
	}
	vs.charge("restore")
	if err := vs.restic().Restore(ctx, latest.ID, target); err != nil {
		fail("restoring snapshot %s: %v", latest.ShortID, err)
		return
	}
	in := func(p string) string { return filepath.Join(target, p) }
	l := vs.s.currentLayout()
	ids := make([]string, 0, len(l.absBrainDirs))
	for id := range l.absBrainDirs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	brains := 0
	for _, id := range ids {
		if !slices.ContainsFunc(latest.Paths, func(p string) bool { return insideAny(l.absBrainDirs[id], []string{p}) }) {
			continue
		}
		c := drillCheck{Name: "brain folder " + id}
		if _, err := os.Stat(in(filepath.Join(l.absBrainDirs[id], "brain.json"))); err != nil {
			c.Detail = "no brain.json in the restored folder"
		} else {
			c.OK, c.Detail = true, "brain.json restored"
		}
		brains++
		rec.Checks = append(rec.Checks, c)
	}
	if brains == 0 {
		rec.Checks = append(rec.Checks, drillCheck{Name: "brain folder", Detail: "the snapshot holds no brain folder"})
	}
	if slices.Contains(latest.Tags, snapshotNightly) {
		names := make([]string, 0, 2)
		for name := range vs.databases() {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			c := drillCheck{Name: "database copy " + name}
			p := in(filepath.Join(vs.stagingDir(), name))
			if err := store.IntegrityCheck(ctx, p); err != nil {
				c.Detail = err.Error()
			} else {
				c.OK, c.Detail = true, "integrity_check ok"
			}
			rec.Checks = append(rec.Checks, c)
		}
	}
	if lite := vs.run.b.lite; lite != nil {
		if m := vs.run.b.liteMirror; m != nil {
			// From the cloud's copy, through both layers, as a new
			// machine would.
			fetched := filepath.Join(tmp, "litestream-fetched")
			fc := drillCheck{Name: "litestream copy in the cloud"}
			if _, err := m.Sync(ctx, time.Now()); err != nil {
				fc.Detail = "uploading the replicas first: " + err.Error()
			} else if n, err := cloud.Fetch(ctx, m.Store, fetched); err != nil {
				fc.Detail = err.Error()
			} else {
				fc.OK, fc.Detail = true, fmt.Sprintf("fetched %d files through both layers", n)
			}
			rec.Checks = append(rec.Checks, fc)
			lite = liteFrom(lite, m.Dir, fetched)
		}
		for _, db := range lite.DBs {
			name := filepath.Base(db.Path)
			c := drillCheck{Name: "litestream " + name}
			out := filepath.Join(tmp, "litestream-"+name)
			vs.s.chargeB2(vs.run, "litestream:restore", cloud.Cost{B: 10, C: 10})
			if err := lite.Restore(ctx, db.Path, out); err != nil {
				c.Detail = err.Error()
			} else if err := store.IntegrityCheck(ctx, out); err != nil {
				c.Detail = err.Error()
			} else {
				c.OK, c.Detail = true, "restored, integrity_check ok"
			}
			rec.Checks = append(rec.Checks, c)
		}
	}
	rec.OK = true
	for _, c := range rec.Checks {
		rec.OK = rec.OK && c.OK
	}
	rec.Detail = fmt.Sprintf("restored snapshot %s (%s) and ran %d checks", latest.ShortID, latest.Time.UTC().Format(time.RFC3339), len(rec.Checks))
	if !rec.OK {
		vs.s.log.Warn("vault: the restore drill found a problem", "snapshot", latest.ShortID)
	}
}

// liteFrom is lite with its file replicas under from moved under to: the
// same databases, restored from another copy of the replicas.
func liteFrom(lite *snapshot.Litestream, from, to string) *snapshot.Litestream {
	out := &snapshot.Litestream{Binary: lite.Binary, TempDir: lite.TempDir, Logger: lite.Logger, Env: lite.Env}
	for _, db := range lite.DBs {
		if rel, err := filepath.Rel(from, db.Replica.Dir); err == nil && db.Replica.Type == "file" {
			db.Replica.Dir = filepath.Join(to, rel)
		}
		out.DBs = append(out.DBs, db)
	}
	return out
}
