package nodeagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// A box's updates (docs/NODES.md, "Box updates"). The core asks for the
// images of its version, signed with its update key; the agent checks the
// signature against the key it pinned and keeps the request in the state
// dir, where the box's updater on the host finds it. The updater checks
// it again in a throwaway container of the image the box runs now, with
// the images' build provenance, pulls them, recreates the services and
// writes the result back, which the agent reports once it is connected.
// The agent itself never reaches Docker.
const (
	// BoxUpdateFile is the request the updater picks up.
	BoxUpdateFile = "box-update.json"
	// BoxUpdateTakenFile is the request while the updater applies it.
	BoxUpdateTakenFile = "box-update.taken.json"
	// BoxUpdateResultFile is how the updater's last update ended.
	BoxUpdateResultFile = "box-update-result.json"
	// BoxStatusFile is when the agent last knew itself connected, for the
	// node service's health check (fridolin node health).
	BoxStatusFile = "box-status.json"
)

// BoxUpdateRequest is BoxUpdateFile: the core's params and when and from
// which version they were asked for.
type BoxUpdateRequest struct {
	nodeproto.BoxUpdateParams
	From        string    `json:"from"`
	RequestedAt time.Time `json:"requested_at"`
}

// BoxStatus is BoxStatusFile.
type BoxStatus struct {
	Connected bool      `json:"connected"`
	Version   string    `json:"version"`
	At        time.Time `json:"at"`
}

// boxUpdate keeps a signed request for the box's updater.
func (a *Agent) boxUpdate(raw json.RawMessage) (any, *nodeproto.Error) {
	var p nodeproto.BoxUpdateParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%s params: %v", nodeproto.MethodUpdateBox, err)
	}
	if err := p.Check(); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	key, e := a.trustedCoreKey()
	if e != nil {
		return nil, e
	}
	if !nodeproto.VerifyBoxUpdate(key, p) {
		return nil, nodeproto.Errorf(nodeproto.CodeDenied, "the box update is not signed by the core's key %s", nodeproto.KeyFingerprint(key))
	}
	req := BoxUpdateRequest{BoxUpdateParams: p, From: a.Version, RequestedAt: time.Now().UTC()}
	data, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	if err := writeFile(filepath.Join(a.Dir, BoxUpdateFile), append(data, '\n'), 0o600); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "keeping the request: %v", err)
	}
	a.logf("node: box: the core asked for %s (%s); the box's updater picks it up within %d minutes", p.Version, p.NodeImage, nodeproto.BoxUpdaterMinutes)
	return nodeproto.BoxUpdateResult{From: a.Version, To: p.Version, UpdaterMinutes: nodeproto.BoxUpdaterMinutes}, nil
}

// boxResult is the updater's last result to report, nil for none.
func (a *Agent) boxResult() *nodeproto.UpdateReport {
	if a.Config.Box == nil || a.Dir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(a.Dir, BoxUpdateResultFile))
	if err != nil {
		return nil
	}
	var r nodeproto.UpdateReport
	if json.Unmarshal(data, &r) != nil {
		a.logf("node: box: %s does not read; removing it", BoxUpdateResultFile)
		a.boxResultReported()
		return nil
	}
	return &r
}

// boxResultReported forgets the result the core has.
func (a *Agent) boxResultReported() {
	_ = os.Remove(filepath.Join(a.Dir, BoxUpdateResultFile))
}

// boxStatus records whether the agent is connected, for the health check.
func (a *Agent) boxStatus(connected bool) {
	if a.Config.Box == nil || a.Dir == "" {
		return
	}
	data, _ := json.Marshal(BoxStatus{Connected: connected, Version: a.Version, At: time.Now().UTC()})
	if err := writeFile(filepath.Join(a.Dir, BoxStatusFile), append(data, '\n'), 0o600); err != nil {
		a.logf("node: box: recording the status: %v", err)
	}
}

// BoxHealthy reports whether the agent of the box in dir was connected to
// the core within maxAge.
func BoxHealthy(dir string, maxAge time.Duration, now time.Time) error {
	data, err := os.ReadFile(filepath.Join(dir, BoxStatusFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("the agent has not been connected yet")
		}
		return err
	}
	var st BoxStatus
	if err := json.Unmarshal(data, &st); err != nil {
		return fmt.Errorf("%s: %w", BoxStatusFile, err)
	}
	switch {
	case !st.Connected:
		return fmt.Errorf("not connected to the core since %s", st.At.Format(time.RFC3339))
	case now.Sub(st.At) > maxAge:
		return fmt.Errorf("no word from the agent since %s", st.At.Format(time.RFC3339))
	}
	return nil
}

// ReadBoxUpdate reads the box's pending request in dir; nil when there
// is none.
func ReadBoxUpdate(dir string) (*BoxUpdateRequest, error) {
	data, err := os.ReadFile(filepath.Join(dir, BoxUpdateFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r BoxUpdateRequest
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", BoxUpdateFile, err)
	}
	return &r, nil
}

// VerifyBoxUpdate checks a request against the core's key the box in dir
// pinned: its params, and the signature.
func VerifyBoxUpdate(dir string, r *BoxUpdateRequest) error {
	if err := r.Check(); err != nil {
		return err
	}
	key, err := readCoreKey(dir)
	if err != nil {
		return err
	}
	if key == nil {
		return errors.New("the box has not pinned the core's update key")
	}
	if !nodeproto.VerifyBoxUpdate(key, r.BoxUpdateParams) {
		return fmt.Errorf("the request is not signed by the core's key %s", nodeproto.KeyFingerprint(key))
	}
	return nil
}

// TakeBoxUpdate marks the pending request as being applied, so that the
// next round of the updater does not apply it again.
func TakeBoxUpdate(dir string) error {
	return os.Rename(filepath.Join(dir, BoxUpdateFile), filepath.Join(dir, BoxUpdateTakenFile))
}

// FinishBoxUpdate records how an update ended, for the agent to report,
// and forgets the request.
func FinishBoxUpdate(dir string, r nodeproto.UpdateReport) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, BoxUpdateResultFile), append(data, '\n'), 0o600); err != nil {
		return err
	}
	for _, f := range []string{BoxUpdateFile, BoxUpdateTakenFile} {
		if err := os.Remove(filepath.Join(dir, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
