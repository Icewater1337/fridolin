package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/snapshot"
	"github.com/Icewater1337/fridolin/internal/vault"
)

// The vault service (docs/PLAN.md, M3): with vault.enabled it opens the
// vault over the hot root (vaultLayout), uploads what changes, evicts by
// policy, takes restic snapshots and runs Litestream, and keeps what it
// did in core/vault-status.json, which the dashboard and fridolin doctor
// read.

// Vault states, vaultStatus.State.
const (
	vaultOff      = "off"
	vaultStarting = "starting"
	vaultRunning  = "running"
	vaultError    = "error"
	vaultStopped  = "stopped" // the bridge stopped it; only the status file says so
)

// The vault's rhythm: how often it pings the cold tier, evicts, walks the
// whole hot tier, and looks whether a snapshot is due.
var (
	vaultPingEvery     = 10 * time.Minute
	vaultEvictEvery    = 15 * time.Minute
	vaultFullSyncEvery = 6 * time.Hour
	vaultTickEvery     = time.Minute
)

// vaultEvictionsShown is how many evictions the status lists.
const vaultEvictionsShown = 50

// vaultStatusFile is the status's file in the data dir's core/ folder.
const vaultStatusFile = "vault-status.json"

type vaultRecord struct {
	At    time.Time `json:"at,omitzero"`
	Error string    `json:"error,omitempty"`
}

type vaultSyncRecord struct {
	At            time.Time `json:"at,omitzero"`
	Uploaded      int       `json:"uploaded"`
	UploadedBytes int64     `json:"uploaded_bytes"`
	Failed        int       `json:"failed"`
	Error         string    `json:"error,omitempty"`
}

type vaultEvictRecord struct {
	At         time.Time `json:"at,omitzero"`
	Evicted    int       `json:"evicted"`
	FreedBytes int64     `json:"freed_bytes"`
	OverBudget bool      `json:"over_budget"`
	Error      string    `json:"error,omitempty"`
}

type vaultReach struct {
	At    time.Time `json:"at,omitzero"`
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
}

type vaultFileCounts struct {
	Hot  int `json:"hot"`
	Cold int `json:"cold"`
	Both int `json:"both"`
}

// vaultStatus is GET /api/vault/status and the file core/vault-status.json.
type vaultStatus struct {
	Enabled     bool              `json:"enabled"`
	State       string            `json:"state"`
	Error       string            `json:"error,omitempty"`
	Root        string            `json:"root,omitempty"`
	HotBytes    int64             `json:"hot_bytes"`
	BudgetBytes int64             `json:"budget_bytes"`
	TargetBytes int64             `json:"target_bytes"`
	ColdBytes   int64             `json:"cold_bytes"`
	Files       vaultFileCounts   `json:"files"`
	Job         string            `json:"job,omitempty"`
	LastUpload  time.Time         `json:"last_upload,omitzero"`
	LastSync    *vaultSyncRecord  `json:"last_sync,omitempty"`
	LastEvict   *vaultEvictRecord `json:"last_evict,omitempty"`
	Reachable   *vaultReach       `json:"reachable,omitempty"`
	Evictions   []vault.Eviction  `json:"evictions"`
	Snapshots   snapshotsStatus   `json:"snapshots"`
	Litestream  litestreamView    `json:"litestream"`
	LastDrill   *drillRecord      `json:"last_drill,omitempty"`
	// UpdatedAt is when the running service last wrote the file: doctor
	// takes an old one for a bridge that does not run.
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

// vaultBackends are what the service talks to: the cold tier, the restic
// repositories and Litestream. Tests and dev mode hand in local ones.
type vaultBackends struct {
	cold cloud.ObjectStore
	// restic is the snapshots' repository; nil turns snapshots off, and
	// snapErr says why.
	restic  *snapshot.Restic
	snapErr string
	// drive is the second repository, on Google Drive, made when the
	// weekly copy runs (Drive may be connected later); nil means none.
	drive func() (*snapshot.Restic, error)
	// lite replicates bridge.db and vault.db; nil means none.
	lite *snapshot.Litestream
	// bridgeDB is the bridge.db Litestream replicates and the snapshots
	// copy; "" for none.
	bridgeDB string
}

// vaultBackendsFor builds the backends, a variable so tests hand in local
// ones.
var vaultBackendsFor = defaultVaultBackends

// vaultBrain is what the vault knows of a brain.
type vaultBrain struct {
	ID, Dir string
}

// vaultService runs the vault while vault.enabled is on.
type vaultService struct {
	cfgs      *config.Store
	container bool
	log       *slog.Logger
	// brains lists the loaded brains; openMedia the files of the messages
	// they run or have waiting (absolute paths).
	brains    func() []vaultBrain
	openMedia func() []string

	layout atomic.Pointer[vaultLayout]

	mu       sync.Mutex
	ctx      context.Context // Start's; nil before and after
	running  *vaultRun
	settings config.VaultConfig
	status   vaultStatus
	job      string
}

// vaultRun is one start of the vault, until a stop or a settings change.
type vaultRun struct {
	v      *vault.Vault
	b      vaultBackends
	snaps  *vaultSnapshots
	cancel context.CancelFunc
	done   chan struct{}
	// liteErr is why Litestream's supervisor gave up, if it did.
	liteErr string
}

func newVaultService(cfgs *config.Store, container bool, logger *slog.Logger, brains func() []vaultBrain, openMedia func() []string) *vaultService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &vaultService{cfgs: cfgs, container: container, log: logger, brains: brains, openMedia: openMedia}
	s.refreshLayout()
	s.status = s.loadStatus()
	s.status.Enabled = cfgs.Get().Vault.Enabled
	s.status.State = vaultOff
	if s.status.Enabled {
		s.status.State = vaultStarting
	}
	return s
}

func (s *vaultService) statusPath() string {
	return filepath.Join(s.cfgs.Boot().Layout().Core(), vaultStatusFile)
}

// loadStatus reads what the last run kept (when snapshots ran, the
// eviction log aside), so the schedule goes on where it was.
func (s *vaultService) loadStatus() vaultStatus {
	var st vaultStatus
	if data, err := os.ReadFile(s.statusPath()); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	st.Job = ""
	st.Litestream.Running, st.Litestream.PID = false, 0
	return st
}

// saveLocked writes the status file; the caller holds mu.
func (s *vaultService) saveLocked() {
	s.status.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(s.status, "", "  ")
	if err != nil {
		return
	}
	dir, err := config.EnsureDir(s.cfgs.Boot().Layout().Core())
	if err != nil {
		s.log.Warn("vault: keeping its status", "err", err)
		return
	}
	if err := fsx.WriteFileAtomic(filepath.Join(dir, vaultStatusFile), append(data, '\n'), 0o600, false); err != nil {
		s.log.Warn("vault: keeping its status", "err", err)
	}
}

// refreshLayout works the hot root out again (a brain was added or moved).
func (s *vaultService) refreshLayout() {
	brains := map[string]string{}
	if s.brains != nil {
		for _, b := range s.brains() {
			brains[b.ID] = b.Dir
		}
	}
	l := vaultLayoutOf(s.cfgs.Get(), brains)
	s.layout.Store(&l)
}

func (s *vaultService) currentLayout() vaultLayout { return *s.layout.Load() }

// Start starts the vault, in the background, for as long as ctx lasts;
// a change of the vault settings starts it again (Apply).
func (s *vaultService) Start(ctx context.Context) {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	s.Apply(s.cfgs.Get())
}

// Stop stops the vault and waits for it: its uploads, its snapshot and
// Litestream end before the bridge closes bridge.db.
func (s *vaultService) Stop() {
	s.mu.Lock()
	s.ctx = nil
	s.mu.Unlock()
	s.stopRun()
}

// Apply follows a configuration change: a new brain changes the hot root,
// and a change to the vault section starts the vault again with it.
func (s *vaultService) Apply(cfg *config.Settings) {
	s.refreshLayout()
	s.mu.Lock()
	ctx := s.ctx
	same := s.running != nil && s.settings == cfg.Vault
	s.mu.Unlock()
	if ctx == nil || same {
		return
	}
	s.stopRun()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx == nil {
		return
	}
	s.settings = cfg.Vault
	s.status.Enabled = cfg.Vault.Enabled
	if !cfg.Vault.Enabled {
		s.status.State, s.status.Error = vaultOff, ""
		return
	}
	s.status.State, s.status.Error = vaultStarting, ""
	runCtx, cancel := context.WithCancel(s.ctx)
	run := &vaultRun{cancel: cancel, done: make(chan struct{})}
	s.running = run
	go func() {
		defer close(run.done)
		s.serve(runCtx, run, cfg)
	}()
}

func (s *vaultService) stopRun() {
	s.mu.Lock()
	run := s.running
	s.running = nil
	s.mu.Unlock()
	if run == nil {
		return
	}
	run.cancel()
	<-run.done
	// What the status file says once nothing runs it: fridolin vault
	// status shows it while the bridge is down.
	s.mu.Lock()
	if s.status.State == vaultRunning || s.status.State == vaultStarting {
		s.status.State = vaultStopped
		s.status.Litestream.Running, s.status.Litestream.PID = false, 0
		s.saveLocked()
	}
	s.mu.Unlock()
}

func (s *vaultService) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.State, s.status.Error = vaultError, err.Error()
	s.saveLocked()
}

// serve opens the vault and runs it until ctx ends.
func (s *vaultService) serve(ctx context.Context, run *vaultRun, cfg *config.Settings) {
	b, err := vaultBackendsFor(ctx, s, cfg)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("vault: not started", "err", err)
			s.setError(err)
		}
		return
	}
	layout := s.currentLayout()
	policy := vault.DefaultPolicy()
	policy.ActivityDays, policy.ColdAfterDays = cfg.Vault.ActivityDays, cfg.Vault.ColdAfterDays
	v, err := vault.New(vault.Options{
		Root: layout.root, DBPath: cfg.Boot.Layout().VaultDB(), Store: b.cold, Policy: &policy,
		BudgetBytes: int64(cfg.Vault.HotBudgetGB) << 30, HeadroomPercent: cfg.Vault.HeadroomPercent,
		OpenThreadPaths: s.openThreadPaths,
		FreeSpace: func(dir string) (int64, error) {
			n, err := platform.FreeBytes(dir)
			return int64(min(n, 1<<62)), err
		},
		Ignore: func(rel string) bool { return s.currentLayout().ignore(rel) },
		Logger: s.log,
	})
	if err != nil {
		s.log.Error("vault: not started", "err", err)
		s.setError(err)
		return
	}
	defer func() {
		if err := v.Close(); err != nil {
			s.log.Warn("vault: closing vault.db", "err", err)
		}
	}()
	s.mu.Lock()
	run.v, run.b = v, b
	run.snaps = newVaultSnapshots(s, run, cfg)
	s.status.State, s.status.Error = vaultRunning, ""
	s.status.Root = layout.root
	s.status.BudgetBytes = int64(cfg.Vault.HotBudgetGB) << 30
	s.status.TargetBytes = s.status.BudgetBytes * int64(100-cfg.Vault.HeadroomPercent) / 100
	s.status.Snapshots.Enabled = b.restic != nil
	s.status.Snapshots.Error = b.snapErr
	if b.restic != nil {
		s.status.Snapshots.Repo = b.restic.Repo
	}
	s.status.Litestream.Enabled = b.lite != nil
	s.mu.Unlock()
	s.log.Info("vault: running", "root", layout.root)

	var wg sync.WaitGroup
	defer wg.Wait()
	if b.lite != nil {
		// Its config file goes in core/, which must be there first.
		if _, err := config.EnsureDir(cfg.Boot.Layout().Core()); err != nil {
			s.log.Warn("vault: no folder for Litestream's config", "err", err)
		}
		wg.Go(func() {
			if err := b.lite.Run(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("vault: litestream stopped", "err", err)
				s.mu.Lock()
				run.liteErr = err.Error()
				s.mu.Unlock()
			}
		})
	}
	s.ping(ctx, v)
	s.syncAll(ctx, v)
	s.evict(ctx, v)
	wg.Go(func() {
		err := v.Watch(ctx, vault.WatchOptions{OnSync: func(rep vault.SyncReport, err error) { s.recordSync(ctx, v, rep, err) }})
		if err != nil && ctx.Err() == nil {
			s.log.Warn("vault: watching the hot tier stopped; the full sync every few hours still uploads", "err", err)
		}
	})
	lastPing, lastEvict, lastFull := time.Now(), time.Now(), time.Now()
	tick := time.NewTicker(vaultTickEvery)
	defer tick.Stop()
	s.heartbeat(ctx, run, true)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			if now.Sub(lastPing) >= vaultPingEvery {
				lastPing = now
				s.ping(ctx, v)
			}
			if now.Sub(lastFull) >= vaultFullSyncEvery {
				lastFull = now
				s.syncAll(ctx, v)
			}
			if now.Sub(lastEvict) >= vaultEvictEvery {
				lastEvict = now
				s.evict(ctx, v)
			}
			run.snaps.tick(ctx, now)
			s.heartbeat(ctx, run, true)
		}
	}
}

// heartbeat refreshes the numbers and Litestream's state; save writes the
// status file too.
func (s *vaultService) heartbeat(ctx context.Context, run *vaultRun, save bool) {
	st, err := run.v.Stats(ctx)
	if err != nil && ctx.Err() == nil {
		s.log.Warn("vault: reading its index", "err", err)
	}
	ev, _ := run.v.EvictionLog(ctx, vaultEvictionsShown)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		s.status.HotBytes, s.status.ColdBytes = st.HotBytes, st.ColdBytes
		s.status.Files = vaultFileCounts{Hot: st.Hot, Cold: st.Cold, Both: st.Both}
		s.status.LastUpload = st.LastUpload
	}
	s.status.Evictions = ev
	if run.b.lite != nil {
		s.status.Litestream.LitestreamStatus = run.b.lite.Status()
		if s.status.Litestream.LastError == "" {
			s.status.Litestream.LastError = run.liteErr
		}
		s.status.Litestream.DBs = litestreamNames(run.b.lite)
	}
	if save {
		s.saveLocked()
	}
}

func litestreamNames(l *snapshot.Litestream) []string {
	var out []string
	for _, db := range l.DBs {
		out = append(out, filepath.Base(db.Path))
	}
	return out
}

func (s *vaultService) ping(ctx context.Context, v *vault.Vault) {
	err := v.Ping(ctx)
	if ctx.Err() != nil {
		return
	}
	r := &vaultReach{At: time.Now().UTC(), OK: err == nil}
	if err != nil {
		r.Error = err.Error()
		s.log.Warn("vault: the cold tier does not answer", "err", err)
	}
	s.mu.Lock()
	s.status.Reachable = r
	s.saveLocked()
	s.mu.Unlock()
}

func (s *vaultService) syncAll(ctx context.Context, v *vault.Vault) {
	rep, err := v.Sync(ctx)
	s.recordSync(ctx, v, rep, err)
}

func (s *vaultService) recordSync(ctx context.Context, v *vault.Vault, rep vault.SyncReport, err error) {
	if ctx.Err() != nil {
		return
	}
	r := &vaultSyncRecord{At: time.Now().UTC(), Uploaded: len(rep.Uploaded), UploadedBytes: rep.UploadedBytes, Failed: len(rep.Failed)}
	if err != nil {
		r.Error = err.Error()
	} else if len(rep.Failed) > 0 {
		for p, e := range rep.Failed {
			r.Error = p + ": " + e
			break
		}
	}
	if r.Error != "" {
		s.log.Warn("vault: upload", "failed", r.Failed, "err", r.Error)
	}
	s.mu.Lock()
	s.status.LastSync = r
	s.mu.Unlock()
}

func (s *vaultService) evict(ctx context.Context, v *vault.Vault) {
	rep, err := v.Evict(ctx)
	if ctx.Err() != nil {
		return
	}
	r := &vaultEvictRecord{At: time.Now().UTC(), Evicted: len(rep.Evicted), FreedBytes: rep.HotBytesBefore - rep.HotBytesAfter, OverBudget: rep.OverBudget}
	if err != nil {
		r.Error = err.Error()
		s.log.Warn("vault: eviction", "err", err)
	}
	if len(rep.Evicted) > 0 {
		s.log.Info("vault: sent files cold", "files", len(rep.Evicted), "bytes", r.FreedBytes)
	}
	s.mu.Lock()
	s.status.LastEvict = r
	s.mu.Unlock()
}

// openThreadPaths is vault.Options.OpenThreadPaths: the files of the
// messages a brain is running or has waiting, as vault paths
// (docs/DECISIONS.md, M3 wiring: today's open threads).
func (s *vaultService) openThreadPaths(context.Context) ([]string, error) {
	if s.openMedia == nil {
		return nil, nil
	}
	l := s.currentLayout()
	var out []string
	for _, p := range s.openMedia() {
		if rel, ok := l.rel(AbsPath(p)); ok && rel != "" {
			out = append(out, rel)
		}
	}
	return out, nil
}

// vault returns the open vault, or an error that says why there is none.
func (s *vaultService) vault() (*vault.Vault, *vaultRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running != nil && s.running.v != nil && s.status.State == vaultRunning {
		return s.running.v, s.running, nil
	}
	if !s.status.Enabled {
		return nil, nil, apperr.New(apperr.Precondition, "the vault is off: set vault.enabled in config.json")
	}
	if s.status.Error != "" {
		return nil, nil, apperr.New(apperr.Precondition, "the vault does not run: "+s.status.Error)
	}
	return nil, nil, apperr.New(apperr.Precondition, "the vault is starting")
}

// Running reports whether the vault is open, which the vault tools and
// the prompt's vault lines need.
func (s *vaultService) Running() bool {
	_, _, err := s.vault()
	return err == nil
}

// Status is GET /api/vault/status.
func (s *vaultService) Status(ctx context.Context) vaultStatus {
	if _, run, err := s.vault(); err == nil {
		s.heartbeat(ctx, run, false)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status
	st.Job = s.job
	st.Evictions = slices.Clone(st.Evictions)
	if st.Evictions == nil {
		st.Evictions = []vault.Eviction{}
	}
	return st
}

// errVaultBusy is a job asked for while another runs.
func errVaultBusy(job string) error {
	return apperr.New(apperr.Conflict, "the vault is busy with "+job+"; try again when it is done")
}

// startJob runs fn as the vault's one manual job.
func (s *vaultService) startJob(name string, fn func(ctx context.Context, run *vaultRun) error) error {
	_, run, err := s.vault()
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.job != "" {
		job := s.job
		s.mu.Unlock()
		return errVaultBusy(job)
	}
	s.job = name
	ctx := s.ctx
	s.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		err := fn(ctx, run)
		if err != nil && ctx.Err() == nil {
			s.log.Warn("vault: "+name, "err", err)
		}
		s.mu.Lock()
		s.job = ""
		s.mu.Unlock()
		s.heartbeat(ctx, run, true)
	}()
	return nil
}

// Sync, Evict and Rebuild start those jobs (POST /api/vault/…).
func (s *vaultService) Sync() error {
	return s.startJob("sync", func(ctx context.Context, run *vaultRun) error { s.syncAll(ctx, run.v); return nil })
}

func (s *vaultService) Evict() error {
	return s.startJob("evict", func(ctx context.Context, run *vaultRun) error { s.evict(ctx, run.v); return nil })
}

// Rebuild rebuilds vault.db from the cold tier and the disk, then brings
// back every pinned file that is only cold: what a fresh core does.
func (s *vaultService) Rebuild() error {
	return s.startJob("rebuild", func(ctx context.Context, run *vaultRun) error {
		rep, err := run.v.Rebuild(ctx)
		if err != nil {
			return err
		}
		got, err := run.v.HydratePinned(ctx)
		s.log.Info("vault: rebuilt its index", "rows", rep.Rows, "cold", rep.Cold, "both", rep.Both, "hot", rep.Hot, "pinned_hydrated", len(got))
		return err
	})
}

// Get brings rel back from the cold tier (POST /api/vault/get and
// fridolin vault get), asked for by the person: the budget does not stop
// it when allowOverBudget.
func (s *vaultService) Get(ctx context.Context, rel string, allowOverBudget bool) (string, error) {
	v, _, err := s.vault()
	if err != nil {
		return "", err
	}
	p, err := v.Get(ctx, rel, vault.GetOptions{AllowOverBudget: allowOverBudget})
	switch {
	case errors.Is(err, vault.ErrNotFound):
		return "", apperr.New(apperr.NotFound, err.Error())
	case errors.Is(err, vault.ErrInsufficientSpace):
		return "", apperr.New(apperr.Precondition, err.Error()+"; bring it back anyway with allow_over_budget")
	case err != nil:
		return "", err
	}
	return p, nil
}

// hydrate brings back, before a run, the files of its message that went
// cold (automatic hydration for an open thread). The budget holds: a file
// that does not fit stays cold, and the run sees it in .cold.json.
func (s *vaultService) hydrate(ctx context.Context, brainID string, paths []string) {
	v, _, err := s.vault()
	if err != nil || len(paths) == 0 {
		return
	}
	l := s.currentLayout()
	for _, p := range paths {
		rel, ok := l.rel(AbsPath(p))
		if !ok || rel == "" {
			continue
		}
		e, err := v.Stat(ctx, rel)
		if err != nil || e.Tier != vault.TierCold {
			continue
		}
		if _, err := v.Get(ctx, rel, vault.GetOptions{}); err != nil {
			s.log.Warn("vault: bringing back a file of the message before the run", "brain", brainID, "path", rel, "err", err)
		}
	}
}

// readableCopy makes a copy of the vault file at abs that brain id's run
// can read: in its own state's tmp/vault/ folder, the TMPDIR its sandbox
// lets it read and write. The copy is a file of its own (not a hard link),
// so a run that writes to it cannot change the vault's file.
func readableCopy(dataDir, id, rel, abs string) (string, error) {
	dst := filepath.Join(brainStateOf(AbsPath(dataDir), id).tmp, "vault", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	src, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return "", err
	}
	if old, err := os.Stat(dst); err == nil && old.Size() == st.Size() && old.ModTime().Equal(st.ModTime()) {
		return dst, nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".*.tmp")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chtimes(tmp.Name(), st.ModTime(), st.ModTime()); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	return dst, nil
}

// readableTrees are the folders brain id's run reads directly: its folder
// and its media inbox. A vault file in one of them needs no copy.
func (b *App) readableTrees(id string) []string {
	var out []string
	if br := b.brainMgr.Get(id); br != nil {
		out = append(out, br.Dir())
	}
	cfg := b.cfgs.Get()
	if cfg.MediaDir != "" {
		out = append(out, mediaInboxAt(AbsPath(cfg.MediaDir)).Path(id))
	}
	return out
}

func insideAny(p string, dirs []string) bool {
	for _, d := range dirs {
		if d == "" {
			continue
		}
		d = filepath.Clean(d)
		if p == d || strings.HasPrefix(p, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// driveSuperseded is backup.Options.Superseded: once the vault's restic
// repository on Drive has completed a copy, and while the vault runs,
// the old Drive sync's daily run stops (compat: until then it goes on).
func (s *vaultService) driveSuperseded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status.Enabled && s.status.Snapshots.Drive.CompletedOnce
}

// vaultBrains are the loaded brains with their folders.
func (b *App) vaultBrains() []vaultBrain {
	if b.brainMgr == nil {
		return nil
	}
	var out []vaultBrain
	for _, br := range b.brainMgr.Brains() {
		out = append(out, vaultBrain{ID: br.ID(), Dir: br.Dir()})
	}
	return out
}

// openMedia lists the files of every message a brain runs or has waiting.
func (b *App) openMedia() []string {
	if b.brainMgr == nil {
		return nil
	}
	var out []string
	for _, br := range b.brainMgr.Brains() {
		out = append(out, br.OpenMedia()...)
	}
	return out
}
