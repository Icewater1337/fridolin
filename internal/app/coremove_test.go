package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/coreexport"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/lease"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// fakeCore is a core host's core for the move tests: a data dir with its
// own device id whose "data" is a label, on a folder bucket that two
// fakes share, with the real lease and the real export infos. It is a
// nodeagent.CoreRunner, so both agents run their real halves of a move.
type fakeCore struct {
	name   string
	device string
	st     cloud.ObjectStore
	// ready is whether a started core says it is ready; importErr
	// fails every import.
	ready     bool
	importErr error
	// fresh is a host that never pulled the core's image: its core
	// cannot be read, and the image is not there.
	fresh bool
	// keyless is a host without the bucket's keys: its core cannot be
	// read, though it has the image.
	keyless bool
	// files is its core's credentials folder (sandbox.credentials_dir);
	// filesErr fails reading it.
	files    string
	filesErr error

	mu       sync.Mutex
	running  bool
	data     string
	held     *lease.Lease
	imported []string
	exports  int
}

func newFakeCore(t *testing.T, name string, st cloud.ObjectStore, ready bool) *fakeCore {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return &fakeCore{name: name, device: hex.EncodeToString(b), st: st, ready: ready, files: t.TempDir()}
}

// ExportFiles and ImportFiles stand in for core files export and import
// (secretxfer.ReadFiles and StoreFiles in the container): the .json files
// of the folder, and only those not there yet are written.
func (f *fakeCore) ExportFiles(context.Context) (string, map[string][]byte, error) {
	f.mu.Lock()
	ferr := f.filesErr
	f.mu.Unlock()
	if ferr != nil {
		return "", nil, ferr
	}
	out := map[string][]byte{}
	names, err := filepath.Glob(filepath.Join(f.files, "*.json"))
	for _, p := range names {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", nil, err
		}
		out[filepath.Base(p)] = b
	}
	return "~/.credentials", out, err
}

func (f *fakeCore) ImportFiles(_ context.Context, dir string, files map[string][]byte) (stored, kept []string, err error) {
	if dir != "~/.credentials" {
		return nil, nil, fmt.Errorf("the files came for %q", dir)
	}
	for n, b := range files {
		p := filepath.Join(f.files, n)
		if _, err := os.Stat(p); err == nil {
			kept = append(kept, n)
			continue
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			return stored, kept, err
		}
		stored = append(stored, n)
	}
	return stored, kept, nil
}

const fakeStale = time.Minute

func (f *fakeCore) Status(ctx context.Context) (nodeproto.CoreRuntime, error) {
	out := nodeproto.CoreRuntime{Device: f.device}
	if f.isFresh() {
		return out, errors.New("fridolin core run: exit status 1")
	}
	f.mu.Lock()
	keyless := f.keyless
	f.mu.Unlock()
	if keyless {
		return out, errors.New("fridolin core status: neither FRIDOLIN_CORE_STORE nor B2_VAULT_KEY_ID is set")
	}
	r, ok, err := lease.Read(ctx, f.st)
	if err != nil {
		return out, err
	}
	if ok {
		state := nodeproto.LeaseLive
		switch {
		case r.Released:
			state = nodeproto.LeaseReleased
		case !r.Live(time.Now(), fakeStale):
			state = nodeproto.LeaseStale
		}
		out.Lease = &nodeproto.CoreLease{Device: r.DeviceID, Hostname: r.Hostname, State: state, RenewedAt: r.RenewedAt}
	}
	if info, _, err := coreexport.LatestInfo(ctx, f.st); err == nil {
		out.Latest = &nodeproto.CoreExportInfo{Key: info.Key, Device: info.Device, Move: info.Move, CreatedAt: info.CreatedAt, Bytes: info.Bytes}
	}
	return out, nil
}

func (f *fakeCore) Promote(ctx context.Context) error {
	l, err := lease.Acquire(ctx, lease.Options{Store: f.st, DeviceID: f.device, Hostname: f.name, Version: "test", Renew: 10 * time.Second, Stale: fakeStale, Settle: 10 * time.Millisecond})
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.held = l
	f.mu.Unlock()
	return nil
}

func (f *fakeCore) Start(ctx context.Context) error {
	f.mu.Lock()
	held := f.held != nil
	f.mu.Unlock()
	if !held {
		// A core with the lease on takes it at start.
		if err := f.Promote(ctx); err != nil {
			return err
		}
	}
	f.mu.Lock()
	f.running = true
	f.mu.Unlock()
	return nil
}

func (f *fakeCore) Stop(ctx context.Context) error {
	f.mu.Lock()
	l := f.held
	f.running, f.held = false, nil
	f.mu.Unlock()
	if l != nil {
		return l.Release(ctx) // a clean stop releases the lease
	}
	return nil
}

func (f *fakeCore) Release(ctx context.Context) error {
	_, err := lease.ReleaseDevice(ctx, f.st, f.device, time.Now())
	return err
}

func (f *fakeCore) Export(ctx context.Context, move string) (nodeproto.CoreExportInfo, error) {
	f.mu.Lock()
	f.exports++
	data := f.data
	f.mu.Unlock()
	key := fmt.Sprintf("%s%d.tar.gz.age", coreexport.ExportsPrefix, time.Now().UnixNano())
	if err := f.st.Put(ctx, key, strings.NewReader(data)); err != nil {
		return nodeproto.CoreExportInfo{}, err
	}
	info := coreexport.Info{Key: key, Device: f.device, Move: move, CreatedAt: time.Now().UTC(), Bytes: int64(len(data))}
	b, _ := json.Marshal(info)
	if err := f.st.Put(ctx, coreexport.InfoKey(key), bytes.NewReader(b)); err != nil {
		return nodeproto.CoreExportInfo{}, err
	}
	if err := f.st.Put(ctx, coreexport.LatestKey, strings.NewReader(key+"\n")); err != nil {
		return nodeproto.CoreExportInfo{}, err
	}
	return nodeproto.CoreExportInfo{Key: key, Device: f.device, Move: move, Bytes: info.Bytes}, nil
}

func (f *fakeCore) Import(ctx context.Context) error {
	if f.importErr != nil {
		return f.importErr
	}
	key, err := coreexport.Latest(ctx, f.st)
	if err != nil {
		return err
	}
	rc, err := f.st.Get(ctx, key)
	if err != nil {
		return err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.data = string(data)
	f.imported = append(f.imported, string(data))
	f.mu.Unlock()
	return nil
}

func (f *fakeCore) Ready(context.Context) (bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.running {
		return false, "not running"
	}
	if !f.ready {
		return false, "WhatsApp connecting"
	}
	return true, "WhatsApp connected, lease held"
}

func (f *fakeCore) Rehearse(ctx context.Context, move string) (nodeproto.CoreRehearsal, error) {
	key, err := coreexport.Latest(ctx, f.st)
	if err != nil {
		return nodeproto.CoreRehearsal{}, err
	}
	return nodeproto.CoreRehearsal{Move: move, Export: key, OK: true}, nil
}

func (f *fakeCore) Image(context.Context, string, string, bool) (nodeproto.CoreImageResult, error) {
	if f.isFresh() {
		return nodeproto.CoreImageResult{Note: "pulled before anything stops"}, nil
	}
	return nodeproto.CoreImageResult{Present: true, Note: "the image is here"}, nil
}

func (f *fakeCore) isFresh() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fresh
}

func (f *fakeCore) state() (running bool, data string, imported []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running, f.data, append([]string{}, f.imported...)
}

// moveHosts are two core hosts on a dev-mode core: mini runs the core
// (it holds the lease and exported once), icebase may take it; mini
// pins icebase's device key and both are approved for core-host.
type moveHosts struct {
	src, dst         string
	srcCore, dstCore *fakeCore
	srcAgent         *nodeagent.Agent
	dstAgent         *nodeagent.Agent
	dstStore         *memSecrets
	secret           string
	// srcBox and dstBox are the boxes among them (moveBoxes), nil for a
	// native core host.
	srcBox, dstBox *boxSim
}

// moveBoxes says which of the two core hosts are boxes (corebox_test.go):
// their agents run the real BoxCore, with the box's sealed store, on a
// simulated node container.
type moveBoxes struct {
	src, dst bool
	// noSettings is a box made before its core settings (no BoxCore).
	noSettings bool
}

func (h *harness) moveHosts(version string, dstReady bool) moveHosts {
	return h.moveHostsWith(version, dstReady, moveBoxes{})
}

func (h *harness) moveHostsWith(version string, dstReady bool, boxes moveBoxes) moveHosts {
	h.t.Helper()
	t := h.t
	addr := h.StartNodes()
	h.b.nodes.SetMovePoll(50 * time.Millisecond)
	bucket, err := cloud.NewDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := moveHosts{srcCore: newFakeCore(t, "mini", bucket, true), dstCore: newFakeCore(t, "icebase", bucket, dstReady), secret: "test-oauth-move-value"}
	setup := func(c *fakeCore, box bool, sim **boxSim) func(*nodeagent.Agent) {
		return func(a *nodeagent.Agent) {
			a.Version = version
			a.CoreRunner = c
			a.CoreScan = func() []string { return []string{"Apple-only work stays on this Mac"} }
			a.MovePoll, a.MoveGrace, a.MoveExportWait = 50*time.Millisecond, 300*time.Millisecond, 2*time.Second
			if box {
				*sim = newBoxSim(t, c, a)
				if boxes.noSettings {
					a.CoreRunner = nil
				}
			}
		}
	}
	var dstDir string
	m.dst, dstDir, m.dstStore, m.dstAgent = h.coreHostNodeWith(addr, "icebase", setup(m.dstCore, boxes.dst, &m.dstBox))
	key, err := nodeagent.LoadKey(dstDir)
	if err != nil {
		t.Fatal(err)
	}
	pin := nodeagent.CoreHostPin{ID: m.dst, PublicKey: hex.EncodeToString(key.Public().(ed25519.PublicKey)), Name: "icebase", PinnedAt: time.Now().UTC().Truncate(time.Second)}
	var srcStore hostsecrets.Store
	var srcMem *memSecrets
	m.src, _, srcMem, m.srcAgent = h.coreHostNodeWith(addr, "mini", setup(m.srcCore, boxes.src, &m.srcBox), pin)
	srcStore = srcMem
	if m.srcBox != nil {
		srcStore = m.srcBox.store
	}
	if err := srcStore.Set(hostsecrets.ClaudeOAuthToken, m.secret); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{m.src, m.dst} {
		for _, step := range []string{"approve", "approve-commands"} {
			if w := h.Do("POST", "/api/nodes/"+id+"/"+step, nil); w.Code != http.StatusOK {
				t.Fatalf("%s %s: %d %s", step, id, w.Code, w.Body.String())
			}
		}
	}
	h.WaitFor("both approved for core-host", func() bool {
		return m.srcAgent.State().Approved.CoreHost != nil && m.dstAgent.State().Approved.CoreHost != nil
	})
	// mini runs the core: the lease, and an export from before.
	m.srcCore.data = "mini's data"
	var start nodeagent.CoreRunner = m.srcCore
	if m.srcBox != nil {
		start = m.srcAgent.CoreRunner
	}
	if err := start.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.srcCore.Export(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	return m
}

func (h *harness) moveCheck(src, dst string) nodes.MoveReport {
	h.t.Helper()
	w := h.Do("POST", "/api/nodes/move-check", map[string]any{"source": src, "target": dst})
	var rep nodes.MoveReport
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &rep) != nil {
		h.t.Fatalf("move-check: %d %s", w.Code, w.Body.String())
	}
	return rep
}

// startMove runs POST /api/nodes/move and waits until its record ends.
func (h *harness) startMove(src, dst string, deadline, ready int) (nodes.MoveRecord, string) {
	h.t.Helper()
	w := h.Do("POST", "/api/nodes/move", map[string]any{"source": src, "target": dst, "deadline_seconds": deadline, "ready_within_seconds": ready})
	var rec nodes.MoveRecord
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &rec) != nil {
		h.t.Fatalf("move: %d %s", w.Code, w.Body.String())
	}
	var body string
	testutil.WaitFor(h.t, 30*time.Second, "the move to end", func() bool {
		w := h.Do("GET", "/api/nodes/moves/"+rec.ID, nil)
		body = w.Body.String()
		return w.Code == http.StatusOK && json.Unmarshal(w.Body.Bytes(), &rec) == nil && nodes.MoveFinal(rec.State)
	})
	return rec, body
}

// TestCoreMove is #187's handoff end to end: the pre-flight, then a
// move from mini to icebase that the agents finish through the lease and
// the exports alone; the owner hears of it, the source stays stopped,
// the core runs on icebase on mini's final export. Then the way back,
// the same command the other way round.
func TestCoreMove(t *testing.T) {
	h := newHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	m := h.moveHosts(h.b.nodes.Version(), true)

	// mini's credentials folder: the files a move carries, and one it
	// does not.
	for name, body := range map[string]string{"google_calendar_token_personal.json": `{"t":"test-value-1"}`, "dashboard-users.json": "[]", "restore.sh": "#!/bin/sh\n"} {
		if err := os.WriteFile(filepath.Join(m.srcCore.files, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	rep := h.moveCheck(m.src, m.dst)
	if len(rep.Blocking) != 0 || !rep.Secrets.Pinned || !rep.ImagePresent || rep.Source.Core == nil || !rep.Source.Core.HeldBy() {
		t.Fatalf("the pre-flight: %+v", rep)
	}
	if len(rep.MachineBound) != 1 || !strings.Contains(rep.MachineBound[0], "Apple-only") {
		t.Errorf("machine-bound: %q", rep.MachineBound)
	}

	rec, body := h.startMove(m.src, m.dst, 10, 10)
	if rec.State != nodes.MoveDone {
		t.Fatalf("the move ended %s: %s", rec.State, body)
	}
	if strings.Contains(body, m.secret) {
		t.Error("the move's record holds a secret's value")
	}
	h.WaitSent(harnessDM, "Running on *icebase* now")
	if running, _, _ := m.srcCore.state(); running {
		t.Error("mini runs the core after the move")
	}
	running, data, _ := m.dstCore.state()
	if !running || data != "mini's data" {
		t.Errorf("icebase: running %v on %q", running, data)
	}
	st, _ := m.dstCore.Status(context.Background())
	if !st.HeldBy() {
		t.Errorf("icebase does not hold the lease: %+v", st.Lease)
	}
	// The secret files came along, and only those; the record names them
	// and holds no content.
	if b, err := os.ReadFile(filepath.Join(m.dstCore.files, "google_calendar_token_personal.json")); err != nil || !strings.Contains(string(b), "test-value-1") {
		t.Errorf("icebase's calendar token: %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(m.dstCore.files, "restore.sh")); err == nil {
		t.Error("a file a move does not carry went along")
	}
	if strings.Contains(body, "test-value-1") || !strings.Contains(body, "secret files: 2 stored") {
		t.Errorf("the record of the files: %s", body)
	}
	h.WaitFor("mini's watch to end", func() bool {
		st := h.moveStatus(m.src, rec.ID)
		return st.Phase == nodeproto.MoveDone
	})
	for _, msg := range h.Outbox() {
		if strings.Contains(msg.Text, m.secret) {
			t.Error("a chat message holds a secret's value")
		}
	}

	// A plan is used once: the same id again is refused, by both.
	plan := h.signPlan(nodeproto.MovePlan{Move: rec.ID, Source: m.src, Target: m.dst, SourceDevice: m.srcCore.device, Version: h.b.nodes.Version()})
	if err := h.b.nodes.Call(context.Background(), m.src, nodeproto.MethodCoreRelease, plan, nil); err == nil || !strings.Contains(err.Error(), "seen here before") {
		t.Errorf("a reused plan: %v", err)
	}

	// The way back: icebase must pin mini first, which is a change of
	// its core-host.json the owner approves again. icebase's token was
	// refreshed meanwhile; mini keeps its own folder as it was.
	if err := os.WriteFile(filepath.Join(m.dstCore.files, "google_calendar_token_personal.json"), []byte(`{"t":"refreshed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	back := h.moveBack(m)
	if b, _ := os.ReadFile(filepath.Join(m.srcCore.files, "google_calendar_token_personal.json")); !strings.Contains(string(b), "test-value-1") {
		t.Errorf("the way back replaced mini's token: %s", b)
	}
	if !slices.ContainsFunc(back.Steps, func(st nodeproto.CoreMoveStep) bool {
		return strings.Contains(st.Detail, "secret files: 0 stored, 2 kept")
	}) {
		t.Errorf("the way back's files: %+v", back.Steps)
	}
}

// moveBack pins mini on icebase and moves the core back.
func (h *harness) moveBack(m moveHosts) nodes.MoveRecord {
	t := h.t
	t.Helper()
	h.pinBack(m)
	rec, body := h.startMove(m.dst, m.src, 10, 10)
	if rec.State != nodes.MoveDone {
		t.Fatalf("the way back ended %s: %s", rec.State, body)
	}
	if running, data, _ := m.srcCore.state(); !running || data != "icebase's data" {
		t.Errorf("mini after the way back: running %v on %q", running, data)
	}
	if running, _, _ := m.dstCore.state(); running {
		t.Error("icebase still runs the core")
	}
	return rec
}

// pinBack lets icebase send the secrets back to mini: it pins mini's
// device key, a change of its core-host.json the owner approves again,
// and its core gets data of its own.
func (h *harness) pinBack(m moveHosts) {
	t := h.t
	t.Helper()
	dir := m.dstAgent.Dir
	srcKey, err := nodeagent.LoadKey(m.srcAgent.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := nodeagent.SaveCoreHost(dir, nodeagent.CoreHostConfig{Enabled: true, SendTo: []nodeagent.CoreHostPin{{ID: m.src, PublicKey: hex.EncodeToString(srcKey.Public().(ed25519.PublicKey)), Name: "mini", PinnedAt: time.Now().UTC().Truncate(time.Second)}}}); err != nil {
		t.Fatal(err)
	}
	h.WaitSent(harnessDM, "*icebase* ("+m.dst+") changed its core-host settings")
	if w := h.Do("POST", "/api/nodes/"+m.dst+"/approve-commands", nil); w.Code != http.StatusOK {
		t.Fatalf("approve-commands: %d %s", w.Code, w.Body.String())
	}
	h.WaitFor("icebase approved again", func() bool { return h.coreHostApproved(m.dst) && m.dstAgent.State().Approved.CoreHost != nil })
	m.dstCore.mu.Lock()
	m.dstCore.data = "icebase's data"
	m.dstCore.mu.Unlock()
}

func (h *harness) moveStatus(id, move string) nodeproto.CoreMoveState {
	var st nodeproto.CoreMoveState
	_ = h.b.nodes.Call(context.Background(), id, nodeproto.MethodCoreMoveStatus, nodeproto.CoreMoveStatusParams{Move: move}, &st)
	return st
}

// signPlan signs p with the core's update key, filling what it leaves
// out as a fresh plan has it.
func (h *harness) signPlan(p nodeproto.MovePlan) nodeproto.SignedMovePlan {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.b.cfgs.Boot().Layout().Nodes(), nodes.UpdateKeyFile))
	if err != nil {
		h.t.Fatal(err)
	}
	seed, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		h.t.Fatal(err)
	}
	if p.IssuedAt.IsZero() {
		p.IssuedAt = time.Now().UTC().Truncate(time.Second)
	}
	if p.HandoffDeadline.IsZero() {
		p.HandoffDeadline = p.IssuedAt.Add(nodeproto.DefaultHandoffDeadline)
	}
	if p.ReadyWithin == 0 {
		p.ReadyWithin = 60
	}
	s, err := nodeproto.SignMovePlan(ed25519.NewKeyFromSeed(seed), p)
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

// TestCoreMoveRollsBack: icebase takes over but its core never gets
// ready, so it stops, exports and releases the lease, and mini starts
// again by itself on that export; the owner hears that it rolled back.
// A target that never ran the core's version has not its image, and
// `fridolin core run core status` there needs it: the pre-flight warns
// instead of refusing, as the move pulls the image first (seen on a fresh
// Linux server). A target with the image whose core cannot be read still
// refuses.
func TestCoreMoveCheckFreshTarget(t *testing.T) {
	h := newHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	m := h.moveHosts(h.b.nodes.Version(), true)
	m.dstCore.mu.Lock()
	m.dstCore.fresh = true
	m.dstCore.mu.Unlock()
	rep := h.moveCheck(m.src, m.dst)
	for _, b := range rep.Blocking {
		if strings.Contains(b, "cannot be read") {
			t.Fatalf("a fresh target blocks: %q", rep.Blocking)
		}
	}
	if !slices.ContainsFunc(rep.Warnings, func(w string) bool { return strings.Contains(w, "has not the core's image yet") }) {
		t.Fatalf("no warning about the image: %q", rep.Warnings)
	}
}

// The secret files of a credentials folder that cannot be read keep the
// core where it is: the pre-flight says so, and the transfer, the move's
// first step, sends nothing (round 2 of the live test sent the secrets
// with 0 files).
func TestCoreMoveRefusesUnreadableSecretFiles(t *testing.T) {
	h := newHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	m := h.moveHosts(h.b.nodes.Version(), true)
	m.srcCore.mu.Lock()
	m.srcCore.filesErr = errors.New("the secret files' folder is not a folder under ~")
	m.srcCore.mu.Unlock()
	rep := h.moveCheck(m.src, m.dst)
	if !slices.ContainsFunc(rep.Blocking, func(b string) bool { return strings.Contains(b, "secret files") }) {
		t.Fatalf("the pre-flight does not block: %q", rep.Blocking)
	}
	w := h.Do("POST", "/api/nodes/core-secrets-transfer", map[string]any{"source": m.src, "target": m.dst, "run": true})
	if w.Code == http.StatusOK && strings.Contains(w.Body.String(), `"ran":true`) {
		t.Fatalf("the transfer ran without the files: %s", w.Body.String())
	}
}

func TestCoreMoveRollsBack(t *testing.T) {
	h := newHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	m := h.moveHosts(h.b.nodes.Version(), false)
	rec, body := h.startMove(m.src, m.dst, 10, 1)
	if rec.State != nodes.MoveRolledBack {
		t.Fatalf("the move ended %s: %s", rec.State, body)
	}
	h.WaitSent(harnessDM, "The core move to *icebase* failed and the core runs on *mini* again")
	running, data, imported := m.srcCore.state()
	if !running || data != "mini's data" || len(imported) != 1 {
		t.Errorf("mini: running %v on %q, imported %q", running, data, imported)
	}
	if running, _, _ := m.dstCore.state(); running {
		t.Error("icebase still runs the core")
	}
	if st, _ := m.srcCore.Status(context.Background()); !st.HeldBy() {
		t.Errorf("mini does not hold the lease again: %+v", st.Lease)
	}
	if rec.TargetState == nil || rec.TargetState.Phase != nodeproto.MoveRolledBack || !strings.Contains(rec.TargetState.Detail, "not ready within") {
		t.Errorf("icebase's state: %+v", rec.TargetState)
	}
}

// TestCoreMoveTargetFails: icebase's import fails, so it takes no lease;
// past the deadline and its grace mini starts again on its own data dir.
func TestCoreMoveTargetFails(t *testing.T) {
	h := newHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	m := h.moveHosts(h.b.nodes.Version(), true)
	m.dstCore.importErr = errors.New("the disk is full")
	rec, body := h.startMove(m.src, m.dst, 2, 10)
	if rec.State != nodes.MoveRolledBack || !strings.Contains(rec.Detail, "did not take the core over by the deadline") {
		t.Fatalf("the move ended %s: %s", rec.State, body)
	}
	running, data, imported := m.srcCore.state()
	if !running || data != "mini's data" || len(imported) != 0 {
		t.Errorf("mini: running %v on %q, imported %q", running, data, imported)
	}
	if rec.TargetState == nil || rec.TargetState.Phase != nodeproto.MoveFailed || !strings.Contains(rec.TargetState.Detail, "the disk is full") {
		t.Errorf("icebase's state: %+v", rec.TargetState)
	}
}

// TestCoreMoveRefusesPlans: an agent refuses a plan that does not name
// it, a stale one, one of another version, and one not signed by the
// core's key; and the pre-flight blocks a move to a host of another
// version, which the move then refuses to start.
func TestCoreMoveRefusesPlans(t *testing.T) {
	h := newHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	m := h.moveHosts("v0.0.1-other", true)
	base := nodeproto.MovePlan{Move: "m-20261024-1200-0001", Source: m.src, Target: m.dst, SourceDevice: m.srcCore.device, Version: "v0.0.1-other"}
	call := func(method string, p nodeproto.SignedMovePlan) error {
		return h.b.nodes.Call(context.Background(), map[string]string{nodeproto.MethodCoreRelease: m.src, nodeproto.MethodCoreHandoff: m.dst}[method], method, p, nil)
	}
	foreign := base
	foreign.Move, foreign.Target, foreign.Source = "m-20261024-1200-0002", m.src, m.dst // names mini as the target
	if err := call(nodeproto.MethodCoreRelease, h.signPlan(foreign)); err == nil || !strings.Contains(err.Error(), "not this node") {
		t.Errorf("a plan for another node: %v", err)
	}
	stale := base
	stale.Move, stale.IssuedAt = "m-20261024-1200-0003", time.Now().Add(-20*time.Minute).UTC().Truncate(time.Second)
	stale.HandoffDeadline = time.Now().Add(10 * time.Minute)
	if err := call(nodeproto.MethodCoreHandoff, h.signPlan(stale)); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Errorf("a stale plan: %v", err)
	}
	other := base
	other.Move, other.Version = "m-20261024-1200-0004", "v9.9.9"
	if err := call(nodeproto.MethodCoreHandoff, h.signPlan(other)); err == nil || !strings.Contains(err.Error(), "for version v9.9.9") {
		t.Errorf("a plan of another version: %v", err)
	}
	_, forged, _ := ed25519.GenerateKey(rand.Reader)
	fp, _ := nodeproto.SignMovePlan(forged, nodeproto.MovePlan{Move: "m-20261024-1200-0005", Source: m.src, Target: m.dst, SourceDevice: m.srcCore.device, Version: "v0.0.1-other",
		IssuedAt: time.Now().UTC(), HandoffDeadline: time.Now().Add(time.Minute), ReadyWithin: 60})
	if err := call(nodeproto.MethodCoreRelease, fp); err == nil || !strings.Contains(err.Error(), "not signed by the core's key") {
		t.Errorf("a forged plan: %v", err)
	}
	// Nothing ran: mini still runs the core, icebase did not.
	if running, _, _ := m.srcCore.state(); !running {
		t.Error("a refused plan stopped mini's core")
	}
	if running, _, imported := m.dstCore.state(); running || len(imported) != 0 {
		t.Error("a refused plan started icebase")
	}

	// The core's own pre-flight sees the versions differ.
	rep := h.moveCheck(m.src, m.dst)
	if !strings.Contains(strings.Join(rep.Blocking, "\n"), "runs version v0.0.1-other and the core") {
		t.Errorf("the pre-flight: %q", rep.Blocking)
	}
	w := h.Do("POST", "/api/nodes/move", map[string]any{"source": m.src, "target": m.dst})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "cannot move") {
		t.Errorf("a move across versions: %d %s", w.Code, w.Body.String())
	}
	// A move's steps do not go through the generic call.
	if code, _, _ := h.nodeCall(m.src, nodeproto.MethodCoreRelease, map[string]any{}); code != http.StatusBadRequest {
		t.Errorf("core.release through /call: %d", code)
	}
}
