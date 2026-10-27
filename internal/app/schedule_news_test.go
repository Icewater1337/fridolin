package app

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/sched"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// runAgentNow starts brain's agent name by hand and waits for its nth
// run to end.
func (h *harness) runAgentNow(brain, name string, n int) sched.Run {
	h.t.Helper()
	if w := h.Do("POST", "/api/schedules/"+brain+"/"+name+"/run", nil); w.Code != http.StatusOK {
		h.t.Fatalf("run %s now: %d %s", name, w.Code, w.Body.String())
	}
	return h.waitRunEnded(brain, name, n)
}

// A scheduled agent's FrIDE work keeps its news to the agent: nothing of
// it reaches WhatsApp; a question starts a run of the agent with FrIDE's
// text, which answers it itself, and news that arrives while a run of the
// agent goes on waits for one more. A workstation session still needs a
// chat that hears it, which a silent agent has not.
func TestScheduledAgentsHandWorkToFride(t *testing.T) {
	t.Parallel()
	optionsOf(t).waits.frideAgentGap = 2 * time.Second
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	prompts := filepath.Join(t.TempDir(), "prompts.log")
	setenv(t, "FRIDOLIN_FAKE_PROMPT_LOG", prompts)
	ff := testutil.StartFakeFride(t, "api")
	h := newServedHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	addr := h.StartNodes()
	h.fakeFrideNode("laptop", addr, ff)

	for name, report := range map[string]string{"fix-bugs": "", "quiet": manifest.ReportSilent} {
		if w := h.Do("PUT", "/api/schedules/core/"+name, map[string]any{"when": "daily 18:00", "prompt": "Fix the bugs of " + name + " " + testutil.FakeMCPTrigger, "report": report}); w.Code != http.StatusOK {
			t.Fatalf("PUT %s: %d %s", name, w.Code, w.Body.String())
		}
	}
	h.WaitFor("the agents", func() bool { return len(h.schedules().Agents) == 2 })

	setenv(t, "FRIDOLIN_FAKE_MCP", `[{"brain":"core","tool":"fride_task","args":{"message":"Fix GitHub issue #12","repos":["api"]}}]`)
	r := h.runAgentNow("core", "fix-bugs", 1)
	if r.Status != sched.StatusOK || !strings.Contains(r.Result, "Handed to FrIDE on laptop as FrIDE #1") || !strings.Contains(r.Result, "Its news does not go to WhatsApp") {
		t.Fatalf("fride_task from a scheduled run: %+v", r)
	}
	dm := len(h.SentTo(harnessDM))

	// FrIDE asks: a run of the agent answers, and the DM hears nothing.
	setenv(t, "FRIDOLIN_FAKE_MCP", `[{"brain":"core","tool":"fride_reply","args":{"thread":1,"text":"the small one"}}]`)
	if code := ff.Ask(1, "Which fix?"); code != http.StatusNoContent {
		t.Fatalf("the webhook answered %d", code)
	}
	r = h.waitRunEnded("core", "fix-bugs", 2)
	if r.Trigger != sched.TriggerFride || r.Status != sched.StatusOK {
		t.Fatalf("the run for FrIDE's question: %+v", r)
	}
	if got := ff.Replies(1); len(got) != 1 || got[0] != "the small one" {
		t.Errorf("FrIDE got %q", got)
	}
	data, _ := os.ReadFile(prompts)
	for _, want := range []string{"FrIDE #1 on laptop: question", "Answer it with fride_reply, thread 1", "The work you handed it: Fix GitHub issue #12", "Which fix?", "Nobody answers FrIDE in WhatsApp"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the run's prompt lacks %q:\n%s", want, data)
		}
	}
	// Progress and a stall notice start no run; work done does, once
	// frideAgentGap has passed since the last.
	ff.Start(1, "fix-12", "fix/12")
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	ff.Emit(map[string]any{"event": "question", "thread_id": 1, "text": "Agent fix-12 has not moved for 2h. Should I nudge it?", "status": "needs_reply"})
	ff.Emit(map[string]any{"event": "done", "thread_id": 1, "task_id": "fix-12", "text": "PR merged", "status": "done"})
	r3 := h.waitRunEnded("core", "fix-bugs", 3)
	if r3.Trigger != sched.TriggerFride || r3.Started.Sub(r.Started) < 2*time.Second {
		t.Fatalf("the run for FrIDE's done: %+v, %s after the last", r3, r3.Started.Sub(r.Started))
	}
	if runs := h.agentRuns("core", "fix-bugs"); len(runs) != 3 {
		t.Errorf("%d runs; progress started one", len(runs))
	}
	for _, m := range h.SentTo(harnessDM)[dm:] {
		// The fake agent's own report echoes its prompt; FrIDE's messages
		// name the thread in bold.
		if strings.Contains(m, "*FrIDE #") {
			t.Errorf("FrIDE's news reached the DM: %q", m)
		}
	}
	// The dashboard keeps it all.
	w := h.Do("GET", "/api/fride", nil)
	if !strings.Contains(w.Body.String(), "Which fix?") || !strings.Contains(w.Body.String(), `"chat": "schedule:fix-bugs"`) && !strings.Contains(w.Body.String(), `"chat":"schedule:fix-bugs"`) {
		t.Errorf("GET /api/fride: %s", w.Body.String())
	}

	// Silent: its FrIDE work is its own too, but a workstation session
	// has nobody to tell.
	setenv(t, "FRIDOLIN_FAKE_MCP", `[{"brain":"core","tool":"fride_task","args":{"message":"Fix bug #13"}},{"brain":"core","tool":"node_agent_run","args":{"prompt":"x"}}]`)
	r = h.runAgentNow("core", "quiet", 1)
	if !strings.Contains(r.Result, "as FrIDE #2") {
		t.Fatalf("fride_task from a silent agent: %+v", r)
	}
	if strings.Contains(r.Result, "Started Agent") {
		t.Errorf("a workstation session started from a silent agent: %+v", r)
	}

	// Threads an agent started before #300 report to the owner's DM: they
	// go back to the agent that started them, by the run they began in
	// or by its task; the owner's own stays.
	ctx := context.Background()
	for _, id := range []int64{1, 2} {
		if err := h.b.store.SetFrideThreadChat(ctx, id, harnessDM); err != nil {
			t.Fatal(err)
		}
	}
	byTask, _ := h.b.store.AddFrideThread(ctx, store.FrideThread{Chat: harnessDM, Brain: "core", Message: "Fix the bugs of fix-bugs\nissue #14"})
	own, _ := h.b.store.AddFrideThread(ctx, store.FrideThread{Chat: harnessDM, Brain: "core", Message: "Fix the login page, it looks odd"})
	// A thread that began after #300 stays where it is, even when it began
	// while a run of an agent went on: the owner's DM run may have started
	// it then (#337). The relay adopts only older ones, at every start.
	h.b.fride.adoptAgentThreads(agentThreadsBefore)
	for _, id := range []int64{1, 2, byTask, own} {
		if th, _ := h.b.store.GetFrideThread(ctx, id); th.Chat != harnessDM {
			t.Errorf("FrIDE #%d, newer than #300, reports to %q", id, th.Chat)
		}
	}
	// As if they all began before #300.
	h.b.fride.adoptAgentThreads(time.Now().Add(time.Minute))
	for id, want := range map[int64]string{1: "schedule:fix-bugs", 2: "schedule:quiet", byTask: "schedule:fix-bugs", own: harnessDM} {
		if th, _ := h.b.store.GetFrideThread(ctx, id); th.Chat != want {
			t.Errorf("FrIDE #%d reports to %q, want %q", id, th.Chat, want)
		}
	}
}

// A run that writes its summary and then only calls tools ends with an
// empty result event: the summary is still the report. One that ends in
// an error keeps what it wrote last in its history, not sent.
func TestScheduledAgentsKeepTheirSummary(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "")
	h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
	if w := h.Do("PUT", "/api/schedules/main/digest", map[string]any{"when": "daily 18:00", "prompt": "Sum up.", "report": "always"}); w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	h.WaitFor("the agent", func() bool { return len(h.schedules().Agents) == 1 })

	const text = `{"type":"assistant","message":{"content":[{"type":"text","text":"Fixed two bugs today."}]}}`
	const tool = `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{}}]}}`
	setenv(t, "FRIDOLIN_FAKE_RESULT", text+"\n"+tool+"\n"+`{"type":"result","result":""}`)
	n := len(h.SentTo(harnessDM))
	r := h.runAgentNow("main", "digest", 1)
	if r.Status != sched.StatusOK || !r.Sent || r.Result != "Fixed two bugs today." {
		t.Fatalf("the run with tools after its summary: %+v", r)
	}
	h.WaitFor("the summary in the owner's chat", func() bool { return len(h.SentTo(harnessDM)) > n })
	if got := h.SentTo(harnessDM)[n]; !strings.Contains(got, "Fixed two bugs today.") {
		t.Errorf("sent %q", got)
	}

	setenv(t, "FRIDOLIN_FAKE_RESULT", text+"\n"+`{"type":"result","is_error":true,"result":"overloaded"}`)
	r = h.runAgentNow("main", "digest", 2)
	if r.Status != sched.StatusFailed || r.Result != "Fixed two bugs today." || !strings.Contains(r.Error, "overloaded") {
		t.Fatalf("the run that ended in an error: %+v", r)
	}
	w := h.Do("GET", "/api/schedules/main/digest/runs/"+r.ID+"/log", nil)
	if !strings.Contains(w.Body.String(), "Its last text before the error") {
		t.Errorf("the log: %s", w.Body.String())
	}
}
