package app

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// fakeClaudeScript stands in for `claude -p --output-format stream-json`
// on a workstation node: its first turn asks which colour, a resumed turn
// writes the answer (the prompt's last word) to colour.txt.
const fakeClaudeScript = `#!/bin/sh
input=$(cat)
echo '{"type":"system","subtype":"init","session_id":"s1"}'
case " $* " in
*" --resume "*)
  answer=$(printf '%s\n' "$input" | tail -n 1 | awk '{print $NF}')
  printf '%s' "$answer" > colour.txt
  echo '{"type":"result","subtype":"success","result":"Wrote colour.txt.","session_id":"s1"}'
  ;;
*)
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"Looking around."}]}}'
  echo '{"type":"result","subtype":"success","result":"QUESTION: Which colour?","session_id":"s1"}'
  ;;
esac
`

// workstationNode joins a node named name that offers agent and terminal
// in folder, with the fake claude and /bin/sh and no sandbox, runs it,
// and returns its id and its agent, before anything is approved.
func (h *harness) workstationNode(name, addr, folder string) (string, *nodeagent.Agent) {
	h.t.Helper()
	t := h.t
	w := h.Do("POST", "/api/nodes/join-code", map[string]any{})
	var jc struct{ Code string }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &jc) != nil {
		t.Fatalf("join-code: %d %s", w.Code, w.Body.String())
	}
	dir := t.TempDir()
	caps := nodeproto.Capabilities{Host: true, Agent: &nodeproto.WorkstationCap{Roots: []string{folder}}, Terminal: &nodeproto.WorkstationCap{Roots: []string{folder}}}
	j, err := nodeagent.Join(context.Background(), nodeagent.JoinOptions{Dir: dir, Core: addr, Code: jc.Code, Name: name, Capabilities: caps, AndroidBuild: noAndroidBuild, Version: "test", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(claude, []byte(fakeClaudeScript), 0o700); err != nil {
		t.Fatal(err)
	}
	agent, err := nodeagent.Load(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	agent.Logf, agent.HeartbeatEvery = t.Logf, 100*time.Millisecond
	agent.WorkSandbox = nodeproto.SandboxNone
	agent.AgentPaths = map[string]string{nodeproto.ProviderClaude: claude}
	agent.TerminalShell = []string{"/bin/sh"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = agent.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	h.WaitFor(name+" offering agent and terminal", func() bool {
		n, err := h.b.nodes.Registry().Get(j.ID)
		return err == nil && h.b.nodes.Online(j.ID) && n.Declared.Agent != nil && n.Declared.Terminal != nil
	})
	return j.ID, agent
}

// approveNode approves node id's device and what it offers.
func (h *harness) approveNode(id string) {
	h.t.Helper()
	for _, s := range []string{"approve", "approve-commands"} {
		if w := h.Do("POST", "/api/nodes/"+id+"/"+s, nil); w.Code != http.StatusOK {
			h.t.Fatalf("%s: %d %s", s, w.Code, w.Body.String())
		}
	}
}

func skipWithoutSh(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude is a shell script; nodeagent's tests cover Windows terminals")
	}
}

// The workstation mode end to end (#159): the tools appear only on the
// core brain and only once a node's agent and terminal are approved; an
// agent session asks a question that reaches the chat, gets the answer
// and reports what changed; a terminal runs a command; nothing leaves the
// granted folder; everything is audited; a lockdown refuses starting.
func TestWorkstationThroughNodes(t *testing.T) {
	t.Parallel()
	skipWithoutSh(t)
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	h := newServedHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8767"}}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	h.AddBrain(manifest.Manifest{ID: "family", Name: "Family", Routing: manifest.Routing{Groups: []string{harnessGroup}}})
	addr := h.StartNodes()
	folder, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := h.workstationNode("laptop", addr, folder)

	// Not approved yet: no tool.
	reply := h.coreSays("look", step("node_agent_run", `{"prompt":"x"}`))
	if !strings.Contains(reply, "unknown tool: node_agent_run") {
		t.Fatalf("node_agent_run before approval:\n%s", reply)
	}
	h.approveNode(id)
	h.WaitFor("laptop approved", func() bool { return h.b.work.anyWorkNode("agent") && h.b.work.anyWorkNode("terminal") })

	// A sub-brain never has them.
	setenv(t, "FRIDOLIN_FAKE_MCP", `[{"brain":"family"}]`)
	if reply := h.Answer(inbound{JID: harnessGroup, Sender: "15551234567", Text: "@claude fix the app #mcp"}); strings.Contains(reply, "node_agent") || strings.Contains(reply, "node_terminal") {
		t.Errorf("family has workstation tools:\n%s", reply)
	}

	// An agent session asks, is answered, and reports what changed.
	reply = h.coreSays("pick a colour", step("node_agent_run", `{"prompt":"pick a colour"}`))
	if !strings.Contains(reply, "Started Agent #1 on laptop in "+folder) {
		t.Fatalf("node_agent_run:\n%s", reply)
	}
	h.WaitSent(harnessDM, "❓ *Agent #1* asks (on laptop, in "+folder+"):\n\nWhich colour?")
	reply = h.coreSays("blue", step("node_agent_reply", `{"session":1,"text":"blue"}`), step("node_agent_reply", `{"session":7,"text":"x"}`))
	if !strings.Contains(reply, "Passed on to Agent #1") || !strings.Contains(reply, "there is no Agent #7") {
		t.Fatalf("node_agent_reply:\n%s", reply)
	}
	done := h.WaitSent(harnessDM, "✅ *Agent #1* is done on laptop")
	if !strings.Contains(done, "colour.txt") {
		t.Errorf("the end names no change:\n%s", done)
	}
	if b, err := os.ReadFile(filepath.Join(folder, "colour.txt")); err != nil || strings.TrimSpace(string(b)) != "blue" {
		t.Errorf("colour.txt: %q %v", b, err)
	}
	reply = h.coreSays("status", step("node_agent_status", `{"session":1}`))
	if !strings.Contains(reply, `"state": "done"`) {
		t.Errorf("node_agent_status:\n%s", reply)
	}

	// Nothing outside the granted folder.
	reply = h.coreSays("etc", step("node_terminal_open", `{"folder":"/etc"}`), step("node_agent_run", `{"prompt":"x","folder":"../.."}`))
	if strings.Count(reply, "is not in a folder laptop may use") != 2 {
		t.Fatalf("outside the folder:\n%s", reply)
	}

	// A terminal runs a command; the command line is audited, its secret
	// masked.
	reply = h.coreSays("shell", step("node_terminal_open", `{}`),
		step("node_terminal_send", `{"terminal":2,"text":"echo hi-$((40+2)) # token=abcdefghijklmnopqrstuvwxyz0123456789"}`),
		step("node_terminal_read", `{"terminal":2,"wait_seconds":3}`))
	if !strings.Contains(reply, "Opened terminal #2 on laptop") || !strings.Contains(reply, "hi-42") {
		t.Fatalf("terminal:\n%s", reply)
	}
	reply = h.coreSays("close", step("node_terminal_close", `{"terminal":2}`))
	if !strings.Contains(reply, "Closed terminal #2.") {
		t.Fatalf("node_terminal_close:\n%s", reply)
	}
	dir := filepath.Join(h.DataDir(), audit.DirName)
	cmds, err := audit.Read(dir, audit.Filter{Kind: audit.NodeCommand})
	if err != nil || len(cmds) != 1 || !strings.Contains(cmds[0].Detail, "laptop: echo hi-$((40+2))") || strings.Contains(cmds[0].Detail, "abcdefghijklmnop") {
		t.Errorf("command audit: %+v %v", cmds, err)
	}
	sessions, err := audit.Read(dir, audit.Filter{Kind: audit.NodeSession})
	if err != nil || len(sessions) != 4 {
		t.Errorf("session audit, want agent start and end, terminal open and end: %+v %v", sessions, err)
	}

	// A restart forgets the sessions: housekeeping stops what then runs
	// on the node with nobody following it.
	// From now on, before the housekeeping run below reads it.
	h.b.waits.nodeSessionMinAge = 0
	if reply := h.coreSays("shell", step("node_terminal_open", `{}`)); !strings.Contains(reply, "Opened terminal #3") {
		t.Fatalf("terminal #3:\n%s", reply)
	}
	h.b.work.mu.Lock()
	h.b.work.terms = map[int64]*workTerm{}
	h.b.work.mu.Unlock()
	found, errs := h.b.housekeeping.nodeSessionFindings(context.Background(), false, 0)
	if len(errs) > 0 || len(found) != 1 || found[0].Outcome != "stopped" || !strings.HasPrefix(found[0].Target, "terminal ") {
		t.Fatalf("orphans: %+v %v", found, errs)
	}
	if again, _ := h.b.housekeeping.nodeSessionFindings(context.Background(), false, 0); len(again) != 0 {
		t.Errorf("still orphaned after the stop: %+v", again)
	}

	// A lockdown refuses starting and typing; reading goes on.
	if _, err := h.b.setLockdown(context.Background(), true, "dashboard"); err != nil {
		t.Fatal(err)
	}
	reply = h.coreSays("again", step("node_agent_run", `{"prompt":"x"}`), step("node_terminal_open", `{}`), step("node_agent_status", `{}`))
	if strings.Count(reply, "the bridge is in lockdown") != 2 || strings.Contains(reply, "[core:node_agent_status failed]") {
		t.Fatalf("in a lockdown:\n%s", reply)
	}
}
