package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
)

// boxSim is the node container of a box for the move tests (#191 phase
// 4): the agent runs the real BoxCore, with the box's real sealed store
// in its state dir, and boxSim stands in for the binary BoxCore runs
// (`fridolin core …` in the container) and for the core it starts, both
// on a fakeCore. It checks what a real container would get: every
// command on the box's data volume, the host secrets from the sealed
// store and nothing else, the core started as a box runs it.
type boxSim struct {
	t       *testing.T
	core    *fakeCore
	store   hostsecrets.Store
	runner  *nodeagent.BoxCore
	dataDir string
	ready   *httptest.Server
	// probeErr fails the sandbox's probe.
	probeErr error

	mu     sync.Mutex
	calls  []string
	spawns [][]string
	// envs is the environment of every command and every start.
	envs [][]string
	logs []string
}

// newBoxSim makes the agent a's node a box with its core settings.
func newBoxSim(t *testing.T, c *fakeCore, a *nodeagent.Agent) *boxSim {
	key, err := nodeagent.CoreSecretsKey(a.Key)
	if err != nil {
		t.Fatal(err)
	}
	s := &boxSim{t: t, core: c, store: nodeagent.BoxSecretStore(a.Dir, key), dataDir: filepath.Join(t.TempDir(), "data")}
	s.ready = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, _ := c.Ready(r.Context())
		_ = json.NewEncoder(w).Encode(map[string]any{"ready": ok, "whatsapp": "connected", "version": a.Version, "lease": map[string]string{"state": "held"}})
	}))
	t.Cleanup(s.ready.Close)
	logf := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		s.mu.Lock()
		s.logs = append(s.logs, line)
		s.mu.Unlock()
		t.Log(line)
	}
	s.runner = &nodeagent.BoxCore{Exe: "/opt/fridolin/fridolin", StateDir: a.Dir, DataDir: s.dataDir, Secrets: s.store,
		// A secret's variable in the container's own environment never
		// reaches the core: the box's store is where its secrets are.
		Environ:  []string{"PATH=/usr/bin:/bin", "HOME=/var/lib/fridolin", "FRIDOLIN_CONTAINER=1", "CLAUDE_CODE_OAUTH_TOKEN=stale-test-value"},
		ReadyURL: s.ready.URL + "/ready", Exec: s.exec, Spawn: s.spawn, StopWait: 5 * time.Second, RestartWait: time.Hour,
		Probe: func(context.Context) error { return s.probeErr }, Logf: logf}
	a.Config.Box = &nodeagent.BoxConfig{Hostname: nodeagent.BoxHostname(c.name)}
	a.CoreSecrets, a.CoreSecretsPlace, a.CoreSetup = nil, "", nil
	a.CoreRunner = s.runner
	a.Logf = logf
	return s
}

func (s *boxSim) record(args, env []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, strings.Join(args, " "))
	s.envs = append(s.envs, slices.Clone(env))
}

// exec is `fridolin args...` in the box's node container.
func (s *boxSim) exec(ctx context.Context, env []string, stdin []byte, args ...string) ([]byte, error) {
	s.record(args, env)
	cmd := strings.Join(args, " ")
	if rest, ok := strings.CutPrefix(cmd, "core import --latest --rehearsal --data-dir "); ok {
		// The rehearsal's scratch data dir is in the node's volume, never
		// the box's data volume.
		scratch, _, _ := strings.Cut(rest, " ")
		if !strings.HasPrefix(scratch, s.runner.StateDir+string(filepath.Separator)) || !slices.Contains(env, "FRIDOLIN_HOME="+scratch) {
			return nil, fmt.Errorf("the rehearsal ran on %s", scratch)
		}
		_, err := s.core.Rehearse(ctx, "m-20261024-0000-0000")
		return nil, err
	}
	if !slices.Contains(env, "FRIDOLIN_HOME="+s.dataDir) {
		return nil, fmt.Errorf("%s did not run on the box's data volume", cmd)
	}
	out := func(v any, err error) ([]byte, error) {
		if err != nil {
			return nil, err
		}
		b, err := json.Marshal(v)
		return append([]byte("a line of the command's log\n"), b...), err
	}
	switch {
	case cmd == "core status --json":
		return out(s.core.Status(ctx))
	case strings.HasPrefix(cmd, "core export --move ") && strings.HasSuffix(cmd, " --json"):
		return out(s.core.Export(ctx, args[3]))
	case cmd == "core import --latest --force":
		return nil, s.core.Import(ctx)
	case cmd == "core promote":
		return nil, s.core.Promote(ctx)
	case cmd == "core release":
		return nil, s.core.Release(ctx)
	case cmd == "core files export":
		dir, files, err := s.core.ExportFiles(ctx)
		return out(map[string]any{"dir": dir, "files": files}, err)
	case cmd == "core files import":
		var in struct {
			Dir   string            `json:"dir"`
			Files map[string][]byte `json:"files"`
		}
		if err := json.Unmarshal(stdin, &in); err != nil {
			return nil, err
		}
		stored, kept, err := s.core.ImportFiles(ctx, in.Dir, in.Files)
		return out(map[string]any{"stored": stored, "kept": kept}, err)
	}
	return nil, fmt.Errorf("the box has no command %q", cmd)
}

// spawn starts the core in the box.
func (s *boxSim) spawn(env []string, args ...string) (nodeagent.BoxProcess, error) {
	s.record(args, env)
	s.mu.Lock()
	s.spawns = append(s.spawns, slices.Clone(args))
	s.mu.Unlock()
	if !slices.Contains(env, "FRIDOLIN_HOME="+s.dataDir) {
		return nil, errors.New("the core was started on another data dir")
	}
	if err := s.core.Start(context.Background()); err != nil {
		return nil, err
	}
	return &simProc{core: s.core, done: make(chan struct{})}, nil
}

// simProc is the core's process: SIGTERM stops it cleanly, which
// releases the lease.
type simProc struct {
	core *fakeCore
	once sync.Once
	done chan struct{}
}

func (p *simProc) end() {
	p.once.Do(func() {
		_ = p.core.Stop(context.Background())
		close(p.done)
	})
}
func (p *simProc) Signal(os.Signal) error { p.end(); return nil }
func (p *simProc) Kill() error            { p.end(); return nil }
func (p *simProc) Wait() error            { <-p.done; return nil }

func (s *boxSim) role() string {
	s.t.Helper()
	r, err := nodeagent.LoadCoreRole(s.runner.StateDir)
	if err != nil {
		s.t.Fatal(err)
	}
	return r
}

func (s *boxSim) started() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.spawns)
}

// noSecretIn fails when the move's secret is anywhere the owner or a log
// could read it: the box's logs, the files of its state dir (the sealed
// store holds it encrypted), its transfer log.
func (s *boxSim) noSecretIn(secret string) {
	s.t.Helper()
	s.mu.Lock()
	logs := strings.Join(s.logs, "\n")
	s.mu.Unlock()
	if strings.Contains(logs, secret) {
		s.t.Error("the box's log holds a secret's value")
	}
	_ = filepath.WalkDir(s.runner.StateDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if b, _ := os.ReadFile(p); strings.Contains(string(b), secret) {
			s.t.Errorf("%s holds a secret's value in clear", p)
		}
		return nil
	})
}

// envOfLastStart is the environment the core was last started with.
func (s *boxSim) envOfLastStart() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.calls) - 1; i >= 0; i-- {
		if s.calls[i] == strings.Join(nodeagent.BoxCoreArgs, " ") {
			return s.envs[i]
		}
	}
	return nil
}

// TestCoreMoveToABoxAndBack is #191 phase 4 end to end: the core moves
// from a native core host to a box, which imports mini's final export
// into its data volume, takes the lease and starts the core inside its
// node container with the host secrets of its sealed store; then back.
func TestCoreMoveToABoxAndBack(t *testing.T) {
	t.Parallel()
	h := newHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	m := h.moveHostsWith(h.b.nodes.Version(), true, moveBoxes{dst: true})
	box := m.dstBox
	if err := os.WriteFile(filepath.Join(m.srcCore.files, "google_calendar_token_personal.json"), []byte(`{"t":"test-value-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// A new box holds no bucket keys yet: its core cannot be read before
	// the move sends them, which the pre-flight says without refusing.
	m.dstCore.mu.Lock()
	m.dstCore.keyless = true
	m.dstCore.mu.Unlock()
	rep := h.moveCheck(m.src, m.dst)
	if len(rep.Blocking) != 0 || !slices.ContainsFunc(rep.Warnings, func(w string) bool { return strings.Contains(w, "holds no host secrets yet") }) {
		t.Fatalf("the pre-flight of a box without keys: %q, %q", rep.Blocking, rep.Warnings)
	}
	m.dstCore.mu.Lock()
	m.dstCore.keyless = false
	m.dstCore.mu.Unlock()
	rep = h.moveCheck(m.src, m.dst)
	if len(rep.Blocking) != 0 || !rep.Target.Box || !rep.ImagePresent {
		t.Fatalf("the pre-flight of a move to a box: %+v", rep)
	}
	warnings := strings.Join(rep.Warnings, "\n")
	for _, want := range []string{"icebase is a box", "fridolin-box-data", "stays there and runs on its node"} {
		if !strings.Contains(warnings, want) {
			t.Errorf("the pre-flight does not say %q: %q", want, rep.Warnings)
		}
	}
	if box.role() != nodeagent.RoleStandby || len(box.started()) != 0 {
		t.Fatalf("the box before the move: %s, started %q", box.role(), box.started())
	}

	rec, body := h.startMove(m.src, m.dst, 10, 10)
	if rec.State != nodes.MoveDone {
		t.Fatalf("the move to the box ended %s: %s", rec.State, body)
	}
	h.WaitSent(harnessDM, "Running on *icebase* now")
	if running, _, _ := m.srcCore.state(); running {
		t.Error("mini runs the core after the move")
	}
	running, data, _ := m.dstCore.state()
	if !running || data != "mini's data" || !box.runner.Running() || box.role() != nodeagent.RoleCore {
		t.Fatalf("the box: core running %v on %q, process %v, role %s", running, data, box.runner.Running(), box.role())
	}
	if got := box.started(); len(got) != 1 || !slices.Equal(got[0], nodeagent.BoxCoreArgs) {
		t.Errorf("the box started the core with %q", got)
	}
	// The core got the secret the move sent, from the box's sealed store,
	// not the container's own variable.
	env := box.envOfLastStart()
	if !slices.Contains(env, "CLAUDE_CODE_OAUTH_TOKEN="+m.secret) || slices.Contains(env, "CLAUDE_CODE_OAUTH_TOKEN=stale-test-value") {
		t.Errorf("the core's Claude token in the box: %v", slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "CLAUDE_CODE_OAUTH_TOKEN=") }))
	}
	if v, err := box.store.Get(hostsecrets.ClaudeOAuthToken); err != nil || v != m.secret {
		t.Errorf("the box's sealed store: %v", err)
	}
	// The secret file came into the box's home, through core files import.
	if b, err := os.ReadFile(filepath.Join(m.dstCore.files, "google_calendar_token_personal.json")); err != nil || !strings.Contains(string(b), "test-value-1") {
		t.Errorf("the box's calendar token: %q, %v", b, err)
	}
	box.noSecretIn(m.secret)
	if strings.Contains(body, m.secret) || strings.Contains(body, "test-value-1") {
		t.Error("the move's record holds a secret")
	}
	for _, msg := range h.Outbox() {
		if strings.Contains(msg.Text, m.secret) {
			t.Error("a chat message holds a secret's value")
		}
	}
	h.WaitFor("mini's watch to end", func() bool { return h.moveStatus(m.src, rec.ID).Phase == nodeproto.MoveDone })

	// The box reads its own core for the next move.
	if st := h.coreCheck(m.dst); st.Core == nil || !st.Core.HeldBy() || !st.SetupOK {
		t.Fatalf("core.check of the box that is the core: %+v", st)
	}

	// The way back: the box stops its core cleanly, exports, and stands
	// down; mini runs on the box's export.
	h.moveBack(m)
	if box.role() != nodeagent.RoleStandby || box.runner.Running() {
		t.Errorf("the box after the way back: role %s, running %v", box.role(), box.runner.Running())
	}
	box.noSecretIn(m.secret)
}

// TestCoreMoveRollsBackToABox: the box runs the core; the move to mini
// fails because mini's core never gets ready, so mini gives the core up
// and the box takes it back on mini's export, starting the core in its
// container again.
func TestCoreMoveRollsBackToABox(t *testing.T) {
	t.Parallel()
	h := newHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	m := h.moveHostsWith(h.b.nodes.Version(), false, moveBoxes{src: true})
	box := m.srcBox
	if box.role() != nodeagent.RoleCore || !box.runner.Running() {
		t.Fatalf("the box does not run the core: %s", box.role())
	}
	rep := h.moveCheck(m.src, m.dst)
	if len(rep.Blocking) != 0 || !rep.Source.Box || rep.Source.Core == nil || !rep.Source.Core.HeldBy() {
		t.Fatalf("the pre-flight of a move from a box: %+v", rep)
	}
	rec, body := h.startMove(m.src, m.dst, 10, 1)
	if rec.State != nodes.MoveRolledBack {
		t.Fatalf("the move ended %s: %s", rec.State, body)
	}
	h.WaitSent(harnessDM, "The core move to *icebase* failed and the core runs on *mini* again")
	running, data, imported := m.srcCore.state()
	if !running || data != "mini's data" || len(imported) != 1 {
		t.Errorf("the box's core: running %v on %q, imported %q", running, data, imported)
	}
	if box.role() != nodeagent.RoleCore || !box.runner.Running() || len(box.started()) != 2 {
		t.Errorf("the box after the rollback: role %s, running %v, started %d times", box.role(), box.runner.Running(), len(box.started()))
	}
	if st, _ := m.srcCore.Status(context.Background()); !st.HeldBy() {
		t.Errorf("the box does not hold the lease again: %+v", st.Lease)
	}
	if running, _, _ := m.dstCore.state(); running {
		t.Error("icebase still runs the core")
	}
	box.noSecretIn(m.secret)
}

// A box made before it could take the core, and one whose sandbox cannot
// run, are no target: the pre-flight says what to do.
func TestCoreMoveToABoxNeedsItsSettings(t *testing.T) {
	t.Parallel()
	h := newHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	m := h.moveHostsWith(h.b.nodes.Version(), true, moveBoxes{dst: true, noSettings: true})
	rep := h.moveCheck(m.src, m.dst)
	if !slices.ContainsFunc(rep.Blocking, func(b string) bool { return strings.Contains(b, "box-core.yaml") }) {
		t.Errorf("a box without its core settings: %q", rep.Blocking)
	}

	h2 := newHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	m2 := h2.moveHostsWith(h2.b.nodes.Version(), true, moveBoxes{dst: true})
	m2.dstBox.probeErr = errors.New("bwrap: setting up uid map: Permission denied")
	rep = h2.moveCheck(m2.src, m2.dst)
	if !slices.ContainsFunc(rep.Blocking, func(b string) bool {
		return strings.Contains(b, "sandbox") && strings.Contains(b, "FRIDOLIN_BOX_APPARMOR")
	}) {
		t.Errorf("a box whose sandbox cannot run: %q", rep.Blocking)
	}
	w := h2.Do("POST", "/api/nodes/move", map[string]any{"source": m2.src, "target": m2.dst})
	if w.Code != http.StatusConflict {
		t.Errorf("a move to that box: %d %s", w.Code, w.Body.String())
	}
	if len(m2.dstBox.started()) != 0 {
		t.Error("the box started a core")
	}
}

// coreCheck is core.check of node id.
func (h *harness) coreCheck(id string) nodeproto.CoreCheckResult {
	h.t.Helper()
	var res nodeproto.CoreCheckResult
	if err := h.b.nodes.Call(context.Background(), id, nodeproto.MethodCoreCheck, nil, &res); err != nil {
		h.t.Fatal(err)
	}
	return res
}
