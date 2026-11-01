package app

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/sched"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// opsToolNames are the core brain's ops tools, in the order a run lists
// them.
const opsToolNames = "system_status,schedule_run,run_stop,housekeeping_run"

// putAgent adds a scheduled agent to brain through the dashboard's API.
func (h *harness) putAgent(brain, name string, s map[string]any) {
	h.t.Helper()
	if w := h.Do("PUT", "/api/schedules/"+brain+"/"+name, s); w.Code != http.StatusOK {
		h.t.Fatalf("PUT %s/%s: %d %s", brain, name, w.Code, w.Body.String())
	}
}

// toolAnswer is what step tool got back in reply, the core brain's
// answer: the text after its label, up to the next step's.
func toolAnswer(t *testing.T, reply, label string) string {
	t.Helper()
	_, after, ok := strings.Cut(reply, "["+label+"] ")
	if !ok {
		t.Fatalf("no %s in the answer:\n%s", label, reply)
	}
	if i := strings.Index(after, " [core:"); i >= 0 {
		after = after[:i]
	}
	return after
}

// TestOpsToolsAreTheCoreBrainsOnly: a core run lists the ops tools, a
// sub-brain's run does not, and calling one there is calling a tool that
// does not exist.
func TestOpsToolsAreTheCoreBrainsOnly(t *testing.T) {
	t.Parallel()
	h := coreHarness(t, []testutil.FakeMCPStep{
		{Tool: ""},
		{Brain: "family", Tool: "system_status", Args: json.RawMessage(`{}`)},
		{Brain: "family", Tool: "run_stop", Args: json.RawMessage(`{"brain":"core","lane":"chat"}`)},
	})
	reply := h.Answer(inbound{JID: harnessDM, Text: "What runs? #mcp"})
	if !strings.Contains(reply, "check_approval,"+opsToolNames) {
		t.Errorf("the core's tools:\n%s", reply)
	}
	reply = h.Answer(inbound{JID: harnessGroup, Sender: "15551234567", Text: "@claude what runs? #mcp"})
	if strings.Contains(reply, "system_status,") || strings.Contains(reply, "run_stop,") {
		t.Errorf("family lists ops tools:\n%s", reply)
	}
	for _, want := range []string{"[family:system_status] error -32602 unknown tool: system_status", "[family:run_stop] error -32602 unknown tool: run_stop"} {
		if !strings.Contains(reply, want) {
			t.Errorf("family's answer lacks %q:\n%s", want, reply)
		}
	}
}

// TestSystemStatus: one JSON snapshot with each part, the calling run in
// its brain's chat lane, the agents with their state, and no token,
// secret or message text in it.
func TestSystemStatus(t *testing.T) {
	t.Parallel()
	h := coreHarness(t, nil)
	h.putAgent("core", "upkeep", map[string]any{"when": "every 6h", "prompt": "Tidy the notes.", "report": "silent"})
	h.WaitFor("the brain to reload", func() bool { return len(h.schedules().Agents) == 1 })
	const asked = "How does the system stand? Secret word ostrich."
	reply := h.coreSays(asked, step("system_status", `{}`))
	out := toolAnswer(t, reply, "core:system_status")

	var st systemStatus
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("system_status is no JSON: %v\n%s", err, out)
	}
	if st.Core.Version == "" || st.Core.UptimeS < 0 || st.Core.Started.IsZero() || st.At.IsZero() {
		t.Errorf("core: %+v", st.Core)
	}
	brains := map[string]opsBrain{}
	for _, b := range st.Brains {
		brains[b.ID] = b
	}
	core, family := brains["core"], brains["family"]
	if len(st.Brains) != 2 || core.ChatRun == nil || core.ChatRun.Chat != harnessDM || core.Background != nil || family.ChatRun != nil || core.LiveSession {
		t.Errorf("brains: %+v", st.Brains)
	}
	if r := core.ChatRun; r != nil && (r.Started.IsZero() || r.AgeS < 0 || r.TimeoutS < 0 || r.Agent != "") {
		t.Errorf("the calling run: %+v", r)
	}
	if len(st.Agents) != 1 {
		t.Fatalf("scheduled agents: %+v", st.Agents)
	}
	if a := st.Agents[0]; a.Brain != "core" || a.Name != "upkeep" || a.State != AgentActive || !a.Enabled || a.Running || a.Next == nil || a.LastRun != nil || a.ConsecutiveFailures != 0 || a.PendingApproval != "" {
		t.Errorf("the agent: %+v", a)
	}
	if st.Housekeeping.Enabled != h.b.cfgs.Get().Housekeeping.Enabled || st.Housekeeping.Last != nil {
		t.Errorf("housekeeping %+v", st.Housekeeping)
	}
	// Without nodes there is no fleet and no FrIDE to show.
	if st.Nodes != nil || st.Fride != nil || len(st.Problems) != 0 {
		t.Errorf("nodes %+v, FrIDE %+v, problems %v", st.Nodes, st.Fride, st.Problems)
	}
	for _, secret := range []string{h.token(), h.readToken("scripts.token"), "ostrich", "Tidy the notes"} {
		if strings.Contains(out, secret) {
			t.Errorf("system_status holds %q:\n%s", secret, out)
		}
	}
	for _, key := range []string{`"core"`, `"brains"`, `"scheduled_agents"`, `"housekeeping"`, `"backups"`, `"drive"`, `"vault"`, `"chat_run"`, `"background_run"`, `"data_dir_free_bytes"`} {
		if !strings.Contains(out, key) {
			t.Errorf("system_status lacks %s:\n%s", key, out)
		}
	}
}

// TestScheduleRunAndRunStop: the core brain starts an agent, which runs
// in its brain's background lane, and stops it there; run_stop refuses
// the run that calls it, in either lane, and a lane that is not one.
func TestScheduleRunAndRunStop(t *testing.T) {
	t.Parallel()
	release := filepath.Join(t.TempDir(), "release")
	setenv(t, "FRIDOLIN_FAKE_WAIT_FILE", release)
	setenv(t, "FRIDOLIN_FAKE_WAIT_ON", "the long upkeep")
	h := coreHarness(t, nil)
	h.putAgent("core", "upkeep", map[string]any{"when": "every 6h", "prompt": "Do the long upkeep.", "report": "silent"})
	h.putAgent("core", "keeper", map[string]any{"when": "every 6h", "prompt": "Mind the lanes " + testutil.FakeMCPTrigger, "report": "silent", "risk": "write"})
	h.WaitFor("the brain to reload", func() bool { return len(h.schedules().Agents) == 2 })

	reply := h.coreSays("start the upkeep",
		step("schedule_run", `{"brain":"core","name":"upkeep"}`),
		step("schedule_run", `{"brain":"core","name":"nope"}`))
	if got := toolAnswer(t, reply, "core:schedule_run"); !strings.HasPrefix(got, "Started scheduled agent upkeep of brain core (run ") {
		t.Errorf("schedule_run: %s", got)
	}
	if !strings.Contains(reply, "[core:schedule_run failed] ") {
		t.Errorf("schedule_run of an agent that is not there:\n%s", reply)
	}
	h.WaitFor("the agent's run", func() bool { _, bg := h.lanes("core"); return bg != nil && bg.PID > 0 })
	if r := h.agentRuns("core", "upkeep")[0]; r.Trigger != sched.TriggerManual || r.By != opsTrigger {
		t.Errorf("the run: %+v", r)
	}

	reply = h.coreSays("stop it",
		step("run_stop", `{"brain":"core","lane":"chat"}`),
		step("run_stop", `{"brain":"core","lane":"background"}`),
		step("run_stop", `{"brain":"family","lane":"chat"}`),
		step("run_stop", `{"brain":"core","lane":"both"}`),
		step("run_stop", `{"brain":"nobody","lane":"chat"}`))
	for _, want := range []string{
		"[core:run_stop failed] that is this run itself (brain core, chat lane)",
		"[core:run_stop] Stopped brain core's background run for scheduled agent upkeep, which had run for ",
		"[core:run_stop] Nothing ran in brain family's chat lane.",
		`[core:run_stop failed] lane must be "chat" or "background"`,
		`[core:run_stop failed] there is no brain "nobody"`,
	} {
		if !strings.Contains(reply, want) {
			t.Errorf("run_stop lacks %q:\n%s", want, reply)
		}
	}
	if r := h.waitRunEnded("core", "upkeep", 1); r.Status != sched.StatusStopped {
		t.Errorf("the upkeep's run: %+v", r)
	}

	// A scheduled run cannot stop itself either: its lane is the
	// background one.
	data, _ := json.Marshal([]testutil.FakeMCPStep{
		step("run_stop", `{"brain":"core","lane":"background"}`),
		step("run_stop", `{"brain":"core","lane":"chat"}`),
	})
	setenv(t, "FRIDOLIN_FAKE_MCP", string(data))
	if w := h.Do("POST", "/api/schedules/core/keeper/run", nil); w.Code != http.StatusOK {
		t.Fatalf("run now: %d %s", w.Code, w.Body.String())
	}
	if r := h.waitRunEnded("core", "keeper", 1); r.Status != sched.StatusOK {
		t.Fatalf("the keeper's run: %+v", r)
	}
	var said string
	for _, e := range readHistory(t, h.b.brainMgr.Get("core")) {
		if e.Type == history.Out && e.Chat == chat.ScheduleChatPrefix+"keeper" {
			said = e.Text
		}
	}
	for _, want := range []string{
		"[core:run_stop failed] that is this run itself (brain core, background lane)",
		"[core:run_stop] Nothing ran in brain core's chat lane.",
	} {
		if !strings.Contains(said, want) {
			t.Errorf("the keeper's run lacks %q:\n%s", want, said)
		}
	}
}

// TestHousekeepingRunTool: housekeeping_run runs the job now and answers
// its report, without the command lines it found.
func TestHousekeepingRunTool(t *testing.T) {
	t.Parallel()
	h := coreHarness(t, nil)
	f := useFakeProcs(h)
	reply := h.coreSays("tidy up", step("housekeeping_run", `{"dry_run":true}`))
	out := toolAnswer(t, reply, "core:housekeeping_run")
	var rep opsReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("housekeeping_run is no JSON: %v\n%s", err, out)
	}
	if !rep.DryRun || rep.Summary == "" || rep.Finished.IsZero() {
		t.Errorf("the report: %+v", rep)
	}
	found := false
	for _, fd := range rep.Findings {
		found = found || fd.PID == 7001 && fd.Outcome != ""
	}
	if !found {
		t.Errorf("the orphaned run is not in the report:\n%s", out)
	}
	if strings.Contains(out, "server.js") || strings.Contains(out, `"command"`) {
		t.Errorf("the report holds command lines:\n%s", out)
	}
	if k := f.killedPIDs(); len(k) != 0 {
		t.Errorf("a dry run signalled %v", k)
	}
	if last := h.b.housekeeping.opsView().Last; last == nil || last.Trigger != opsTrigger {
		t.Errorf("the kept report: %+v", last)
	}
}

// TestSystemStatusShowsTheFleet: with nodes on, system_status lists the
// nodes with what they offer, and FrIDE's instances and open threads.
func TestSystemStatusShowsTheFleet(t *testing.T) {
	t.Parallel()
	optionsOf(t).waits.frideRetry = time.Hour
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	h := newServedHarness(t, frideInstancesConfig)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	h.fakeHostNode("mini", h.StartNodes(), 32768, 16000, 6144)

	out := toolAnswer(t, h.coreSays("how is the fleet", step("system_status", `{}`)), "core:system_status")
	var st systemStatus
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("system_status is no JSON: %v\n%s", err, out)
	}
	if len(st.Nodes) != 1 {
		t.Fatalf("nodes: %+v", st.Nodes)
	}
	if n := st.Nodes[0]; n.Name != "mini" || n.ID == "" || !n.Online || n.LastSeen == nil || !strings.Contains(n.Offers, "fride-host") {
		t.Errorf("the node: %+v", n)
	}
	if st.Fride == nil || len(st.Fride.Instances) != 2 || st.Fride.Instances[0].Name != "fridolin" || st.Fride.Instances[1].Name != "work" || len(st.Fride.Threads) != 0 {
		t.Errorf("FrIDE: %+v", st.Fride)
	}
}
