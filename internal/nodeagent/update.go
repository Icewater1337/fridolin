package nodeagent

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// Updates from the core (docs/NODES.md, "Updates"). The core streams the
// gzip'd build of its version into update-transfers/ (update.write) and
// asks the node to apply it (update.apply) with its signature over the
// version, platform and SHA-256. The node checks the signature against
// the core's update key it pinned (core.pub), unpacks the binary next to
// its own, checks its SHA-256 and runs it once (`version`), swaps it in,
// keeping the old one as <exe>.prev, writes update.json and restarts.
// The new binary has UpdateTrialMinutes to connect to the core; when it
// does not, it puts <exe>.prev back and restarts into it, and either way
// the result reaches the core as an update event once a binary connects.

// The update files in the state dir.
const (
	// CoreKeyFile is the core's update key, hex, pinned when the node
	// joined or at its first update.
	CoreKeyFile = "core.pub"
	// updateMarkerFile is an update in progress or its result to report.
	updateMarkerFile = "update.json"
	// updateTransfers is where the new binary waits to be applied.
	updateTransfers = "update-transfers"
)

// ExitRestart is the exit code with which an agent under a service
// manager ends to be started again (launchd's KeepAlive, systemd's
// Restart=always, the Windows Service's restart on failure).
const ExitRestart = 75

// The phases of update.json.
const (
	phaseTrial = "trial"
	phaseDone  = "done"
)

// updateMarker is update.json.
type updateMarker struct {
	Phase      string `json:"phase"`
	From       string `json:"from"`
	To         string `json:"to"`
	Executable string `json:"executable"`
	// Deadline is when the trial ends; set when the new binary first runs.
	Deadline time.Time `json:"deadline,omitzero"`
	// Starts counts the new binary's starts during the trial.
	Starts int                     `json:"starts,omitempty"`
	Result *nodeproto.UpdateReport `json:"result,omitempty"`
}

// maxTrialStarts is how often a new binary may start without connecting
// before the node goes back to the old one.
const maxTrialStarts = 3

// updater is the agent's update state.
type updater struct {
	mu       sync.Mutex
	applying bool
	// committed closes when the new binary of a trial connected.
	committed chan struct{}
	trial     bool
	// pinned is the core's key of an agent without a state dir.
	pinned    []byte
	warnedKey bool
	// interactive is an agent run from a terminal, not a service.
	interactive bool
}

// updateOffered reports whether node.json lets the agent offer update.
func (a *Agent) updateOffered() bool { return a.Config.Update == nil || *a.Config.Update }

// executable is the binary an update replaces, symlinks resolved.
func (a *Agent) executable() (string, error) {
	exe := a.Executable
	if exe == "" {
		return "", errors.New("the agent was not told where its binary is")
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return filepath.Abs(exe)
}

func (a *Agent) trialDuration() time.Duration {
	if a.UpdateTrial > 0 {
		return a.UpdateTrial
	}
	return time.Duration(nodeproto.UpdateTrialMinutes) * time.Minute
}

// readCoreKey reads the pinned core key; nil when there is none.
func readCoreKey(dir string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(dir, CoreKeyFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%s is not an ed25519 public key", filepath.Join(dir, CoreKeyFile))
	}
	return key, nil
}

func writeCoreKey(dir string, key []byte) error {
	return writeFile(filepath.Join(dir, CoreKeyFile), []byte(hex.EncodeToString(key)+"\n"), 0o600)
}

// CoreKeyFingerprint names the core key node dir pinned, "" for none.
func CoreKeyFingerprint(dir string) string {
	key, err := readCoreKey(dir)
	if err != nil || key == nil {
		return ""
	}
	return nodeproto.KeyFingerprint(key)
}

// trustedCoreKey is the key an update's signature must verify with: the
// pinned one, or, for a node that joined before it knew the core's key,
// the key of the core it is connected to now, which it pins at this, its
// first update: the owner approved update before the core could call it.
func (a *Agent) trustedCoreKey() ([]byte, *nodeproto.Error) {
	session := a.State().CoreKey
	var pinned []byte
	if a.Dir != "" {
		k, err := readCoreKey(a.Dir)
		if err != nil {
			return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
		}
		pinned = k
	} else {
		a.upd.mu.Lock()
		pinned = a.upd.pinned
		a.upd.mu.Unlock()
	}
	if pinned != nil {
		return pinned, nil
	}
	if len(session) != ed25519.PublicKeySize {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "the core sent no update key")
	}
	if a.Dir != "" {
		if err := writeCoreKey(a.Dir, session); err != nil {
			return nil, nodeproto.Errorf(nodeproto.CodeFailed, "pinning the core's update key: %v", err)
		}
	} else {
		a.upd.mu.Lock()
		a.upd.pinned = bytes.Clone(session)
		a.upd.mu.Unlock()
	}
	a.logf("node: trusting the core's update key %s from now on (its first update)", nodeproto.KeyFingerprint(session))
	return session, nil
}

// checkCoreKey warns, once, when the core sends another update key than
// the one pinned: its updates are refused until the owner says otherwise.
func (a *Agent) checkCoreKey(st nodeproto.State) {
	if a.Dir == "" || len(st.CoreKey) == 0 {
		return
	}
	pinned, err := readCoreKey(a.Dir)
	if err != nil || pinned == nil || bytes.Equal(pinned, st.CoreKey) {
		return
	}
	a.upd.mu.Lock()
	warned := a.upd.warnedKey
	a.upd.warnedKey = true
	a.upd.mu.Unlock()
	if !warned {
		a.logf("node: the core's update key is %s, not %s as pinned: updates from it are refused. If the core moved on purpose, delete %s to trust its key at the next update",
			nodeproto.KeyFingerprint(st.CoreKey), nodeproto.KeyFingerprint(pinned), filepath.Join(a.Dir, CoreKeyFile))
	}
}

func (a *Agent) readMarker() (*updateMarker, error) {
	if a.Dir == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(a.Dir, updateMarkerFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m updateMarker
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("reading %s: %w", updateMarkerFile, err)
	}
	return &m, nil
}

func (a *Agent) writeMarker(m *updateMarker) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(a.Dir, updateMarkerFile), append(data, '\n'), 0o600)
}

func (a *Agent) removeMarker() { _ = os.Remove(filepath.Join(a.Dir, updateMarkerFile)) }

// updateStartup looks at update.json when the agent starts. A new binary
// on trial counts its start and gets a watchdog, which puts the old one
// back unless it connects in time; any other version that finds a trial
// was not the one to try, and records that the update did not hold.
func (a *Agent) updateStartup(ctx context.Context, wg *sync.WaitGroup) {
	if a.Dir == "" {
		return
	}
	pruneAndroid(filepath.Join(a.Dir, updateTransfers), time.Now(), 0)
	m, err := a.readMarker()
	if err != nil {
		a.logf("node: %v; forgetting it", err)
		a.removeMarker()
		return
	}
	if m == nil || m.Phase != phaseTrial {
		if exe, err := a.executable(); err == nil {
			cleanAside(exe)
		}
		return
	}
	if a.Version != m.To {
		m.Phase = phaseDone
		m.Result = &nodeproto.UpdateReport{From: m.From, To: m.To, Error: "the node runs " + a.Version + " instead"}
		_ = a.writeMarker(m)
		return
	}
	m.Starts++
	if m.Deadline.IsZero() {
		m.Deadline = time.Now().Add(a.trialDuration())
	}
	if m.Starts > maxTrialStarts {
		a.rollback(m, fmt.Sprintf("%s started %d times without connecting to the core", m.To, maxTrialStarts))
		return
	}
	if err := a.writeMarker(m); err != nil {
		a.logf("node: recording the update's trial: %v", err)
	}
	committed := make(chan struct{})
	a.upd.mu.Lock()
	a.upd.trial, a.upd.committed = true, committed
	a.upd.mu.Unlock()
	a.logf("node: running %s on trial: it has until %s to connect to the core, else %s comes back", m.To, m.Deadline.Local().Format("15:04:05"), m.From)
	wg.Go(func() {
		t := time.NewTimer(time.Until(m.Deadline))
		defer t.Stop()
		select {
		case <-ctx.Done():
		case <-committed:
		case <-t.C:
			a.rollback(m, fmt.Sprintf("%s did not connect to the core within %v", m.To, a.trialDuration().Round(time.Second)))
		}
	})
}

// rollback puts the previous binary back and restarts into it.
func (a *Agent) rollback(m *updateMarker, why string) {
	a.logf("node: the update to %s failed: %s; going back to %s", m.To, why, m.From)
	m.Phase = phaseDone
	m.Result = &nodeproto.UpdateReport{From: m.From, To: m.To, Error: why}
	if err := rollbackBinary(m.Executable); err != nil {
		m.Result.Error += "; going back failed: " + err.Error()
		a.logf("node: going back to %s failed: %v; staying on %s", m.From, err, m.To)
		_ = a.writeMarker(m)
		return
	}
	if err := a.writeMarker(m); err != nil {
		a.logf("node: recording the rollback: %v", err)
	}
	a.restart()
}

// updateConnected runs when a session is established: a trial ends well,
// and an update's result to report is returned (nil for none).
func (a *Agent) updateConnected() *nodeproto.UpdateReport {
	m, err := a.readMarker()
	if err != nil || m == nil {
		return nil
	}
	if m.Phase == phaseTrial {
		if a.Version != m.To {
			return nil
		}
		m.Phase = phaseDone
		m.Result = &nodeproto.UpdateReport{From: m.From, To: m.To, OK: true}
		if err := a.writeMarker(m); err != nil {
			a.logf("node: recording the update: %v", err)
		}
		a.upd.mu.Lock()
		if a.upd.trial {
			close(a.upd.committed)
			a.upd.trial = false
		}
		a.upd.mu.Unlock()
		a.logf("node: %s connected to the core: the update from %s holds (%s.prev is the old binary)", m.To, m.From, m.Executable)
	}
	return m.Result
}

// updateReported forgets an update whose result the core has.
func (a *Agent) updateReported() {
	if m, err := a.readMarker(); err == nil && m != nil && m.Phase == phaseDone {
		a.removeMarker()
	}
}

// restart starts the agent again, into whatever binary is in place now.
func (a *Agent) restart() {
	if a.Restart != nil {
		if err := a.Restart(); err != nil {
			a.logf("node: restarting: %v", err)
		}
		return
	}
	a.upd.mu.Lock()
	interactive := a.upd.interactive
	a.upd.mu.Unlock()
	if interactive {
		exe, err := a.executable()
		if err == nil {
			a.logf("node: restarting into %s", exe)
			err = reexec(exe, a.Args, a.Environ)
		}
		a.logf("node: restarting in place failed (%v); start it again yourself", err)
		os.Exit(ExitRestart)
	}
	a.logf("node: exiting for the service manager to start the agent again")
	os.Exit(ExitRestart)
}

// updateCall serves an update.* method.
func (a *Agent) updateCall(ctx context.Context, method string, raw json.RawMessage) (any, *nodeproto.Error) {
	if a.Dir == "" {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "the agent has no state dir")
	}
	switch method {
	case nodeproto.MethodUpdateWrite, nodeproto.MethodUpdateDrop:
		var p nodeproto.FrideChunkParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%s params: %v", method, err)
			}
		}
		if method == nodeproto.MethodUpdateWrite && p.ID == "" {
			if p.Offset != 0 {
				return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "a new transfer starts at offset 0")
			}
			p.ID = newTransferID()
		}
		path, e := a.updateTransferPath(p.ID)
		if e != nil {
			return nil, e
		}
		if method == nodeproto.MethodUpdateDrop {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
			}
			return struct{}{}, nil
		}
		if p.Offset+int64(len(p.Data)) > nodeproto.MaxUpdateTransferBytes {
			return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "an update is at most %d bytes", nodeproto.MaxUpdateTransferBytes)
		}
		tr, e := appendChunk(path, p)
		if e != nil {
			return nil, e
		}
		return tr, nil
	case nodeproto.MethodUpdateApply:
		var p nodeproto.UpdateApplyParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%s params: %v", method, err)
		}
		return a.updateApply(ctx, p)
	}
	return nil, nodeproto.Errorf(nodeproto.CodeUnsupported, "no method %q", method)
}

func (a *Agent) updateTransferPath(id string) (string, *nodeproto.Error) {
	if !transferIDRE.MatchString(id) {
		return "", nodeproto.Errorf(nodeproto.CodeBadRequest, "transfer id %q", id)
	}
	dir := filepath.Join(a.Dir, updateTransfers)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	return filepath.Join(dir, id+".gz"), nil
}

// updateApply checks a staged binary and puts it in place of the agent's
// own, then restarts into it.
func (a *Agent) updateApply(ctx context.Context, p nodeproto.UpdateApplyParams) (any, *nodeproto.Error) {
	if err := p.Check(); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	path, e := a.updateTransferPath(p.ID)
	if e != nil {
		return nil, e
	}
	defer os.Remove(path)
	if p.OS != runtime.GOOS || p.Arch != runtime.GOARCH {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "a binary for %s/%s; this node is %s/%s", p.OS, p.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if p.Version == a.Version {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "this node runs %s already", p.Version)
	}
	if m, err := a.readMarker(); err == nil && m != nil && m.Phase == phaseTrial {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "the update to %s is still on trial; try again once it connected", m.To)
	}
	a.upd.mu.Lock()
	if a.upd.applying {
		a.upd.mu.Unlock()
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "an update is being applied already")
	}
	a.upd.applying = true
	a.upd.mu.Unlock()
	applied := false
	defer func() {
		if !applied {
			a.upd.mu.Lock()
			a.upd.applying = false
			a.upd.mu.Unlock()
		}
	}()
	// Not in the middle of an Android build: the restart would cut it off.
	if !a.android.building.TryLock() {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "this node is building an app; update it when the build is done")
	}
	defer a.android.building.Unlock()

	key, e := a.trustedCoreKey()
	if e != nil {
		return nil, e
	}
	if !nodeproto.VerifyUpdate(key, p) {
		return nil, nodeproto.Errorf(nodeproto.CodeDenied, "the update's signature does not verify with the core's update key %s", nodeproto.KeyFingerprint(key))
	}
	if sum, err := fileSHA256(path); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeNotFound, "no transfer %s", p.ID)
	} else if sum != p.TransferSHA256 {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "the transfer's SHA-256 is %s, the core sent %s: it changed on the way", sum, p.TransferSHA256)
	}
	exe, err := a.executable()
	if err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "finding this agent's binary: %v", err)
	}
	newPath, err := unpackBinary(path, exe, p)
	if err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(newPath)
		}
	}()
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	check := a.CheckBinary
	if check == nil {
		check = runVersion
	}
	if err := check(cctx, newPath, p.Version); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "the new binary does not run here: %v", err)
	}
	m := &updateMarker{Phase: phaseTrial, From: a.Version, To: p.Version, Executable: exe}
	if err := a.writeMarker(m); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	if err := swapBinary(exe, newPath); err != nil {
		a.removeMarker()
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "putting the new binary in place: %v", err)
	}
	keep, applied = true, true
	a.logf("node: put %s in place of %s at %s (the old one is %s.prev); restarting", p.Version, a.Version, exe, exe)
	// After the answer went out.
	time.AfterFunc(500*time.Millisecond, a.restart)
	return nodeproto.UpdateApplyResult{From: a.Version, To: p.Version, Executable: exe, TrialMinutes: int(a.trialDuration().Round(time.Minute) / time.Minute)}, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// unpackBinary unpacks the gzip'd binary at path next to exe, checking
// its size and SHA-256 against p, and returns where it lies.
func unpackBinary(path, exe string, p nodeproto.UpdateApplyParams) (string, error) {
	in, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer in.Close()
	zr, err := gzip.NewReader(in)
	if err != nil {
		return "", fmt.Errorf("the transfer is not gzip'd: %w", err)
	}
	out, err := os.CreateTemp(filepath.Dir(exe), "."+filepath.Base(exe)+".new-*")
	if err != nil {
		return "", fmt.Errorf("writing next to %s: %w", exe, err)
	}
	name := out.Name()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(zr, nodeproto.MaxBinaryBytes+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(name, 0o755)
	}
	if err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("unpacking the binary: %w", err)
	}
	if sum := hex.EncodeToString(h.Sum(nil)); n != p.Size || sum != p.SHA256 {
		_ = os.Remove(name)
		return "", fmt.Errorf("the binary has %d bytes with SHA-256 %s; the core signed %d with %s", n, sum, p.Size, p.SHA256)
	}
	return name, nil
}

// runVersion runs `bin version` and checks it says version.
func runVersion(ctx context.Context, bin, version string) error {
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return err
	}
	if got := strings.TrimSpace(string(out)); got != version {
		return fmt.Errorf("it says it is %q, not %s", got, version)
	}
	return nil
}

// Rollback puts exe.prev back in place of exe, for `fridolin node
// rollback`; the agent then needs a restart.
func Rollback(exe string) error {
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return rollbackBinary(exe)
}
