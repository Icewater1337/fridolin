package app

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// StartNodes opens the bridge's node port on a loopback port of its own,
// as Start does on nodes.listen, and returns its address. Like the rest
// of the harness it reaches inside only for what Start would do.
func (h *harness) StartNodes() string {
	h.t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.b.nodes.Serve(ln); err != nil {
		h.t.Fatal(err)
	}
	return h.b.nodes.Addr()
}

// nodeCall is POST /api/nodes/{id}/call.
func (h *harness) nodeCall(id, method string, params any) (int, json.RawMessage, string) {
	h.t.Helper()
	w := h.Do("POST", "/api/nodes/"+id+"/call", map[string]any{"method": method, "params": params})
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
		Code   string          `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		h.t.Fatalf("%s: %d %s", method, w.Code, w.Body.String())
	}
	return w.Code, resp.Result, resp.Code
}

func (h *harness) nodeViews() []struct {
	ID, Device, Commands string
	Online               bool
} {
	h.t.Helper()
	var resp struct {
		Enabled bool `json:"enabled"`
		Nodes   []struct {
			ID, Device, Commands string
			Online               bool
		} `json:"nodes"`
	}
	w := h.Do("GET", "/api/nodes", nil)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &resp) != nil || !resp.Enabled {
		h.t.Fatalf("GET /api/nodes: %d %s", w.Code, w.Body.String())
	}
	return resp.Nodes
}

// TestNodesGate is M2's gate end to end, on a dev-mode bridge with nodes
// on and an agent in this process: a node joins with a code, its device
// and then its commands are approved, the core runs an allowlisted
// command on it and reads a granted folder, is refused a command the
// allowlist lacks and a path outside the root, and revoking the node
// disconnects it and keeps it out.
func TestNodesGate(t *testing.T) {
	h := newHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	addr := h.StartNodes()

	// The core makes a join code and the line to paste.
	w := h.Do("POST", "/api/nodes/join-code", map[string]any{})
	var jc struct{ Code, Command string }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &jc) != nil {
		t.Fatalf("join-code: %d %s", w.Code, w.Body.String())
	}
	_, port, _ := net.SplitHostPort(addr)
	if want := "fridolin node join " + jc.Code + " --core 127.0.0.1:" + port; jc.Command != want {
		t.Errorf("join command %q, want %q", jc.Command, want)
	}

	// The node: an allowlist of the test binary asked for its version (it
	// answers as the fake Claude CLI), and one granted folder.
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.md"), []byte("# granted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "private.md"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	caps := nodeproto.Capabilities{
		Exec: &nodeproto.ExecCap{Allow: []nodeproto.ExecRule{{Name: exe, Args: []string{"--version"}}}},
		FS:   &nodeproto.FSCap{Roots: []nodeproto.FSRoot{{Path: root}}},
		Host: true,
	}
	j, err := nodeagent.Join(context.Background(), nodeagent.JoinOptions{Dir: stateDir, Core: addr, Code: jc.Code, Name: "laptop", Capabilities: caps, AndroidBuild: noAndroidBuild, Version: "test", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := nodeagent.Load(stateDir, "test")
	if err != nil {
		t.Fatal(err)
	}
	agent.Logf, agent.HeartbeatEvery = t.Logf, 100*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agent.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("the agent did not stop")
		}
	})
	h.WaitFor("the node online and pending", func() bool {
		v := h.nodeViews()
		return len(v) == 1 && v[0].ID == j.ID && v[0].Online && v[0].Device == nodeproto.DevicePending
	})
	if code, _, ecode := h.nodeCall(j.ID, nodeproto.MethodHostInfo, nil); code != http.StatusForbidden || ecode != nodeproto.CodeDenied {
		t.Fatalf("host.info on a pending node: %d %s", code, ecode)
	}

	// Stage one: the device. Its commands still wait.
	if w := h.Do("POST", "/api/nodes/"+j.ID+"/approve", nil); w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	if code, _, _ := h.nodeCall(j.ID, nodeproto.MethodExec, nodeproto.ExecParams{Argv: []string{exe, "--version"}}); code != http.StatusForbidden {
		t.Fatalf("exec before the commands are approved: %d", code)
	}
	// Stage two: what it offers.
	if w := h.Do("POST", "/api/nodes/"+j.ID+"/approve-commands", nil); w.Code != http.StatusOK {
		t.Fatalf("approve-commands: %d %s", w.Code, w.Body.String())
	}
	h.WaitFor("the agent to hear of the approval", func() bool { return agent.State().Commands == nodeproto.CommandsApproved })

	code, raw, _ := h.nodeCall(j.ID, nodeproto.MethodExec, nodeproto.ExecParams{Argv: []string{exe, "--version"}})
	var res nodeproto.ExecResult
	if code != http.StatusOK || json.Unmarshal(raw, &res) != nil || res.ExitCode != 0 || !strings.Contains(res.Stdout, claudecli.TestedClaudeVersion) {
		t.Fatalf("the allowlisted command: %d %s", code, raw)
	}
	code, raw, _ = h.nodeCall(j.ID, nodeproto.MethodFSList, nodeproto.PathParams{Path: root})
	if code != http.StatusOK || !strings.Contains(string(raw), `"report.md"`) {
		t.Fatalf("listing the granted folder: %d %s", code, raw)
	}
	code, raw, _ = h.nodeCall(j.ID, nodeproto.MethodFSRead, nodeproto.FSReadParams{Path: filepath.Join(root, "report.md")})
	var rd nodeproto.FSReadResult
	if code != http.StatusOK || json.Unmarshal(raw, &rd) != nil || string(rd.Data) != "# granted\n" {
		t.Fatalf("reading the granted folder: %d %s", code, raw)
	}
	for _, argv := range [][]string{{"sh", "-c", "id"}, {exe, "--evil"}, {"uname", "-a"}} {
		if code, _, ecode := h.nodeCall(j.ID, nodeproto.MethodExec, nodeproto.ExecParams{Argv: argv}); code != http.StatusForbidden || ecode != nodeproto.CodeDenied {
			t.Errorf("exec %q: %d %s, want 403 denied", argv, code, ecode)
		}
	}
	if code, _, ecode := h.nodeCall(j.ID, nodeproto.MethodFSRead, nodeproto.FSReadParams{Path: filepath.Join(outside, "private.md")}); code != http.StatusForbidden || ecode != nodeproto.CodeOutsideRoot {
		t.Errorf("reading outside the root: %d %s", code, ecode)
	}
	if code, _, ecode := h.nodeCall(j.ID, nodeproto.MethodFSWrite, nodeproto.FSWriteParams{Path: filepath.Join(root, "x"), Data: []byte("x")}); code != http.StatusForbidden || ecode != nodeproto.CodeDenied {
		t.Errorf("writing a read-only root: %d %s", code, ecode)
	}

	// Tags, and !fleet in the owner's DM.
	if w := h.Do("PUT", "/api/nodes/"+j.ID+"/tags", map[string]any{"tags": []string{"windows", "gpu"}}); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"gpu"`) {
		t.Fatalf("tags: %d %s", w.Code, w.Body.String())
	}
	h.Inject(inbound{JID: harnessDM, Text: "!fleet"})
	fleet := h.WaitSent(harnessDM, "*Fleet*")
	for _, want := range []string{"*laptop*", "online", "_ref " + j.ID, "gpu, windows", "can: run these programs there when asked: "} {
		if !strings.Contains(fleet, want) {
			t.Errorf("!fleet lacks %q:\n%s", want, fleet)
		}
	}

	// Revoked: off at once, and kept out.
	if w := h.Do("POST", "/api/nodes/"+j.ID+"/revoke", nil); w.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
	select {
	case err := <-done:
		if ce, ok := errors.AsType[*nodeagent.CloseError](err); !ok || ce.Reason != nodeproto.CloseRevoked {
			t.Fatalf("the agent ended with %v, want revoked", err)
		}
		done <- err
	case <-time.After(time.Minute):
		t.Fatal("the revoked agent is still connected after a minute")
	}
	if v := h.nodeViews(); len(v) != 1 || v[0].Online || v[0].Device != nodeproto.DeviceRevoked {
		t.Fatalf("after the revocation: %+v", v)
	}
	again, err := nodeagent.Load(stateDir, "test")
	if err != nil {
		t.Fatal(err)
	}
	again.Logf = t.Logf
	if err := again.Run(context.Background()); err == nil {
		t.Fatal("the revoked node reconnected")
	} else if ce, ok := errors.AsType[*nodeagent.CloseError](err); !ok || ce.Reason != nodeproto.CloseRevoked {
		t.Fatalf("reconnecting: %v", err)
	}
	if code, _, _ := h.nodeCall(j.ID, nodeproto.MethodExec, nodeproto.ExecParams{Argv: []string{exe, "--version"}}); code != http.StatusForbidden {
		t.Errorf("exec on the revoked node: %d", code)
	}
}

// With nodes off the registry still answers, and no join code is made.
func TestNodesOff(t *testing.T) {
	h := newHarness(t, "")
	if v := h.Do("GET", "/api/nodes", nil); v.Code != http.StatusOK || !strings.Contains(v.Body.String(), `"enabled":false`) {
		t.Fatalf("GET /api/nodes: %d %s", v.Code, v.Body.String())
	}
	if w := h.Do("POST", "/api/nodes/join-code", map[string]any{}); w.Code != http.StatusConflict {
		t.Fatalf("join-code with nodes off: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("POST", "/api/nodes/nope/approve", nil); w.Code != http.StatusNotFound {
		t.Fatalf("approving an unknown node: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("PUT", "/api/nodes/nope/tags", map[string]any{"tags": []string{"Bad Tag"}}); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a bad tag: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("POST", "/api/nodes/nope/call", map[string]any{"method": "shell"}); w.Code != http.StatusBadRequest {
		t.Fatalf("an unknown method: %d %s", w.Code, w.Body.String())
	}
	h.Inject(inbound{JID: harnessDM, Text: "!fleet"})
	h.WaitSent(harnessDM, "Nodes are off")
}

func TestNodeCoreAddress(t *testing.T) {
	for _, c := range []struct {
		listen  string
		tailnet config.TailnetConfig
		dnsName string
		want    string
	}{
		{":8766", config.TailnetConfig{}, "", "mini:8766"},
		{"0.0.0.0:8766", config.TailnetConfig{}, "", "mini:8766"},
		{"[::]:8766", config.TailnetConfig{}, "", "mini:8766"},
		{"100.64.0.1:9000", config.TailnetConfig{}, "", "100.64.0.1:9000"},
		{":8766", config.TailnetConfig{Enabled: true, Hostname: "fridolin-core"}, "", "fridolin-core:8766"},
		{":8766", config.TailnetConfig{Enabled: true, Hostname: "fridolin-core"}, "fridolin-core.example.ts.net.", "fridolin-core.example.ts.net:8766"},
		{":8766", config.TailnetConfig{}, "fridolin-core.example.ts.net", "mini:8766"},
	} {
		if got := nodeCoreAddress(c.listen, c.tailnet, c.dnsName, "mini"); got != c.want {
			t.Errorf("%s %+v: %s, want %s", c.listen, c.tailnet, got, c.want)
		}
	}
}
