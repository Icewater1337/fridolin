package app

import (
	"context"
	"encoding/json"
	"fmt"
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

// toolAnswer is what step label (core:tool, or core:tool failed) got
// back in reply, the core brain's answer: the text after it, up to the
// next step's.
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

// stamp is a run's started as system_status gives it.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// runStop is run_stop's step for brain's lane and the run started then.
func runStop(brain, lane string, started time.Time) testutil.FakeMCPStep {
	return step("run_stop", fmt.Sprintf(`{"brain":%q,"lane":%q,"started":%q}`, brain, lane, stamp(started)))
}

// TestOpsToolsAreTheCoreBrainsOnly: a core run that answers the owner
// lists the ops tools; a sub-brain's run does not, and calling one there
// is calling a tool that does not exist.
func TestOpsToolsAreTheCoreBrainsOnly(t *testing.T) {
	t.Parallel()
	h := coreHarness(t, []testutil.FakeMCPStep{
		{Tool: ""},
		{Brain: "family", Tool: "system_status", Args: json.RawMessage(`{}`)},
		{Brain: "family", Tool: "run_stop", Args: json.RawMessage(`{"brain":"core","lane":"chat","started":"2026-01-01T00:00:00Z"}`)},
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

// TestOpsToolsOnlyForTheOwner: the core brain's own runs get the ops tools
// only when they answer its owner: not in a group, even for the owner's
// own message there, and not for a file handed in through /ingest.
func TestOpsToolsOnlyForTheOwner(t *testing.T) {
	t.Parallel()
	setenv(t, "FRIDOLIN_FAKE_MCP", `[{"tool":""},{"tool":"system_status","args":{}}]`)
	h := newServedHarness(t, `{"ingest": {"enabled": true}}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true, Groups: []string{harnessGroup}}})

	if reply := h.Answer(inbound{JID: harnessDM, Text: "What runs? #mcp"}); !strings.Contains(reply, opsToolNames) || !strings.Contains(reply, "[core:system_status] {") {
		t.Errorf("the owner's run:\n%s", reply)
	}
	reply := h.Answer(inbound{JID: harnessGroup, Sender: "15551234567", Text: "@claude what runs? #mcp"})
	if strings.Contains(reply, "system_status,") || !strings.Contains(reply, "[core:system_status] error -32602 unknown tool: system_status") {
		t.Errorf("the core's run in a group:\n%s", reply)
	}

	before := len(h.Outbox())
	if w := h.Script("POST", "/ingest", `{"brain":"core","text":"file this #mcp"}`); w.Code != http.StatusAccepted {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	var got string
	h.WaitFor("the ingested message's answer", func() bool {
		for _, m := range h.Outbox()[before:] {
			if strings.Contains(m.Text, "[core:list]") {
				got = m.Text
				return true
			}
		}
		return false
	})
	if strings.Contains(got, "system_status,") || !strings.Contains(got, "[core:system_status] error -32602 unknown tool: system_status") {
		t.Errorf("the core's run for an ingested file:\n%s", got)
	}
}

// TestSystemStatus: one JSON snapshot with each part, the calling run in
// its brain's chat lane named by kind, the agents with their state, and
// no token, secret, phone number, JID or message text in it.
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
	if st.Core.Version == "" || st.Core.UptimeS < 0 || st.Core.Started.IsZero() || st.At.IsZero() || st.Core.Lockdown {
		t.Errorf("core: %+v", st.Core)
	}
	brains := map[string]opsBrain{}
	for _, b := range st.Brains {
		brains[b.ID] = b
	}
	core, family := brains["core"], brains["family"]
	if len(st.Brains) != 2 || core.ChatRun == nil || core.ChatRun.Chat != "dm" || core.Background != nil || family.ChatRun != nil || core.LiveSession {
		t.Errorf("brains: %+v", st.Brains)
	}
	if r := core.ChatRun; r != nil && (r.Started.IsZero() || r.AgeS < 0 || r.TimeoutS < 0 || r.Agent != "" || r.Source != chat.SourceDev) {
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
	for _, secret := range []string{h.token(), h.readToken("scripts.token"), "ostrich", "Tidy the notes", harnessDM, "15551234567", "@s.whatsapp.net", "@g.us"} {
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

// TestChatKind names every kind of chat without its JID.
func TestChatKind(t *testing.T) {
	t.Parallel()
	for jid, want := range map[string]string{
		"15551234567@s.whatsapp.net":     "dm",
		"15559876543@lid":                "dm",
		"120363000000000000@g.us":        "group",
		chat.ScheduleChatPrefix + "tidy": "schedule:tidy",
		chat.DelegateChatPrefix + "r1":   "delegate",
		"build:core:1":                   "internal",
		"":                               "internal",
	} {
		if got := chatKind(jid); got != want {
			t.Errorf("chatKind(%q) = %q, want %q", jid, got, want)
		}
	}
}

// TestScheduleRunAndRunStop: the core brain starts an agent, which runs
// in its brain's background lane, and stops exactly the run it aims at:
// not the run that calls it, in either lane, not one that started in
// between, and nothing in a brain it holds no grant on.
func TestScheduleRunAndRunStop(t *testing.T) {
	t.Parallel()
	release := filepath.Join(t.TempDir(), "release")
	setenv(t, "FRIDOLIN_FAKE_WAIT_FILE", release)
	setenv(t, "FRIDOLIN_FAKE_WAIT_ON", "the long upkeep")
	h := coreHarness(t, nil)
	h.putAgent("core", "upkeep", map[string]any{"when": "every 6h", "prompt": "Do the long upkeep.", "report": "silent"})
	h.putAgent("core", "tidy", map[string]any{"when": "every 6h", "prompt": "Do the long upkeep, tidily.", "report": "silent"})
	h.putAgent("core", "keeper", map[string]any{"when": "every 6h", "prompt": "Mind the lanes " + testutil.FakeMCPTrigger, "report": "silent", "risk": "write"})
	h.WaitFor("the brain to reload", func() bool { return len(h.schedules().Agents) == 3 })
	background := func() string {
		_, bg := h.lanes("core")
		if bg == nil {
			return ""
		}
		return bg.Chat
	}

	reply := h.coreSays("start the upkeep",
		step("schedule_run", `{"brain":"core","name":"upkeep"}`),
		step("schedule_run", `{"brain":"core","name":"nope"}`),
		step("schedule_run", `{"brain":"family","name":"upkeep"}`))
	if got := toolAnswer(t, reply, "core:schedule_run"); !strings.HasPrefix(got, "Started scheduled agent upkeep of brain core (run ") {
		t.Errorf("schedule_run: %s", got)
	}
	for _, want := range []string{`[core:schedule_run failed] brain core has no scheduled agent "nope"`, `[core:schedule_run failed] brain "family" is neither this brain nor one it holds a grant on`} {
		if !strings.Contains(reply, want) {
			t.Errorf("schedule_run lacks %q:\n%s", want, reply)
		}
	}
	h.WaitFor("upkeep's run", func() bool { _, bg := h.lanes("core"); return bg != nil && bg.PID > 0 })
	_, bg := h.lanes("core")
	upkeep := bg.Started
	if r := h.agentRuns("core", "upkeep")[0]; r.Trigger != sched.TriggerManual || r.By != opsTrigger {
		t.Errorf("the run: %+v", r)
	}
	// tidy waits behind upkeep in the background lane.
	if got := toolAnswer(t, h.coreSays("tidy too", step("schedule_run", `{"brain":"core","name":"tidy"}`)), "core:schedule_run"); !strings.HasPrefix(got, "Started scheduled agent tidy") {
		t.Errorf("schedule_run tidy: %s", got)
	}

	reply = h.coreSays("stop it",
		runStop("core", "chat", time.Now()),
		runStop("core", "background", upkeep.Add(-time.Second)),
		runStop("family", "chat", time.Now()),
		runStop("core", "both", upkeep),
		step("run_stop", `{"brain":"core","lane":"background"}`))
	for _, want := range []string{
		"[core:run_stop failed] that is this run itself (brain core, chat lane)",
		"[core:run_stop failed] the run in brain core's background lane now is another one, for schedule:upkeep, started " + stamp(upkeep),
		`[core:run_stop failed] brain "family" is neither this brain nor one it holds a grant on`,
		`[core:run_stop failed] lane must be "chat" or "background"`,
		"[core:run_stop failed] started must be the run's started",
	} {
		if !strings.Contains(reply, want) {
			t.Errorf("run_stop lacks %q:\n%s", want, reply)
		}
	}
	if got := background(); got != chat.ScheduleChatPrefix+"upkeep" {
		t.Fatalf("background runs %q after the refused stops, want upkeep still", got)
	}

	// upkeep ends otherwise and tidy starts: a stop aimed at upkeep's run
	// leaves tidy's alone.
	if w := h.Do("POST", "/api/brains/core/stop", nil); w.Code != http.StatusOK {
		t.Fatalf("stop: %d %s", w.Code, w.Body.String())
	}
	h.WaitFor("tidy's run", func() bool {
		_, bg := h.lanes("core")
		return bg != nil && bg.Chat == chat.ScheduleChatPrefix+"tidy" && bg.PID > 0
	})
	_, bg = h.lanes("core")
	tidy := bg.Started
	if got := toolAnswer(t, h.coreSays("stop the upkeep", runStop("core", "background", upkeep)), "core:run_stop failed"); !strings.Contains(got, "another one, for schedule:tidy") {
		t.Errorf("a stop aimed at upkeep's run: %s", got)
	}
	time.Sleep(200 * time.Millisecond)
	if got := background(); got != chat.ScheduleChatPrefix+"tidy" {
		t.Fatalf("background runs %q, want tidy, which the stop aimed at upkeep must leave", got)
	}
	if got := toolAnswer(t, h.coreSays("stop tidy", runStop("core", "background", tidy)), "core:run_stop"); !strings.HasPrefix(got, "Stopped brain core's background run for schedule:tidy, which had run for ") {
		t.Errorf("run_stop of tidy: %s", got)
	}
	if r := h.waitRunEnded("core", "tidy", 1); r.Status != sched.StatusStopped {
		t.Errorf("tidy's run: %+v", r)
	}

	// With a grant on family, it reaches family: nothing runs there.
	h.grant(map[string][]string{"family": {"read"}})
	if got := toolAnswer(t, h.coreSays("and family?", runStop("family", "chat", time.Now())), "core:run_stop failed"); !strings.HasPrefix(got, "nothing runs in brain family's chat lane now: nothing was stopped") {
		t.Errorf("run_stop of family with a grant: %s", got)
	}

	// A scheduled run cannot stop itself either: its lane is the
	// background one.
	data, _ := json.Marshal([]testutil.FakeMCPStep{runStop("core", "background", time.Now()), runStop("core", "chat", time.Now())})
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
		"[core:run_stop failed] nothing runs in brain core's chat lane now",
	} {
		if !strings.Contains(said, want) {
			t.Errorf("the keeper's run lacks %q:\n%s", want, said)
		}
	}
}

// TestScheduleRunRefusals: schedule_run starts no agent that is paused,
// switched off or waits for the owner's approval, nor one whose risk
// level is above the calling run's.
func TestScheduleRunRefusals(t *testing.T) {
	t.Parallel()
	h := coreHarness(t, nil)
	dir := filepath.Join(h.Home(), "core")
	h.putAgent("core", "paused", map[string]any{"when": "every 6h", "prompt": "Look.", "report": "silent"})
	h.putAgent("core", "off", map[string]any{"when": "every 6h", "prompt": "Look.", "report": "silent", "enabled": false})
	h.putAgent("core", "changed", map[string]any{"when": "every 6h", "prompt": "Look.", "report": "silent"})
	h.putAgent("core", "danger", map[string]any{"when": "every 6h", "prompt": "Look.", "report": "silent", "risk": "irreversible"})
	h.WaitFor("the brain to reload", func() bool { return len(h.schedules().Agents) == 4 })
	if w := h.Do("POST", "/api/schedules/core/paused/pause", nil); w.Code != http.StatusOK {
		t.Fatalf("pause: %d %s", w.Code, w.Body.String())
	}
	writeAgentFile(t, dir, "changed", `{"name": "changed", "when": "every 2h", "prompt": "Look harder.", "report": "silent"}`)
	h.WaitFor("the change to wait for approval", func() bool { a, _ := h.review("core", "changed"); return a.Review != nil })

	reply := h.coreSays("run them",
		step("schedule_run", `{"brain":"core","name":"paused"}`),
		step("schedule_run", `{"brain":"core","name":"off"}`),
		step("schedule_run", `{"brain":"core","name":"changed"}`),
		step("schedule_run", `{"brain":"core","name":"danger"}`))
	for _, want := range []string{
		"[core:schedule_run failed] scheduled agent paused is paused on the Schedules page",
		"[core:schedule_run failed] scheduled agent off is switched off",
		"[core:schedule_run failed] scheduled agent changed waits for the owner's approval on the Schedules page (its file is changed)",
		"[core:schedule_run failed] scheduled agent danger runs at risk level irreversible, above this run's write",
	} {
		if !strings.Contains(reply, want) {
			t.Errorf("schedule_run lacks %q:\n%s", want, reply)
		}
	}
	for _, name := range []string{"paused", "off", "changed", "danger"} {
		if runs := h.agentRuns("core", name); len(runs) != 0 {
			t.Errorf("%s ran: %+v", name, runs)
		}
	}
}

// TestOpsToolsInALockdown: system_status still reads, and the three that
// act are refused.
func TestOpsToolsInALockdown(t *testing.T) {
	t.Parallel()
	h := coreHarness(t, nil)
	f := useFakeProcs(h)
	h.putAgent("core", "upkeep", map[string]any{"when": "every 6h", "prompt": "Look.", "report": "silent"})
	h.WaitFor("the brain to reload", func() bool { return len(h.schedules().Agents) == 1 })
	if _, err := h.b.setLockdown(context.Background(), true, "dashboard"); err != nil {
		t.Fatal(err)
	}
	reply := h.coreSays("what now",
		step("system_status", `{}`),
		step("schedule_run", `{"brain":"core","name":"upkeep"}`),
		runStop("core", "background", time.Now()),
		step("housekeeping_run", `{}`))
	var st systemStatus
	if err := json.Unmarshal([]byte(toolAnswer(t, reply, "core:system_status")), &st); err != nil || !st.Core.Lockdown {
		t.Errorf("system_status in a lockdown: %+v, %v", st.Core, err)
	}
	for _, tool := range []string{"schedule_run", "run_stop", "housekeeping_run"} {
		if !strings.Contains(reply, "[core:"+tool+" failed] the bridge is in lockdown") {
			t.Errorf("%s in a lockdown:\n%s", tool, reply)
		}
	}
	if runs := h.agentRuns("core", "upkeep"); len(runs) != 0 {
		t.Errorf("an agent ran in a lockdown: %+v", runs)
	}
	if last := h.b.housekeeping.opsView().Last; last != nil || len(f.killedPIDs()) != 0 {
		t.Errorf("housekeeping ran in a lockdown: %+v", last)
	}
}

// TestHousekeepingRunTool: housekeeping_run runs the job now and answers
// its report, without the command lines it found; while
// housekeeping.dry_run is on (the default) it stays a dry run, whatever
// the tool's dry_run says.
func TestHousekeepingRunTool(t *testing.T) {
	t.Parallel()
	h := coreHarness(t, nil)
	f := useFakeProcs(h)
	if !h.b.cfgs.Get().Housekeeping.DryRun {
		t.Fatal("housekeeping.dry_run is off by default")
	}
	for _, args := range []string{`{}`, `{"dry_run":false}`, `{"dry_run":true}`} {
		out := toolAnswer(t, h.coreSays("tidy up", step("housekeeping_run", args)), "core:housekeeping_run")
		var rep opsReport
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("housekeeping_run %s is no JSON: %v\n%s", args, err, out)
		}
		if !rep.DryRun || rep.Summary == "" || rep.Finished.IsZero() {
			t.Errorf("housekeeping_run %s: %+v", args, rep)
		}
		found := false
		for _, fd := range rep.Findings {
			found = found || fd.PID == 7001 && fd.Outcome != ""
		}
		if !found {
			t.Errorf("housekeeping_run %s: the orphaned run is not in the report:\n%s", args, out)
		}
		if strings.Contains(out, "server.js") || strings.Contains(out, `"command"`) {
			t.Errorf("housekeeping_run %s: the report holds command lines:\n%s", args, out)
		}
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
