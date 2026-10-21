package nodeagent

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
)

// fakeBinary is what a test's "fridolin" of version v holds: the update
// tests never run it, CheckBinary reads it.
func fakeBinary(v string) []byte { return []byte("#!fake fridolin\nversion " + v + "\n" + strings.Repeat("x", 1000)) }

// checkFake is CheckBinary for fake binaries.
func checkFake(_ context.Context, path, version string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, fakeBinary(version)) {
		return fmt.Errorf("%s is not the fake binary of %s", path, version)
	}
	return nil
}

// writeBinaries writes a binaries folder for version with a fake binary
// of this platform, and returns it.
func writeBinaries(t *testing.T, version string, content []byte) string {
	t.Helper()
	dir := t.TempDir()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(content)
	zw.Close()
	file := "fridolin-" + runtime.GOOS + "-" + runtime.GOARCH + ".gz"
	if err := os.WriteFile(filepath.Join(dir, file), gz.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	m := map[string]any{"version": version, "binaries": []map[string]any{{
		"os": runtime.GOOS, "arch": runtime.GOARCH, "file": file, "sha256": hex.EncodeToString(sum[:]), "size": len(content),
	}}}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, nodes.BinariesManifest), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func startCoreWith(t *testing.T, o nodes.Options) *core {
	t.Helper()
	if o.Dir == "" {
		o.Dir = t.TempDir()
	}
	o.Logf = t.Logf
	svc, err := nodes.New(o)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Serve(ln); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	return &core{svc: svc, addr: svc.Addr()}
}

// updNode is a joined node of version, with a fake binary of it, whose
// restarts start a fresh agent of the binary in place: Restart stops the
// running agent and starts the next one with the version the binary
// holds.
type updNode struct {
	t       *testing.T
	c       *core
	dir     string
	exe     string
	mu      sync.Mutex
	cur     *running
	starts  []string
	tweak   func(a *Agent) // applied to each agent before it runs
	stopped bool
}

func newUpdNode(t *testing.T, c *core, version string) *updNode {
	t.Helper()
	dir := t.TempDir()
	no := false
	if _, err := Join(context.Background(), JoinOptions{Dir: dir, Core: c.addr, Code: c.code(t), Name: "upd", Capabilities: nodeproto.Capabilities{Host: true}, AndroidBuild: &no, Version: version, Logf: t.Logf}); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "fridolin")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, fakeBinary(version), 0o755); err != nil {
		t.Fatal(err)
	}
	n := &updNode{t: t, c: c, dir: dir, exe: exe}
	t.Cleanup(func() {
		n.mu.Lock()
		n.stopped = true
		cur := n.cur
		n.mu.Unlock()
		if cur != nil {
			cur.cancel()
			<-cur.done
		}
	})
	return n
}

// installedVersion is the version the fake binary in place holds.
func (n *updNode) installedVersion() string {
	data, err := os.ReadFile(n.exe)
	if err != nil {
		return ""
	}
	_, rest, _ := strings.Cut(string(data), "version ")
	v, _, _ := strings.Cut(rest, "\n")
	return v
}

func (n *updNode) start() *Agent {
	n.t.Helper()
	a, err := Load(n.dir, n.installedVersion())
	if err != nil {
		n.t.Fatal(err)
	}
	a.Logf, a.MinBackoff, a.MaxBackoff, a.HeartbeatEvery = n.t.Logf, 10*time.Millisecond, 100*time.Millisecond, 50*time.Millisecond
	a.Executable, a.CheckBinary = n.exe, checkFake
	a.Restart = func() error { go n.restart(); return nil }
	if n.tweak != nil {
		n.tweak(a)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{agent: a, cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- a.Run(ctx) }()
	n.mu.Lock()
	n.cur = r
	n.starts = append(n.starts, a.Version)
	n.mu.Unlock()
	return a
}

// restart is what the service manager does: the agent ends, the binary
// in place starts.
func (n *updNode) restart() {
	n.mu.Lock()
	cur, stopped := n.cur, n.stopped
	n.mu.Unlock()
	if stopped {
		return
	}
	if cur != nil {
		cur.cancel()
		<-cur.done
	}
	n.start()
}

func (n *updNode) agent() *Agent {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.cur.agent
}

func approveUpd(t *testing.T, c *core, id string) {
	t.Helper()
	if _, err := c.svc.ApproveDevice(id); err != nil {
		t.Fatal(err)
	}
	if _, err := c.svc.ApproveCommands(id); err != nil {
		t.Fatal(err)
	}
}

func nodeOf(t *testing.T, c *core, id string) *nodes.Node {
	t.Helper()
	n, err := c.svc.Registry().Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// An in-process agent of version A is updated to the core's B: the core
// streams the signed binary, the node swaps it in, keeps A as .prev,
// restarts into B, which connects, and the core records it.
func TestUpdateEndToEnd(t *testing.T) {
	const vA, vB = "v1.0.0", "v1.1.0"
	c := startCoreWith(t, nodes.Options{Version: vB, BinariesDir: writeBinaries(t, vB, fakeBinary(vB))})
	n := newUpdNode(t, c, vA)
	a := n.start()
	id := a.ID()
	waitFor(t, "the node to connect", func() bool { return c.svc.Online(id) })
	if CoreKeyFingerprint(n.dir) == "" {
		t.Fatal("joining did not pin the core's update key")
	}

	// Before update is approved, nothing.
	if err := c.svc.Update(context.Background(), id, nodes.UpdateOptions{}); !errors.Is(err, nodes.ErrUpdate) {
		t.Fatalf("an update of a pending node: %v", err)
	}
	approveUpd(t, c, id)
	waitFor(t, "the approval", func() bool { return a.State().Approved.Update != nil })
	views, _ := c.svc.Views()
	if len(views) != 1 || views[0].UpdateTo != vB {
		t.Fatalf("the view offers %+v", views)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := c.svc.Update(ctx, id, nodes.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := n.installedVersion(); got != vB {
		t.Fatalf("the binary in place is %s, want %s", got, vB)
	}
	if prev, _ := os.ReadFile(n.exe + ".prev"); !bytes.Equal(prev, fakeBinary(vA)) {
		t.Fatal("the old binary is not kept as .prev")
	}
	nd := nodeOf(t, c, id)
	if nd.Version != vB || nd.Update == nil || nd.Update.State != nodes.UpdateOK || nd.Update.From != vA || nd.Update.To != vB {
		t.Fatalf("the registry has version %s, update %+v", nd.Version, nd.Update)
	}
	waitFor(t, "update.json to go once reported", func() bool {
		_, err := os.Stat(filepath.Join(n.dir, updateMarkerFile))
		return errors.Is(err, os.ErrNotExist)
	})
	if left, _ := os.ReadDir(filepath.Join(n.dir, updateTransfers)); len(left) != 0 {
		t.Fatalf("the transfer stays: %v", left)
	}
	views, _ = c.svc.Views()
	if views[0].UpdateTo != "" {
		t.Fatalf("an updated node is still offered %s", views[0].UpdateTo)
	}
	if err := c.svc.Update(ctx, id, nodes.UpdateOptions{}); !errors.Is(err, nodes.ErrUpdate) {
		t.Fatalf("updating a node that runs the core's version: %v", err)
	}
}

// A new binary that does not connect within its trial is replaced by the
// old one again, which tells the core why.
func TestUpdateRollsBackWhenTheNewVersionCannotConnect(t *testing.T) {
	const vA, vB = "v1.0.0", "v1.1.0"
	c := startCoreWith(t, nodes.Options{Version: vB, BinariesDir: writeBinaries(t, vB, fakeBinary(vB))})
	n := newUpdNode(t, c, vA)
	// B cannot reach the core.
	n.tweak = func(a *Agent) {
		a.UpdateTrial = 300 * time.Millisecond
		if a.Version == vB {
			a.Config.Core = "127.0.0.1:1"
		}
	}
	a := n.start()
	id := a.ID()
	waitFor(t, "the node to connect", func() bool { return c.svc.Online(id) })
	approveUpd(t, c, id)
	waitFor(t, "the approval", func() bool { return a.State().Approved.Update != nil })

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := c.svc.Update(ctx, id, nodes.UpdateOptions{})
	if err == nil || !strings.Contains(err.Error(), "went back to "+vA) {
		t.Fatalf("an update whose binary cannot connect: %v", err)
	}
	if got := n.installedVersion(); got != vA {
		t.Fatalf("the binary in place is %s, want %s back", got, vA)
	}
	nd := nodeOf(t, c, id)
	if nd.Update == nil || nd.Update.State != nodes.UpdateFailed || nd.Update.FailedVersion != vB {
		t.Fatalf("the registry's update: %+v", nd.Update)
	}
	waitFor(t, "the report to name the reason", func() bool {
		u := nodeOf(t, c, id).Update
		return strings.Contains(u.Error, "did not connect")
	})
	n.mu.Lock()
	starts := strings.Join(n.starts, ",")
	n.mu.Unlock()
	if starts != vA+","+vB+","+vA {
		t.Fatalf("the agents started: %s", starts)
	}
}

// The node checks what it is sent: a signature of another key, a chunk
// changed on the way, a binary other than the one signed.
func TestUpdateApplyChecks(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(t.TempDir(), "fridolin")
	if err := os.WriteFile(exe, fakeBinary("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	if err := writeCoreKey(dir, pub); err != nil {
		t.Fatal(err)
	}
	var restarted atomic.Int32
	a := &Agent{Dir: dir, Version: "v1", Executable: exe, CheckBinary: checkFake, Logf: t.Logf, Restart: func() error { restarted.Add(1); return nil }}
	content := fakeBinary("v2")
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(content)
	zw.Close()
	stage := func(data []byte) (string, string) {
		res, perr := a.updateCall(context.Background(), nodeproto.MethodUpdateWrite, mustJSON(nodeproto.FrideChunkParams{Data: data}))
		if perr != nil {
			t.Fatal(perr)
		}
		sum := sha256.Sum256(data)
		return res.(nodeproto.FrideTransfer).ID, hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(content)
	params := func(id, tsum string, k ed25519.PrivateKey, binSum string) nodeproto.UpdateApplyParams {
		return nodeproto.UpdateApplyParams{ID: id, Version: "v2", OS: runtime.GOOS, Arch: runtime.GOARCH, SHA256: binSum, Size: int64(len(content)),
			TransferSHA256: tsum, Signature: nodeproto.SignUpdate(k, "v2", runtime.GOOS, runtime.GOARCH, binSum)}
	}
	apply := func(p nodeproto.UpdateApplyParams) *nodeproto.Error {
		_, perr := a.updateCall(context.Background(), nodeproto.MethodUpdateApply, mustJSON(p))
		return perr
	}
	good := hex.EncodeToString(sum[:])

	_, other, _ := ed25519.GenerateKey(rand.Reader)
	id, ts := stage(gz.Bytes())
	if perr := apply(params(id, ts, other, good)); perr == nil || perr.Code != nodeproto.CodeDenied {
		t.Fatalf("a signature of another key: %v", perr)
	}
	id, ts = stage(gz.Bytes())
	tampered := bytes.Clone(gz.Bytes())
	tampered[len(tampered)/2] ^= 0xff
	if err := os.WriteFile(filepath.Join(dir, updateTransfers, id+".gz"), tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if perr := apply(params(id, ts, key, good)); perr == nil || !strings.Contains(perr.Message, "changed on the way") {
		t.Fatalf("a chunk changed on the way: %v", perr)
	}
	// Changed before it was sent, with a transfer hash to match: the
	// binary is not the one the core signed.
	id, ts = stage(tampered)
	if perr := apply(params(id, ts, key, good)); perr == nil {
		t.Fatal("a binary other than the one signed was applied")
	}
	// Signed for another platform.
	id, ts = stage(gz.Bytes())
	p := params(id, ts, key, good)
	p.Arch = "other"
	if perr := apply(p); perr == nil || perr.Code != nodeproto.CodeBadRequest {
		t.Fatalf("a binary for another platform: %v", perr)
	}
	if got, _ := os.ReadFile(exe); !bytes.Equal(got, fakeBinary("v1")) || restarted.Load() != 0 {
		t.Fatal("a refused update changed the binary or restarted")
	}
	if left, _ := os.ReadDir(filepath.Join(dir, updateTransfers)); len(left) != 0 {
		t.Fatalf("refused transfers stay: %d", len(left))
	}

	id, ts = stage(gz.Bytes())
	if perr := apply(params(id, ts, key, good)); perr != nil {
		t.Fatal(perr)
	}
	if got, _ := os.ReadFile(exe); !bytes.Equal(got, content) {
		t.Fatal("the good update is not in place")
	}
	waitFor(t, "the restart", func() bool { return restarted.Load() == 1 })
	m, err := a.readMarker()
	if err != nil || m == nil || m.Phase != phaseTrial || m.From != "v1" || m.To != "v2" {
		t.Fatalf("update.json: %+v, %v", m, err)
	}
	// A second one waits for the trial.
	id, ts = stage(gz.Bytes())
	if perr := apply(params(id, ts, key, good)); perr == nil || !strings.Contains(perr.Message, "on trial") {
		t.Fatalf("an update during a trial: %v", perr)
	}
}

// A node that joined before it knew the core's key pins the key of the
// core it is connected to at its first update; a node that pinned
// another refuses the update.
func TestUpdatePinsTheCoreKeyOnce(t *testing.T) {
	const vA, vB = "v1.0.0", "v1.1.0"
	c := startCoreWith(t, nodes.Options{Version: vB, BinariesDir: writeBinaries(t, vB, fakeBinary(vB))})

	n := newUpdNode(t, c, vA)
	if err := os.Remove(filepath.Join(n.dir, CoreKeyFile)); err != nil {
		t.Fatal(err)
	}
	a := n.start()
	waitFor(t, "the node to connect", func() bool { return c.svc.Online(a.ID()) })
	approveUpd(t, c, a.ID())
	waitFor(t, "the approval", func() bool { return a.State().Approved.Update != nil })
	if CoreKeyFingerprint(n.dir) != "" {
		t.Fatal("the key was pinned before the first update")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := c.svc.Update(ctx, a.ID(), nodes.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, want := CoreKeyFingerprint(n.dir), nodeproto.KeyFingerprint(c.svc.CoreKey()); got != want {
		t.Fatalf("pinned %s, the core's key is %s", got, want)
	}

	m := newUpdNode(t, c, vA)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := writeCoreKey(m.dir, other); err != nil {
		t.Fatal(err)
	}
	b := m.start()
	waitFor(t, "the second node to connect", func() bool { return c.svc.Online(b.ID()) })
	approveUpd(t, c, b.ID())
	waitFor(t, "the approval", func() bool { return b.State().Approved.Update != nil })
	err := c.svc.Update(ctx, b.ID(), nodes.UpdateOptions{})
	if err == nil || !strings.Contains(err.Error(), "signature does not verify") {
		t.Fatalf("an update signed by a core other than the pinned one: %v", err)
	}
	if m.installedVersion() != vA {
		t.Fatal("the refused update replaced the binary")
	}
}

// update in node.json false: the node does not offer it, and the core
// cannot update it.
func TestUpdateOptOut(t *testing.T) {
	no := false
	a := &Agent{Config: Config{Core: "x:1", Name: "n", Update: &no}}
	if a.declared().Update != nil {
		t.Fatal("a node with update false offers it")
	}
	a.Config.Update = nil
	if a.declared().Update == nil {
		t.Fatal("a node offers no update by default")
	}
	if err := (Config{Core: "x:1", Name: "n", Capabilities: nodeproto.Capabilities{Update: &nodeproto.UpdateCap{}}}).Check(); err == nil {
		t.Fatal("node.json may not declare update by hand")
	}
}

// Auto updates: on when the node connects, never while it is busy.
func TestUpdateAuto(t *testing.T) {
	const vA, vB = "v1.0.0", "v1.1.0"
	var busy sync.Map
	c := startCoreWith(t, nodes.Options{Version: vB, BinariesDir: writeBinaries(t, vB, fakeBinary(vB)), Busy: func(id string) string {
		if _, ok := busy.Load(id); ok {
			return "a FrIDE task runs there"
		}
		return ""
	}})
	n := newUpdNode(t, c, vA)
	a := n.start()
	id := a.ID()
	waitFor(t, "the node to connect", func() bool { return c.svc.Online(id) })
	approveUpd(t, c, id)
	waitFor(t, "the approval", func() bool { return a.State().Approved.Update != nil })
	busy.Store(id, true)
	if _, err := c.svc.SetUpdateAuto(id, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if n.installedVersion() != vA {
		t.Fatal("a busy node was updated")
	}
	if err := c.svc.Update(context.Background(), id, nodes.UpdateOptions{}); err == nil || !strings.Contains(err.Error(), "FrIDE") {
		t.Fatalf("a manual update of a busy node: %v", err)
	}
	busy.Delete(id)
	// The next connection updates it.
	n.restart()
	waitFor(t, "the auto update", func() bool {
		nd := nodeOf(t, c, id)
		return nd.Version == vB && nd.Update.State == nodes.UpdateOK
	})
	if !nodeOf(t, c, id).Update.Auto {
		t.Fatal("the auto policy was lost")
	}
}

// A new binary that keeps dying before it connects goes back to the old
// one at its fourth start.
func TestUpdateTrialStartsAreCounted(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(t.TempDir(), "fridolin")
	os.WriteFile(exe, fakeBinary("v2"), 0o755)
	os.WriteFile(exe+".prev", fakeBinary("v1"), 0o755)
	restarted := 0
	a := &Agent{Dir: dir, Version: "v2", Executable: exe, Logf: t.Logf, Restart: func() error { restarted++; return nil }, UpdateTrial: time.Hour}
	if err := a.writeMarker(&updateMarker{Phase: phaseTrial, From: "v1", To: "v2", Executable: exe}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= maxTrialStarts+1; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		a.updateStartup(ctx, &wg)
		cancel()
		wg.Wait()
	}
	if got, _ := os.ReadFile(exe); !bytes.Equal(got, fakeBinary("v1")) || restarted != 1 {
		t.Fatalf("after %d starts the binary is %q, restarted %d", maxTrialStarts+1, got[:20], restarted)
	}
	m, _ := a.readMarker()
	if m == nil || m.Phase != phaseDone || m.Result == nil || m.Result.OK || !strings.Contains(m.Result.Error, "started 3 times") {
		t.Fatalf("update.json: %+v", m)
	}
	// The old version, started, reports it.
	a.Version = "v1"
	if r := a.updateConnected(); r == nil || r.OK {
		t.Fatalf("the report: %+v", r)
	}
}

// The swap and its rollback, the way of this OS and the Windows way,
// with a binary that runs while it is swapped: a copy of the test binary
// sleeping. Windows refuses to replace or delete a running .exe, and
// lets it be renamed.
func TestSwapARunningBinary(t *testing.T) {
	src := testExe(t)
	for _, goos := range []string{runtime.GOOS, "windows"} {
		t.Run(goos, func(t *testing.T) {
			dir := t.TempDir()
			exe := filepath.Join(dir, "fridolin")
			if runtime.GOOS == "windows" {
				exe += ".exe"
			}
			if err := copyFile(src, exe); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(exe, "helper-sleep")
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
			old, _ := os.ReadFile(exe)
			newPath := filepath.Join(dir, ".fridolin.new-1")
			if err := os.WriteFile(newPath, []byte("new"), 0o755); err != nil {
				t.Fatal(err)
			}
			// A .prev of an earlier update.
			if err := os.WriteFile(exe+".prev", []byte("older"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := swapBinaryOS(goos, exe, newPath); err != nil {
				t.Fatal(err)
			}
			if got, _ := os.ReadFile(exe); string(got) != "new" {
				t.Fatalf("in place: %q", got)
			}
			if got, _ := os.ReadFile(exe + ".prev"); !bytes.Equal(got, old) {
				t.Fatal(".prev is not the binary that ran")
			}
			if _, err := os.Stat(newPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("the new file stays beside")
			}
			// The rollback, while the old binary still runs (from .prev).
			if err := rollbackBinaryOS(goos, exe); err != nil {
				t.Fatal(err)
			}
			if got, _ := os.ReadFile(exe); !bytes.Equal(got, old) {
				t.Fatal("the rollback did not put the old binary back")
			}
			if goos == "windows" {
				if got, _ := os.ReadFile(exe + ".failed"); string(got) != "new" {
					t.Fatalf(".failed: %q", got)
				}
			}
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			cleanAside(exe)
			if left, _ := filepath.Glob(exe + ".*"); len(left) != 0 && goos == "windows" {
				t.Fatalf("left beside it: %v", left)
			}
		})
	}
}

func TestRollbackWithoutPrev(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "fridolin")
	os.WriteFile(exe, []byte("x"), 0o755)
	if err := Rollback(exe); err == nil {
		t.Fatal("a rollback without .prev")
	}
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
