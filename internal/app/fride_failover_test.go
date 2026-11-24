package app

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// #761: every brief carries the commit rule; work on a node offline past
// fride.failover_minutes moves to another node, resumes from its pushed
// branch, and the chat hears it once; what it left on the offline node is
// archived and removed there once it is back.
func TestFrideFailover(t *testing.T) {
	t.Parallel()
	// fride.failover_minutes' default, 60, is 600 ms here.
	optionsOf(t).waits.frideRetry = 100 * time.Millisecond
	optionsOf(t).waits.frideFailoverUnit = 10 * time.Millisecond
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	laptopFride, miniFride := testutil.StartFakeFride(t, "api"), testutil.StartFakeFride(t, "api")
	h := newServedHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}, "fride": {"news": "all"}}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	addr := h.StartNodes()
	miniFride.SetCanStart(false)
	laptop, laptopDir, stopLaptop := h.stoppableFrideNode("laptop", addr, laptopFride)
	h.fakeFrideNode("mini", addr, miniFride)

	reply := h.coreSays("add a health endpoint", step("fride_task", `{"message":"Add a health endpoint","repos":["api"]}`))
	if !strings.Contains(reply, "Handed to FrIDE on laptop as FrIDE #1") {
		t.Fatalf("fride_task:\n%s", reply)
	}
	if brief := laptopFride.Message(1); !strings.HasPrefix(brief, "Add a health endpoint") || !strings.Contains(brief, frideCommitRule) || strings.Contains(brief, "continues FrIDE") {
		t.Errorf("the brief:\n%s", brief)
	}
	laptopFride.Start(1, "health", "feat/health")
	h.WaitSent(harnessDM, "🛠️ *FrIDE #1* started on laptop: task health, branch feat/health.")

	// The laptop goes away; nothing else can take the work yet: it waits,
	// said once, and goes once the mini can.
	stopLaptop()
	h.WaitSent(harnessDM, "⏳ *FrIDE #1* left laptop (laptop has been offline for")
	h.WaitSent(harnessDM, "Once one can, it resumes from the last pushed commit of branch feat/health")
	time.Sleep(500 * time.Millisecond)
	if n := countContaining(h.SentTo(harnessDM), "*FrIDE #1* left laptop"); n != 1 {
		t.Errorf("the wait was said %d times", n)
	}
	miniFride.SetCanStart(true)
	h.WaitSent(harnessDM, "🛠️ *FrIDE #1* went to mini now.")
	brief := miniFride.Message(1)
	if !strings.Contains(brief, "Continue on the existing branch feat/health") || !strings.Contains(brief, frideCommitRule) {
		t.Errorf("the resumed brief:\n%s", brief)
	}
	th, err := h.b.store.GetFrideThread(t.Context(), 1)
	if err != nil || th.State != store.FrideSent || th.FrideThread != 1 || th.Branch != "feat/health" || th.Node == laptop {
		t.Errorf("FrIDE #1 after the failover: %+v, %v", th, err)
	}

	// The laptop is back: what #1 left there is archived and removed.
	h.runFakeAgent(laptopDir)
	h.WaitFor("health removed from the laptop", func() bool {
		exists, _ := laptopFride.Task("health")
		return !exists
	})
	checkBundle(t, filepath.Join(h.DataDir(), "vault", "workspaces", "health.fride.tar.gz"))
	h.WaitFor("the orphan forgotten", func() bool {
		o, err := h.b.store.FrideOrphans(t.Context())
		return err == nil && len(o) == 0
	})
}

// #761: a thread whose node is offline closes here at once, and moves with
// !move / fride_move, without waiting for fride.failover_minutes.
func TestFrideCloseAndMoveWhileNodeOffline(t *testing.T) {
	t.Parallel()
	optionsOf(t).waits.frideRetry = 100 * time.Millisecond
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	laptopFride, miniFride := testutil.StartFakeFride(t, "api"), testutil.StartFakeFride(t, "api")
	h := newServedHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}, "fride": {"news": "all", "failover_minutes": -1}}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	addr := h.StartNodes()
	miniFride.SetCanStart(false)
	laptop, laptopDir, stopLaptop := h.stoppableFrideNode("laptop", addr, laptopFride)
	mini, _ := h.fakeFrideNode("mini", addr, miniFride)

	h.coreSays("two things", step("fride_task", `{"message":"Add a health endpoint","repos":["api"]}`), step("fride_task", `{"message":"Write docs","repos":["api"]}`))
	laptopFride.Start(1, "health", "feat/health")
	h.WaitSent(harnessDM, "🛠️ *FrIDE #1* started on laptop: task health, branch feat/health.")
	laptopFride.Start(2, "docs", "feat/docs")
	h.WaitFor("FrIDE #2 on its branch", func() bool {
		th, err := h.b.store.GetFrideThread(t.Context(), 2)
		return err == nil && th.Branch == "feat/docs"
	})
	stopLaptop()
	h.WaitFor("the laptop offline", func() bool { return !h.b.nodes.Online(laptop) })

	reply := h.coreSays("close the docs", step("fride_close", `{"thread":2}`))
	if !strings.Contains(reply, "FrIDE #2 is closed here while laptop is offline. Branch feat/docs keeps what was pushed.") {
		t.Fatalf("fride_close while offline:\n%s", reply)
	}
	reply = h.coreSays("move it nowhere", step("fride_move", `{"thread":1,"node":"nowhere"}`))
	if !strings.Contains(reply, `[core:fride_move failed] no online node offers FrIDE that is or is tagged "nowhere" with api`) {
		t.Fatalf("fride_move to no node while offline:\n%s", reply)
	}
	miniFride.SetCanStart(true)
	reply = h.coreSays("move the health work", step("fride_move", `{"thread":1,"node":"`+mini+`"}`))
	if !strings.Contains(reply, "🔀 *FrIDE #1* moved from laptop to mini (its thread 1 there)") || !strings.Contains(reply, "branch feat/health") {
		t.Fatalf("fride_move while offline:\n%s", reply)
	}
	if brief := miniFride.Message(1); !strings.Contains(brief, "Continue on the existing branch feat/health") {
		t.Errorf("the moved brief:\n%s", brief)
	}

	// Back again: both tasks left there are archived and removed.
	h.runFakeAgent(laptopDir)
	h.WaitFor("both tasks removed from the laptop", func() bool {
		health, _ := laptopFride.Task("health")
		docs, _ := laptopFride.Task("docs")
		return !health && !docs
	})
	checkBundle(t, filepath.Join(h.DataDir(), "vault", "workspaces", "docs.fride.tar.gz"))
}

// #761, the incident: a FrIDE instance whose open thread is on a node that
// went offline holds every new thread of that instance there, so they wait
// for good. The failover moves the thread to the instance on another node,
// and the waiting work follows it.
func TestFrideInstanceFailoverUnblocksTheInstance(t *testing.T) {
	t.Parallel()
	optionsOf(t).waits.frideRetry = 100 * time.Millisecond
	optionsOf(t).waits.frideFailoverUnit = 10 * time.Millisecond
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	// The failover is off until the instance is seen held on icebase.
	h := newServedHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}, "fride": {"failover_minutes": -1, "instances": {"work": {}}}}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	addr := h.StartNodes()
	icebase := h.fakeHostNode("icebase", addr, 32768, 30000, 6144)
	reply := h.coreSays("fix it", step("fride_task", `{"message":"Fix it","instance":"work"}`))
	if !strings.Contains(reply, "FrIDE #1") || !icebase.calledWith(nodeproto.MethodFrideTask, "work") {
		t.Fatalf("fride_task:\n%s", reply)
	}
	icebase.emit(nodeproto.FrideEvent{Instance: "work", Event: "started", ThreadID: 1, TaskID: "fix", Branch: "fix/it", Status: "active"})
	h.WaitFor("FrIDE #1 on its branch", func() bool {
		th, err := h.b.store.GetFrideThread(t.Context(), 1)
		return err == nil && th.Branch == "fix/it"
	})
	mini := h.fakeHostNode("mini", addr, 32768, 30000, 6144)

	// icebase goes away: new work for the instance is held there…
	_ = icebase.conn.CloseNow()
	h.WaitFor("icebase offline", func() bool { return !h.b.nodes.Online(icebase.ID) })
	reply = h.coreSays("and the docs", step("fride_task", `{"message":"Write docs","instance":"work"}`))
	if !strings.Contains(reply, "FrIDE #2 waits") || !strings.Contains(reply, "open threads on icebase") {
		t.Fatalf("fride_task while icebase is offline:\n%s", reply)
	}
	// …until the failover moves #1 off it, and #2 follows.
	if _, err := h.b.cfgs.Update("", func(c *config.File) error {
		c.Fride.FailoverMinutes = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.WaitSent(harnessDM, "🔀 *FrIDE #1* moved from icebase/work to mini/work")
	h.WaitSent(harnessDM, "🛠️ *FrIDE #2* went to mini/work now.")
	for _, id := range []int64{1, 2} {
		if th, err := h.b.store.GetFrideThread(t.Context(), id); err != nil || th.Node != mini.ID || th.State != store.FrideSent {
			t.Errorf("FrIDE #%d: %+v, %v", id, th, err)
		}
	}
	if o, err := h.b.store.FrideOrphans(t.Context()); err != nil || len(o) != 1 || o[0].Node != icebase.ID || o[0].TaskID != "fix" {
		t.Errorf("what #1 left on icebase: %+v, %v", o, err)
	}
}

// The 2026-11-23 incident: icebase came back online, but its sandboxes
// could not run (sbx was not signed in), so the instance stayed held there
// by its open thread and new work waited for good. Work on a node that is
// online but cannot run its instance closes at once, and fails over once
// that lasted fride.failover_minutes, as on an offline node.
func TestFrideInstanceFailoverOffANodeThatCannotRunIt(t *testing.T) {
	t.Parallel()
	optionsOf(t).waits.frideRetry = 100 * time.Millisecond
	optionsOf(t).waits.frideFailoverUnit = 10 * time.Millisecond
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	h := newServedHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}, "fride": {"failover_minutes": -1, "instances": {"work": {}}}}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	addr := h.StartNodes()
	icebase := h.fakeHostNode("icebase", addr, 32768, 30000, 6144)
	h.coreSays("two things", step("fride_task", `{"message":"Fix it","instance":"work"}`), step("fride_task", `{"message":"Look into it","instance":"work"}`))
	icebase.emit(nodeproto.FrideEvent{Instance: "work", Event: "started", ThreadID: 1, TaskID: "fix", Branch: "fix/it", Status: "active"})
	icebase.emit(nodeproto.FrideEvent{Instance: "work", Event: "started", ThreadID: 2, TaskID: "look", Branch: "look/it", Status: "active"})
	h.WaitFor("both threads on their branches", func() bool {
		a, err1 := h.b.store.GetFrideThread(t.Context(), 1)
		b, err2 := h.b.store.GetFrideThread(t.Context(), 2)
		return err1 == nil && err2 == nil && a.Branch == "fix/it" && b.Branch == "look/it"
	})
	mini := h.fakeHostNode("mini", addr, 32768, 30000, 6144)

	// icebase stays online, but its sandboxes break: new work is held there.
	icebase.breakSandboxes("sbx is not signed in to Docker: run sbx login on this machine")
	h.WaitFor("icebase not ready", func() bool {
		n, err := h.b.nodes.Registry().Get(icebase.ID)
		return err == nil && h.b.nodes.Online(icebase.ID) && !n.Effective().FrideHost.Ready
	})
	reply := h.coreSays("and the docs", step("fride_task", `{"message":"Write docs","instance":"work"}`))
	if !strings.Contains(reply, "FrIDE #3 waits") || !strings.Contains(reply, "open threads on icebase, which cannot start instances now: sbx is not signed in") {
		t.Fatalf("fride_task while icebase cannot run the instance:\n%s", reply)
	}

	// A close does not need icebase to run it.
	reply = h.coreSays("close the look", step("fride_close", `{"thread":2}`))
	if !strings.Contains(reply, "FrIDE #2 is closed here while the FrIDE instance work cannot run on icebase (icebase cannot start instances now: sbx is not signed in") || !strings.Contains(reply, "Branch look/it keeps what was pushed.") {
		t.Fatalf("fride_close while icebase cannot run the instance:\n%s", reply)
	}

	// The failover moves #1 off it, and #3 follows.
	if _, err := h.b.cfgs.Update("", func(c *config.File) error {
		c.Fride.FailoverMinutes = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.WaitSent(harnessDM, "🔀 *FrIDE #1* moved from icebase/work to mini/work")
	h.WaitSent(harnessDM, "the FrIDE instance work has not been able to run on icebase")
	h.WaitSent(harnessDM, "🛠️ *FrIDE #3* went to mini/work now.")
	for _, id := range []int64{1, 3} {
		if th, err := h.b.store.GetFrideThread(t.Context(), id); err != nil || th.Node != mini.ID || th.State != store.FrideSent {
			t.Errorf("FrIDE #%d: %+v, %v", id, th, err)
		}
	}
	if o, err := h.b.store.FrideOrphans(t.Context()); err != nil || len(o) != 2 {
		t.Errorf("what #1 and #2 left on icebase: %+v, %v", o, err)
	}
}

func countContaining(texts []string, substr string) int {
	n := 0
	for _, s := range texts {
		if strings.Contains(s, substr) {
			n++
		}
	}
	return n
}

// #772: work that fails over before it has a branch and goes back to the
// node it left gets a FrIDE thread of its own there, not its old one,
// whose task the orphan cleanup then closes and removes.
func TestFrideFailoverBackToItsNodeGetsANewThread(t *testing.T) {
	t.Parallel()
	optionsOf(t).waits.frideRetry = 100 * time.Millisecond
	optionsOf(t).waits.frideFailoverUnit = 10 * time.Millisecond
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	laptopFride := testutil.StartFakeFride(t, "api")
	h := newServedHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}, "fride": {"news": "all"}}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	addr := h.StartNodes()
	laptop, laptopDir, stopLaptop := h.stoppableFrideNode("laptop", addr, laptopFride)

	h.coreSays("add a health endpoint", step("fride_task", `{"message":"Add a health endpoint","repos":["api"]}`))
	laptopFride.Start(1, "health", "")
	h.WaitFor("FrIDE #1 on its task", func() bool {
		th, err := h.b.store.GetFrideThread(t.Context(), 1)
		return err == nil && th.TaskID == "health" && th.Branch == ""
	})

	stopLaptop()
	h.WaitSent(harnessDM, "⏳ *FrIDE #1* left laptop")
	h.runFakeAgent(laptopDir)
	h.WaitFor("FrIDE #1 back on the laptop", func() bool {
		th, err := h.b.store.GetFrideThread(t.Context(), 1)
		return err == nil && th.State == store.FrideSent && th.Node == laptop
	})
	h.WaitFor("the orphan done with", func() bool {
		o, err := h.b.store.FrideOrphans(t.Context())
		return err == nil && len(o) == 0
	})
	th, err := h.b.store.GetFrideThread(t.Context(), 1)
	if err != nil || th.FrideThread == 1 || th.Failovers != 1 {
		t.Fatalf("FrIDE #1 back on the laptop has its old FrIDE thread: %+v, %v", th, err)
	}
	if th.TaskID != "" {
		if exists, _ := laptopFride.Task(th.TaskID); !exists {
			t.Errorf("the live task %s was removed", th.TaskID)
		}
	}
}

// #772: the orphan cleanup never closes or removes work a live thread
// has on that node and instance (its FrIDE thread or task): it drops the
// orphan without touching FrIDE.
func TestFrideOrphanOfLiveWorkIsDroppedNotClosed(t *testing.T) {
	t.Parallel()
	optionsOf(t).waits.frideRetry = 100 * time.Millisecond
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	h := newServedHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}, "fride": {"failover_minutes": -1, "instances": {"work": {}}}}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	addr := h.StartNodes()
	icebase := h.fakeHostNode("icebase", addr, 32768, 30000, 6144)
	h.coreSays("fix it", step("fride_task", `{"message":"Fix it","instance":"work"}`))
	icebase.emit(nodeproto.FrideEvent{Instance: "work", Event: "started", ThreadID: 1, TaskID: "fix", Status: "active"})
	h.WaitFor("FrIDE #1 on its task", func() bool {
		th, err := h.b.store.GetFrideThread(t.Context(), 1)
		return err == nil && th.TaskID == "fix" && th.FrideThread == 1
	})

	// What an earlier failover recorded, now the live work again: by
	// FrIDE's thread, and by task.
	for _, o := range []store.FrideOrphan{
		{Thread: 1, Node: icebase.ID, Instance: "work", FrideThread: 1},
		{Thread: 1, Node: icebase.ID, Instance: "work", FrideThread: 9, TaskID: "fix"},
	} {
		if err := h.b.store.AddFrideOrphan(t.Context(), o); err != nil {
			t.Fatal(err)
		}
	}
	h.WaitFor("the orphans dropped", func() bool {
		o, err := h.b.store.FrideOrphans(t.Context())
		return err == nil && len(o) == 0
	})
	for _, m := range []string{nodeproto.MethodFrideClose, nodeproto.MethodFrideDelete} {
		if icebase.calledWith(m, "work") {
			t.Errorf("the live task got %s", m)
		}
	}
}
