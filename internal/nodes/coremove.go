package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// The core moves itself (docs/CORE.md, "The core moves itself"; #187 §3,
// §4). MoveCheck is the pre-flight of a move from one core host to
// another, read only. StartMove prepares the move on the target while
// the core keeps running (the image, the host secrets, a rehearsal),
// then signs a move plan with the update key and hands it to both
// agents, which do the rest through the lease and the exports. The
// record of the move is written into the nodes folder, which the final
// export carries, so the core that runs afterwards, on either machine,
// follows it to its end and tells the source when the target runs.
const movesDir = "moves"

// MoveEnd is one side of a move as the report shows it.
type MoveEnd struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	OS          string `json:"os,omitempty"`
	Arch        string `json:"arch,omitempty"`
	Version     string `json:"version,omitempty"`
	Box         bool   `json:"box,omitempty"`
	Online      bool   `json:"online"`
	Fingerprint string `json:"fingerprint"`
	DiskFree    uint64 `json:"disk_free,omitempty"`
	// Core is the core there as the lease and the bucket see it;
	// CoreError why it could not be read.
	Core      *nodeproto.CoreRuntime `json:"core,omitempty"`
	CoreError string                 `json:"core_error,omitempty"`
}

// MoveReport is a move's pre-flight (#187 §4). Blocking keeps the move
// from running; Warnings and MachineBound are for the owner to know.
type MoveReport struct {
	Source  MoveEnd `json:"source"`
	Target  MoveEnd `json:"target"`
	Version string  `json:"version"`
	// SecretFiles are the secret files of the source's credentials folder
	// that go with the host secrets (#187), SecretFilesDir that folder.
	SecretFiles    []string `json:"secret_files,omitempty"`
	SecretFilesDir string   `json:"secret_files_dir,omitempty"`
	// Image is the core's image by digest, "" for a local build;
	// ImagePresent whether the target has it, ImageNote what it said.
	Image        string `json:"image,omitempty"`
	ImagePresent bool   `json:"image_present"`
	ImageNote    string `json:"image_note,omitempty"`
	// Secrets is the host secrets' dry run.
	Secrets CoreTransferReport `json:"secrets"`
	// ExportBytes is the source's latest export, Outage a rough guess of
	// the time the core is offline.
	ExportBytes  int64    `json:"export_bytes,omitempty"`
	Outage       string   `json:"outage,omitempty"`
	MachineBound []string `json:"machine_bound"`
	// Site is the site and what becomes of it, nil when it is off.
	Site     *MoveSite `json:"site,omitempty"`
	Warnings []string  `json:"warnings"`
	Blocking []string  `json:"blocking"`
}

// ErrMoveBlocked is a move whose pre-flight found something against it.
var ErrMoveBlocked = errors.New("the core cannot move")

// ErrMoveRunning is a second move while one runs.
var ErrMoveRunning = errors.New("a core move runs already")

// MoveCheck is the pre-flight of moving the core from node src to dst:
// both core hosts approved and online, the same version as the core, the
// image on the target, the disk, the host secrets and the pin, the core
// on both (the source holds the lease), and what of the source does not
// move.
func (s *Service) MoveCheck(ctx context.Context, src, dst string) (MoveReport, error) {
	rep := MoveReport{Version: s.version, MachineBound: []string{}, Warnings: []string{}, Blocking: []string{}}
	tr, checks, err := s.coreTransferCheck(ctx, src, dst)
	if err != nil {
		return rep, err
	}
	rep.Secrets = tr
	rep.Blocking = append(rep.Blocking, tr.Problems...)
	ends := [2]*MoveEnd{&rep.Source, &rep.Target}
	dstUnread, dstNoSecrets := "", false
	for i, id := range []string{src, dst} {
		n, err := s.reg.Get(id)
		if err != nil {
			return rep, err
		}
		c := checks[i]
		*ends[i] = MoveEnd{ID: n.ID, Name: n.Name, OS: n.OS, Arch: c.Arch, Version: n.Version, Box: c.Box, Online: s.Online(id),
			Fingerprint: nodeproto.Fingerprint(n.PublicKey), DiskFree: c.DiskFree, Core: c.Core, CoreError: c.CoreError}
		if c.ID == "" {
			continue // the transfer's check said why
		}
		if c.Version != s.version {
			rep.Blocking = append(rep.Blocking, fmt.Sprintf("%s runs version %s and the core %s: update it first (fridolin nodes update %s)", n.Name, c.Version, s.version, n.ID))
		}
		if c.Core == nil {
			if i == 1 {
				// Decided once moveImage knows whether the target has the
				// image: without it, nothing can read the core there yet.
				dstUnread = fmt.Sprintf("%s: the core there cannot be read: %s", n.Name, c.CoreError)
				dstNoSecrets = !holdsSecrets(c)
				continue
			}
			rep.Blocking = append(rep.Blocking, fmt.Sprintf("%s: the core there cannot be read: %s", n.Name, c.CoreError))
		}
	}
	srcName, dstName := rep.Source.Name, rep.Target.Name
	if sc := rep.Source.Core; sc != nil {
		if !sc.HeldBy() {
			rep.Blocking = append(rep.Blocking, fmt.Sprintf("the core on %s does not hold the lease (%s): is it the core?", srcName, leaseText(sc.Lease)))
		}
		if sc.Latest != nil {
			rep.ExportBytes = sc.Latest.Bytes
		}
	}
	if tc := rep.Target.Core; tc != nil && tc.HeldBy() {
		rep.Blocking = append(rep.Blocking, fmt.Sprintf("the core on %s holds the lease: two cores?", dstName))
	}
	if checks[0].ID != "" {
		rep.MachineBound = append(rep.MachineBound, checks[0].MachineBound...)
		rep.SecretFiles, rep.SecretFilesDir = checks[0].SecretFiles, checks[0].SecretFilesDir
		if e := checks[0].SecretFilesError; e != "" {
			// The core there may need them (calendars, the dashboard's
			// users): never move without them.
			rep.Blocking = append(rep.Blocking, fmt.Sprintf("%s: the secret files of the core's credentials folder cannot be read: %s", srcName, e))
		}
	}
	if checks[1].ID != "" {
		need := uint64(2*rep.ExportBytes) + 4<<30
		switch free := checks[1].DiskFree; {
		case free == 0:
		case free < uint64(2*rep.ExportBytes):
			rep.Blocking = append(rep.Blocking, fmt.Sprintf("%s has %s free, less than twice the export (%s)", dstName, bytesText(int64(free)), bytesText(rep.ExportBytes)))
		case free < need:
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s has %s free: the image and two exports want about %s", dstName, bytesText(int64(free)), bytesText(int64(need))))
		}
		if n, err := s.reg.Get(dst); err == nil && n.Effective().Fride != nil {
			rep.Warnings = append(rep.Warnings, dstName+" runs FrIDE: once the secrets are there, any FrIDE task can read them")
		}
		s.moveImage(ctx, &rep, dst, true)
		if rep.Target.Box {
			s.moveToBox(&rep, checks[1])
		}
	}
	if dstUnread != "" {
		switch {
		case !rep.ImagePresent:
			// A host that never ran this version reads its core with the
			// image the move pulls first; the lease is the source's to hold.
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s has not the core's image yet, so its core cannot be read before the move pulls it", rep.Target.Name))
		case dstNoSecrets:
			// Nor can a host that never held the bucket's keys (a new box)
			// before the move sends them; the rehearsal reads the bucket
			// there before anything stops.
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s holds no host secrets yet, so its core cannot be read before the move sends them", rep.Target.Name))
		default:
			rep.Blocking = append(rep.Blocking, dstUnread)
		}
	}
	s.moveSite(&rep)
	// About 10 MB/s each way, plus stopping and starting.
	secs := 2*rep.ExportBytes/(10<<20) + 120
	rep.Outage = fmt.Sprintf("about %d to %d minutes", max(1, secs/60), max(2, 2*secs/60))
	return rep, nil
}

// moveToBox is what the pre-flight adds for a box target (docs/NODES.md,
// "The core in a box"): its own checks (the core's settings, the brains'
// sandbox) block when they fail, and the owner learns what changes when
// the core runs in a box.
func (s *Service) moveToBox(rep *MoveReport, c nodeproto.CoreCheckResult) {
	name := rep.Target.Name
	for _, it := range c.Setup {
		if it.Required && it.Status == "fail" {
			rep.Blocking = append(rep.Blocking, fmt.Sprintf("%s: %s: %s", name, it.Title, it.Detail))
		}
	}
	rep.Warnings = append(rep.Warnings,
		fmt.Sprintf("%s is a box: the core runs in its node container, the brain folders in its volume fridolin-box-data. They go with every export and come back on a move back, but no folder of that machine shows them, so Finder or Obsidian there cannot edit them", name),
		fmt.Sprintf("the dashboard on %s is at http://127.0.0.1:8765 (FRIDOLIN_BOX_DASHBOARD_PORT in the box's .env), and on the tailnet as before", name))
	if len(rep.MachineBound) > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("what is bound to %s stays there and runs on its node, called over the node channel while it is online: a box runs no host scripts", rep.Source.Name))
	}
}

// moveImage finds the core's image by digest and asks the target whether
// it has it (checkOnly), or to pull it.
func (s *Service) moveImage(ctx context.Context, rep *MoveReport, dst string, checkOnly bool) {
	if s.boxImages != nil {
		ictx, cancel := context.WithTimeout(ctx, time.Minute)
		img, _, err := s.boxImages(ictx, s.version)
		cancel()
		if err != nil {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("the image of %s: %v", s.version, err))
		} else {
			rep.Image = img
		}
	}
	var res nodeproto.CoreImageResult
	if err := s.Call(ctx, dst, nodeproto.MethodCoreImagePull, nodeproto.CoreImagePullParams{Version: s.version, Image: rep.Image, CheckOnly: checkOnly}, &res); err != nil {
		rep.Blocking = append(rep.Blocking, fmt.Sprintf("%s: the image: %v", rep.Target.Name, err))
		return
	}
	rep.ImagePresent, rep.ImageNote = res.Present, res.Note
	if !res.Present && rep.Image == "" {
		rep.Blocking = append(rep.Blocking, fmt.Sprintf("the core runs a local build (%s), which is not published: build it on %s first (fridolin core build at that commit)", s.version, rep.Target.Name))
	}
	if !res.Present && rep.Target.Box {
		rep.Blocking = append(rep.Blocking, fmt.Sprintf("%s: %s", rep.Target.Name, res.Note))
	}
}

// holdsSecrets reports whether the core host of c holds any host secret.
func holdsSecrets(c nodeproto.CoreCheckResult) bool {
	return slices.ContainsFunc(c.Secrets, func(p nodeproto.CoreSecretPresent) bool { return p.Stored })
}

func leaseText(l *nodeproto.CoreLease) string {
	if l == nil {
		return "no lease"
	}
	return fmt.Sprintf("lease %s, device %s on %s", l.State, l.Device, l.Hostname)
}

func bytesText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%d MB", n>>20)
	}
	return fmt.Sprintf("%d KB", n>>10)
}

// The states of a move on the core (MoveRecord.State).
const (
	MovePreparing  = "preparing"
	MoveHandingOff = "handing_off"
	MoveHandedOff  = "handed_off"
	MoveDone       = "done"
	MoveRolledBack = "rolled_back"
	MoveFailed     = "failed"
	MoveUnknown    = "unknown"
)

// MoveFinal reports whether a record's state is an end.
func MoveFinal(state string) bool {
	return state == MoveDone || state == MoveRolledBack || state == MoveFailed || state == MoveUnknown
}

// MoveRecord is a move as the core follows it.
type MoveRecord struct {
	ID         string    `json:"id"`
	Source     string    `json:"source"`
	Target     string    `json:"target"`
	SourceName string    `json:"source_name"`
	TargetName string    `json:"target_name"`
	State      string    `json:"state"`
	Detail     string    `json:"detail,omitempty"`
	Started    time.Time `json:"started"`
	Updated    time.Time `json:"updated"`
	// Report is the pre-flight it ran on; Plan the plan it handed off.
	Report MoveReport               `json:"report"`
	Plan   *nodeproto.MovePlan      `json:"plan,omitempty"`
	Steps  []nodeproto.CoreMoveStep `json:"steps"`
	// SourceState and TargetState are what each host last said.
	SourceState *nodeproto.CoreMoveState `json:"source_state,omitempty"`
	TargetState *nodeproto.CoreMoveState `json:"target_state,omitempty"`
	// Told is set once the source heard that the target runs the core.
	Told bool `json:"told,omitempty"`
}

// MoveOptions are a move's timings: HandoffDeadline after the plan is
// issued, and ReadyWithin for the target's core (the defaults of
// nodeproto when zero).
type MoveOptions struct {
	HandoffDeadline, ReadyWithin time.Duration
	// Approval names the approval that let the move run.
	Approval string
}

// movePath is the record of move id: one name in moves, never a path
// (callers check the id with nodeproto.CheckMoveID first).
func (s *Service) movePath(id string) string {
	return filepath.Join(s.dir, movesDir, filepath.Base(id+".json"))
}

func (s *Service) saveMoveLocked(r *MoveRecord) {
	r.Updated = time.Now().UTC()
	data, err := json.MarshalIndent(r, "", "  ")
	if err == nil {
		if err = os.MkdirAll(filepath.Join(s.dir, movesDir), 0o700); err == nil {
			err = fsx.WriteFileAtomic(s.movePath(r.ID), append(data, '\n'), 0o600, false)
		}
	}
	if err != nil {
		s.logf("nodes: core move %s: writing its record: %v", r.ID, err)
	}
}

// moveStep records a step of r, and its state when state is not "".
func (s *Service) moveStep(r *MoveRecord, state, step, format string, args ...any) {
	detail := fmt.Sprintf(format, args...)
	s.moveMu.Lock()
	r.Steps = append(r.Steps, nodeproto.CoreMoveStep{At: time.Now().UTC(), Step: step, Detail: detail})
	if state != "" {
		r.State = state
		r.Detail = detail
	}
	s.saveMoveLocked(r)
	final := MoveFinal(r.State) && state != ""
	cp := cloneMove(r)
	s.moveMu.Unlock()
	s.logf("nodes: core move %s: %s: %s", r.ID, step, detail)
	if final && s.onMove != nil {
		s.onMove(cp)
	}
}

func cloneMove(r *MoveRecord) MoveRecord {
	cp := *r
	cp.Steps = slices.Clone(r.Steps)
	return cp
}

// MoveActive is the id of the move that runs, "" for none ("checking"
// while one is checked before it starts).
func (s *Service) MoveActive() string {
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	return s.moveActive
}

// Move is the record of move id.
func (s *Service) Move(id string) (MoveRecord, error) {
	if err := nodeproto.CheckMoveID(id); err != nil {
		return MoveRecord{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	if r, ok := s.moves[id]; ok {
		return cloneMove(r), nil
	}
	r, err := s.loadMove(id)
	if err != nil {
		return MoveRecord{}, err
	}
	return *r, nil
}

func (s *Service) loadMove(id string) (*MoveRecord, error) {
	if err := nodeproto.CheckMoveID(id); err != nil || strings.Contains(id, "..") {
		return nil, fmt.Errorf("%w: %q is not a move id", ErrInvalidInput, id)
	}
	data, err := os.ReadFile(s.movePath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: no move %s", ErrUnknownNode, id)
	}
	if err != nil {
		return nil, err
	}
	var r MoveRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Moves lists the moves the core has a record of, newest first.
func (s *Service) Moves() []MoveRecord {
	entries, _ := os.ReadDir(filepath.Join(s.dir, movesDir))
	var out []MoveRecord
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		if r, err := s.Move(id); err == nil {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out
}

// StartMove checks a move from src to dst and, when nothing blocks it,
// runs it in the background: the record it returns is followed with
// Move. It never runs two at once.
func (s *Service) StartMove(ctx context.Context, src, dst string, o MoveOptions) (MoveRecord, error) {
	if o.HandoffDeadline <= 0 {
		o.HandoffDeadline = nodeproto.DefaultHandoffDeadline
	}
	if o.ReadyWithin <= 0 {
		o.ReadyWithin = nodeproto.DefaultReadyWithin
	}
	s.moveMu.Lock()
	running := s.moveActive != ""
	if !running {
		s.moveActive = "checking"
	}
	s.moveMu.Unlock()
	if running {
		return MoveRecord{}, ErrMoveRunning
	}
	release := func() {
		s.moveMu.Lock()
		s.moveActive = ""
		s.moveMu.Unlock()
	}
	rep, err := s.MoveCheck(ctx, src, dst)
	if err != nil {
		release()
		return MoveRecord{Report: rep}, err
	}
	if len(rep.Blocking) > 0 {
		release()
		return MoveRecord{Report: rep}, fmt.Errorf("%w: %s", ErrMoveBlocked, rep.Blocking[0])
	}
	now := time.Now().UTC()
	r := &MoveRecord{ID: newMoveID(), Source: src, Target: dst, SourceName: rep.Source.Name, TargetName: rep.Target.Name,
		State: MovePreparing, Started: now, Report: rep, Steps: []nodeproto.CoreMoveStep{}}
	s.moveMu.Lock()
	s.moveActive = r.ID
	s.moves[r.ID] = r
	s.saveMoveLocked(r)
	cp := cloneMove(r)
	s.moveMu.Unlock()
	s.wg.Go(func() {
		defer release()
		s.runMove(s.ctx, r, o)
	})
	return cp, nil
}

// runMove prepares the move on the target, hands it off and follows it.
func (s *Service) runMove(ctx context.Context, r *MoveRecord, o MoveOptions) {
	src, dst := r.Source, r.Target
	fail := func(format string, args ...any) {
		s.moveStep(r, MoveFailed, "cancelled", format+"; nothing was stopped", args...)
	}
	s.moveStep(r, "", "start", "moving the core from %s to %s (version %s)", r.SourceName, r.TargetName, s.version)
	// The slow steps first, while the core runs.
	if !r.Report.ImagePresent {
		rep := r.Report
		rep.Blocking = nil
		pctx, cancel := context.WithTimeout(ctx, time.Hour)
		s.moveImage(pctx, &rep, dst, false)
		cancel()
		if len(rep.Blocking) > 0 {
			fail("%s", rep.Blocking[0])
			return
		}
		s.moveStep(r, "", "image", "%s: %s", r.TargetName, rep.ImageNote)
	}
	tr, err := s.coreTransferRun(ctx, src, dst, r.Report.Secrets, CoreTransferOptions{Run: true, Move: r.ID})
	if err != nil {
		fail("the host secrets: %v", err)
		return
	}
	s.moveStep(r, "", "secrets", "%s: %d stored, %d unchanged; secret files: %d stored, %d kept (names only through the core)", r.TargetName, len(tr.Stored), len(tr.Unchanged), len(tr.FilesStored), len(tr.FilesKept))
	if tr.FilesError != "" {
		s.moveStep(r, "", "secrets", "the secret files of %s could not be read, so none went: %s", r.SourceName, tr.FilesError)
	}
	var reh nodeproto.CoreRehearsal
	rctx, cancel := context.WithTimeout(ctx, time.Hour)
	err = s.Call(rctx, dst, nodeproto.MethodCoreRehearse, nodeproto.CoreRehearseParams{Move: r.ID}, &reh)
	cancel()
	switch {
	case err != nil:
		fail("the rehearsal on %s: %v", r.TargetName, err)
		return
	case !reh.OK:
		fail("the rehearsal on %s: %s", r.TargetName, reh.Note)
		return
	}
	s.moveStep(r, "", "rehearsal", "%s imported and checked the latest export in %ds", r.TargetName, reh.Seconds)

	// The plan. The source's data dir is as the pre-flight read it.
	key, err := s.updateKey()
	if err != nil {
		fail("the update key: %v", err)
		return
	}
	now := time.Now().UTC().Truncate(time.Second)
	plan := nodeproto.MovePlan{Move: r.ID, Source: src, Target: dst, SourceDevice: r.Report.Source.Core.Device, Version: s.version, Image: r.Report.Image,
		IssuedAt: now, Approval: o.Approval, HandoffDeadline: now.Add(o.HandoffDeadline), ReadyWithin: int(o.ReadyWithin / time.Second)}
	signed, err := nodeproto.SignMovePlan(key, plan)
	if err != nil {
		fail("the plan: %v", err)
		return
	}
	s.moveMu.Lock()
	r.Plan = &plan
	s.moveMu.Unlock()
	s.moveStep(r, MoveHandingOff, "plan", "deadline %s, ready within %s", plan.HandoffDeadline.Format(time.RFC3339), o.ReadyWithin)
	var accepted nodeproto.CoreMoveState
	if err := s.Call(ctx, dst, nodeproto.MethodCoreHandoff, signed, &accepted); err != nil {
		fail("%s refused the handoff: %v", r.TargetName, err)
		return
	}
	s.moveStep(r, "", "handoff", "%s waits for the core", r.TargetName)
	if s.onStopping != nil {
		s.moveMu.Lock()
		cp := cloneMove(r)
		s.moveMu.Unlock()
		s.onStopping(cp)
	}
	// From here the source stops this core (when it runs there): this
	// record goes into the final export as it is now.
	if err := s.Call(ctx, src, nodeproto.MethodCoreRelease, signed, &accepted); err != nil {
		s.moveStep(r, MoveFailed, "release", "%s refused to release the core: %v; %s gives up at the deadline, nothing changed", r.SourceName, err, r.TargetName)
		return
	}
	s.moveStep(r, MoveHandedOff, "release", "%s stops the core and exports; %s takes over", r.SourceName, r.TargetName)
	s.watchMove(ctx, r)
}

// resumeMoves follows the moves that were handed off before this core
// started: on the target after a move, or on the source after a rollback.
func (s *Service) resumeMoves() {
	entries, _ := os.ReadDir(filepath.Join(s.dir, movesDir))
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || nodeproto.CheckMoveID(id) != nil {
			continue
		}
		r, err := s.loadMove(id)
		if err != nil {
			continue
		}
		switch r.State {
		case MovePreparing, MoveHandingOff:
			// The core stopped before it handed off: nothing moved.
			s.moves[id] = r
			s.saveMoveLocked(r)
			go s.moveStep(r, MoveFailed, "restart", "the core stopped while the move was %s; nothing was handed off", r.State)
		case MoveHandedOff:
			s.moves[id] = r
			s.moveActive = id
			s.wg.Go(func() {
				defer func() {
					s.moveMu.Lock()
					s.moveActive = ""
					s.moveMu.Unlock()
				}()
				s.watchMove(s.ctx, r)
			})
		}
	}
}

// SetMovePoll sets how often a move's hosts are asked how it goes, for
// tests.
func (s *Service) SetMovePoll(d time.Duration) {
	s.moveMu.Lock()
	s.movePollEvery = d
	s.moveMu.Unlock()
}

func (s *Service) movePoll() time.Duration {
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	if s.movePollEvery > 0 {
		return s.movePollEvery
	}
	return 5 * time.Second
}

// watchMove asks both hosts how the move goes until it ends: the target
// runs the core (then the source hears it, which ends its watch), or the
// source started again.
func (s *Service) watchMove(ctx context.Context, r *MoveRecord) {
	var end time.Time
	if r.Plan != nil {
		end = r.Plan.HandoffDeadline.Add(nodeproto.MoveRollbackGrace).Add(r.Plan.Ready()).Add(2 * time.Hour)
	} else {
		end = time.Now().Add(3 * time.Hour)
	}
	for {
		var srcState, dstState *nodeproto.CoreMoveState
		ask := func(id string, complete bool) *nodeproto.CoreMoveState {
			if !s.Online(id) {
				return nil
			}
			var st nodeproto.CoreMoveState
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if err := s.Call(cctx, id, nodeproto.MethodCoreMoveStatus, nodeproto.CoreMoveStatusParams{Move: r.ID, Complete: complete}, &st); err != nil {
				return nil
			}
			return &st
		}
		dstState = ask(r.Target, false)
		srcState = ask(r.Source, dstState != nil && dstState.Phase == nodeproto.MoveDone)
		s.moveMu.Lock()
		if srcState != nil {
			r.SourceState = srcState
		}
		if dstState != nil {
			r.TargetState = dstState
		}
		if srcState != nil && dstState != nil && dstState.Phase == nodeproto.MoveDone {
			r.Told = true
		}
		told := r.Told
		s.saveMoveLocked(r)
		s.moveMu.Unlock()
		switch {
		case told:
			detail := ""
			if r.TargetState != nil {
				detail = r.TargetState.Detail
			}
			s.moveStep(r, MoveDone, "done", "the core runs on %s (%s)", r.TargetName, detail)
			return
		case srcState != nil && srcState.Phase == nodeproto.MoveRolledBack:
			why := srcState.Detail
			if dstState != nil && dstState.Detail != "" {
				why += "; " + r.TargetName + ": " + dstState.Detail
			}
			s.moveStep(r, MoveRolledBack, "rolled back", "%s: %s", r.SourceName, why)
			return
		case srcState != nil && srcState.Phase == nodeproto.MoveFailed:
			s.moveStep(r, MoveFailed, "failed", "%s: %s", r.SourceName, srcState.Detail)
			return
		case srcState != nil && srcState.Phase == nodeproto.MoveDone:
			s.moveStep(r, MoveDone, "done", "%s: %s", r.SourceName, srcState.Detail)
			return
		case time.Now().After(end):
			s.moveStep(r, MoveUnknown, "timeout", "neither host said how the move ended; see fridolin core status on both")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.movePoll()):
		}
	}
}
