package app

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/sched"
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

// A scheduled agent's run answers no chat of its own, but reports to one:
// FrIDE work it starts sends its questions and news there, the owner's
// DM here. An agent that reports nowhere may not start any, nor may a
// workstation session start from it.
func TestScheduledAgentsHandWorkToFride(t *testing.T) {
	t.Parallel()
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	ff := testutil.StartFakeFride(t, "api")
	h := newServedHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	addr := h.StartNodes()
	h.fakeFrideNode("laptop", addr, ff)

	for name, report := range map[string]string{"fix-bugs": "", "quiet": manifest.ReportSilent} {
		if w := h.Do("PUT", "/api/schedules/core/"+name, map[string]any{"when": "daily 18:00", "prompt": "Fix the bugs " + testutil.FakeMCPTrigger, "report": report}); w.Code != http.StatusOK {
			t.Fatalf("PUT %s: %d %s", name, w.Code, w.Body.String())
		}
	}
	h.WaitFor("the agents", func() bool { return len(h.schedules().Agents) == 2 })

	setenv(t, "FRIDOLIN_FAKE_MCP", `[{"brain":"core","tool":"fride_task","args":{"message":"Fix bug #12","repos":["api"]}}]`)
	r := h.runAgentNow("core", "fix-bugs", 1)
	if r.Status != sched.StatusOK || !strings.Contains(r.Result, "Handed to FrIDE on laptop as FrIDE #1") || !strings.Contains(r.Result, "the owner's chat ("+harnessDM+")") {
		t.Fatalf("fride_task from a scheduled run: %+v", r)
	}
	if code := ff.Ask(1, "Which fix?"); code != http.StatusNoContent {
		t.Fatalf("the webhook answered %d", code)
	}
	h.WaitSent(harnessDM, "❓ *FrIDE #1* asks (on laptop):\n\nWhich fix?")
	// The owner answers in the chat, where the brain's DM run passes it on.
	reply := h.coreSays("the small one", step("fride_reply", `{"thread":1,"text":"the small one"}`))
	if !strings.Contains(reply, "Passed on to FrIDE #1 on laptop") {
		t.Fatalf("fride_reply:\n%s", reply)
	}

	// Silent: nobody would hear FrIDE, so nothing starts.
	setenv(t, "FRIDOLIN_FAKE_MCP", `[{"brain":"core","tool":"fride_task","args":{"message":"Fix bug #13"}},{"brain":"core","tool":"node_agent_run","args":{"prompt":"x"}}]`)
	r = h.runAgentNow("core", "quiet", 1)
	if !strings.Contains(r.Result, "[core:fride_task failed] this run answers no chat") {
		t.Fatalf("fride_task from a silent agent: %+v", r)
	}
	if strings.Contains(r.Result, "Started Agent") {
		t.Errorf("a workstation session started from a silent agent: %+v", r)
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
