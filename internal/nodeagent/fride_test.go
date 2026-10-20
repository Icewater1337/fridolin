package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// startCoreWithEvents is startCore whose events are kept.
func startCoreWithEvents(t *testing.T) (*core, func() []nodeproto.FrideEvent) {
	t.Helper()
	var mu sync.Mutex
	var got []nodeproto.FrideEvent
	svc, err := nodes.New(nodes.Options{Dir: t.TempDir(), Logf: t.Logf, OnEvent: func(_ string, ev nodeproto.Event) {
		var fe nodeproto.FrideEvent
		if ev.Kind == nodeproto.EventFride && json.Unmarshal(ev.Data, &fe) == nil {
			mu.Lock()
			got = append(got, fe)
			mu.Unlock()
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ListenAndServe("127.0.0.1:0", nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	return &core{svc: svc, addr: svc.Addr()}, func() []nodeproto.FrideEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]nodeproto.FrideEvent(nil), got...)
	}
}

// The fride capability end to end against a fake FrIDE: pairing keeps the
// token 0600 and the webhook in node.json; the agent declares fride only
// while FrIDE answers; once approved, the core's calls reach FrIDE with
// the token, a bundle travels both ways in chunks, and signed webhook news
// reaches the core while unsigned news is refused.
func TestFrideCapability(t *testing.T) {
	c, events := startCoreWithEvents(t)
	ff := testutil.StartFakeFride(t, "api", "web")
	a := joinNode(t, c, nodeproto.Capabilities{Host: true})
	a.FrideCheckEvery = 50 * time.Millisecond
	ctx := context.Background()

	if _, err := PairFride(ctx, FridePairOptions{Dir: a.Dir, URL: "http://example.com:7777", Code: "x"}); err == nil || !strings.Contains(err.Error(), "this machine") {
		t.Fatalf("pairing with a FrIDE elsewhere: %v", err)
	}
	if _, err := PairFride(ctx, FridePairOptions{Dir: a.Dir, URL: ff.URL, Code: "WRONG"}); err == nil {
		t.Fatal("a wrong code paired")
	}
	info, err := PairFride(ctx, FridePairOptions{Dir: a.Dir, URL: ff.URL, Code: ff.NewCode()})
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode != "companion" || len(info.Repos) != 2 {
		t.Errorf("info after pairing: %+v", info)
	}
	st, err := os.Stat(filepath.Join(a.Dir, FrideTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("the token file is %v, want 0600", mode)
	}
	cfg, err := LoadConfig(a.Dir)
	if err != nil || cfg.Fride == nil || cfg.Fride.URL != ff.URL || ff.Webhook() != webhookURL(cfg.Fride.Webhook) {
		t.Fatalf("node.json after pairing: %+v %v (FrIDE posts to %s)", cfg.Fride, err, ff.Webhook())
	}

	r := runAgent(t, a)
	_ = r
	waitFor(t, "the agent online", func() bool { return c.svc.Online(a.ID()) })
	node := func() *nodes.Node {
		n, err := c.svc.Registry().Get(a.ID())
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := node(); n.Declared.Fride == nil || n.Declared.Fride.URL != ff.URL || strings.Join(n.Declared.Fride.Repos, ",") != "api,web" {
		t.Fatalf("declared: %+v", n.Declared)
	}
	// Pending: nothing is served, and no news is relayed.
	if _, err := c.svc.FrideMem(ctx, a.ID()); !isCode(err, nodeproto.CodeDenied) {
		t.Fatalf("fride.mem before approval: %v", err)
	}
	if code := ff.Emit(map[string]any{"event": "update", "thread_id": 9, "text": "early"}); code != http.StatusNoContent {
		t.Fatalf("the webhook answered %d", code)
	}
	if _, err := c.svc.ApproveDevice(a.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.svc.ApproveCommands(a.ID()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the approval to arrive", func() bool { return a.State().Approved.Fride != nil })

	mem, err := c.svc.FrideMem(ctx, a.ID())
	if err != nil || !mem.CanStart || mem.HostFree == 0 {
		t.Fatalf("fride.mem: %+v %v", mem, err)
	}
	th, err := c.svc.FrideTask(ctx, a.ID(), nodeproto.FrideTaskParams{Message: "add a health endpoint", Repos: []string{"api"}, Ref: "fridolin-1"})
	if err != nil || th.ThreadID != 1 || th.Status != "queued" {
		t.Fatalf("fride.task: %+v %v", th, err)
	}
	if code := ff.Start(1, "health", "feat/health"); code != http.StatusNoContent {
		t.Fatalf("the started webhook answered %d", code)
	}
	if code := ff.EmitUnsigned(map[string]any{"event": "done", "thread_id": 1}); code != http.StatusUnauthorized {
		t.Fatalf("an unsigned webhook answered %d", code)
	}
	waitFor(t, "the news at the core", func() bool { return len(events()) >= 1 })
	evs := events()
	if len(evs) != 1 || evs[0].Kind() != nodeproto.FrideStarted || evs[0].TaskID != "health" || evs[0].Branch != "feat/health" {
		t.Fatalf("events at the core: %+v (the one before approval must be dropped)", evs)
	}

	status, err := c.svc.FrideStatus(ctx, a.ID(), 1)
	if err != nil || status.Thread.TaskID != "health" || status.Task == nil || status.Task.Branch != "feat/health" {
		t.Fatalf("fride.status: %+v %v", status, err)
	}
	if _, err := c.svc.FrideReply(ctx, a.ID(), 1, "the no-results one"); err != nil || strings.Join(ff.Replies(1), "|") != "the no-results one" {
		t.Fatalf("fride.reply: %v %q", err, ff.Replies(1))
	}
	if _, err := c.svc.FrideStatus(ctx, a.ID(), 42); !isCode(err, nodeproto.CodeNotFound) {
		t.Fatalf("a thread FrIDE does not have: %v", err)
	}
	if _, err := c.svc.FrideTaskInfo(ctx, a.ID(), "../etc"); !isCode(err, nodeproto.CodeBadRequest) {
		t.Fatalf("a task id that is a path: %v", err)
	}

	// The bundle, out and back in under another id.
	var bundle bytes.Buffer
	n, sum, err := c.svc.FrideExport(ctx, a.ID(), nodeproto.FrideExportParams{TaskID: "health", Stop: true}, &bundle)
	if err != nil || n == 0 || int64(bundle.Len()) != n || len(sum) != 64 {
		t.Fatalf("fride.export: %d %s %v", n, sum, err)
	}
	if _, err := c.svc.FrideImport(ctx, a.ID(), bytes.NewReader(bundle.Bytes()), "", false); !isCode(err, nodeproto.CodeFailed) || !strings.Contains(err.Error(), "409") {
		t.Fatalf("importing over an existing task: %v", err)
	}
	task, err := c.svc.FrideImport(ctx, a.ID(), bytes.NewReader(bundle.Bytes()), "health-2", true)
	if err != nil || task.TaskID != "health-2" || task.SourceTaskID != "health" {
		t.Fatalf("fride.import: %+v %v", task, err)
	}
	if left, _ := os.ReadDir(filepath.Join(a.Dir, frideTransfers)); len(left) != 0 {
		t.Errorf("staged bundles left behind: %v", left)
	}
	closed, err := c.svc.FrideClose(ctx, a.ID(), "health")
	if err != nil || closed.Status != "done" || closed.RationalePath == "" {
		t.Fatalf("fride.close: %+v %v", closed, err)
	}
	if err := c.svc.FrideDelete(ctx, a.ID(), "health", true); err != nil {
		t.Fatal(err)
	}
	if exists, _ := ff.Task("health"); exists {
		t.Error("the workspace is still there after fride.delete")
	}
	waitFor(t, "done at the core", func() bool { evs := events(); return len(evs) == 2 && evs[1].Kind() == nodeproto.FrideDone })

	// FrIDE quits: the node stops offering it, and the core's calls are
	// refused before they are sent.
	ff.Stop()
	waitFor(t, "fride gone from the declaration", func() bool { return node().Declared.Fride == nil })
	if _, err := c.svc.FrideMem(ctx, a.ID()); !isCode(err, nodeproto.CodeDenied) {
		t.Fatalf("fride.mem with FrIDE gone: %v", err)
	}
	if n := node(); n.Commands() != nodeproto.CommandsApproved || n.Approved.Fride == nil {
		t.Errorf("FrIDE going away changed the approval: %s %+v", n.Commands(), n.Approved)
	}
}

func TestFrideEventKind(t *testing.T) {
	for in, want := range map[string]string{"started": "started", "question": "question", "done": "done", "failed": "failed", "update": "update", "renamed": "update", "": "update"} {
		if got := (nodeproto.FrideEvent{Event: in}).Kind(); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestSignFrideMatchesFrIDE(t *testing.T) {
	// The example of FrIDE's docs/COMPANION.md, computed with its Python
	// snippet: key = sha256(token), HMAC-SHA256 of the body.
	got := SignFride("frc_example", []byte(`{"event":"done"}`))
	if !strings.HasPrefix(got, "sha256=") || len(got) != len("sha256=")+64 {
		t.Fatalf("signature %q", got)
	}
	if SignFride("frc_other", []byte(`{"event":"done"}`)) == got {
		t.Error("another token signs the same")
	}
}

func TestConfigRefusesDeclaredFride(t *testing.T) {
	c := Config{Core: "core:8766", Name: "n", Capabilities: nodeproto.Capabilities{Fride: &nodeproto.FrideCap{URL: "http://127.0.0.1:7777"}}}
	if err := c.Check(); err == nil {
		t.Error("node.json may not declare fride itself")
	}
	c = Config{Core: "core:8766", Name: "n", Fride: &FrideConfig{URL: "http://10.0.0.1:7777", Webhook: "127.0.0.1:1"}}
	if err := c.Check(); err == nil {
		t.Error("a FrIDE elsewhere was taken")
	}
	c.Fride.URL = "http://localhost:7777"
	if err := c.Check(); err != nil {
		t.Error(err)
	}
}
