package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/housekeeping"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/snapshot"
	"github.com/Icewater1337/fridolin/internal/store"
)

// The housekeeping job (internal/housekeeping, docs/CONFIGURATION.md
// "housekeeping"): every housekeeping.every_hours, and when the dashboard
// or `fridolin housekeeping run` asks, it looks for what the bridge and
// its children left running and for stale locks, stops or removes what
// is the bridge's own (unless housekeeping.dry_run, the default), and
// puts heavy processes that are not the bridge's to the owner, in the
// core's container only. Its last report is core/housekeeping.json,
// which the dashboard, the CLI and doctor read.

// housekeepingFile is the job's state in the data dir's core folder.
const housekeepingFile = "housekeeping.json"

// housekeepingNotify is the Notify of the approvals the job asks for:
// their answers come back to it (housekeepingDecided), not to a brain.
const housekeepingNotify = "fridolin:housekeeping"

// housekeepingFirstRun is how long after the bridge starts the first
// scheduled run may come, so that a restart has settled first.
var housekeepingFirstRun = 10 * time.Minute

// housekeepingTick is how often the schedule is looked at.
var housekeepingTick = 5 * time.Minute

// maxHousekeepingAsks bounds the approval polls one run sends.
const maxHousekeepingAsks = 3

// housekeepingState is core/housekeeping.json.
type housekeepingState struct {
	Last *housekeeping.Report `json:"last,omitempty"`
	// Asked are the processes put to the owner and not decided yet, by
	// approval id.
	Asked map[string]housekeeping.Finding `json:"asked,omitempty"`
}

// housekeepingService runs the job.
type housekeepingService struct {
	b         *App
	container bool
	started   time.Time

	// run is held for a run, one at a time.
	run sync.Mutex
	mu  sync.Mutex
	st  housekeepingState
	// running says a run is under way, for the dashboard.
	running bool

	// The system, as functions, so tests use stand-ins.
	list      func() ([]platform.Proc, error)
	env       func(pid int) ([]string, error)
	signal    func(pid int, force bool) error
	sampleGap time.Duration
	grace     time.Duration
}

func newHousekeeping(b *App, container bool) *housekeepingService {
	h := &housekeepingService{b: b, container: container, started: time.Now(),
		list: platform.Processes, env: platform.ProcessEnv, signal: platform.SignalProcess,
		sampleGap: 2 * time.Second, grace: 5 * time.Second}
	h.st = h.load()
	return h
}

func (h *housekeepingService) path() string {
	return filepath.Join(h.b.cfgs.Boot().Layout().Core(), housekeepingFile)
}

func (h *housekeepingService) load() housekeepingState {
	var st housekeepingState
	if data, err := os.ReadFile(h.path()); err == nil {
		if err := json.Unmarshal(data, &st); err != nil {
			log.Printf("housekeeping: reading %s: %v", h.path(), err)
		}
	}
	return st
}

// saveLocked writes the state; the caller holds h.mu.
func (h *housekeepingService) saveLocked() {
	dir, err := config.EnsureDir(h.b.cfgs.Boot().Layout().Core())
	if err == nil {
		var data []byte
		if data, err = json.MarshalIndent(h.st, "", "  "); err == nil {
			err = fsx.WriteFileAtomic(filepath.Join(dir, housekeepingFile), append(data, '\n'), 0o600, false)
		}
	}
	if err != nil {
		log.Printf("housekeeping: saving its report: %v", err)
	}
}

// ReadHousekeepingFile reads core/housekeeping.json of dataDir, for the CLI
// and doctor.
func ReadHousekeepingFile(dataDir string, read func(string) ([]byte, error)) ([]byte, error) {
	return read(filepath.Join(dataDir, "core", housekeepingFile))
}

// nextRun is when the schedule runs the job next.
func (h *housekeepingService) nextRun(cfg *config.Settings) time.Time {
	h.mu.Lock()
	last := h.st.Last
	h.mu.Unlock()
	first := h.started.Add(housekeepingFirstRun)
	if last == nil {
		return first
	}
	return later(last.Started.Add(cfg.Housekeeping.Every), first)
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// Start runs the schedule until ctx ends.
func (h *housekeepingService) Start(ctx context.Context) {
	t := time.NewTicker(housekeepingTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cfg := h.b.cfgs.Get()
		if !cfg.Housekeeping.Enabled || time.Now().Before(h.nextRun(cfg)) {
			continue
		}
		if _, err := h.Run(ctx, "schedule", false); err != nil && !errors.Is(err, errHousekeepingBusy) {
			log.Printf("housekeeping: %v", err)
		}
	}
}

var errHousekeepingBusy = apperr.New(apperr.Conflict, "a housekeeping run is under way; try again when it is done")

// ownership is what tells the bridge's processes from others.
func (h *housekeepingService) ownership() housekeeping.Ownership {
	boot := h.b.cfgs.Boot()
	tokens := filepath.Join(AbsPath(boot.DataDir), runTokensDirName)
	return housekeeping.Ownership{
		Self: os.Getpid(), Exe: h.b.exe, RunTokensDir: tokens, CoreDir: boot.Layout().Core(),
		TokenLive: func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		},
		MCPRunLive: func(id string) bool {
			m, _ := filepath.Glob(filepath.Join(tokens, "*", "run-"+id+".mcp.json"))
			return len(m) > 0
		},
	}
}

// Run runs the job once. forceDry only reports, whatever
// housekeeping.dry_run says.
func (h *housekeepingService) Run(ctx context.Context, trigger string, forceDry bool) (housekeeping.Report, error) {
	if !h.run.TryLock() {
		return housekeeping.Report{}, errHousekeepingBusy
	}
	defer h.run.Unlock()
	h.mu.Lock()
	h.running = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.running = false
		h.mu.Unlock()
	}()

	cfg := h.b.cfgs.Get()
	hk := cfg.Housekeeping
	rep := housekeeping.Report{Started: time.Now().UTC(), Trigger: trigger, DryRun: hk.DryRun || forceDry}
	if !rep.DryRun && h.b.locked() {
		rep.DryRun, rep.Note = true, "the bridge is in lockdown: stopping is a write action, so this run only reported"
	}
	set := housekeeping.Settings{DryRun: rep.DryRun, CPUPercent: float64(hk.CPUPercent), RSSBytes: uint64(hk.RSSMB) << 20,
		MinAge: hk.MinAge, Container: h.container}

	var findings []housekeeping.Finding
	sample, err := housekeeping.TakeSample(ctx, h.list, h.sampleGap)
	if err != nil {
		rep.Errors = append(rep.Errors, "listing processes: "+err.Error())
	} else {
		rep.Scanned = len(sample.Procs)
		findings = housekeeping.Plan(sample, h.env, set, h.ownership())
	}
	findings = append(findings, h.viewerFindings(cfg, set.MinAge)...)
	findings = append(findings, h.runRecordFindings(set.MinAge)...)
	findings = append(findings, h.bridgeLockFindings()...)

	asks := 0
	for i := range findings {
		h.act(ctx, &findings[i], rep.DryRun, &asks)
	}
	locks, errs := h.resticLockFindings(ctx, rep.DryRun)
	findings = append(findings, locks...)
	rep.Errors = append(rep.Errors, errs...)
	sessions, errs := h.nodeSessionFindings(ctx, rep.DryRun, set.MinAge)
	findings = append(findings, sessions...)
	rep.Errors = append(rep.Errors, errs...)
	// The scheduled agents' history within schedules.keep_days, keep_runs
	// and max_total_mb, in a dry run too: it removes only the bridge's own
	// records, as a log rotation does, and nobody is told.
	if n, err := h.b.agents.prune(time.Now()); err != nil {
		rep.Errors = append(rep.Errors, "the scheduled agents' history: "+err.Error())
	} else if n > 0 {
		log.Printf("housekeeping: dropped %d runs of scheduled agents beyond schedules.keep_days, keep_runs or max_total_mb", n)
	}
	rep.Findings = findings
	if rep.Findings == nil {
		rep.Findings = []housekeeping.Finding{}
	}
	rep.Finished = time.Now().UTC()

	h.mu.Lock()
	h.st.Last = &rep
	h.saveLocked()
	h.mu.Unlock()

	h.b.audit.Append(audit.Entry{Kind: audit.Housekeeping, By: "housekeeping", Detail: trigger + ": " + rep.Summary(), Outcome: runOutcome(rep)})
	log.Printf("housekeeping (%s): %s", trigger, rep.Summary())
	h.tellOwner(ctx, rep)
	return rep, nil
}

func runOutcome(r housekeeping.Report) string {
	switch {
	case len(r.Errors) > 0:
		return "errors: " + strings.Join(r.Errors, "; ")
	case r.DryRun:
		return "dry-run"
	}
	return "done"
}

// act carries out f, or in a dry run says what it would do.
func (h *housekeepingService) act(ctx context.Context, f *housekeeping.Finding, dry bool, asks *int) {
	switch f.Action {
	case housekeeping.ActionReport:
		if f.Outcome == "" {
			f.Outcome = housekeeping.OutcomeReported
		}
		return
	case housekeeping.ActionAsk:
		if dry {
			f.Outcome = housekeeping.OutcomeWouldAsk
			return
		}
		h.ask(ctx, f, asks)
		return
	}
	if dry {
		f.Outcome = housekeeping.OutcomeWouldStop
		return
	}
	var err error
	switch f.Kind {
	case housekeeping.KindViewer:
		err = h.viewer(h.b.cfgs.Get()).KillSession(f.Target)
	case housekeeping.KindRunRecord:
		err = os.Remove(f.Target)
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
	default:
		var n int
		n, err = h.stopper().Stop(ctx, *f)
		if err == nil && n == 0 {
			f.Outcome = "gone already"
			h.auditFinding(*f)
			return
		}
	}
	if err != nil {
		f.Outcome = housekeeping.OutcomeFailed + ": " + err.Error()
		f.Problem = true
	} else if f.Action == housekeeping.ActionRemove {
		f.Outcome = housekeeping.OutcomeRemoved
	} else {
		f.Outcome = housekeeping.OutcomeStopped
	}
	h.auditFinding(*f)
}

func (h *housekeepingService) stopper() housekeeping.Stopper {
	return housekeeping.Stopper{List: h.list, Signal: h.signal, Grace: h.grace}
}

func (h *housekeepingService) auditFinding(f housekeeping.Finding) {
	h.b.audit.Append(audit.Entry{Kind: audit.Housekeeping, By: "housekeeping", ID: f.Approval, Risk: "write", Detail: f.Describe(), Outcome: f.Outcome})
}

// ask puts f, a process that is not the bridge's, to the owner, unless
// it was asked about already and the poll is still open.
func (h *housekeepingService) ask(ctx context.Context, f *housekeeping.Finding, asks *int) {
	h.mu.Lock()
	for id, a := range h.st.Asked {
		if a.PID == f.PID && a.Start.Equal(f.Start) {
			h.mu.Unlock()
			f.Outcome, f.Approval = housekeeping.OutcomeAsked, id
			return
		}
	}
	h.mu.Unlock()
	if *asks >= maxHousekeepingAsks {
		f.Outcome = housekeeping.OutcomeReported + ": no more than 3 questions a run"
		return
	}
	id, err := h.b.askForHousekeeping(ctx, *f)
	if err != nil {
		f.Outcome = housekeeping.OutcomeFailed + ": asking the owner: " + err.Error()
		f.Problem = true
		return
	}
	*asks++
	f.Outcome, f.Approval = housekeeping.OutcomeAsked, id
	h.mu.Lock()
	if h.st.Asked == nil {
		h.st.Asked = map[string]housekeeping.Finding{}
	}
	h.st.Asked[id] = *f
	h.saveLocked()
	h.mu.Unlock()
	h.auditFinding(*f)
}

// askForHousekeeping stores an approval for stopping f and puts its poll
// into the owner's chat. Its answer comes to housekeepingDecided.
func (b *App) askForHousekeeping(ctx context.Context, f housekeeping.Finding) (string, error) {
	target := ownerChat(b.cfgs.Get())
	if target == "" {
		return "", errors.New("allowed_numbers holds no number of the owner")
	}
	now := time.Now()
	a := store.Approval{Kind: approvalKind, ID: "a" + randomToken(9), Run: "housekeeping", Thread: target, Chat: target,
		Notify: housekeepingNotify, Risk: "write",
		Action:    fmt.Sprintf("Housekeeping: stop PID %d (%s), %.0f%% CPU, %s, up %s, which the bridge did not start", f.PID, f.Name, f.CPUPercent, housekeeping.Bytes(f.RSS), (time.Duration(f.AgeSeconds) * time.Second).String()),
		Details:   f.Command + "\n\n" + f.Reason,
		CreatedAt: now, ExpiresAt: now.Add(approvalTimeout(b))}
	if err := b.store.AddApproval(ctx, a); err != nil {
		return "", err
	}
	if err := b.outbox.Poll(ctx, target, outboundmsg.ApprovalQuestion(a.Action), []string{outboundmsg.ApprovalYes, outboundmsg.ApprovalNo, outboundmsg.ApprovalDraft}, approvalRefPrefix+a.ID); err != nil {
		_, _ = b.store.DecideApproval(ctx, a.ID, store.ApprovalDenied, "", "bridge", now)
		return "", err
	}
	b.audit.Append(audit.Entry{Kind: audit.ApprovalRequested, Chat: target, Run: a.Run, ID: a.ID, Risk: a.Risk, Detail: a.Action, Outcome: "pending"})
	return a.ID, nil
}

// housekeepingDecided takes the owner's answer to one of the job's
// approvals: a yes stops the process, if it is still the one asked about.
func (b *App) housekeepingDecided(a store.Approval) {
	h := b.housekeeping
	h.mu.Lock()
	f, ok := h.st.Asked[a.ID]
	delete(h.st.Asked, a.ID)
	h.saveLocked()
	h.mu.Unlock()
	if !ok {
		log.Printf("housekeeping: approval %s is not one of the job's", a.ID)
		return
	}
	if a.Status != store.ApprovalApproved {
		f.Outcome = "not stopped: the owner's answer was " + a.Status
		h.auditFinding(f)
		return
	}
	ctx := context.Background()
	n, err := h.stopper().Stop(ctx, f)
	what := fmt.Sprintf("PID %d (%s)", f.PID, f.Name)
	failure := ""
	switch {
	case err != nil:
		failure = err.Error()
		f.Outcome = housekeeping.OutcomeFailed + ": " + failure
	case n == 0:
		failure = "it had ended, or its PID is another process's now"
		f.Outcome = "gone already"
	default:
		f.Outcome = housekeeping.OutcomeStopped
	}
	h.auditFinding(f)
	if target := ownerChat(b.cfgs.Get()); target != "" {
		if err := b.outbox.Text(ctx, target, outboundmsg.HousekeepingStopResult(what, failure), b.cfgs.Get().MaxChunkSize); err != nil {
			log.Printf("housekeeping: telling the owner: %v", err)
		}
	}
}

// tellOwner sends the WhatsApp summary of a run that stopped or removed
// something or found a problem it cannot fix. A dry run says nothing.
func (h *housekeepingService) tellOwner(ctx context.Context, rep housekeeping.Report) {
	if rep.DryRun || !rep.Acted() {
		return
	}
	var done, problems []string
	for _, f := range rep.Findings {
		switch {
		case f.Outcome == housekeeping.OutcomeStopped || f.Outcome == housekeeping.OutcomeRemoved:
			done = append(done, f.Describe())
		case f.Problem:
			problems = append(problems, f.Describe()+" ("+f.Outcome+")")
		}
	}
	if len(done) == 0 && len(problems) == 0 {
		return
	}
	cfg := h.b.cfgs.Get()
	target := ownerChat(cfg)
	if target == "" {
		return
	}
	if err := h.b.outbox.Text(ctx, target, outboundmsg.HousekeepingSummary(done, problems), cfg.MaxChunkSize); err != nil {
		log.Printf("housekeeping: sending its summary: %v", err)
	}
}

// viewer is a handle on the activity viewer's tmux server.
func (h *housekeepingService) viewer(cfg *config.Settings) *platform.Viewer {
	return &platform.Viewer{TmuxPath: findTmux(), Env: childEnv(h.b.env, anyChild, cfg.ChildEnvPassthrough), Timeout: viewerTimeout}
}

// viewerFindings are activity viewer sessions of loaded brains that
// nobody watches while the viewer is off: a bridge that stopped, or the
// setting turned off, left them.
func (h *housekeepingService) viewerFindings(cfg *config.Settings, minAge time.Duration) []housekeeping.Finding {
	if cfg.Viewer || runtime.GOOS == "windows" || findTmux() == "" || h.b.brainMgr == nil {
		return nil
	}
	sessions, err := h.viewer(cfg).Sessions()
	if err != nil {
		return nil
	}
	ids := map[string]bool{}
	for _, br := range h.b.brainMgr.Brains() {
		ids[br.ID()] = true
	}
	var out []housekeeping.Finding
	for _, s := range sessions {
		id, ok := strings.CutPrefix(s.Name, "claude-")
		if !ok || !ids[id] || s.Attached > 0 || time.Since(s.Created) < minAge {
			continue
		}
		out = append(out, housekeeping.Finding{Kind: housekeeping.KindViewer, Action: housekeeping.ActionRemove, Target: s.Name,
			Owner: "brain " + id, AgeSeconds: int64(time.Since(s.Created) / time.Second),
			Reason: "the activity viewer of brain " + id + ", which nobody watches, while the viewer is off"})
	}
	return out
}

// runRecordFindings are records in runs/ whose process group is gone: a
// run's record is removed when it ends, so these are leftovers.
func (h *housekeepingService) runRecordFindings(minAge time.Duration) []housekeeping.Finding {
	dir := runsDir(h.b.cfgs.Boot().DataDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	boot, _ := bootID()
	var out []housekeeping.Finding
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < minAge {
			continue
		}
		var rec runRecord
		data, err := os.ReadFile(p)
		if err == nil {
			err = json.Unmarshal(data, &rec)
		}
		if err == nil && rec.Boot == boot && boot != "" && runIsOurs(rec) {
			continue // the run goes on
		}
		out = append(out, housekeeping.Finding{Kind: housekeeping.KindRunRecord, Action: housekeeping.ActionRemove, Target: p,
			Owner: "brain " + rec.Brain, Reason: fmt.Sprintf("the record of run %d of brain %s, whose processes are gone", rec.PGID, rec.Brain)})
	}
	return out
}

// bridgeLockFindings checks bridge.lock, which this bridge holds: it must
// name this bridge. Another PID in it means something rewrote it.
func (h *housekeepingService) bridgeLockFindings() []housekeeping.Finding {
	p := h.b.cfgs.Boot().Layout().Lock()
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if pid == os.Getpid() {
		return nil
	}
	return []housekeeping.Finding{{Kind: housekeeping.KindBridgeLock, Action: housekeeping.ActionReport, Target: p, Problem: true,
		Reason: fmt.Sprintf("bridge.lock names PID %d, not this bridge (PID %d): something rewrote it; the lock itself still holds", pid, os.Getpid())}}
}

// resticLockFindings finds the locks restic runs of this host left in the
// snapshot repositories when they died, and removes them with restic
// unlock (which leaves the locks of runs still going), between the
// vault's own restic jobs.
func (h *housekeepingService) resticLockFindings(ctx context.Context, dry bool) ([]housekeeping.Finding, []string) {
	host, err := os.Hostname()
	if err != nil {
		return nil, []string{"restic locks: no host name: " + err.Error()}
	}
	var out []housekeeping.Finding
	var errs []string
	_, err = h.b.vault.withRestic(func(repos map[string]*snapshot.Restic, charge func(string)) {
		names := make([]string, 0, len(repos))
		for n := range repos {
			names = append(names, n)
		}
		slices.Sort(names)
		for _, name := range names {
			r := repos[name]
			locks, err := r.Locks(ctx)
			charge("list-locks")
			for range locks {
				charge("cat-lock")
			}
			if err != nil {
				errs = append(errs, "restic locks of "+name+": "+err.Error())
				continue
			}
			dead := snapshot.DeadLocks(locks, host, platform.ProcessAlive)
			var fs []housekeeping.Finding
			for _, l := range dead {
				kind := "shared"
				if l.Exclusive {
					kind = "exclusive"
				}
				fs = append(fs, housekeeping.Finding{Kind: housekeeping.KindResticLock, Action: housekeeping.ActionRemove,
					Target: name + " " + shortID(l.ID), Owner: "vault",
					AgeSeconds: int64(time.Since(l.Time) / time.Second),
					Reason:     fmt.Sprintf("a %s lock of restic PID %d on this host, which is gone", kind, l.PID)})
			}
			if len(fs) > 0 {
				outcome := housekeeping.OutcomeWouldStop
				if !dry {
					err := r.Unlock(ctx)
					charge("unlock")
					outcome = housekeeping.OutcomeRemoved
					if err != nil {
						outcome = housekeeping.OutcomeFailed + ": " + err.Error()
					}
				}
				for i := range fs {
					fs[i].Outcome = outcome
					fs[i].Problem = strings.HasPrefix(outcome, housekeeping.OutcomeFailed)
					if !dry {
						h.auditFinding(fs[i])
					}
				}
			}
			out = append(out, fs...)
		}
	})
	if err != nil {
		errs = append(errs, "restic locks: "+err.Error())
	}
	return out, errs
}

// view is GET /api/housekeeping.
func (h *housekeepingService) view() map[string]any {
	cfg := h.b.cfgs.Get()
	hk := cfg.Housekeeping
	h.mu.Lock()
	last := h.st.Last
	running := h.running
	asked := make([]housekeeping.Finding, 0, len(h.st.Asked))
	for _, f := range h.st.Asked {
		asked = append(asked, f)
	}
	h.mu.Unlock()
	slices.SortFunc(asked, func(a, b housekeeping.Finding) int { return strings.Compare(a.Approval, b.Approval) })
	out := map[string]any{
		"enabled": hk.Enabled, "dry_run": hk.DryRun, "every_hours": int(hk.Every / time.Hour),
		"cpu_percent": hk.CPUPercent, "rss_mb": hk.RSSMB, "min_age_minutes": int(hk.MinAge / time.Minute),
		"container": h.container, "running": running, "asked": asked, "last": nil,
	}
	if hk.Enabled {
		out["next_run"] = h.nextRun(cfg).UTC().Format(time.RFC3339)
	}
	if last != nil {
		out["last"] = last
		out["counts"] = last.Counts()
		out["summary"] = last.Summary()
	}
	return out
}

func (b *App) newHousekeepingAPI() *httpapi.HousekeepingAPI {
	return &httpapi.HousekeepingAPI{
		Status: func(context.Context) (any, error) { return b.housekeeping.view(), nil },
		Run: func(ctx context.Context, dry bool, trigger string) (any, error) {
			return b.housekeeping.Run(context.WithoutCancel(ctx), trigger, dry)
		},
	}
}
