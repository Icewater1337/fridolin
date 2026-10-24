package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// The core's move between two core hosts (docs/CORE.md, "The core moves
// itself"; #187 §3). The core prepares it (core.image.pull, the host
// secrets, core.rehearse), then signs a move plan with its update key and
// hands it to both agents: core.release here when this machine runs the
// core now, core.handoff when it is to run it next. Each accepts the plan
// at once and runs its half in the background, since the core that sent
// it stops on the way: from then on the two agents coordinate only
// through the lease and the exports in the bucket, which CoreRunner reads.
// Each step is written to core-moves/<id>.json in the state dir, which
// core.move.status reads back to the core that runs afterwards.
const coreMovesDir = "core-moves"

// CoreRunner is what core-host does to the core on this machine. Every
// method does one fixed thing; NativeCore runs the agent's own binary
// with a fixed `fridolin core` subcommand for each.
// SecretFiler is a CoreRunner that also moves the secret files of the
// core's credentials folder (sandbox.credentials_dir) with the host
// secrets.
type SecretFiler interface {
	// ExportFiles reads them, with the folder under ~ config names
	// (sandbox.credentials_dir; "" for none).
	ExportFiles(ctx context.Context) (dir string, files map[string][]byte, err error)
	// ImportFiles writes them into dir under the core's home, keeping
	// any there already.
	ImportFiles(ctx context.Context, dir string, files map[string][]byte) (stored, kept []string, err error)
}

type CoreRunner interface {
	// Status reads this data dir's device id, the lease and the latest
	// export.
	Status(ctx context.Context) (nodeproto.CoreRuntime, error)
	// Stop stops the core cleanly (which releases the lease), Start
	// starts it.
	Stop(ctx context.Context) error
	Start(ctx context.Context) error
	// Export exports the data dir, tagged with move.
	Export(ctx context.Context, move string) (nodeproto.CoreExportInfo, error)
	// Import imports the latest export, moving a data dir with data in
	// it aside first.
	Import(ctx context.Context) error
	// Promote takes the lease for this data dir; Release frees it when
	// this data dir holds it.
	Promote(ctx context.Context) error
	Release(ctx context.Context) error
	// Ready reports whether the core here is ready: WhatsApp connected,
	// the lease held.
	Ready(ctx context.Context) (bool, string)
	// Rehearse imports the latest export into a scratch place, checks it
	// and removes it.
	Rehearse(ctx context.Context, move string) (nodeproto.CoreRehearsal, error)
	// Image makes sure the host can start image (by digest) of version,
	// pulling it unless checkOnly.
	Image(ctx context.Context, version, image string, checkOnly bool) (nodeproto.CoreImageResult, error)
}

// moveRun is one move on this machine.
type moveRun struct {
	Plan  nodeproto.SignedMovePlan `json:"plan"`
	State nodeproto.CoreMoveState  `json:"state"`
	// Complete is set when the core that runs after the move said so.
	Complete bool `json:"complete,omitempty"`
	// OtherSince is when the source first saw the lease released by
	// another device without an export of this move by it.
	OtherSince time.Time `json:"other_since,omitzero"`
	// Started is when the target started the core.
	Started time.Time `json:"started,omitzero"`

	plan nodeproto.MovePlan
}

// coreMoves is the agent's moves, by id.
type coreMoves struct {
	mu    sync.Mutex
	runs  map[string]*moveRun
	ctx   context.Context
	wg    sync.WaitGroup
	alive bool
}

func (a *Agent) movePoll() time.Duration {
	if a.MovePoll > 0 {
		return a.MovePoll
	}
	return 10 * time.Second
}

func (a *Agent) moveGrace() time.Duration {
	if a.MoveGrace > 0 {
		return a.MoveGrace
	}
	return nodeproto.MoveRollbackGrace
}

// moveExportWait is how long the source waits, after the target released
// the lease, for the target's export of the move before it starts again
// on its own data: the target stops (which releases the lease) before it
// exports.
func (a *Agent) moveExportWait() time.Duration {
	if a.MoveExportWait > 0 {
		return a.MoveExportWait
	}
	return 15 * time.Minute
}

// coreRunner is the runner of this machine, or why there is none.
func (a *Agent) coreRunner() (CoreRunner, *nodeproto.Error) {
	if a.CoreRunner != nil {
		return a.CoreRunner, nil
	}
	if a.Config.Box != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "this box has not the core's settings (box-core.yaml): update it with box.sh --update (box.ps1 -Update), which writes them (docs/NODES.md, \"The core in a box\")")
	}
	if runtime.GOOS == "windows" {
		return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "the node service runs as LocalSystem, whose Docker Desktop and Credential Manager are not the owner's: move the core by hand (docs/CORE.md, Moving the core to another machine)")
	}
	if a.Executable == "" {
		return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "the agent does not know its own binary")
	}
	return NativeCore{Exe: a.Executable}, nil
}

// coreMovesStart is called as Run starts: it resumes the moves that had
// not ended when the agent last stopped.
func (a *Agent) coreMovesStart(ctx context.Context) {
	a.moves.mu.Lock()
	a.moves.ctx, a.moves.alive = ctx, true
	if a.moves.runs == nil {
		a.moves.runs = map[string]*moveRun{}
	}
	a.moves.mu.Unlock()
	if a.Dir == "" {
		return
	}
	entries, _ := os.ReadDir(filepath.Join(a.Dir, coreMovesDir))
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || nodeproto.CheckMoveID(id) != nil {
			continue
		}
		m, err := a.loadMove(id)
		if err != nil {
			a.logf("node: core move %s: %v", id, err)
			continue
		}
		a.moves.mu.Lock()
		a.moves.runs[id] = m
		a.moves.mu.Unlock()
		if !nodeproto.MoveFinal(m.State.Phase) {
			a.logf("node: core move %s: resuming as the %s (%s)", id, m.State.Role, m.State.Phase)
			a.goMove(m)
		}
	}
}

// coreMovesStop waits for the moves' goroutines once Run's context ended.
func (a *Agent) coreMovesStop() {
	a.moves.mu.Lock()
	a.moves.alive = false
	a.moves.mu.Unlock()
	a.moves.wg.Wait()
}

// movePath is the state file of move id, which must be a move id: one
// name in core-moves, never a path.
func (a *Agent) movePath(id string) (string, error) {
	if err := nodeproto.CheckMoveID(id); err != nil {
		return "", err
	}
	name := filepath.Base(id + ".json")
	if name != id+".json" || strings.Contains(name, "..") {
		return "", fmt.Errorf("%q is not a move id", id)
	}
	return filepath.Join(a.Dir, coreMovesDir, name), nil
}

func (a *Agent) loadMove(id string) (*moveRun, error) {
	p, err := a.movePath(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var m moveRun
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(m.Plan.Plan, &m.plan); err != nil {
		return nil, err
	}
	return &m, nil
}

// saveMove writes m; the caller holds a.moves.mu.
func (a *Agent) saveMove(m *moveRun) {
	if a.Dir == "" {
		return
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err == nil {
		var p string
		if p, err = a.movePath(m.State.Move); err == nil {
			if err = os.MkdirAll(filepath.Dir(p), 0o700); err == nil {
				err = writeFile(p, append(data, '\n'), 0o600)
			}
		}
	}
	if err != nil {
		a.logf("node: core move %s: writing its state: %v", m.State.Move, err)
	}
}

// step records a step of m, and its phase when phase is not "".
func (a *Agent) step(m *moveRun, phase, step, format string, args ...any) {
	detail := fmt.Sprintf(format, args...)
	a.moves.mu.Lock()
	defer a.moves.mu.Unlock()
	now := time.Now().UTC()
	if phase != "" {
		m.State.Phase = phase
	}
	m.State.Steps = append(m.State.Steps, nodeproto.CoreMoveStep{At: now, Step: step, Detail: detail})
	if len(m.State.Steps) > 200 {
		m.State.Steps = m.State.Steps[len(m.State.Steps)-200:]
	}
	if nodeproto.MoveFinal(m.State.Phase) {
		m.State.Detail = detail
	}
	m.State.Updated = now
	a.saveMove(m)
	a.logf("node: core move %s (%s): %s: %s", m.State.Move, m.State.Role, step, detail)
}

func (a *Agent) setRunning(m *moveRun, running bool) {
	a.moves.mu.Lock()
	m.State.Running = running
	a.saveMove(m)
	a.moves.mu.Unlock()
}

// coreMoveAccept checks a plan for role and starts its half.
func (a *Agent) coreMoveAccept(raw json.RawMessage, role string) (any, *nodeproto.Error) {
	var s nodeproto.SignedMovePlan
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "the move plan: %v", err)
	}
	key, perr := a.trustedCoreKey()
	if perr != nil {
		return nil, perr
	}
	p, err := nodeproto.OpenMovePlan(key, s)
	if err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeDenied, "%v", err)
	}
	if err := nodeproto.CheckMovePlanFor(p, role, a.ID(), a.Version, time.Now()); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeDenied, "%v", err)
	}
	if _, perr := a.coreRunner(); perr != nil {
		return nil, perr
	}
	now := time.Now().UTC()
	m := &moveRun{Plan: s, plan: p, State: nodeproto.CoreMoveState{Move: p.Move, Role: role, Phase: nodeproto.MoveAccepted, Running: role == nodeproto.MoveSource, Steps: []nodeproto.CoreMoveStep{}, Updated: now}}
	a.moves.mu.Lock()
	if a.moves.runs == nil {
		a.moves.runs = map[string]*moveRun{}
	}
	_, seen := a.moves.runs[p.Move]
	if !seen && a.Dir != "" {
		path, err := a.movePath(p.Move)
		if err == nil {
			_, err = os.Stat(path)
		}
		seen = err == nil
	}
	busy := slices.ContainsFunc(mapValues(a.moves.runs), func(o *moveRun) bool { return !nodeproto.MoveFinal(o.State.Phase) })
	alive := a.moves.alive
	if !seen && !busy && alive {
		a.moves.runs[p.Move] = m
		a.saveMove(m)
	}
	a.moves.mu.Unlock()
	switch {
	case seen:
		return nil, nodeproto.Errorf(nodeproto.CodeDenied, "move %s was seen here before: a plan is used once", p.Move)
	case busy:
		return nil, nodeproto.Errorf(nodeproto.CodeDenied, "another core move runs on this machine")
	case !alive:
		return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "the agent is stopping")
	}
	a.step(m, "", "accepted", "the %s of move %s from %s to %s, version %s; deadline %s, ready within %s",
		role, p.Move, p.Source, p.Target, p.Version, p.HandoffDeadline.Format(time.RFC3339), p.Ready())
	a.goMove(m)
	return a.moveState(m), nil
}

func mapValues[K comparable, V any](m map[K]V) []V {
	out := make([]V, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// goMove runs m's half in the background, with the agent's lifetime.
func (a *Agent) goMove(m *moveRun) {
	a.moves.mu.Lock()
	ctx := a.moves.ctx
	a.moves.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	a.moves.wg.Go(func() {
		r, perr := a.coreRunner()
		if perr != nil {
			a.step(m, nodeproto.MoveFailed, "runner", "%s", perr.Message)
			return
		}
		if m.State.Role == nodeproto.MoveSource {
			a.runSource(ctx, r, m)
		} else {
			a.runTarget(ctx, r, m)
		}
	})
}

func (a *Agent) moveState(m *moveRun) nodeproto.CoreMoveState {
	a.moves.mu.Lock()
	defer a.moves.mu.Unlock()
	st := m.State
	st.Steps = slices.Clone(m.State.Steps)
	return st
}

// coreMoveStatus is core.move.status.
func (a *Agent) coreMoveStatus(p nodeproto.CoreMoveStatusParams) (any, *nodeproto.Error) {
	if err := nodeproto.CheckMoveID(p.Move); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	a.moves.mu.Lock()
	m, ok := a.moves.runs[p.Move]
	a.moves.mu.Unlock()
	if !ok && a.Dir != "" {
		if lm, err := a.loadMove(p.Move); err == nil {
			m, ok = lm, true
			a.moves.mu.Lock()
			if a.moves.runs == nil {
				a.moves.runs = map[string]*moveRun{}
			}
			a.moves.runs[p.Move] = m
			a.moves.mu.Unlock()
		}
	}
	if !ok {
		return nil, nodeproto.Errorf(nodeproto.CodeNotFound, "no move %s on this machine", p.Move)
	}
	if p.Complete && m.State.Role == nodeproto.MoveSource {
		a.moves.mu.Lock()
		was := m.Complete
		m.Complete = true
		a.saveMove(m)
		a.moves.mu.Unlock()
		if !was {
			a.logf("node: core move %s: the core runs on %s now", p.Move, m.plan.Target)
		}
	}
	return a.moveState(m), nil
}

// wait sleeps for d, false when ctx ended first.
func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// runSource is the source's half: stop the core, check the lease was
// released, export, then watch the bucket until the target runs the core
// or gave it up, and start again here when it did (§3, "Automatic
// rollback").
func (a *Agent) runSource(ctx context.Context, r CoreRunner, m *moveRun) {
	p := m.plan
	switch m.State.Phase {
	case nodeproto.MoveAccepted:
		a.step(m, nodeproto.MoveStopping, "stop", "stopping the core here")
		if err := r.Stop(ctx); err != nil {
			a.sourceRestart(ctx, r, m, nodeproto.MoveFailed, "the core did not stop: %v", err)
			return
		}
		a.setRunning(m, false)
		st, err := r.Status(ctx)
		if err != nil || st.Lease == nil || st.Lease.Device != st.Device || st.Lease.State != nodeproto.LeaseReleased {
			why := "the lease is not released by this data dir"
			if err != nil {
				why = err.Error()
			}
			a.sourceRestart(ctx, r, m, nodeproto.MoveFailed, "the stop did not release the lease (%s); the core started again here", why)
			return
		}
		a.step(m, nodeproto.MoveExporting, "export", "the lease is released; exporting")
		info, err := r.Export(ctx, p.Move)
		if err != nil {
			a.sourceRestart(ctx, r, m, nodeproto.MoveFailed, "the final export failed (%v); the core started again here", err)
			return
		}
		a.step(m, nodeproto.MoveWatching, "exported", "%s (%d bytes); waiting for %s to take over", info.Key, info.Bytes, p.Target)
	case nodeproto.MoveStopping, nodeproto.MoveExporting:
		// The agent stopped in the middle: whatever happened, the core
		// is stopped here and nobody may run it before the deadline.
		a.step(m, nodeproto.MoveWatching, "resumed", "the agent restarted while it was %s; watching the lease", m.State.Phase)
	}
	a.sourceWatch(ctx, r, m)
}

// sourceRestart starts the core here again after a failed step, and ends
// the move as phase.
func (a *Agent) sourceRestart(ctx context.Context, r CoreRunner, m *moveRun, phase, format string, args ...any) {
	why := fmt.Sprintf(format, args...)
	if err := r.Start(ctx); err != nil {
		a.step(m, nodeproto.MoveFailed, "start", "%s; and starting it again failed: %v. No core runs: fridolin core start here", why, err)
		return
	}
	a.setRunning(m, true)
	a.step(m, phase, "started", "%s", why)
}

func (a *Agent) sourceWatch(ctx context.Context, r CoreRunner, m *moveRun) {
	p := m.plan
	grace := p.HandoffDeadline.Add(a.moveGrace())
	// The longest the target can take: the deadline, its start and ready
	// time, the export it makes when it gives up.
	end := grace.Add(p.Ready()).Add(a.moveExportWait())
	for {
		a.moves.mu.Lock()
		complete := m.Complete
		a.moves.mu.Unlock()
		if complete {
			a.step(m, nodeproto.MoveDone, "done", "the core runs on %s", p.Target)
			return
		}
		st, err := r.Status(ctx)
		now := time.Now()
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			a.logf("node: core move %s: reading the lease: %v", p.Move, err)
		case st.Lease == nil || st.Lease.Device == st.Device:
			// Nobody took it: past the deadline and its grace the
			// target can no longer promote, and nothing newer exists.
			if now.After(grace) {
				a.sourceRollBack(ctx, r, m, false, "%s did not take the core over by the deadline", p.Target)
				return
			}
		default:
			l := st.Lease
			ours := st.Latest != nil && st.Latest.Move == p.Move && st.Latest.Device == st.Device
			theirs := st.Latest != nil && st.Latest.Move == p.Move && st.Latest.Device == l.Device
			switch l.State {
			case nodeproto.LeaseReleased:
				if theirs {
					a.sourceRollBack(ctx, r, m, true, "%s gave the core up (%s); its state comes back with it", p.Target, "not ready in time")
					return
				}
				a.moves.mu.Lock()
				if m.OtherSince.IsZero() {
					m.OtherSince = now.UTC()
					a.saveMove(m)
				}
				since := m.OtherSince
				a.moves.mu.Unlock()
				if now.Sub(since) > a.moveExportWait() {
					a.sourceRollBack(ctx, r, m, false, "%s released the lease without an export of this move; what it received is lost", p.Target)
					return
				}
			case nodeproto.LeaseStale:
				switch {
				case theirs:
					a.sourceRollBack(ctx, r, m, true, "%s stopped renewing the lease; its last export of this move comes back, what it received after it is lost (WhatsApp may ask to pair again)", p.Target)
				case ours:
					a.sourceRollBack(ctx, r, m, false, "%s stopped renewing the lease and exported nothing; what it received is lost (WhatsApp may ask to pair again)", p.Target)
				default:
					a.step(m, nodeproto.MoveFailed, "stale", "%s stopped renewing the lease and the latest export is not of this move: nothing started here; see fridolin core status", p.Target)
				}
				return
			default:
				if now.After(end) {
					a.step(m, nodeproto.MoveDone, "done", "%s holds the lease; the core there did not confirm the move", p.Target)
					return
				}
			}
		}
		if !wait(ctx, a.movePoll()) {
			return
		}
	}
}

// sourceRollBack starts the core here again: on the target's export of
// the move when imported, else on this data dir.
func (a *Agent) sourceRollBack(ctx context.Context, r CoreRunner, m *moveRun, imported bool, format string, args ...any) {
	why := fmt.Sprintf(format, args...)
	if imported {
		a.step(m, "", "import", "%s: importing the target's export", why)
		if err := r.Import(ctx); err != nil {
			a.step(m, nodeproto.MoveFailed, "import", "%s; importing its export failed: %v. No core runs: see fridolin core status", why, err)
			return
		}
	}
	if err := r.Promote(ctx); err != nil {
		a.step(m, nodeproto.MoveFailed, "promote", "%s; taking the lease failed: %v. No core runs: see fridolin core status", why, err)
		return
	}
	if err := r.Start(ctx); err != nil {
		a.step(m, nodeproto.MoveFailed, "start", "%s; starting the core failed: %v", why, err)
		return
	}
	a.setRunning(m, true)
	a.step(m, nodeproto.MoveRolledBack, "started", "%s; the core runs here again", why)
}

// runTarget is the target's half: wait until the source released the
// lease and exported for this move (until the deadline, else give up and
// touch nothing), import, promote, start, and wait for the core to be
// ready; when it is not in time, stop, export and release, so that the
// source takes it back.
func (a *Agent) runTarget(ctx context.Context, r CoreRunner, m *moveRun) {
	p := m.plan
	switch m.State.Phase {
	case nodeproto.MoveAccepted, nodeproto.MoveWaiting, nodeproto.MoveImporting:
		if m.State.Phase != nodeproto.MoveWaiting {
			a.step(m, nodeproto.MoveWaiting, "wait", "waiting for %s to release the lease and export", p.Source)
		}
		for {
			st, err := r.Status(ctx)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				a.logf("node: core move %s: reading the lease: %v", p.Move, err)
			} else if st.Lease != nil && st.Lease.Device == p.SourceDevice && st.Lease.State == nodeproto.LeaseReleased &&
				st.Latest != nil && st.Latest.Move == p.Move && st.Latest.Device == p.SourceDevice {
				break
			}
			if !time.Now().Before(p.HandoffDeadline) {
				a.step(m, nodeproto.MoveGaveUp, "deadline", "%s did not release the core by the deadline; nothing changed here", p.Source)
				return
			}
			if !wait(ctx, min(a.movePoll(), time.Until(p.HandoffDeadline)+time.Millisecond)) {
				return
			}
		}
		a.step(m, nodeproto.MoveImporting, "import", "%s released the lease and exported; importing", p.Source)
		if err := r.Import(ctx); err != nil {
			a.step(m, nodeproto.MoveFailed, "import", "the import failed: %v; no lease taken, %s starts again after the deadline", err, p.Source)
			return
		}
		if !time.Now().Before(p.HandoffDeadline) {
			a.step(m, nodeproto.MoveGaveUp, "deadline", "the deadline passed during the import: not promoting; %s starts again", p.Source)
			return
		}
		if err := r.Promote(ctx); err != nil {
			a.step(m, nodeproto.MoveFailed, "promote", "taking the lease failed: %v; %s starts again after the deadline", err, p.Source)
			return
		}
		a.step(m, nodeproto.MoveStarting, "start", "holding the lease; starting the core")
		if err := r.Start(ctx); err != nil {
			a.targetGiveUp(ctx, r, m, "the core did not start: %v", err)
			return
		}
		a.moves.mu.Lock()
		m.Started, m.State.Running = time.Now().UTC(), true
		a.saveMove(m)
		a.moves.mu.Unlock()
	case nodeproto.MoveStarting:
		a.moves.mu.Lock()
		if m.Started.IsZero() {
			m.Started = time.Now().UTC()
			a.saveMove(m)
		}
		a.moves.mu.Unlock()
	}
	deadline := m.Started.Add(p.Ready())
	last := ""
	for {
		ok, detail := r.Ready(ctx)
		if ctx.Err() != nil {
			return
		}
		if ok {
			a.step(m, nodeproto.MoveDone, "ready", "%s", detail)
			return
		}
		last = detail
		if time.Now().After(deadline) {
			a.targetGiveUp(ctx, r, m, "the core was not ready within %s (%s)", p.Ready(), last)
			return
		}
		if !wait(ctx, min(a.movePoll(), time.Until(deadline)+time.Millisecond)) {
			return
		}
	}
}

// targetGiveUp stops the core here, exports its state for the source
// (it may have answered messages) and frees the lease.
func (a *Agent) targetGiveUp(ctx context.Context, r CoreRunner, m *moveRun, format string, args ...any) {
	why := fmt.Sprintf(format, args...)
	a.step(m, "", "give up", "%s: stopping, exporting, releasing the lease", why)
	var errs []string
	if err := r.Stop(ctx); err != nil {
		errs = append(errs, "stopping: "+err.Error())
	}
	a.setRunning(m, false)
	if _, err := r.Export(ctx, m.plan.Move); err != nil {
		errs = append(errs, "exporting: "+err.Error())
	}
	if err := r.Release(ctx); err != nil {
		errs = append(errs, "releasing the lease: "+err.Error())
	}
	if len(errs) > 0 {
		a.step(m, nodeproto.MoveRolledBack, "gave up", "%s; %s. %s takes the core back", why, strings.Join(errs, "; "), m.plan.Source)
		return
	}
	a.step(m, nodeproto.MoveRolledBack, "gave up", "%s; %s takes the core back", why, m.plan.Source)
}

// coreImage is core.image.pull.
func (a *Agent) coreImage(ctx context.Context, p nodeproto.CoreImagePullParams) (any, *nodeproto.Error) {
	if p.Version == "" || len(p.Version) > 128 {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "name the core's version")
	}
	if p.Image != "" {
		if err := nodeproto.CheckImageDigest(p.Image); err != nil {
			return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
		}
	}
	if a.Config.Box != nil {
		// A box runs its image pinned by digest; the core's version is
		// the box's when the box was updated to it.
		if p.Version == a.Version {
			return nodeproto.CoreImageResult{Image: p.Image, Present: true, Note: "the box runs " + a.Version}, nil
		}
		return nodeproto.CoreImageResult{Image: p.Image, Note: "the box runs " + a.Version + ", not " + p.Version + ": update it first (fridolin nodes update " + a.ID() + ")"}, nil
	}
	r, perr := a.coreRunner()
	if perr != nil {
		return nil, perr
	}
	if p.Image == "" && !p.CheckOnly {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "the core's image is not published (a local build): nothing to pull by digest")
	}
	res, err := r.Image(ctx, p.Version, p.Image, p.CheckOnly)
	if err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	return res, nil
}

// coreRehearse is core.rehearse.
func (a *Agent) coreRehearse(ctx context.Context, p nodeproto.CoreRehearseParams) (any, *nodeproto.Error) {
	if err := nodeproto.CheckMoveID(p.Move); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	r, perr := a.coreRunner()
	if perr != nil {
		return nil, perr
	}
	a.moves.mu.Lock()
	busy := slices.ContainsFunc(mapValues(a.moves.runs), func(o *moveRun) bool { return !nodeproto.MoveFinal(o.State.Phase) })
	a.moves.mu.Unlock()
	if busy {
		return nil, nodeproto.Errorf(nodeproto.CodeDenied, "a core move runs on this machine")
	}
	res, err := r.Rehearse(ctx, p.Move)
	if err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "the rehearsal: %v", err)
	}
	res.Move = p.Move
	a.logf("node: core move %s: rehearsal of %s: ok=%v in %ds", p.Move, res.Export, res.OK, res.Seconds)
	return res, nil
}

// coreRuntime is the core here for core.check, or why it cannot be read.
func (a *Agent) coreRuntime(ctx context.Context) (*nodeproto.CoreRuntime, string) {
	r, perr := a.coreRunner()
	if perr != nil {
		return nil, perr.Message
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	st, err := r.Status(ctx)
	if err != nil {
		return nil, err.Error()
	}
	return &st, ""
}
