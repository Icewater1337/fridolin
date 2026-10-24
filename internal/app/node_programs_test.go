package app

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/sched"
)

func coreRuleFor(c nodeproto.Capabilities, name string) bool {
	return c.Exec != nil && slices.ContainsFunc(c.Exec.Allow, func(r nodeproto.ExecRule) bool { return r.Core && r.Name == name })
}

// TestCommandAgentAddsItsProgram: a node opts a folder in with
// accept_from_core; approving a command agent whose program is in it adds
// the program to the node's allowlist, with exactly its arguments, and the
// agent runs; a program that escapes the folder by a symlink is refused,
// and the Schedules page says why; removing the agent removes the program,
// but only once no other agent runs it.
func TestCommandAgentAddsItsProgram(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test programs are shell scripts")
	}
	h := newHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	addr := h.StartNodes()
	dir := h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})

	tools, _ := filepath.EvalSymlinks(t.TempDir())
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	daily := filepath.Join(tools, "daily.sh")
	if err := os.WriteFile(daily, []byte("#!/bin/sh\necho \"daily $*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "evil.sh"), []byte("#!/bin/sh\necho evil\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(tools, "escape.sh")
	if err := os.Symlink(filepath.Join(outside, "evil.sh"), escape); err != nil {
		t.Fatal(err)
	}

	w := h.Do("POST", "/api/nodes/join-code", map[string]any{})
	var jc struct{ Code string }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &jc) != nil {
		t.Fatalf("join-code: %d %s", w.Code, w.Body.String())
	}
	stateDir := t.TempDir()
	caps := nodeproto.Capabilities{Exec: &nodeproto.ExecCap{Allow: []nodeproto.ExecRule{}, AcceptFromCore: []string{tools}}}
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
		h.b.programs.wait()
	})
	h.WaitFor("the node online", func() bool { v := h.nodeViews(); return len(v) == 1 && v[0].Online })

	// A command agent before the node's commands are approved: it says
	// the node's folder waits for that.
	writeAgentFile(t, dir, "daily", `{"when": "daily 21:00", "command": {"node": "laptop", "argv": [`+quoteJSON(daily)+`, "--quiet"]}}`)
	if a := h.agent("main", "daily"); a.Program == nil || a.Program.State != ProgramNotAccepted || !strings.Contains(a.Program.Detail, "waits for your approval") {
		t.Fatalf("before the node's approval: %+v", a.Program)
	}
	for _, p := range []string{"/approve", "/approve-commands"} {
		if w := h.Do("POST", "/api/nodes/"+j.ID+p, nil); w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", p, w.Code, w.Body.String())
		}
	}
	h.WaitFor("the commands approved", func() bool { return agent.State().Commands == nodeproto.CommandsApproved })
	a := h.agent("main", "daily")
	if a.Program == nil || a.Program.State != ProgramWaiting || !strings.Contains(a.Program.Detail, "approving the agent adds it") {
		t.Fatalf("a pending agent: %+v", a.Program)
	}
	if w := h.Do("POST", "/api/schedules/main/daily/approve", map[string]any{"schedule": a.Review.Disk}); w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	h.WaitFor("the program on the node", func() bool { return coreRuleFor(agent.State().Approved, daily) })
	h.WaitFor("the program allowed", func() bool {
		p := h.agent("main", "daily").Program
		return p != nil && p.State == ProgramAllowed && p.By == "core"
	})
	if w := h.Do("POST", "/api/schedules/main/daily/run", nil); w.Code != http.StatusOK {
		t.Fatalf("run: %d %s", w.Code, w.Body.String())
	}
	if r := h.waitRunEnded("main", "daily", 1); r.Status != sched.StatusOK || !strings.Contains(r.Result, "daily --quiet") {
		t.Fatalf("the agent's run: %+v", r)
	}
	// Exactly its arguments: nothing else of the program runs.
	if code, _, ecode := h.nodeCall(j.ID, nodeproto.MethodExec, nodeproto.ExecParams{Argv: []string{daily, "--other"}}); code != http.StatusForbidden || ecode != nodeproto.CodeDenied {
		t.Fatalf("other arguments: %d %s", code, ecode)
	}

	// An agent whose program escapes the folder by a symlink.
	writeAgentFile(t, dir, "escape", `{"when": "daily 22:00", "command": {"node": "laptop", "argv": [`+quoteJSON(escape)+`]}}`)
	if w := h.Do("POST", "/api/schedules/main/escape/approve", map[string]any{"schedule": h.agent("main", "escape").Review.Disk}); w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	h.WaitFor("the escape refused", func() bool {
		p := h.agent("main", "escape").Program
		return p != nil && p.State == ProgramRefused && strings.Contains(p.Detail, "in no folder")
	})
	if coreRuleFor(agent.State().Approved, escape) {
		t.Fatal("the escaping program is allowed")
	}
	if w := h.Do("POST", "/api/schedules/main/escape/run", nil); w.Code != http.StatusOK {
		t.Fatalf("run: %d %s", w.Code, w.Body.String())
	}
	if r := h.waitRunEnded("main", "escape", 1); r.Status != sched.StatusFailed {
		t.Fatalf("the escaping program ran: %+v", r)
	}
	var nodesResp struct {
		Nodes []struct {
			Programs *struct {
				Refused []nodeproto.RefusedProgram `json:"refused"`
			} `json:"programs"`
		} `json:"nodes"`
	}
	if w := h.Do("GET", "/api/nodes", nil); json.Unmarshal(w.Body.Bytes(), &nodesResp) != nil || len(nodesResp.Nodes) != 1 || nodesResp.Nodes[0].Programs == nil || len(nodesResp.Nodes[0].Programs.Refused) != 1 {
		t.Fatalf("the Nodes page's programs: %s", w.Body.String())
	}

	// A second agent runs the same program; removing the first keeps it.
	writeAgentFile(t, dir, "daily2", `{"when": "daily 23:00", "command": {"node": "laptop", "argv": [`+quoteJSON(daily)+`, "--quiet"]}}`)
	if w := h.Do("POST", "/api/schedules/main/daily2/approve", map[string]any{"schedule": h.agent("main", "daily2").Review.Disk}); w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	h.b.programs.wait()
	if w := h.Do("DELETE", "/api/schedules/main/daily", nil); w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	h.b.programs.wait()
	if !coreRuleFor(agent.State().Approved, daily) {
		t.Fatal("removing one of two agents removed their program")
	}
	if w := h.Do("DELETE", "/api/schedules/main/daily2", nil); w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	h.WaitFor("the program removed", func() bool { return !coreRuleFor(agent.State().Approved, daily) })
	if code, _, ecode := h.nodeCall(j.ID, nodeproto.MethodExec, nodeproto.ExecParams{Argv: []string{daily, "--quiet"}}); code != http.StatusForbidden || ecode != nodeproto.CodeDenied {
		t.Fatalf("the removed program: %d %s", code, ecode)
	}
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
