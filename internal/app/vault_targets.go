package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/backup"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/noderepo"
	"github.com/Icewater1337/fridolin/internal/nodes"
	"github.com/Icewater1337/fridolin/internal/rcrypt"
	"github.com/Icewater1337/fridolin/internal/snapshot"
	"github.com/Icewater1337/fridolin/internal/store"
)

// Node targets (docs/VAULT.md, "Node targets"): restic repositories in
// folders on nodes, besides (or instead of) the cloud. Each brain.json
// says which targets get its folder (backup.targets, backup.node_only).
// restic runs on the core, as for the cloud's snapshots, against a REST
// backend on the core's loopback that carries the repository's files
// over the node channel (internal/noderepo): data never passes through
// the cloud, and the node needs only its agent.
//
// They run whether or not the vault itself is on, on the schedule and
// with the retention each target sets, and keep what they did in
// core/vault-targets.json, which fridolin vault status, the Vault page and
// doctor read.

// vaultTargetsFile is the targets' status file in the data dir's core/.
const vaultTargetsFile = "vault-targets.json"

// targetTickEvery is how often the targets look whether a job is due.
var targetTickEvery = time.Minute

// targetRetryAfter is how long a target waits after a failed snapshot
// before it tries again (the node was away, the disk unplugged).
var targetRetryAfter = time.Hour

// targetStatus is what one target did, as the status file keeps it.
type targetStatus struct {
	LastHourly   *snapshotRecord `json:"last_hourly,omitempty"`
	LastNightly  *snapshotRecord `json:"last_nightly,omitempty"`
	Last         *snapshotRecord `json:"last,omitempty"`
	LastCheck    *checkRecord    `json:"last_check,omitempty"`
	LastReadData *checkRecord    `json:"last_read_data,omitempty"`
	LastPrune    *vaultRecord    `json:"last_prune,omitempty"`
	LastDrill    *drillRecord    `json:"last_drill,omitempty"`
	DeletionHold *deletionHold   `json:"deletion_hold,omitempty"`
	// Size and Files are the repository's on the node, Free the space
	// left there, as of SizeAt.
	Size   int64     `json:"size,omitempty"`
	Files  int       `json:"files,omitempty"`
	Free   uint64    `json:"free,omitempty"`
	SizeAt time.Time `json:"size_at,omitzero"`
	// Layers is what the folder held when it was last opened: "restic"
	// (one layer, from before) or "restic+crypt" (docs/VAULT.md,
	// "Encryption").
	Layers string `json:"layers,omitempty"`
}

// targetView is a target as the status shows it: its settings, its
// node's state, and what it did.
type targetView struct {
	Name           string                 `json:"name"`
	Kind           string                 `json:"kind"`
	Node           string                 `json:"node"`
	NodeName       string                 `json:"node_name,omitempty"`
	Folder         string                 `json:"folder"`
	NightlyAt      string                 `json:"nightly_at"`
	Hourly         bool                   `json:"hourly"`
	IncludeDataDir bool                   `json:"include_data_dir"`
	Retention      config.TargetRetention `json:"retention"`
	WindowHours    int                    `json:"window_hours"`
	Online         bool                   `json:"online"`
	// Ready says the node may serve the folder now: its backup and the
	// folder approved. Problem says why not.
	Ready   bool   `json:"ready"`
	Problem string `json:"problem,omitempty"`
	// Brains are the brains whose folders go to the target.
	Brains []string `json:"brains"`
	Job    string   `json:"job,omitempty"`
	targetStatus
}

// targetsFile is core/vault-targets.json.
type targetsFile struct {
	Targets   map[string]*targetStatus `json:"targets"`
	UpdatedAt time.Time                `json:"updated_at,omitzero"`
}

// targetService runs the node targets.
type targetService struct {
	cfgs  *config.Store
	vault *vaultService
	nodes *nodes.Service
	log   *slog.Logger
	// secrets reads and makes the passwords; restic finds restic.
	secrets targetSecrets
	restic  func() string

	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	file   targetsFile
	jobs   map[string]string
	wg     sync.WaitGroup
}

func newTargetService(cfgs *config.Store, v *vaultService, n *nodes.Service, container bool, logger *slog.Logger) *targetService {
	if logger == nil {
		logger = slog.Default()
	}
	t := &targetService{cfgs: cfgs, vault: v, nodes: n, log: logger, jobs: map[string]string{},
		secrets: targetSecrets{getenv: func(name string) string { return getenv(v.env, name) }, store: hostSecretStore(container), secretsDir: cfgs.Boot().Layout().Secrets(), random: newVaultPassword, log: logger},
		restic:  func() string { return findVaultTool("restic") }}
	t.file = readTargetsFile(cfgs.Boot().Layout().Core(), os.ReadFile)
	return t
}

func readTargetsFile(coreDir string, read func(string) ([]byte, error)) targetsFile {
	var f targetsFile
	if data, err := read(filepath.Join(coreDir, vaultTargetsFile)); err == nil {
		_ = json.Unmarshal(data, &f)
	}
	if f.Targets == nil {
		f.Targets = map[string]*targetStatus{}
	}
	return f
}

// saveLocked writes the status file; the caller holds mu.
func (t *targetService) saveLocked() {
	t.file.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(t.file, "", "  ")
	if err != nil {
		return
	}
	dir, err := config.EnsureDir(t.cfgs.Boot().Layout().Core())
	if err == nil {
		err = fsx.WriteFileAtomic(filepath.Join(dir, vaultTargetsFile), append(data, '\n'), 0o600, false)
	}
	if err != nil {
		t.log.Warn("vault: keeping the targets' status", "err", err)
	}
}

func (t *targetService) update(name string, fn func(st *targetStatus)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.file.Targets[name]
	if st == nil {
		st = &targetStatus{}
		t.file.Targets[name] = st
	}
	fn(st)
	t.saveLocked()
}

func (t *targetService) state(name string) targetStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	if st := t.file.Targets[name]; st != nil {
		return *st
	}
	return targetStatus{}
}

// Start runs the targets' schedule until ctx ends.
func (t *targetService) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	t.mu.Lock()
	t.ctx, t.cancel = ctx, cancel
	t.mu.Unlock()
	t.wg.Go(func() {
		tick := time.NewTicker(targetTickEvery)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-tick.C:
				t.tick(now)
			}
		}
	})
}

// Stop ends the schedule and the jobs, and waits for them.
func (t *targetService) Stop() {
	t.mu.Lock()
	cancel := t.cancel
	if cancel != nil {
		cancel()
	}
	t.mu.Unlock()
	t.wg.Wait()
}

// tick starts what is due of each target whose node is there.
func (t *targetService) tick(now time.Time) {
	cfg := t.cfgs.Get()
	now = now.In(cfg.Location())
	for _, tg := range cfg.Vault.Targets {
		// A node an update restarts waits for the next tick.
		if !t.nodes.Online(tg.Node) || t.nodes.Updating(tg.Node) {
			continue
		}
		hourly, nightly, check, read := targetDue(tg, t.state(tg.Name), now)
		if !hourly && !nightly && !check && !read {
			continue
		}
		_ = t.startJob(tg.Name, "scheduled", func(ctx context.Context, r *targetRun) {
			switch {
			case nightly:
				r.snapshot(ctx, snapshotNightly)
			case hourly:
				r.snapshot(ctx, snapshotHourly)
			}
			if check {
				r.check(ctx, "")
			}
			if read {
				r.check(ctx, snapshotReadSubset)
			}
		})
	}
}

// targetDue reports which jobs of target tg are due at now, whose zone
// is the one its night's time of day is in (the owner's).
func targetDue(tg config.VaultTarget, st targetStatus, now time.Time) (hourly, nightly, check, read bool) {
	failedLongAgo := func(r *snapshotRecord) bool {
		return r.ID == "" && r.Error != "" && now.Sub(r.At) >= targetRetryAfter
	}
	if tg.Hourly {
		if r := st.LastHourly; r == nil || now.Sub(r.At) >= time.Hour {
			hourly = true
		}
	}
	at, _ := time.Parse("15:04", tg.Night())
	local := now
	night := time.Date(local.Year(), local.Month(), local.Day(), at.Hour(), at.Minute(), 0, 0, local.Location())
	if r := st.LastNightly; r == nil || (r.At.Before(night) && !now.Before(night)) || now.Sub(r.At) >= 26*time.Hour || failedLongAgo(r) {
		nightly = true
	}
	if st.Last == nil || st.Last.ID == "" {
		return
	}
	if r := st.LastCheck; r == nil || now.Sub(r.At) >= tg.CheckEvery() {
		check = true
	}
	if r := st.LastReadData; r == nil || now.Sub(r.At) >= tg.ReadDataEvery() {
		read = true
	}
	return
}

// errTargetBusy is a job asked for while another of the target runs.
func errTargetBusy(name, job string) error {
	return apperr.New(apperr.Conflict, "the target "+name+" is busy with "+job+"; try again when it is done")
}

// startJob runs fn for target name in the background, one job per target
// at a time.
func (t *targetService) startJob(name, job string, fn func(context.Context, *targetRun)) error {
	tg, ok := t.cfgs.Get().Vault.Target(name)
	if !ok {
		return apperr.New(apperr.NotFound, "no backup target "+name)
	}
	t.mu.Lock()
	if cur := t.jobs[name]; cur != "" {
		t.mu.Unlock()
		return errTargetBusy(name, cur)
	}
	ctx := t.ctx
	if ctx == nil || ctx.Err() != nil {
		t.mu.Unlock()
		return apperr.New(apperr.Precondition, "the bridge is starting or stopping")
	}
	t.jobs[name] = job
	t.wg.Add(1)
	t.mu.Unlock()
	go func() {
		defer t.wg.Done()
		defer func() {
			t.mu.Lock()
			delete(t.jobs, name)
			t.mu.Unlock()
		}()
		r, err := t.open(ctx, tg)
		if err != nil {
			if ctx.Err() == nil {
				t.log.Warn("vault: node target", "target", name, "err", err)
				t.update(name, func(st *targetStatus) {
					rec := &snapshotRecord{At: time.Now().UTC(), Kind: job, Error: err.Error()}
					st.Last = rec
					if job == "scheduled" {
						st.LastNightly = rec
					}
				})
			}
			return
		}
		defer r.close()
		fn(ctx, r)
	}()
	return nil
}

// targetRun is one session with a target's repository: the REST bridge
// to the node and restic over it.
type targetRun struct {
	t      *targetService
	tg     config.VaultTarget
	srv    *noderepo.Server
	restic *snapshot.Restic
}

func (r *targetRun) close() { _ = r.srv.Close() }

// open checks the node, starts the bridge, finds the password and makes
// the repository the first time.
func (t *targetService) open(ctx context.Context, tg config.VaultTarget) (*targetRun, error) {
	if problem := t.problem(tg); problem != "" {
		return nil, errors.New(problem)
	}
	bin := t.restic()
	if bin == "" {
		return nil, errors.New("restic is not installed on the core (brew install restic; the container has it)")
	}
	srv, err := noderepo.Start(t.nodes, tg.Node, tg.Folder)
	if err != nil {
		return nil, err
	}
	layers, err := t.outerLayer(ctx, srv)
	if err != nil {
		_ = srv.Close()
		return nil, err
	}
	t.update(tg.Name, func(st *targetStatus) { st.Layers = string(layers) })
	base := append(vaultBaseEnv(t.vault.env), srv.Env()...)
	cache := filepath.Join(t.cfgs.Boot().Layout().Core(), "restic-cache-"+tg.Name)
	mk := func(pw string) *snapshot.Restic {
		return &snapshot.Restic{Binary: bin, Repo: srv.Repo(), Env: append(slices.Clone(base), "RESTIC_PASSWORD="+pw),
			CacheDir: cache, Secrets: []string{pw, srv.Secret()}}
	}
	pw, err := t.secrets.ensure(ctx, tg.Name, func(ctx context.Context, pw string) (bool, error) { return mk(pw).Exists(ctx) })
	if err != nil {
		_ = srv.Close()
		return nil, err
	}
	r := &targetRun{t: t, tg: tg, srv: srv, restic: mk(pw)}
	ok, err := r.restic.Exists(ctx)
	if err == nil && !ok {
		if err = r.restic.Init(ctx, nil); err == nil {
			t.log.Info("vault: made the restic repository of a node target", "target", tg.Name, "node", tg.Node, "folder", tg.Folder)
		}
	}
	if err != nil {
		_ = srv.Close()
		return nil, err
	}
	return r, nil
}

// outerLayer gives the bridge to the node its rclone-crypt layer, under
// the snapshots' crypt password, unless the folder holds a repository of
// restic's layer alone from before: that one stays as it is (a new folder
// for the target gets both; docs/VAULT.md, "Encryption").
func (t *targetService) outerLayer(ctx context.Context, srv *noderepo.Server) (noderepo.Layers, error) {
	layers, err := srv.Detect(ctx)
	if err != nil || layers == noderepo.LayersRestic {
		return layers, err
	}
	pw, err := t.secrets.cryptPassword()
	if err != nil {
		return "", err
	}
	if pw == "" {
		if layers == noderepo.LayersCrypt {
			return "", fmt.Errorf("the folder's repository is behind rclone crypt, but %s is not here: restore it from the recovery sheet", hostsecrets.SnapshotsCryptPassword)
		}
		return noderepo.LayersRestic, nil
	}
	c, err := rcrypt.New(pw, "")
	if err != nil {
		return "", err
	}
	srv.SetCipher(c)
	return noderepo.LayersCrypt, nil
}

// problem says why the target cannot run now, "" when it can.
func (t *targetService) problem(tg config.VaultTarget) string {
	n, err := t.nodes.Registry().Get(tg.Node)
	switch {
	case err != nil:
		return "the node " + tg.Node + " is not known to this core"
	case n.Device == nodeproto.DeviceRevoked:
		return "the node " + n.Name + " was revoked"
	case n.Effective().Backup == nil:
		return "the node " + n.Name + " does not offer backups, or they are not approved (fridolin nodes approve " + n.ID + " --commands)"
	case !n.Effective().AllowsBackupFolder(tg.Folder):
		return "the folder " + tg.Folder + " is not approved on " + n.Name + ": add the target again"
	case !t.nodes.Online(tg.Node):
		return "the node " + n.Name + " is offline"
	}
	return ""
}

// paths are what a snapshot of kind holds for the target: the folders of
// the brains that choose it, each less what it leaves out of the node
// targets, and with include_data_dir, the nightly takes the data dir too,
// less the folders of the brains that do not choose it.
func (r *targetRun) paths(kind string) (paths, excludes []string, err error) {
	l := r.t.vault.currentLayout()
	ids := slices.Sorted(func(yield func(string) bool) {
		for id := range l.absBrainDirs {
			if !yield(id) {
				return
			}
		}
	})
	data := r.tg.IncludeDataDir && kind == snapshotNightly
	for _, id := range ids {
		dir := l.absBrainDirs[id]
		b, known := l.backups[dir]
		patterns, ok := b.toNode(r.tg.Name)
		if !known || !ok {
			excludes = append(excludes, backup.ResticPath(dir))
			continue
		}
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		excludes = append(excludes, backup.ResticExcludes(dir, patterns)...)
		if _, in := l.rel(dir); !data || !in {
			paths = append(paths, dir)
		}
	}
	if data {
		paths = append([]string{l.root}, paths...)
		excludes = append(excludes, dataDirExcludes(l.root, r.databases())...)
	}
	if len(paths) == 0 {
		return nil, nil, errors.New("no brain chooses this target yet (brain.json backup.targets \"" + r.tg.TargetKey() + "\": true), and it does not take the data dir")
	}
	return paths, excludes, nil
}

func (r *targetRun) databases() map[string]string {
	cfg := r.t.cfgs.Get()
	return map[string]string{"vault.db": cfg.Boot.Layout().VaultDB(), "bridge.db": AbsPath(cfg.DBPath)}
}

func (r *targetRun) stagingDir() string {
	return filepath.Join(r.t.cfgs.Boot().Layout().Core(), "target-staging", r.tg.Name)
}

// snapshot takes a snapshot of kind and, after a nightly one, prunes.
func (r *targetRun) snapshot(ctx context.Context, kind string) {
	name := r.tg.Name
	rec := &snapshotRecord{At: time.Now().UTC(), Kind: kind}
	r.t.vault.refreshLayout()
	paths, excludes, err := r.paths(kind)
	if err == nil && r.tg.IncludeDataDir && kind == snapshotNightly {
		if err = stageDatabases(ctx, r.stagingDir(), r.databases()); err == nil {
			defer os.RemoveAll(r.stagingDir())
		}
	}
	var sum snapshot.BackupSummary
	if err == nil {
		sum, err = r.restic.Backup(ctx, snapshot.BackupOptions{Paths: paths, Excludes: excludes, Tags: []string{snapshotTag, kind}, Host: snapshotHost})
		if errors.Is(err, snapshot.ErrIncomplete) && sum.SnapshotID != "" {
			rec.Error, err = err.Error(), nil
		}
	}
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		rec.Error = err.Error()
		r.t.log.Warn("vault: snapshot to a node target failed", "target", name, "kind", kind, "err", err)
	} else {
		rec.ID, rec.Files, rec.BytesAdded = shortID(sum.SnapshotID), sum.TotalFilesProcessed, sum.DataAdded
	}
	var hold bool
	r.t.update(name, func(st *targetStatus) {
		var prev *snapshotRecord
		if kind == snapshotHourly {
			prev, st.LastHourly = st.LastHourly, rec
		} else {
			prev, st.LastNightly = st.LastNightly, rec
		}
		st.Last = rec
		if err == nil && prev != nil && prev.Error == "" && snapshot.MassDeletion(prev.Files, rec.Files) && st.DeletionHold == nil {
			st.DeletionHold = &deletionHold{Since: rec.At, Snapshot: rec.ID, Kind: kind, PrevFiles: prev.Files, Files: rec.Files}
			r.t.log.Warn("vault: a snapshot to a node target lost most of the files of the one before; pruning waits until you allow it",
				"target", name, "files", rec.Files, "before", prev.Files)
		}
		hold = st.DeletionHold != nil
	})
	if err != nil || kind != snapshotNightly {
		return
	}
	if !hold {
		keep := r.tg.Keep()
		prune := &vaultRecord{At: time.Now().UTC()}
		if err := r.restic.Forget(ctx, snapshot.Retention{Hourly: keep.Hourly, Daily: keep.Daily, Weekly: keep.Weekly, Monthly: keep.Monthly}, []string{snapshotTag}, true); err != nil {
			if ctx.Err() != nil {
				return
			}
			prune.Error = err.Error()
			r.t.log.Warn("vault: pruning a node target failed", "target", name, "err", err)
		}
		r.t.update(name, func(st *targetStatus) { st.LastPrune = prune })
	}
	r.info(ctx)
}

// info reads the repository's size and the space left on the node.
func (r *targetRun) info(ctx context.Context) {
	info, err := r.t.nodes.BackupInfo(ctx, r.tg.Node, r.tg.Folder)
	if err != nil {
		return
	}
	r.t.update(r.tg.Name, func(st *targetStatus) {
		st.Size, st.Files, st.Free, st.SizeAt = info.Size, info.Files, info.Free, time.Now().UTC()
	})
}

// check runs restic check, reading subset of the data with one.
func (r *targetRun) check(ctx context.Context, subset string) {
	rec := &checkRecord{At: time.Now().UTC(), ReadDataSubset: subset}
	err := r.restic.Check(ctx, subset)
	if ctx.Err() != nil {
		return
	}
	rec.OK = err == nil
	if err != nil {
		rec.Error = err.Error()
		r.t.log.Warn("vault: checking a node target failed", "target", r.tg.Name, "subset", subset, "err", err)
	}
	r.t.update(r.tg.Name, func(st *targetStatus) {
		if subset != "" {
			st.LastReadData = rec
		}
		st.LastCheck = rec
	})
}

// drill restores the target's latest snapshot into an empty folder on
// the core and checks that each brain folder in it has its brain.json
// and, for a snapshot with the data dir, that the database copies pass
// SQLite's integrity check.
func (r *targetRun) drill(ctx context.Context) {
	rec := &drillRecord{At: time.Now().UTC()}
	defer func() {
		if ctx.Err() == nil {
			r.t.update(r.tg.Name, func(st *targetStatus) { st.LastDrill = rec })
		}
	}()
	fail := func(format string, a ...any) {
		rec.Detail = fmt.Sprintf(format, a...)
		r.t.log.Warn("vault: restore drill of a node target failed", "target", r.tg.Name, "detail", rec.Detail)
	}
	snaps, err := r.restic.Snapshots(ctx, []string{snapshotTag})
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
	if err := r.restic.Restore(ctx, latest.ID, target); err != nil {
		fail("restoring snapshot %s: %v", latest.ShortID, err)
		return
	}
	in := func(p string) string { return filepath.Join(target, restoredPath(p)) }
	l := r.t.vault.currentLayout()
	ids := make([]string, 0, len(l.absBrainDirs))
	for id := range l.absBrainDirs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		dir := l.absBrainDirs[id]
		if !slices.ContainsFunc(latest.Paths, func(p string) bool { return insideAny(dir, []string{p}) }) {
			continue
		}
		c := drillCheck{Name: "brain folder " + id}
		if _, err := os.Stat(in(filepath.Join(dir, "brain.json"))); err != nil {
			c.Detail = "no brain.json in the restored folder"
		} else {
			c.OK, c.Detail = true, "brain.json restored"
		}
		rec.Checks = append(rec.Checks, c)
	}
	if staged := in(r.stagingDir()); exists(staged) {
		for _, name := range []string{"bridge.db", "vault.db"} {
			p := filepath.Join(staged, name)
			if !exists(p) {
				continue
			}
			c := drillCheck{Name: "database copy " + name}
			if err := store.IntegrityCheck(ctx, p); err != nil {
				c.Detail = err.Error()
			} else {
				c.OK, c.Detail = true, "integrity_check ok"
			}
			rec.Checks = append(rec.Checks, c)
		}
	}
	if len(rec.Checks) == 0 {
		rec.Checks = append(rec.Checks, drillCheck{Name: "brain folder", Detail: "the snapshot holds no brain folder"})
	}
	rec.OK = true
	for _, c := range rec.Checks {
		rec.OK = rec.OK && c.OK
	}
	rec.Detail = fmt.Sprintf("restored snapshot %s (%s) from %s and ran %d checks", latest.ShortID, latest.Time.UTC().Format(time.RFC3339), r.tg.Name, len(rec.Checks))
}

// restoredPath is where restic restores the absolute path p below its
// target: the path without its root (and on Windows, the drive letter as
// a folder).
func restoredPath(p string) string {
	vol := filepath.VolumeName(p)
	rest := strings.TrimPrefix(p, vol)
	if vol != "" {
		return filepath.Join(strings.TrimSuffix(vol, ":"), rest)
	}
	return rest
}

// Views are every target with its node's state and what it did.
func (t *targetService) Views() []targetView {
	cfg := t.cfgs.Get()
	if len(cfg.Vault.Targets) == 0 {
		return nil
	}
	l := t.vault.currentLayout()
	out := make([]targetView, 0, len(cfg.Vault.Targets))
	for _, tg := range cfg.Vault.Targets {
		v := targetView{Name: tg.Name, Kind: tg.Kind, Node: tg.Node, Folder: tg.Folder, NightlyAt: tg.Night(), Hourly: tg.Hourly,
			IncludeDataDir: tg.IncludeDataDir, Retention: tg.Keep(), WindowHours: int(tg.Window() / time.Hour),
			Online: t.nodes.Online(tg.Node), Brains: []string{}, targetStatus: t.state(tg.Name)}
		if n, err := t.nodes.Registry().Get(tg.Node); err == nil {
			v.NodeName = n.Name
		}
		v.Problem = t.problem(tg)
		v.Ready = v.Problem == ""
		for _, b := range l.backups {
			if _, ok := b.toNode(tg.Name); ok {
				v.Brains = append(v.Brains, b.ID)
			}
		}
		slices.Sort(v.Brains)
		t.mu.Lock()
		v.Job = t.jobs[tg.Name]
		t.mu.Unlock()
		out = append(out, v)
	}
	return out
}

// Snapshot, Check and RestoreDrill start those jobs on target name.
func (t *targetService) Snapshot(name, kind string) error {
	if kind == "" {
		kind = snapshotNightly
	}
	if kind != snapshotNightly && kind != snapshotHourly {
		return apperr.New(apperr.Invalid, `kind is "nightly" or "hourly"`)
	}
	return t.startJob(name, "snapshot", func(ctx context.Context, r *targetRun) { r.snapshot(ctx, kind) })
}

func (t *targetService) Check(name, subset string) error {
	return t.startJob(name, "check", func(ctx context.Context, r *targetRun) { r.check(ctx, subset) })
}

func (t *targetService) RestoreDrill(name string) error {
	return t.startJob(name, "restore-drill", func(ctx context.Context, r *targetRun) { r.drill(ctx) })
}

// AllowPrune lifts target name's deletion hold.
func (t *targetService) AllowPrune(name string) error {
	if _, ok := t.cfgs.Get().Vault.Target(name); !ok {
		return apperr.New(apperr.NotFound, "no backup target "+name)
	}
	t.update(name, func(st *targetStatus) { st.DeletionHold = nil })
	return nil
}

// Add adds a target: the node must offer backups, approved, and be
// online, so that the folder is made and checked now. Adding it approves
// the folder on the node for backups.
func (t *targetService) Add(ctx context.Context, in config.VaultTarget) (targetView, error) {
	in.Kind = config.TargetNode
	if err := in.Check(); err != nil {
		return targetView{}, apperr.New(apperr.Invalid, err.Error())
	}
	if _, ok := t.cfgs.Get().Vault.Target(in.Name); ok {
		return targetView{}, apperr.New(apperr.Conflict, "a target named "+in.Name+" exists already")
	}
	n, err := t.nodes.Registry().Get(in.Node)
	if err != nil {
		return targetView{}, apperr.New(apperr.NotFound, err.Error())
	}
	if !t.nodes.Online(in.Node) {
		return targetView{}, apperr.New(apperr.Precondition, "the node "+n.Name+" is offline: a target is added while its node is online, so the folder can be made and checked")
	}
	if _, err := t.nodes.ApproveBackupFolder(in.Node, in.Folder); err != nil {
		return targetView{}, apperr.New(apperr.Precondition, err.Error())
	}
	undo := func() { _, _ = t.nodes.RemoveBackupFolder(in.Node, in.Folder) }
	// The node hears the folder in its state; wait for it, then make it.
	var info nodeproto.BackupInfo
	deadline := time.Now().Add(10 * time.Second)
	for {
		err = t.nodes.Call(ctx, in.Node, nodeproto.MethodBackupCreate, nodeproto.BackupFolderParams{Folder: in.Folder}, &info)
		if pe, ok := errors.AsType[*nodeproto.Error](err); !ok || pe.Code != nodeproto.CodeDenied || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		undo()
		return targetView{}, apperr.New(apperr.Precondition, "making the folder on "+n.Name+": "+err.Error())
	}
	if _, err := t.cfgs.Update("", func(c *config.File) error {
		c.Vault.Targets = append(c.Vault.Targets, in)
		return nil
	}); err != nil {
		undo()
		return targetView{}, err
	}
	t.log.Info("vault: added a node target", "target", in.Name, "node", n.Name, "folder", in.Folder)
	for _, v := range t.Views() {
		if v.Name == in.Name {
			return v, nil
		}
	}
	return targetView{}, nil
}

// Change changes a target's schedule, retention and data dir setting; its
// node and folder stay (add another target for another folder).
func (t *targetService) Change(name string, in config.VaultTarget) error {
	cur, ok := t.cfgs.Get().Vault.Target(name)
	if !ok {
		return apperr.New(apperr.NotFound, "no backup target "+name)
	}
	in.Name, in.Kind, in.Node, in.Folder = cur.Name, cur.Kind, cur.Node, cur.Folder
	if err := in.Check(); err != nil {
		return apperr.New(apperr.Invalid, err.Error())
	}
	_, err := t.cfgs.Update("", func(c *config.File) error {
		for i := range c.Vault.Targets {
			if c.Vault.Targets[i].Name == name {
				c.Vault.Targets[i] = in
			}
		}
		return nil
	})
	return err
}

// Remove removes a target and withdraws its folder on the node. The
// repository stays there, and so do brain.json's references to the
// target, which then mean nothing.
func (t *targetService) Remove(name string) error {
	cur, ok := t.cfgs.Get().Vault.Target(name)
	if !ok {
		return apperr.New(apperr.NotFound, "no backup target "+name)
	}
	if _, err := t.cfgs.Update("", func(c *config.File) error {
		c.Vault.Targets = slices.DeleteFunc(c.Vault.Targets, func(x config.VaultTarget) bool { return x.Name == name })
		return nil
	}); err != nil {
		return err
	}
	if _, err := t.nodes.RemoveBackupFolder(cur.Node, cur.Folder); err != nil && !errors.Is(err, nodes.ErrUnknownNode) {
		t.log.Warn("vault: withdrawing a removed target's folder", "target", name, "err", err)
	}
	t.mu.Lock()
	delete(t.file.Targets, name)
	t.saveLocked()
	t.mu.Unlock()
	return nil
}
