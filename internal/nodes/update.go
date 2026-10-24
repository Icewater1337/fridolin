package nodes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// Updates (docs/NODES.md, "Updates"): the core keeps a build of its own
// version for each OS and architecture a node runs on (BinariesDir, which
// the core image fills at build time), and replaces a node's fridolin
// with it when the owner asks or the node's auto policy says so. It
// streams the gzip'd binary to the node in chunks and signs its version,
// platform and SHA-256 with its update key (UpdateKeyFile), which the
// node pinned when it joined; the node checks both, swaps the binary,
// keeps the old one and restarts, and puts the old one back when the new
// one does not connect within nodeproto.UpdateTrialMinutes.

// UpdateKeyFile is the core's update key in the nodes folder: an ed25519
// seed, hex, made the first time a node connects.
const UpdateKeyFile = "update.key"

// BinariesManifest is the file in the binaries folder that lists them.
const BinariesManifest = "manifest.json"

// The states of an update (UpdateStatus.State).
const (
	UpdateSending    = "sending"
	UpdateRestarting = "restarting"
	UpdateOK         = "ok"
	UpdateFailed     = "failed"
)

// UpdateStatus is a node's updates as nodes.json keeps them: the owner's
// auto policy, and how the last update went.
type UpdateStatus struct {
	// Auto updates the node to the core's version whenever it runs an
	// older one, after the core itself was updated: when it connects, and
	// while it is connected, never while it is busy.
	Auto bool `json:"auto,omitempty"`
	// State is the last update's: sending, restarting, ok or failed.
	State string `json:"state,omitempty"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
	Error string `json:"error,omitempty"`
	// Auto updates leave a version that failed alone: FailedVersion is it.
	FailedVersion string     `json:"failed_version,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
}

// Running reports whether an update is under way.
func (u *UpdateStatus) Running() bool {
	return u != nil && (u.State == UpdateSending || u.State == UpdateRestarting)
}

// Binary is one build in the binaries folder.
type Binary struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
	// File is the gzip'd binary's name in the folder.
	File string `json:"file"`
	// SHA256 and Size are of the binary unpacked.
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// binaries is the manifest.
type binaries struct {
	Version  string   `json:"version"`
	Binaries []Binary `json:"binaries"`
}

// ErrNoBinary is a platform the core has no build of its version for.
var ErrNoBinary = errors.New("no node binary")

var binaryFileRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// Binary finds the build of the core's version for goos/goarch, and the
// path of its gzip'd file.
func (s *Service) Binary(goos, goarch string) (Binary, string, error) {
	if s.binDir == "" {
		return Binary{}, "", fmt.Errorf("%w: this core has no folder of node binaries (the core image carries them; a native install builds them with make node-binaries)", ErrNoBinary)
	}
	data, err := os.ReadFile(filepath.Join(s.binDir, BinariesManifest))
	if errors.Is(err, os.ErrNotExist) {
		return Binary{}, "", fmt.Errorf("%w: %s holds no node binaries (the core image carries them; a native install builds them with make node-binaries)", ErrNoBinary, s.binDir)
	}
	if err != nil {
		return Binary{}, "", err
	}
	var m binaries
	if err := json.Unmarshal(data, &m); err != nil {
		return Binary{}, "", fmt.Errorf("reading %s: %w", filepath.Join(s.binDir, BinariesManifest), err)
	}
	if m.Version != s.version {
		return Binary{}, "", fmt.Errorf("%w: the node binaries in %s are of version %s, this core is %s; build them again (make node-binaries)", ErrNoBinary, s.binDir, m.Version, s.version)
	}
	for _, b := range m.Binaries {
		if b.OS == goos && b.Arch == goarch {
			if !binaryFileRE.MatchString(b.File) || strings.HasPrefix(b.File, ".") {
				return Binary{}, "", fmt.Errorf("%s: the file %q", BinariesManifest, b.File)
			}
			return b, filepath.Join(s.binDir, b.File), nil
		}
	}
	return Binary{}, "", fmt.Errorf("%w for %s/%s in %s", ErrNoBinary, goos, goarch, s.binDir)
}

// updateKey is the core's update key, made the first time it is needed.
func (s *Service) updateKey() (ed25519.PrivateKey, error) {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	if s.key != nil {
		return s.key, nil
	}
	path := filepath.Join(s.dir, UpdateKeyFile)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		seed, derr := hex.DecodeString(strings.TrimSpace(string(data)))
		if derr != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("%s is not an ed25519 seed", path)
		}
		s.key = ed25519.NewKeyFromSeed(seed)
	case errors.Is(err, os.ErrNotExist):
		_, key, gerr := ed25519.GenerateKey(rand.Reader)
		if gerr != nil {
			return nil, gerr
		}
		if err := os.MkdirAll(s.dir, 0o700); err != nil {
			return nil, err
		}
		if err := fsx.WriteFileAtomic(path, []byte(hex.EncodeToString(key.Seed())+"\n"), 0o600, false); err != nil {
			return nil, err
		}
		s.key = key
		s.logf("nodes: made the core's update key (%s), which signs the binaries nodes are updated to", nodeproto.KeyFingerprint(key.Public().(ed25519.PublicKey)))
	default:
		return nil, err
	}
	return s.key, nil
}

// CoreKey is the public half of the core's update key, nil when it cannot
// be read or made.
func (s *Service) CoreKey() []byte {
	key, err := s.updateKey()
	if err != nil {
		s.logf("nodes: the core's update key: %v", err)
		return nil
	}
	return key.Public().(ed25519.PublicKey)
}

// stateOf is what node n is told, with the core's update key.
func (s *Service) stateOf(n *Node) nodeproto.State {
	st := n.State()
	if n.Device != nodeproto.DeviceRevoked {
		st.CoreKey = s.CoreKey()
	}
	return st
}

// UpdateTarget is the version node n can be updated to now, "" when it
// cannot, with why not for a node that runs an older version.
func (s *Service) UpdateTarget(n *Node) (to, why string) {
	if n.Device != nodeproto.DeviceApproved || n.Version == "" || !nodeproto.Outdated(n.Version, s.version) {
		return "", ""
	}
	if cmp, ok := nodeproto.CompareVersions(n.Version, s.version); ok && cmp > 0 {
		return "", ""
	}
	if n.Declared.Update == nil {
		return "", "its fridolin " + n.Version + " cannot be updated from the core: update it by hand once (docs/NODES.md, Updates)"
	}
	if n.Effective().Update == nil {
		return "", "approve what it offers (update) to let the core update it"
	}
	if n.Declared.Update.Box {
		if s.boxImages == nil {
			return "", errNoBoxImages.Error()
		}
		return s.version, ""
	}
	if _, _, err := s.Binary(n.OS, n.Arch); err != nil {
		return "", err.Error()
	}
	return s.version, ""
}

// errNoBoxImages is a core that cannot name its images for a box.
var errNoBoxImages = errors.New("this core does not know where its images are published (a local build), so it cannot update a node in a box; update the box by hand (box.sh --update)")

// SetUpdateAuto sets node id's auto policy.
func (s *Service) SetUpdateAuto(id string, auto bool) (*Node, error) {
	n, err := s.reg.change(id, func(n *Node, _ time.Time) error {
		if n.Device == nodeproto.DeviceRevoked {
			return ErrRevoked
		}
		if n.Update == nil {
			n.Update = &UpdateStatus{}
		}
		n.Update.Auto = auto
		if auto {
			// A version that failed before is tried again when the owner
			// turns auto on again.
			n.Update.FailedVersion = ""
		}
		return nil
	})
	if err == nil {
		s.logf("nodes: auto updates of %s (%s): %v", n.Name, n.ID, auto)
		if auto {
			s.maybeAuto(n.ID)
		}
	}
	return n, err
}

// setUpdate changes node id's update status.
func (s *Service) setUpdate(id string, fn func(u *UpdateStatus, now time.Time)) {
	_, err := s.reg.change(id, func(n *Node, now time.Time) error {
		if n.Update == nil {
			n.Update = &UpdateStatus{}
		}
		fn(n.Update, now)
		return nil
	})
	if err != nil {
		s.logf("nodes: recording the update of %s: %v", id, err)
	}
}

// Updating reports whether an update runs on node id.
func (s *Service) Updating(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updating[id]
}

// ErrUpdate is an update that cannot start.
var ErrUpdate = errors.New("cannot update")

// UpdateOptions say how to update.
type UpdateOptions struct {
	// Force updates a node that is busy (a FrIDE task, a backup).
	Force bool
	// Auto marks an update the auto policy started.
	Auto bool
}

// StartUpdate checks that node id can be updated now and starts doing so
// in the background; nodes.json's update says how it goes. It returns the
// version it updates to.
func (s *Service) StartUpdate(id string, o UpdateOptions) (string, error) {
	n, bin, path, err := s.prepareUpdate(id, o)
	if err != nil {
		return "", err
	}
	s.wg.Go(func() { _ = s.runUpdate(s.ctx, n, bin, path, o) })
	return s.version, nil
}

// Update updates node id and returns when it is done: the node connected
// again with the new version, or the update failed.
func (s *Service) Update(ctx context.Context, id string, o UpdateOptions) error {
	n, bin, path, err := s.prepareUpdate(id, o)
	if err != nil {
		return err
	}
	return s.runUpdate(ctx, n, bin, path, o)
}

// prepareUpdate checks the update and claims it, so that one update runs
// on a node at a time.
func (s *Service) prepareUpdate(id string, o UpdateOptions) (*Node, Binary, string, error) {
	n, err := s.reg.Get(id)
	if err != nil {
		return nil, Binary{}, "", err
	}
	if n.Device != nodeproto.DeviceApproved {
		return nil, Binary{}, "", fmt.Errorf("%w %s: the device is %s", ErrUpdate, n.Name, n.Device)
	}
	if n.Declared.Update == nil {
		return nil, Binary{}, "", fmt.Errorf("%w %s: its fridolin %s cannot be updated from the core; update it by hand once (docs/NODES.md, Updates)", ErrUpdate, n.Name, n.Version)
	}
	if n.Effective().Update == nil {
		return nil, Binary{}, "", fmt.Errorf("%w %s: update is not approved; approve what it offers first (fridolin nodes approve %s --commands)", ErrUpdate, n.Name, n.ID)
	}
	if !s.Online(id) {
		return nil, Binary{}, "", fmt.Errorf("%w %s: it is not connected", ErrUpdate, n.Name)
	}
	if n.Version == s.version {
		return nil, Binary{}, "", fmt.Errorf("%w %s: it runs %s already", ErrUpdate, n.Name, s.version)
	}
	if cmp, ok := nodeproto.CompareVersions(n.Version, s.version); ok && cmp > 0 && !o.Force {
		return nil, Binary{}, "", fmt.Errorf("%w %s: it runs %s, newer than this core's %s (force to go back)", ErrUpdate, n.Name, n.Version, s.version)
	}
	if s.hold != nil {
		if why := s.hold(); why != "" {
			return nil, Binary{}, "", fmt.Errorf("%w %s: %s", ErrUpdate, n.Name, why)
		}
	}
	if s.busy != nil && !o.Force {
		if why := s.busy(id); why != "" {
			return nil, Binary{}, "", fmt.Errorf("%w %s now: %s (force to update anyway)", ErrUpdate, n.Name, why)
		}
	}
	var bin Binary
	var path string
	if n.Declared.Update.Box {
		if s.boxImages == nil {
			return nil, Binary{}, "", fmt.Errorf("%w %s: %v", ErrUpdate, n.Name, errNoBoxImages)
		}
	} else if bin, path, err = s.Binary(n.OS, n.Arch); err != nil {
		return nil, Binary{}, "", fmt.Errorf("%w %s: %v", ErrUpdate, n.Name, err)
	}
	if _, err := s.updateKey(); err != nil {
		return nil, Binary{}, "", fmt.Errorf("%w %s: the core's update key: %v", ErrUpdate, n.Name, err)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, Binary{}, "", fmt.Errorf("%w %s: the core is stopping", ErrUpdate, n.Name)
	}
	if s.updating[id] {
		s.mu.Unlock()
		return nil, Binary{}, "", fmt.Errorf("%w %s: an update is under way", ErrUpdate, n.Name)
	}
	s.updating[id] = true
	s.mu.Unlock()
	s.setUpdate(id, func(u *UpdateStatus, now time.Time) {
		*u = UpdateStatus{Auto: u.Auto, FailedVersion: u.FailedVersion, State: UpdateSending, From: n.Version, To: s.version, StartedAt: &now}
	})
	return n, bin, path, nil
}

func init() {
	// A chunk of 8 MiB over a slow link; unpacking, checking and trying
	// the new binary on the node.
	slowCalls[nodeproto.MethodUpdateWrite] = 5 * time.Minute
	slowCalls[nodeproto.MethodUpdateApply] = 3 * time.Minute
}

// updateWait is how long the core waits for an updated node to connect
// again: the node's trial, and time to put the old binary back and
// restart.
var updateWait = time.Duration(nodeproto.UpdateTrialMinutes)*time.Minute + 3*time.Minute

// updatePoll is how often the core looks whether the node is back.
var updatePoll = 250 * time.Millisecond

func (s *Service) runUpdate(ctx context.Context, n *Node, bin Binary, path string, o UpdateOptions) (err error) {
	id := n.ID
	defer func() {
		s.mu.Lock()
		delete(s.updating, id)
		s.mu.Unlock()
		s.setUpdate(id, func(u *UpdateStatus, now time.Time) {
			u.FinishedAt = &now
			if err != nil {
				u.State, u.Error = UpdateFailed, err.Error()
				u.FailedVersion = s.version
				return
			}
			u.State, u.Error, u.FailedVersion = UpdateOK, "", ""
		})
		if err != nil {
			s.logf("nodes: updating %s (%s) to %s failed: %v", n.Name, id, s.version, err)
		} else {
			s.logf("nodes: %s (%s) runs %s now", n.Name, id, s.version)
		}
	}()
	how := "updating"
	if o.Auto {
		how = "auto-updating"
	}
	s.logf("nodes: %s %s (%s, %s/%s) from %s to %s", how, n.Name, id, n.OS, n.Arch, n.Version, s.version)
	key, err := s.updateKey()
	if err != nil {
		return err
	}
	if n.Declared.Update.Box {
		return s.runBoxUpdate(ctx, n, key)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var tr nodeproto.FrideTransfer
	h := sha256.New()
	buf := make([]byte, nodeproto.FrideChunkBytes)
	for {
		k, rerr := io.ReadFull(f, buf)
		if k > 0 || tr.ID == "" {
			h.Write(buf[:k])
			if err := s.Call(ctx, id, nodeproto.MethodUpdateWrite, nodeproto.FrideChunkParams{ID: tr.ID, Offset: tr.Size, Data: buf[:k]}, &tr); err != nil {
				if tr.ID != "" {
					s.dropUpdate(id, tr.ID)
				}
				return fmt.Errorf("sending the binary: %w", err)
			}
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			s.dropUpdate(id, tr.ID)
			return rerr
		}
	}
	s.mu.Lock()
	before := s.sessions[id]
	s.mu.Unlock()
	p := nodeproto.UpdateApplyParams{
		ID: tr.ID, Version: s.version, OS: n.OS, Arch: n.Arch, SHA256: bin.SHA256, Size: bin.Size,
		TransferSHA256: hex.EncodeToString(h.Sum(nil)),
		Signature:      nodeproto.SignUpdate(key, s.version, n.OS, n.Arch, bin.SHA256),
	}
	var res nodeproto.UpdateApplyResult
	if err := s.Call(ctx, id, nodeproto.MethodUpdateApply, p, &res); err != nil {
		s.dropUpdate(id, tr.ID)
		return fmt.Errorf("the node refused the update: %w", err)
	}
	s.setUpdate(id, func(u *UpdateStatus, _ time.Time) { u.State = UpdateRestarting })
	s.logf("nodes: %s (%s) put %s in place of %s at %s and restarts", n.Name, id, res.To, res.From, res.Executable)
	return s.awaitUpdated(ctx, id, before)
}

// boxUpdateWait is how long the core waits for a box to come back on the
// new images: the updater's round, pulling several GB, and its own wait
// before it goes back.
var boxUpdateWait = time.Duration(nodeproto.BoxUpdaterMinutes)*time.Minute + 30*time.Minute

// runBoxUpdate asks a node in a box for the images of the core's version,
// pinned by digest and signed with the update key, and waits for it to
// connect again on them. The box's updater on its host does the work
// (docs/NODES.md, "Box updates"); the core never reaches its Docker.
func (s *Service) runBoxUpdate(ctx context.Context, n *Node, key ed25519.PrivateKey) error {
	rctx, cancel := context.WithTimeout(ctx, time.Minute)
	nodeImage, frideImage, err := s.boxImages(rctx, s.version)
	cancel()
	if err != nil {
		return fmt.Errorf("finding the images of %s: %w", s.version, err)
	}
	p := nodeproto.BoxUpdateParams{Version: s.version, NodeImage: nodeImage, FrideImage: frideImage,
		Signature: nodeproto.SignBoxUpdate(key, s.version, nodeImage, frideImage)}
	if err := p.Check(); err != nil {
		return fmt.Errorf("the images of %s: %w", s.version, err)
	}
	s.mu.Lock()
	before := s.sessions[n.ID]
	s.mu.Unlock()
	var res nodeproto.BoxUpdateResult
	if err := s.Call(ctx, n.ID, nodeproto.MethodUpdateBox, p, &res); err != nil {
		return fmt.Errorf("the box refused the update: %w", err)
	}
	s.setUpdate(n.ID, func(u *UpdateStatus, _ time.Time) { u.State = UpdateRestarting })
	s.logf("nodes: %s (%s) keeps the request for %s (%s); its updater picks it up within %d minutes", n.Name, n.ID, p.Version, p.NodeImage, res.UpdaterMinutes)
	return s.awaitUpdatedWithin(ctx, n.ID, before, boxUpdateWait)
}

// dropUpdate removes a transfer the update leaves on the node.
func (s *Service) dropUpdate(id, transfer string) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	_ = s.Call(ctx, id, nodeproto.MethodUpdateDrop, nodeproto.FrideChunkParams{ID: transfer}, nil)
}

// awaitUpdated waits for node id to connect again, on a session other than
// before, and checks the version it runs.
func (s *Service) awaitUpdated(ctx context.Context, id string, before *session) error {
	return s.awaitUpdatedWithin(ctx, id, before, updateWait)
}

// awaitUpdatedWithin is awaitUpdated with its own wait.
func (s *Service) awaitUpdatedWithin(ctx context.Context, id string, before *session, wait time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	t := time.NewTicker(updatePoll)
	defer t.Stop()
	for {
		s.mu.Lock()
		cur := s.sessions[id]
		// A box reports a failed update without restarting when its
		// updater could not even pull the images.
		r, failed := s.reports[id]
		if failed && !r.OK && r.To == s.version && cur == before {
			delete(s.reports, id)
			s.mu.Unlock()
			return fmt.Errorf("the node stays on %s: %s", r.From, r.Error)
		}
		s.mu.Unlock()
		if cur != nil && cur != before {
			n, err := s.reg.Get(id)
			if err != nil {
				return err
			}
			if n.Version == s.version {
				return nil
			}
			// The old version is back: the node rolled back. Its report
			// (EventUpdate) says why, when it comes.
			why := s.awaitReport(ctx, id)
			if why == "" {
				why = "it runs " + n.Version + " again"
			}
			return fmt.Errorf("the node went back to %s: %s", n.Version, why)
		}
		select {
		case <-ctx.Done():
			if s.ctx.Err() != nil {
				return errors.New("the core stopped before the node came back")
			}
			return fmt.Errorf("the node did not connect again within %v", wait)
		case <-t.C:
		}
	}
}

// awaitReport waits a little for the node's report of a failed update.
func (s *Service) awaitReport(ctx context.Context, id string) string {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		s.mu.Lock()
		r, ok := s.reports[id]
		delete(s.reports, id)
		s.mu.Unlock()
		if ok {
			return r.Error
		}
		time.Sleep(updatePoll)
	}
	return ""
}

// onUpdateReport hears a node's report of how an update went.
func (s *Service) onUpdateReport(id string, ev nodeproto.Event) {
	var r nodeproto.UpdateReport
	if err := json.Unmarshal(ev.Data, &r); err != nil {
		s.logf("nodes: an unreadable update report of %s: %v", id, err)
		return
	}
	if r.OK {
		s.logf("nodes: %s reports it runs %s since its update from %s", id, r.To, r.From)
		return
	}
	s.logf("nodes: %s reports its update from %s to %s failed: %s", id, r.From, r.To, r.Error)
	s.mu.Lock()
	running := s.updating[id]
	if running {
		s.reports[id] = r
	}
	s.mu.Unlock()
	if !running {
		// After the core waited: the report says what happened.
		s.setUpdate(id, func(u *UpdateStatus, now time.Time) {
			if u.To == r.To && u.State != UpdateOK {
				if u.State != UpdateFailed {
					u.FinishedAt = &now
				}
				u.State, u.Error, u.FailedVersion = UpdateFailed, r.Error, r.To
			}
		})
	}
}

// AutoEvery is how often the core looks for nodes to auto-update while
// they stay connected (a busy node waits for the next look).
var AutoEvery = 10 * time.Minute

// maybeAuto updates node id when its auto policy says so: auto on, update
// approved, an older version than the core's, a binary for it, and not
// this version failing before. Busy nodes wait.
func (s *Service) maybeAuto(id string) {
	n, err := s.reg.Get(id)
	if err != nil || n.Update == nil || !n.Update.Auto || n.Update.Running() || n.Update.FailedVersion == s.version {
		return
	}
	if to, _ := s.UpdateTarget(n); to == "" {
		return
	}
	// Only a version known to be older: by its number, or as one this
	// core ran before. A build of another commit may be newer, and only
	// the owner updates that one.
	if _, ok := nodeproto.CompareVersions(n.Version, s.version); !ok && !s.ranBefore(n.Version) {
		return
	}
	if _, err := s.StartUpdate(id, UpdateOptions{Auto: true}); err != nil && !errors.Is(err, ErrUpdate) {
		s.logf("nodes: auto update of %s: %v", id, err)
	} else if err != nil {
		s.logf("nodes: auto update of %s waits: %v", id, err)
	}
}

// VersionsFile lists the versions the core ran, oldest first: a node that
// runs one of them is older than the core, whatever the versions' names.
const VersionsFile = "core-versions.json"

// maxVersions bounds the list.
const maxVersions = 200

// noteVersion adds the core's version to VersionsFile.
func (s *Service) noteVersion() {
	if s.version == "" {
		return
	}
	path := filepath.Join(s.dir, VersionsFile)
	var list []string
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &list)
	}
	if len(list) > 0 && list[len(list)-1] == s.version {
		return
	}
	list = append(slices.DeleteFunc(list, func(v string) bool { return v == s.version }), s.version)
	if len(list) > maxVersions {
		list = list[len(list)-maxVersions:]
	}
	data, _ := json.MarshalIndent(list, "", "  ")
	if err := os.MkdirAll(s.dir, 0o700); err == nil {
		err = fsx.WriteFileAtomic(path, append(data, '\n'), 0o600, false)
		if err != nil {
			s.logf("nodes: recording the core's version: %v", err)
		}
	}
}

// ranBefore reports whether the core ran version v before its own.
func (s *Service) ranBefore(v string) bool {
	data, err := os.ReadFile(filepath.Join(s.dir, VersionsFile))
	if err != nil {
		return false
	}
	var list []string
	if json.Unmarshal(data, &list) != nil {
		return false
	}
	return v != s.version && slices.Contains(list, v)
}

// autoLoop looks for nodes to auto-update every AutoEvery.
func (s *Service) autoLoop() {
	t := time.NewTicker(AutoEvery)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		}
		list, _ := s.reg.List()
		for _, n := range list {
			if s.Online(n.ID) {
				s.maybeAuto(n.ID)
			}
		}
	}
}
