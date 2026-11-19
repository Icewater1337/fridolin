package app

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/manifest"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/sched"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// addAgent saves a scheduled agent of brain that reports nothing, and
// waits for the brain to have it.
func (h *harness) addAgent(brain, name, prompt string) {
	h.t.Helper()
	before := len(h.schedules().Agents)
	if w := h.Do("PUT", "/api/schedules/"+brain+"/"+name, map[string]any{"when": "every 6h", "prompt": prompt, "report": "silent"}); w.Code != http.StatusOK {
		h.t.Fatalf("PUT %s: %d %s", name, w.Code, w.Body.String())
	}
	h.WaitFor("the brain to reload", func() bool { return len(h.schedules().Agents) == before+1 })
}

// TestAScheduledAgentsTaskLeavesTheChatsTheirSlot: the task a scheduled
// agent's run delegates runs in the sub-brain's chat lane, in the agent's
// own run slot. With max_concurrent_runs 2 the owner's DM is answered
// while the agent and its task go on, and the task's answer comes back
// to the agent's run.
func TestAScheduledAgentsTaskLeavesTheChatsTheirSlot(t *testing.T) {
	t.Parallel()
	release := filepath.Join(t.TempDir(), "release")
	setenv(t, "FRIDOLIN_FAKE_WAIT_FILE", release)
	setenv(t, "FRIDOLIN_FAKE_WAIT_BRAIN", "family")
	setenv(t, "FRIDOLIN_FAKE_MCP", `[{"brain":"core","tool":"delegate","args":{"brain":"family","task":"Sort the photos","timeout_seconds":60}}]`)
	h := newServedHarness(t, `{"max_concurrent_runs": 2}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	familyDir := h.AddBrain(manifest.Manifest{ID: "family", Name: "Family", Routing: manifest.Routing{Groups: []string{harnessGroup}}})
	h.grant(map[string][]string{"family": {"delegate"}})
	h.addAgent("core", "upkeep", "Do the upkeep "+testutil.FakeMCPTrigger)
	if w := h.Do("POST", "/api/schedules/core/upkeep/run", nil); w.Code != http.StatusOK {
		t.Fatalf("run now: %d %s", w.Code, w.Body.String())
	}

	h.WaitFor("the task in family's chat lane", func() bool {
		run, _ := h.lanes("family")
		return run != nil && strings.HasPrefix(run.Chat, chat.DelegateChatPrefix) && run.Source == chat.SourceDelegate
	})
	if run, bg := h.lanes("core"); run != nil || bg == nil || bg.Chat != chat.ScheduleChatPrefix+"upkeep" {
		t.Errorf("core: chat lane %+v, background %+v; want the agent's run in the background", run, bg)
	}
	if _, bg := h.lanes("family"); bg != nil {
		t.Errorf("family's background lane runs %+v, want the task in its chat lane", bg)
	}
	if reply := h.Answer(inbound{JID: harnessDM, Text: "are you there?"}); !strings.Contains(reply, "are you there?") {
		t.Errorf("the DM's answer: %q", reply)
	}

	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r := h.waitRunEnded("core", "upkeep", 1)
	if r.Status != sched.StatusOK || !strings.Contains(r.Result, "[core:delegate] echo:"+familyDir+":core says: Sort the photos") {
		t.Errorf("the agent's run: %+v, want the task's answer in it", r)
	}
}

// TestWhereAnAgentsFollowUpsRun: the owner's answer to a scheduled
// agent's approval, once its run stopped waiting, goes to the brain as a
// message in the owner's DM, where the owner was asked, and runs in the
// chat lane; the run FrIDE's news starts for the agent goes in the
// background lane, in the agent's chat. Both go at once.
func TestWhereAnAgentsFollowUpsRun(t *testing.T) {
	t.Parallel()
	setenv(t, "FRIDOLIN_FAKE_MCP", `[{"brain":"main","tool":"request_approval","args":{"action":"Send the hold-on report","wait_seconds":1}}]`)
	h := newServedHarness(t, "")
	h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
	h.addAgent("main", "upkeep", "Ask first "+testutil.FakeMCPTrigger)
	if r := h.runAgentNow("main", "upkeep", 1); r.Status != sched.StatusOK || !strings.Contains(r.Result, "pending: no answer yet") {
		t.Fatalf("the agent's run: %+v", r)
	}

	// From here on runs hold on until released, and ask nothing.
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	release := filepath.Join(t.TempDir(), "release")
	setenv(t, "FRIDOLIN_FAKE_WAIT_FILE", release)
	setenv(t, "FRIDOLIN_FAKE_WAIT_ON", "hold-on")
	poll := h.waitPoll(harnessDM, "Send the hold-on report")
	h.vote(poll.ID, "15551234567", outboundmsg.ApprovalYes)
	h.WaitFor("the answer's run in the chat lane", func() bool {
		run, _ := h.lanes("main")
		return run != nil && run.Chat == harnessDM && run.Source == chat.SourceApprovalResult
	})
	if _, bg := h.lanes("main"); bg != nil {
		t.Errorf("the background lane runs %+v, want nothing", bg)
	}

	h.b.agents.frideNews("main", "upkeep", "FrIDE #1 on laptop: done, the hold-on fix merged")
	h.WaitFor("the agent's run for FrIDE's news in the background lane", func() bool {
		_, bg := h.lanes("main")
		return bg != nil && bg.Chat == chat.ScheduleChatPrefix+"upkeep" && bg.Source == chat.SourceScheduled
	})
	if run, _ := h.lanes("main"); run == nil || run.Source != chat.SourceApprovalResult {
		t.Errorf("the chat lane runs %+v, want the answer's run going on", run)
	}

	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if r := h.waitRunEnded("main", "upkeep", 2); r.Trigger != sched.TriggerFride || r.Status != sched.StatusOK {
		t.Errorf("the run for FrIDE's news: %+v", r)
	}
	h.WaitSent(harnessDM, "fridolin says: The owner approved")
}

// TestTheDashboardStopsOneLane: POST /api/brains/{id}/stop with a lane
// stops that lane's run and leaves the other one going; a lane it does
// not know is refused.
func TestTheDashboardStopsOneLane(t *testing.T) {
	t.Parallel()
	release := filepath.Join(t.TempDir(), "release")
	setenv(t, "FRIDOLIN_FAKE_WAIT_FILE", release)
	setenv(t, "FRIDOLIN_FAKE_WAIT_ON", "hold on")
	h := newHarness(t, "")
	h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
	h.addAgent("main", "upkeep", "Do the upkeep, hold on.")
	if w := h.Do("POST", "/api/schedules/main/upkeep/run", nil); w.Code != http.StatusOK {
		t.Fatalf("run now: %d %s", w.Code, w.Body.String())
	}
	h.WaitFor("the agent's run", func() bool { _, bg := h.lanes("main"); return bg != nil && bg.PID > 0 })
	h.Inject(inbound{JID: harnessDM, Text: "hold on, are you there?"})
	h.WaitFor("the DM's run", func() bool { run, _ := h.lanes("main"); return run != nil && run.PID > 0 })

	if w := h.Do("POST", "/api/brains/main/stop", map[string]any{"lane": "upstairs"}); w.Code != http.StatusBadRequest {
		t.Errorf("an unknown lane: %d %s, want 400", w.Code, w.Body.String())
	}
	var stopped struct {
		Stopped bool `json:"stopped"`
	}
	if w := h.Do("POST", "/api/brains/main/stop", map[string]any{"lane": "background"}); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &stopped) != nil || !stopped.Stopped {
		t.Fatalf("stop the background lane: %d %s", w.Code, w.Body.String())
	}
	if r := h.waitRunEnded("main", "upkeep", 1); r.Status != sched.StatusStopped {
		t.Errorf("the agent's run: %+v, want it stopped", r)
	}
	if run, _ := h.lanes("main"); run == nil || run.Chat != harnessDM {
		t.Errorf("the chat lane runs %+v, want the DM's run going on", run)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h.WaitSent(harnessDM, "hold on, are you there?")
}
