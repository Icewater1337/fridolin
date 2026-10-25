package nodeproto

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Moving the core between two core hosts (#187 §2, §3; docs/CORE.md,
// "The core moves itself"). The core prepares the move with
// core.image.pull and core.rehearse on the target, then hands it to the
// two host agents as a move plan signed with its update key: core.release
// on the source (stop, final export, the lease released) and core.handoff
// on the target (wait for that, import, promote, start, ready). From then
// on they coordinate only through the lease and the exports in the
// bucket, since the core that signed the plan stops. core.move.status
// reads a move's state on a host, and ends the source's watch once the
// core runs on the target.
const (
	// MethodCoreImagePull makes sure the target can start the core's
	// image: a box runs it when it runs the core's version; a native host
	// pulls it by digest after checking its build provenance
	// (CoreImagePullParams → CoreImageResult).
	MethodCoreImagePull = "core.image.pull"
	// MethodCoreRehearse imports the latest export into a scratch place,
	// checks it and removes it again; never the core's own volume, never
	// the lease (CoreRehearseParams → CoreRehearsal).
	MethodCoreRehearse = "core.rehearse"
	// MethodCoreRelease is the source's half of a plan (SignedMovePlan →
	// CoreMoveState, as accepted).
	MethodCoreRelease = "core.release"
	// MethodCoreHandoff is the target's half (SignedMovePlan →
	// CoreMoveState, as accepted).
	MethodCoreHandoff = "core.handoff"
	// MethodCoreMoveStatus is a move's state on this host
	// (CoreMoveStatusParams → CoreMoveState).
	MethodCoreMoveStatus = "core.move.status"
)

// CoreMoveMethods are the core-host methods of a move. A lockdown refuses
// all but core.move.status, which only reads (and ends a watch).
var CoreMoveMethods = []string{MethodCoreImagePull, MethodCoreRehearse, MethodCoreRelease, MethodCoreHandoff, MethodCoreMoveStatus}

// CoreMoveWrites are the move's methods that change a host.
var CoreMoveWrites = []string{MethodCoreImagePull, MethodCoreRehearse, MethodCoreRelease, MethodCoreHandoff}

// A move plan's limits.
const (
	// MovePlanMaxAge is how old a plan may be when it reaches an agent.
	MovePlanMaxAge = 15 * time.Minute
	// MovePlanMaxDeadline bounds handoff_deadline after issued_at.
	MovePlanMaxDeadline = time.Hour
	// MoveReadyMax bounds ready_within.
	MoveReadyMax = time.Hour
	// MoveRollbackGrace is how long after the deadline the source waits
	// before it starts again on its own volume: the target never promotes
	// after the deadline, so by then nothing newer can exist.
	MoveRollbackGrace = 2 * time.Minute
	// Defaults the core puts into a plan.
	DefaultHandoffDeadline = 15 * time.Minute
	DefaultReadyWithin     = 10 * time.Minute
)

// coreDeviceRE is a data dir's device id, which the lease and the exports
// name (32 hex digits; internal/lease).
var coreDeviceRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// MovePlan is what the core signs for a move: who gives the core up
// (Source) and who takes it (Target), both node device ids; the data dir
// of the source by its lease device id (SourceDevice), which the target
// waits for in the lease and the latest export; the version both run and
// the image the target starts.
type MovePlan struct {
	Move         string    `json:"move"`
	Source       string    `json:"source"`
	Target       string    `json:"target"`
	SourceDevice string    `json:"source_device"`
	Version      string    `json:"version"`
	Image        string    `json:"image,omitempty"`
	IssuedAt     time.Time `json:"issued_at"`
	// Approval is the approval that let the move run, "" when the owner
	// started it by hand (fridolin nodes move --yes).
	Approval string `json:"approval,omitempty"`
	// HandoffDeadline is when the target gives up waiting for the source:
	// it never promotes after it.
	HandoffDeadline time.Time `json:"handoff_deadline"`
	// ReadyWithin is how long the target's new core has to be ready
	// (WhatsApp connected, the lease held) once started, in seconds.
	ReadyWithin int `json:"ready_within"`
}

// Ready is ReadyWithin as a duration.
func (p MovePlan) Ready() time.Duration { return time.Duration(p.ReadyWithin) * time.Second }

// Check reports what is wrong with the plan's form.
func (p MovePlan) Check() error {
	if err := CheckMoveID(p.Move); err != nil {
		return err
	}
	if err := CheckDeviceID(p.Source); err != nil {
		return fmt.Errorf("source: %w", err)
	}
	if err := CheckDeviceID(p.Target); err != nil {
		return fmt.Errorf("target: %w", err)
	}
	if p.Source == p.Target {
		return errors.New("the source and the target are the same node")
	}
	if !coreDeviceRE.MatchString(p.SourceDevice) {
		return errors.New("source_device is not a data dir's device id")
	}
	if p.Version == "" || len(p.Version) > 128 {
		return errors.New("the plan names no version")
	}
	if p.Image != "" {
		if err := CheckImageDigest(p.Image); err != nil {
			return err
		}
	}
	if len(p.Approval) > 64 {
		return errors.New("the approval id is too long")
	}
	if p.IssuedAt.IsZero() || !p.HandoffDeadline.After(p.IssuedAt) || p.HandoffDeadline.Sub(p.IssuedAt) > MovePlanMaxDeadline {
		return fmt.Errorf("the handoff deadline must come after issued_at, within %s", MovePlanMaxDeadline)
	}
	if p.ReadyWithin <= 0 || p.Ready() > MoveReadyMax {
		return fmt.Errorf("ready_within must be between 1 second and %s", MoveReadyMax)
	}
	return nil
}

// SignedMovePlan is a plan as JSON with the core's signature over it.
type SignedMovePlan struct {
	Plan      json.RawMessage `json:"plan"`
	Signature []byte          `json:"signature"`
}

// movePlanDomain separates a plan's signature from every other one the
// update key makes.
const movePlanDomain = "fridolin-core-move-plan/v1\x00"

// SignMovePlan signs p with the core's update key.
func SignMovePlan(key ed25519.PrivateKey, p MovePlan) (SignedMovePlan, error) {
	if err := p.Check(); err != nil {
		return SignedMovePlan{}, err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return SignedMovePlan{}, err
	}
	return SignedMovePlan{Plan: raw, Signature: ed25519.Sign(key, append([]byte(movePlanDomain), raw...))}, nil
}

// OpenMovePlan checks the signature with pub, the core key the agent
// pinned, and returns the plan, its form checked.
func OpenMovePlan(pub []byte, s SignedMovePlan) (MovePlan, error) {
	var p MovePlan
	if len(pub) != ed25519.PublicKeySize || len(s.Signature) != ed25519.SignatureSize || len(s.Plan) == 0 || len(s.Plan) > 8<<10 {
		return p, errors.New("the move plan is not signed")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), append([]byte(movePlanDomain), s.Plan...), s.Signature) {
		return p, fmt.Errorf("the move plan is not signed by the core's key %s", KeyFingerprint(pub))
	}
	dec := json.NewDecoder(bytes.NewReader(s.Plan))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("the move plan: %w", err)
	}
	return p, p.Check()
}

// The roles of a host in a move.
const (
	MoveSource = "source"
	MoveTarget = "target"
)

// CheckMovePlanFor reports why the agent self, at version, refuses plan p
// for role at now: a plan that does not name it in that role, is older
// than MovePlanMaxAge (or from the future), whose deadline passed, or that
// carries another version. Whether the id was seen before is the agent's
// to check.
func CheckMovePlanFor(p MovePlan, role, self, version string, now time.Time) error {
	switch {
	case role == MoveSource && p.Source != self, role == MoveTarget && p.Target != self:
		return fmt.Errorf("the plan of move %s names %s as its %s, not this node (%s)", p.Move, map[string]string{MoveSource: p.Source, MoveTarget: p.Target}[role], role, self)
	case now.Sub(p.IssuedAt) > MovePlanMaxAge:
		return fmt.Errorf("the plan of move %s is stale: issued %s ago, more than %s", p.Move, now.Sub(p.IssuedAt).Round(time.Second), MovePlanMaxAge)
	case p.IssuedAt.After(now.Add(time.Minute)):
		return fmt.Errorf("the plan of move %s is issued in the future (%s): are the clocks right?", p.Move, p.IssuedAt.Format(time.RFC3339))
	case !now.Before(p.HandoffDeadline):
		return fmt.Errorf("the handoff deadline of move %s has passed", p.Move)
	case p.Version != version:
		return fmt.Errorf("the plan of move %s is for version %s, and this node runs %s: update it first", p.Move, p.Version, version)
	}
	return nil
}

// CoreLease is the lease as a host reads it from the bucket.
type CoreLease struct {
	Device    string    `json:"device"`
	Hostname  string    `json:"hostname,omitempty"`
	Version   string    `json:"version,omitempty"`
	State     string    `json:"state"` // live, stale, released
	RenewedAt time.Time `json:"renewed_at"`
}

// The lease's states in CoreLease.
const (
	LeaseLive     = "live"
	LeaseStale    = "stale"
	LeaseReleased = "released"
)

// CoreExportInfo is what an export says of itself next to the archive,
// readable without its keys: whose data dir it is (Device), the move it
// was made for, and its size.
type CoreExportInfo struct {
	Key       string    `json:"key"`
	Device    string    `json:"device,omitempty"`
	Move      string    `json:"move,omitempty"`
	Version   string    `json:"version,omitempty"`
	Hostname  string    `json:"hostname,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Bytes     int64     `json:"bytes,omitempty"`
}

// CoreRuntime is the core on a host as the lease and the bucket see it:
// this data dir's device id, whether another device took it over, the
// lease and the latest export.
type CoreRuntime struct {
	Device     string          `json:"device"`
	Superseded string          `json:"superseded,omitempty"`
	Lease      *CoreLease      `json:"lease,omitempty"`
	Latest     *CoreExportInfo `json:"latest,omitempty"`
	// DataDir says whether the data dir holds a core's data (DataDirHeld)
	// or nothing but its device id (DataDirEmpty); "" when not known (an
	// older version), which a move takes for held (#266).
	DataDir string `json:"data_dir,omitempty"`
}

// The values of CoreRuntime.DataDir.
const (
	DataDirEmpty = "empty"
	DataDirHeld  = "held"
)

// LiveElsewhere reports whether another device's core holds the lease,
// live: the one state in which this data dir is no core's only copy.
func (r CoreRuntime) LiveElsewhere() bool {
	return r.Lease != nil && r.Lease.State == LeaseLive && r.Lease.Device != r.Device
}

// HeldBy reports whether the lease is live and held by this data dir.
func (r CoreRuntime) HeldBy() bool {
	return r.Lease != nil && r.Lease.State == LeaseLive && r.Lease.Device == r.Device
}

// CoreImagePullParams names the image of the core's version, by digest.
// CheckOnly reports whether it is there without pulling it.
type CoreImagePullParams struct {
	Version   string `json:"version"`
	Image     string `json:"image,omitempty"`
	CheckOnly bool   `json:"check_only,omitempty"`
}

// CoreImageResult says whether the host can start the image: Present
// (already there, or a box on that version), Pulled now, and Note why
// not.
type CoreImageResult struct {
	Image   string `json:"image,omitempty"`
	Present bool   `json:"present"`
	Pulled  bool   `json:"pulled,omitempty"`
	Seconds int    `json:"seconds,omitempty"`
	Note    string `json:"note,omitempty"`
}

// CoreRehearseParams names the move a rehearsal is for.
type CoreRehearseParams struct {
	Move string `json:"move"`
}

// CoreRehearsal is a rehearsal's outcome: the export it imported and how
// long it took.
type CoreRehearsal struct {
	Move    string `json:"move"`
	Export  string `json:"export,omitempty"`
	Bytes   int64  `json:"bytes,omitempty"`
	Seconds int    `json:"seconds"`
	OK      bool   `json:"ok"`
	Note    string `json:"note,omitempty"`
}

// CoreMoveStatusParams asks for a move's state. Complete, from the core
// that runs after the move, ends the source's watch.
type CoreMoveStatusParams struct {
	Move     string `json:"move"`
	Complete bool   `json:"complete,omitempty"`
}

// A move's phases on a host (CoreMoveState.Phase). The final ones are
// MoveDone, MoveRolledBack, MoveGaveUp and MoveFailed.
const (
	MoveAccepted   = "accepted"
	MoveStopping   = "stopping"
	MoveExporting  = "exporting"
	MoveWatching   = "watching"
	MoveWaiting    = "waiting"
	MoveImporting  = "importing"
	MoveStarting   = "starting"
	MoveDone       = "done"
	MoveRolledBack = "rolled_back"
	MoveGaveUp     = "gave_up"
	MoveFailed     = "failed"
)

// MoveFinal reports whether phase is an end.
func MoveFinal(phase string) bool {
	switch phase {
	case MoveDone, MoveRolledBack, MoveGaveUp, MoveFailed:
		return true
	}
	return false
}

// CoreMoveStep is one line of a move's log on a host.
type CoreMoveStep struct {
	At     time.Time `json:"at"`
	Step   string    `json:"step"`
	Detail string    `json:"detail,omitempty"`
}

// CoreMoveState is a move as a host knows it: its role, the phase it is
// in, whether the core runs here now (Running), and the steps so far.
type CoreMoveState struct {
	Move    string         `json:"move"`
	Role    string         `json:"role"`
	Phase   string         `json:"phase"`
	Running bool           `json:"running"`
	Detail  string         `json:"detail,omitempty"`
	Steps   []CoreMoveStep `json:"steps"`
	Updated time.Time      `json:"updated"`
}
