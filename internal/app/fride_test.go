package app

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// fakeFrideNode is frideNode with a fake FrIDE, which needs no FrIDE
// checkout: the flow of TestFrideGate where it does not.
func (h *harness) fakeFrideNode(name, addr string, ff *testutil.FakeFride) (string, string) {
	h.t.Helper()
	t := h.t
	w := h.Do("POST", "/api/nodes/join-code", map[string]any{})
	var jc struct{ Code string }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &jc) != nil {
		t.Fatalf("join-code: %d %s", w.Code, w.Body.String())
	}
	dir := t.TempDir()
	j, err := nodeagent.Join(context.Background(), nodeagent.JoinOptions{Dir: dir, Core: addr, Code: jc.Code, Name: name, Capabilities: nodeproto.Capabilities{Host: true}, Version: "test", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nodeagent.PairFride(context.Background(), nodeagent.FridePairOptions{Dir: dir, URL: ff.URL, Code: ff.NewCode()}); err != nil {
		t.Fatal(err)
	}
	agent, err := nodeagent.Load(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	agent.Logf, agent.HeartbeatEvery, agent.FrideCheckEvery = t.Logf, 100*time.Millisecond, 100*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = agent.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	h.WaitFor(name+" offering FrIDE", func() bool {
		n, err := h.b.nodes.Registry().Get(j.ID)
		return err == nil && h.b.nodes.Online(j.ID) && n.Declared.Fride != nil
	})
	for _, s := range []string{"approve", "approve-commands"} {
		if w := h.Do("POST", "/api/nodes/"+j.ID+"/"+s, nil); w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", s, w.Code, w.Body.String())
		}
	}
	h.WaitFor(name+" approved", func() bool { return agent.State().Approved.Fride != nil })
	return j.ID, dir
}

// The core's side of FrIDE against two fake FrIDEs: placement by FrIDE's
// memory check, news relayed to the chat, answers passed on, a move
// through the vault and a close that archives, and the tools only on the
// core brain.
func TestFrideThroughNodes(t *testing.T) {
	saved := frideRetryEvery
	frideRetryEvery = 100 * time.Millisecond
	t.Cleanup(func() { frideRetryEvery = saved })
	t.Setenv("FRIDOLIN_FAKE_MCP", "[]")
	laptopFride, miniFride := testutil.StartFakeFride(t, "api"), testutil.StartFakeFride(t, "api")
	h := newServedHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	h.AddBrain(manifest.Manifest{ID: "family", Name: "Family", Routing: manifest.Routing{Groups: []string{harnessGroup}}})
	addr := h.StartNodes()
	miniFride.SetCanStart(false)
	h.fakeFrideNode("laptop", addr, laptopFride)
	mini, _ := h.fakeFrideNode("mini", addr, miniFride)

	// A sub-brain has no FrIDE tools.
	t.Setenv("FRIDOLIN_FAKE_MCP", `[{"brain":"family"}]`)
	if reply := h.Answer(inbound{JID: harnessGroup, Sender: "15551234567", Text: "@claude fix the app #mcp"}); strings.Contains(reply, "fride_") {
		t.Errorf("family has FrIDE tools:\n%s", reply)
	}

	// The mini may not start a stack, so the work goes to the laptop.
	reply := h.coreSays("add a health endpoint", step("fride_task", `{"message":"Add a health endpoint","repos":["api"]}`))
	if !strings.Contains(reply, "Handed to FrIDE on laptop as FrIDE #1") {
		t.Fatalf("fride_task:\n%s", reply)
	}
	if code := laptopFride.Ask(1, "Which empty state?"); code != http.StatusNoContent {
		t.Fatalf("the webhook answered %d", code)
	}
	h.WaitSent(harnessDM, "❓ *FrIDE #1* asks (on laptop):\n\nWhich empty state?")
	reply = h.coreSays("the no-results one", step("fride_reply", `{"thread":1,"text":"the no-results one"}`), step("fride_reply", `{"thread":9,"text":"x"}`), step("fride_close", `{}`))
	if !strings.Contains(reply, "Passed on to FrIDE #1 on laptop") || !strings.Contains(reply, "[core:fride_reply failed] there is no FrIDE #9") || !strings.Contains(reply, "[core:fride_close failed] thread is missing") {
		t.Fatalf("fride_reply:\n%s", reply)
	}
	if got := laptopFride.Replies(1); len(got) != 1 || got[0] != "the no-results one" {
		t.Errorf("FrIDE got %q", got)
	}
	laptopFride.Start(1, "health", "feat/health")
	h.WaitSent(harnessDM, "🛠️ *FrIDE #1* started on laptop: task health, branch feat/health.")

	// Moving refuses a node without FrIDE, then goes through the vault.
	h.Inject(inbound{JID: harnessDM, Text: "!move 1 nowhere"})
	h.WaitSent(harnessDM, `⚠️ *FrIDE #1* did not move: no online node offers FrIDE that is or is tagged "nowhere"`)
	h.Inject(inbound{JID: harnessDM, Text: "!move"})
	h.WaitSent(harnessDM, "🚚 Use `!move <thread> <node>`")
	h.Inject(inbound{JID: harnessGroup, Sender: "15551234567", Text: "@claude !move 1 mini"})
	h.WaitSent(harnessGroup, "🚚 !move answers only in a direct chat.")
	reply = h.coreSays("move it to the mini", step("fride_move", `{"thread":1,"node":"`+mini+`"}`))
	if !strings.Contains(reply, "🚚 *FrIDE #1* moved from laptop to mini: task health continues there.") {
		t.Fatalf("fride_move:\n%s", reply)
	}
	checkBundle(t, filepath.Join(h.DataDir(), "vault", "workspaces", "moves", "health.bundle"))
	if exists, _ := laptopFride.Task("health"); exists {
		t.Error("health is still on the laptop")
	}
	if exists, status := miniFride.Task("health"); !exists || status != "in_progress" {
		t.Errorf("health on the mini: %v %s", exists, status)
	}
	// A reply to moved work starts a thread on its new node.
	reply = h.coreSays("also add a test", step("fride_reply", `{"thread":1,"text":"also add a test"}`), step("fride_status", `{}`))
	if !strings.Contains(reply, "as a new thread there for task health (it was moved)") || !strings.Contains(reply, `"node": "mini"`) {
		t.Fatalf("a reply after the move:\n%s", reply)
	}

	// Closing archives into the vault, then removes the workspace; done
	// comes with the branch and the rationale.
	reply = h.coreSays("close it", step("fride_close", `{"thread":1}`))
	if !strings.Contains(reply, "Closed task health on mini (branch feat/health). Rationale: /fake/ws/health/docs/rationale/health.md. Its workspace is archived in the vault as vault/workspaces/health.fride.tar.gz") {
		t.Fatalf("fride_close:\n%s", reply)
	}
	h.WaitSent(harnessDM, "✅ *FrIDE #1* is done on mini: task health, branch feat/health.\nRationale: /fake/ws/health/docs/rationale/health.md")
	checkBundle(t, filepath.Join(h.DataDir(), "vault", "workspaces", "health.fride.tar.gz"))
	if exists, _ := miniFride.Task("health"); exists {
		t.Error("health's workspace is still on the mini")
	}

	// Nothing may start: the work waits, and goes once a FrIDE may start a
	// stack again.
	laptopFride.SetCanStart(false)
	reply = h.coreSays("and docs", step("fride_task", `{"message":"Write docs"}`))
	if !strings.Contains(reply, "FrIDE #2 waits") {
		t.Fatalf("fride_task with no room:\n%s", reply)
	}
	h.WaitSent(harnessDM, "⏳ *FrIDE #2* waits: no node with FrIDE can take it now (laptop: not enough memory for another stack; mini: not enough memory for another stack)")
	miniFride.SetCanStart(true)
	h.WaitSent(harnessDM, "🛠️ *FrIDE #2* went to mini now.")

	// Work that waits can be dropped.
	reply = h.coreSays("on the gpu box", step("fride_task", `{"message":"Train","node":"gpu"}`), step("fride_close", `{"thread":3}`))
	if !strings.Contains(reply, "FrIDE #3 no longer waits for a node: dropped.") {
		t.Fatalf("closing waiting work:\n%s", reply)
	}

	// In a lockdown (M5) FrIDE's writing tools and !move are refused;
	// reading its status goes on.
	if _, err := h.b.setLockdown(context.Background(), true, "dashboard"); err != nil {
		t.Fatal(err)
	}
	reply = h.coreSays("and a changelog", step("fride_task", `{"message":"Write a changelog"}`), step("fride_status", `{"thread":2}`))
	if !strings.Contains(reply, "[core:fride_task failed] the bridge is in lockdown") || strings.Contains(reply, "[core:fride_status failed]") {
		t.Fatalf("FrIDE in a lockdown:\n%s", reply)
	}
	h.Inject(inbound{JID: harnessDM, Text: "!move 2 laptop"})
	h.WaitSent(harnessDM, outboundmsg.LockdownRefusedNotice)
}
