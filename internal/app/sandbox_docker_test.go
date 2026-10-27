package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/sandboxrun/sandboxruntest"
)

// fakeFleet is one node, mini, with the fake Docker Sandboxes of node.
type fakeFleet struct {
	node  *sandboxruntest.Node
	calls atomic.Int32
}

func (f *fakeFleet) SandboxNodes() []sandboxNode {
	online := false
	f.node.Set(func(n *sandboxruntest.Node) { online = !n.Offline })
	return []sandboxNode{{ID: "n1", Name: "mini", Online: online}}
}

func (f *fakeFleet) Call(ctx context.Context, id, method string, params, out any) error {
	f.calls.Add(1)
	return f.node.Call(ctx, method, params, out)
}

// vmOutput is what a Claude run in the VM writes: its reply as
// stream-json.
func vmOutput(reply string) string {
	data, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "result": reply})
	return string(data) + "\n"
}

// dockerHarness is a served harness with brain main, which handles the
// DMs and has sb, on a fake node whose sandboxes answer "hello from the
// VM". A look at the node serves 50 ms.
func dockerHarness(t *testing.T, cfgJSON string, sb *manifest.Sandbox) (*harness, *fakeFleet, string) {
	t.Helper()
	fleet := &fakeFleet{node: &sandboxruntest.Node{Info: sandboxruntest.Ready("claude"), Output: vmOutput("hello from the VM")}}
	optionsOf(t).hooks.sandboxFleet = fleet
	optionsOf(t).waits.sandboxLook = 50 * time.Millisecond
	h := newServedHarness(t, cfgJSON)
	dir := h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}, Sandbox: sb})
	return h, fleet, dir
}

// sentCount is how many messages to harnessDM hold sub.
func sentCount(h *harness, sub string) int {
	n := 0
	for _, s := range h.SentTo(harnessDM) {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

// A node that is offline: runs fall back to the bridge's own sandbox, the
// owner hears it once however many runs fall back, and once when the
// sandboxes are back.
func TestDockerSandboxFallback(t *testing.T) {
	t.Parallel()
	h, fleet, _ := dockerHarness(t, "", &manifest.Sandbox{Runtime: manifest.RuntimeDockerSandbox})
	fleet.node.Set(func(n *sandboxruntest.Node) { n.Offline = true })
	for i, text := range []string{"one", "two"} {
		time.Sleep(60 * time.Millisecond) // the last look is stale
		h.Inject(inbound{JID: harnessDM, Text: text})
		h.WaitFor("the fallback's answer", func() bool { return sentCount(h, "echo:") == i+1 })
	}
	if n := sentCount(h, "Docker Sandboxes are off on mini: mini is offline"); n != 1 {
		t.Errorf("%d outage notices:\n%s", n, strings.Join(h.SentTo(harnessDM), "\n"))
	}
	fleet.node.Set(func(n *sandboxruntest.Node) { n.Offline = false })
	time.Sleep(60 * time.Millisecond)
	h.Inject(inbound{JID: harnessDM, Text: "three"})
	h.WaitFor("the answer from the VM", func() bool { return sentCount(h, "hello from the VM") == 1 })
	if n := sentCount(h, "Docker Sandboxes are back on mini"); n != 1 {
		t.Errorf("%d back notices", n)
	}
	if got := auditRuntimes(t, h); !slices.Equal(got, []string{"bubblewrap", "bubblewrap", "docker-sandbox"}) {
		t.Errorf("audit runtimes %q", got)
	}
}

// Fallback "refuse" refuses with its reason, a sandbox whose setup failed
// refuses, and a marker that is not this core's falls back.
func TestDockerSandboxRefusals(t *testing.T) {
	t.Parallel()
	docker := manifest.RuntimeDockerSandbox
	cases := []struct {
		name        string
		sb          manifest.Sandbox
		set         func(n *sandboxruntest.Node)
		want, audit string
	}{
		{"refuse", manifest.Sandbox{Runtime: docker, Fallback: manifest.FallbackRefuse}, func(n *sandboxruntest.Node) { n.Offline = true }, outboundmsg.SandboxRefusedNotice, "refused"},
		{"setup failed", manifest.Sandbox{Runtime: docker}, func(n *sandboxruntest.Node) { n.EnsureErr = nodeproto.Errorf(nodeproto.CodeFailed, "the mount failed") }, outboundmsg.SandboxSetupFailedNotice, "refused"},
		{"marker mismatch", manifest.Sandbox{Runtime: docker}, func(n *sandboxruntest.Node) {
			n.EnsureErr = nodeproto.Errorf(nodeproto.CodeUnavailable, "the folder's marker is not the core's")
		}, "echo:", "bubblewrap"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, fleet, _ := dockerHarness(t, "", &c.sb)
			fleet.node.Set(c.set)
			h.Inject(inbound{JID: harnessDM, Text: "hi"})
			h.WaitSent(harnessDM, c.want)
			fleet.node.Set(func(n *sandboxruntest.Node) {
				if len(n.Runs) != 0 {
					t.Errorf("%d runs on the node", len(n.Runs))
				}
			})
			h.WaitFor("the run_end line", func() bool { return len(auditRuntimes(t, h)) == 1 })
			if got := auditRuntimes(t, h); got[0] != c.audit {
				t.Errorf("audit runtime %q, want %q", got[0], c.audit)
			}
		})
	}
}

// A run that times out is stopped on the node, inside the VM.
func TestDockerSandboxTimeoutStops(t *testing.T) {
	t.Parallel()
	optionsOf(t).waits.runTimeoutUnit = 300 * time.Millisecond
	h, fleet, _ := dockerHarness(t, `{"run_timeout_minutes": 1}`, &manifest.Sandbox{Runtime: manifest.RuntimeDockerSandbox})
	fleet.node.Set(func(n *sandboxruntest.Node) { n.Hang, n.Output = true, "" })
	h.Inject(inbound{JID: harnessDM, Text: "a long one"})
	h.WaitSent(harnessDM, outboundmsg.ChatErrTimeout)
	fleet.node.Set(func(n *sandboxruntest.Node) {
		if len(n.Runs) != 1 || len(n.Stops) != 1 || n.Stops[0].Run != n.Runs[0].Run {
			t.Errorf("runs %+v, stops %+v", n.Runs, n.Stops)
		}
	})
}

// With the default config nothing changes: the bridge asks no node,
// writes no marker, shows bubblewrap, and its audit lines have no
// runtime.
func TestDockerSandboxDefaultUnchanged(t *testing.T) {
	t.Parallel()
	h, fleet, dir := dockerHarness(t, "", nil)
	h.Inject(inbound{JID: harnessDM, Text: "hi"})
	h.WaitFor("the answer", func() bool { return sentCount(h, "echo:") == 1 })
	if n := fleet.calls.Load(); n != 0 {
		t.Errorf("%d calls to the node", n)
	}
	if _, err := os.Stat(filepath.Join(dir, markerFile)); !os.IsNotExist(err) {
		t.Errorf("a marker was written: %v", err)
	}
	var brains []struct {
		ID      string
		Sandbox struct{ Configured, Effective string }
	}
	if w := h.Do("GET", "/api/brains", nil); json.Unmarshal(w.Body.Bytes(), &brains) != nil || len(brains) != 1 || brains[0].Sandbox.Configured != "bubblewrap" || brains[0].Sandbox.Effective != "bubblewrap" {
		t.Errorf("brains: %+v", brains)
	}
	if got := auditRuntimes(t, h); !slices.Equal(got, []string{""}) {
		t.Errorf("audit runtimes %q", got)
	}
}

// auditRuntimes are the runtime of each run_end line, oldest first.
func auditRuntimes(t *testing.T, h *harness) []string {
	t.Helper()
	var lines []struct{ Kind, Runtime string }
	if w := h.Do("GET", "/api/audit", nil); json.Unmarshal(w.Body.Bytes(), &lines) != nil {
		t.Fatalf("audit: %s", w.Body.String())
	}
	var out []string
	for _, l := range lines {
		if l.Kind == "run_end" {
			out = append(out, l.Runtime)
		}
	}
	slices.Reverse(out)
	return out
}

// A brain set to docker-sandbox runs in its sandbox on the node, end to
// end: ensure with this core's marker, then the run, whose reply reaches
// the chat. The run token is only in token, never in argv or env.
func TestDockerSandboxRun(t *testing.T) {
	t.Parallel()
	h, fleet, dir := dockerHarness(t, "", &manifest.Sandbox{Runtime: manifest.RuntimeDockerSandbox, Egress: []string{"api.github.com"}})
	h.Inject(inbound{JID: harnessDM, Text: "hi there"})
	h.WaitSent(harnessDM, "hello from the VM")
	marker, err := os.ReadFile(filepath.Join(dir, markerFile))
	if err != nil {
		t.Fatal(err)
	}
	fleet.node.Set(func(n *sandboxruntest.Node) {
		if len(n.Ensured) != 1 || len(n.Runs) != 1 {
			t.Fatalf("ensured %d, runs %d", len(n.Ensured), len(n.Runs))
		}
		e, r := n.Ensured[0], n.Runs[0]
		if e.Marker == "" || e.Marker+"\n" != string(marker) || e.Folder != dir || !slices.Equal(e.Egress, []string{"api.github.com"}) || e.APIPort == 0 {
			t.Errorf("ensure: %+v, marker file %q", e, marker)
		}
		if r.Token == "" || r.Argv[0] != "claude" || r.Folder != dir || r.Provider != "claude" || !strings.Contains(r.Stdin, "hi there") || r.APIPort != e.APIPort {
			t.Errorf("run: %+v", r)
		}
		argv := strings.Join(r.Argv, " ")
		if !strings.Contains(argv, `"command":"/opt/fridolin/bin/fridolin"`) || !strings.Contains(argv, `"args":["mcp","--run","`+r.Run+`"]`) {
			t.Errorf("the MCP config does not name the VM's fridolin: %s", argv)
		}
		if strings.Contains(argv, r.Token) {
			t.Error("the run token is in argv")
		}
		for k, v := range r.Env {
			if strings.Contains(v, r.Token) || slices.Contains(nodeproto.SandboxNodeEnv, k) || k == "CODEX_HOME" {
				t.Errorf("env has %s=%s", k, v)
			}
		}
	})
	if got := auditRuntimes(t, h); !slices.Equal(got, []string{"docker-sandbox"}) {
		t.Errorf("audit runtimes %q", got)
	}
}
