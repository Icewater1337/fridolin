package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/ghapp/ghapptest"
	"github.com/Icewater1337/fridolin/internal/intent"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/sched"
)

// listedAgent is an agent as GET /api/schedules lists it.
type listedAgent struct {
	Brain      string            `json:"brain"`
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	Task       string            `json:"task"`
	Report     string            `json:"report"`
	Chat       string            `json:"chat"`
	Enabled    bool              `json:"enabled"`
	Paused     bool              `json:"paused"`
	Running    bool              `json:"running"`
	FromMemory bool              `json:"from_memory"`
	Next       *time.Time        `json:"next"`
	LastRun    *sched.Run        `json:"last_run"`
	Problem    string            `json:"problem"`
	Schedule   manifest.Schedule `json:"schedule"`
}

// schedulesList is GET /api/schedules as a test reads it.
type schedulesList struct {
	Enabled   bool          `json:"enabled"`
	Agents    []listedAgent `json:"agents"`
	Reminders []struct {
		Brain string     `json:"brain"`
		ID    string     `json:"id"`
		Text  string     `json:"text"`
		Next  *time.Time `json:"next"`
	} `json:"reminders"`
	Templates []sched.Template `json:"templates"`
}

func (h *harness) schedules() schedulesList {
	h.t.Helper()
	w := h.Do("GET", "/api/schedules", nil)
	var out schedulesList
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		h.t.Fatalf("GET /api/schedules: %d %s", w.Code, w.Body.String())
	}
	return out
}

// agent is brain's agent name as GET /api/schedules lists it.
func (h *harness) agent(brain, name string) listedAgent {
	h.t.Helper()
	for _, a := range h.schedules().Agents {
		if a.Brain == brain && a.Name == name {
			return a
		}
	}
	h.t.Fatalf("no agent %s/%s listed", brain, name)
	return listedAgent{}
}

func (h *harness) agentRuns(brain, name string) []sched.Run {
	h.t.Helper()
	w := h.Do("GET", "/api/schedules/"+brain+"/"+name+"/runs", nil)
	var out []sched.Run
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		h.t.Fatalf("runs: %d %s", w.Code, w.Body.String())
	}
	return out
}

// waitRunEnded waits for the newest run of brain/name to end and returns
// it.
func (h *harness) waitRunEnded(brain, name string, n int) sched.Run {
	h.t.Helper()
	var last sched.Run
	h.WaitFor("run "+brain+"/"+name+" to end", func() bool {
		runs := h.agentRuns(brain, name)
		if len(runs) < n || runs[0].Status == sched.StatusRunning {
			return false
		}
		last = runs[0]
		return true
	})
	return last
}

// TestScheduledAgentsThroughTheAPI: an agent added on the dashboard is
// written into brain.json and its approved copy, runs now as a normal
// brain run of its own chat, sends its result where its report setting
// says, keeps its run history with each run's log, and pauses, resumes,
// changes and goes away.
func TestScheduledAgentsThroughTheAPI(t *testing.T) {
	runs := fakeRuns(t)
	h := newHarness(t, "")
	dir := h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})

	put := func(name string, s any) *int {
		w := h.Do("PUT", "/api/schedules/main/"+name, s)
		return &w.Code
	}
	if code := put("inbox", map[string]any{"when": "every 6h", "prompt": "Check the inbox folder.", "report": "always"}); *code != http.StatusOK {
		t.Fatalf("PUT: %d", *code)
	}
	if got := h.Approved()["main"].Schedules; len(got) != 1 || got[0].Name != "inbox" || got[0].When != "every 6h" {
		t.Fatalf("approved schedules %+v", got)
	}
	disk, err := manifest.Read(dir)
	if err != nil || len(disk.Schedules) != 1 {
		t.Fatalf("brain.json: %+v %v", disk, err)
	}
	h.WaitFor("the brain to reload", func() bool { return len(h.schedules().Agents) == 1 })
	list := h.schedules()
	a := list.Agents[0]
	if !list.Enabled || a.Brain != "main" || a.Kind != sched.KindBrain || a.Report != manifest.ReportAlways || a.Chat != harnessDM || a.Next == nil || a.LastRun != nil || a.Problem != "" {
		t.Fatalf("the agent as listed: %+v", a)
	}
	if !slices.ContainsFunc(list.Templates, func(tp sched.Template) bool { return tp.Name == "morning-briefing" }) {
		t.Errorf("templates: %+v", list.Templates)
	}

	// Run now: a brain run in the agent's own chat, its answer sent to
	// the owner.
	n := len(h.SentTo(harnessDM))
	if w := h.Do("POST", "/api/schedules/main/inbox/run", nil); w.Code != http.StatusOK {
		t.Fatalf("run now: %d %s", w.Code, w.Body.String())
	}
	r := h.waitRunEnded("main", "inbox", 1)
	if r.Status != sched.StatusOK || r.Trigger != sched.TriggerManual || r.By != "dashboard" || !r.Sent || r.SentTo != harnessDM || !strings.Contains(r.Result, "Check the inbox folder.") {
		t.Fatalf("the run: %+v", r)
	}
	h.WaitFor("the result in the owner's chat", func() bool { return len(h.SentTo(harnessDM)) > n })
	if got := h.SentTo(harnessDM)[n]; !strings.Contains(got, "Scheduled agent") || !strings.Contains(got, "Check the inbox folder.") {
		t.Errorf("sent %q", got)
	}
	p := runs()[len(runs())-1].Prompt
	for _, want := range []string{"a run of one of your scheduled agents", `Scheduled agent \"inbox\"`, "started by hand"} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, p)
		}
	}
	w := h.Do("GET", "/api/schedules/main/inbox/runs/"+r.ID+"/log", nil)
	var lg struct {
		Log string `json:"log"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &lg) != nil || !strings.Contains(lg.Log, "✅ Result") || !strings.Contains(lg.Log, "■ ok") || !strings.Contains(lg.Log, "Sent to") {
		t.Errorf("the log: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("GET", "/api/schedules/main/inbox/runs/20200101T000000-000000/log", nil); w.Code != http.StatusNotFound {
		t.Errorf("a log that is not there: %d", w.Code)
	}

	// Worth reporting only: NOTHING_TO_REPORT sends nothing; what it cost
	// is recorded.
	if code := put("inbox", map[string]any{"when": "every 6h", "prompt": "Check the inbox folder."}); *code != http.StatusOK {
		t.Fatalf("PUT: %d", *code)
	}
	t.Setenv("FRIDOLIN_FAKE_RESULT", `{"type":"result","result":"NOTHING_TO_REPORT","total_cost_usd":0.25,"usage":{"input_tokens":100,"output_tokens":7}}`)
	n = len(h.SentTo(harnessDM))
	h.WaitFor("the edit to apply", func() bool { return h.schedules().Agents[0].Report == manifest.ReportWorth })
	if w := h.Do("POST", "/api/schedules/main/inbox/run", nil); w.Code != http.StatusOK {
		t.Fatalf("run now: %d %s", w.Code, w.Body.String())
	}
	r = h.waitRunEnded("main", "inbox", 2)
	if r.Status != sched.StatusNothing || r.Sent || r.CostUSD != 0.25 || r.InputTokens != 100 || r.OutputTokens != 7 {
		t.Errorf("a run with nothing to report: %+v", r)
	}
	if len(h.SentTo(harnessDM)) != n {
		t.Errorf("nothing to report, but sent %q", h.SentTo(harnessDM)[n:])
	}
	t.Setenv("FRIDOLIN_FAKE_RESULT", "")

	// Never two runs of one agent at once.
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "1500")
	if w := h.Do("POST", "/api/schedules/main/inbox/run", nil); w.Code != http.StatusOK {
		t.Fatalf("run now: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("POST", "/api/schedules/main/inbox/run", nil); w.Code != http.StatusConflict {
		t.Errorf("a second run at once: %d %s", w.Code, w.Body.String())
	}
	if !h.schedules().Agents[0].Running {
		t.Error("the run in progress is not listed as running")
	}
	h.waitRunEnded("main", "inbox", 3)
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "10")

	// Pause and resume.
	if w := h.Do("POST", "/api/schedules/main/inbox/pause", nil); w.Code != http.StatusOK {
		t.Fatalf("pause: %d %s", w.Code, w.Body.String())
	}
	if a := h.schedules().Agents[0]; !a.Paused || a.Next != nil {
		t.Errorf("paused: %+v", a)
	}
	if w := h.Do("POST", "/api/schedules/main/inbox/resume", nil); w.Code != http.StatusOK {
		t.Fatalf("resume: %d", w.Code)
	}
	if a := h.schedules().Agents[0]; a.Paused || a.Next == nil {
		t.Errorf("resumed: %+v", a)
	}

	// Edits the bridge refuses.
	for what, body := range map[string]any{
		"a template that is not there": map[string]any{"when": "every 6h", "template": "nope"},
		"a bad schedule":               map[string]any{"when": "now and then", "prompt": "x"},
		"another name":                 map[string]any{"name": "other", "when": "every 6h", "prompt": "x"},
	} {
		if code := put("inbox", body); *code < 400 || *code >= 500 {
			t.Errorf("%s: %d", what, *code)
		}
	}
	if w := h.Do("POST", "/api/schedules/main/nope/run", nil); w.Code != http.StatusNotFound {
		t.Errorf("run of no agent: %d", w.Code)
	}

	// A task file in the brain's schedules/ folder, with its vars.
	if err := os.MkdirAll(filepath.Join(dir, "schedules"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "schedules", "notes.md"), []byte("---\ndescription: Notes.\nvars: topic=the garden\n---\nWrite notes about {{topic}}.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := put("notes", map[string]any{"when": "weekly mon 09:00", "report": "always"}); *code != http.StatusOK {
		t.Fatalf("PUT notes: %d", *code)
	}
	h.WaitFor("the notes agent", func() bool { return len(h.schedules().Agents) == 2 })
	if w := h.Do("POST", "/api/schedules/main/notes/run", map[string]string{"trigger": "cli"}); w.Code != http.StatusOK {
		t.Fatalf("run notes: %d %s", w.Code, w.Body.String())
	}
	if r := h.waitRunEnded("main", "notes", 1); r.Status != sched.StatusOK || r.By != "cli" || !strings.Contains(r.Result, "Write notes about the garden.") {
		t.Errorf("the notes run: %+v", r)
	}

	// Remove: gone from brain.json; its history stays until it ages out.
	if w := h.Do("DELETE", "/api/schedules/main/inbox", nil); w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if got := h.Approved()["main"].Schedules; len(got) != 1 || got[0].Name != "notes" {
		t.Errorf("after the delete: %+v", got)
	}
	if w := h.Do("DELETE", "/api/schedules/main/inbox", nil); w.Code != http.StatusNotFound {
		t.Errorf("a second delete: %d", w.Code)
	}
}

// TestScheduledAgentsOnTheirSchedule: the scheduler starts an agent when
// it falls due and not before, once however many times it fell due while
// the bridge was down, skips such a run when the agent says so, never
// starts one while its run goes on, and leaves a paused agent and one
// switched off alone.
func TestScheduledAgentsOnTheirSchedule(t *testing.T) {
	h := newHarness(t, "")
	h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}, Schedules: []manifest.Schedule{
		{Name: "often", When: "every 6h", Prompt: "Look around.", Report: manifest.ReportSilent, JitterMinutes: new(0)},
		{Name: "skipper", When: "every 6h", Prompt: "Look around.", Report: manifest.ReportSilent, Missed: manifest.MissedSkip, JitterMinutes: new(0)},
		{Name: "off", When: "every 6h", Prompt: "Look around.", Enabled: new(false), JitterMinutes: new(0)},
	}})
	a := h.b.agents
	t0 := time.Now()
	a.tick(t0) // first sight: counting starts now
	if runs := h.agentRuns("main", "often"); len(runs) != 0 {
		t.Fatalf("ran at first sight: %+v", runs)
	}
	a.tick(t0.Add(5 * time.Hour))
	if runs := h.agentRuns("main", "often"); len(runs) != 0 {
		t.Fatalf("ran before it was due: %+v", runs)
	}
	a.tick(t0.Add(6*time.Hour + time.Minute))
	r := h.waitRunEnded("main", "often", 1)
	if r.Trigger != sched.TriggerSchedule || r.Status != sched.StatusOK || r.Sent {
		t.Errorf("on time: %+v", r)
	}
	h.waitRunEnded("main", "skipper", 1)

	// Down for two days: one late run for often, a skipped one for
	// skipper, nothing for off.
	a.tick(t0.Add(53 * time.Hour))
	r = h.waitRunEnded("main", "often", 2)
	if r.Trigger != sched.TriggerMissed || r.Status != sched.StatusOK {
		t.Errorf("late: %+v", r)
	}
	if runs := h.agentRuns("main", "skipper"); len(runs) != 2 || runs[0].Status != sched.StatusSkipped {
		t.Errorf("skipped: %+v", runs)
	}
	if runs := h.agentRuns("main", "off"); len(runs) != 0 {
		t.Errorf("a disabled agent ran: %+v", runs)
	}

	// Paused, nothing runs; the time it missed meanwhile is not made up.
	if w := h.Do("POST", "/api/schedules/main/often/pause", nil); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	a.tick(t0.Add(61 * time.Hour))
	if runs := h.agentRuns("main", "often"); len(runs) != 2 {
		t.Errorf("a paused agent ran: %+v", runs)
	}
	h.Do("POST", "/api/schedules/main/often/resume", nil)
	a.tick(t0.Add(62 * time.Hour))
	if runs := h.agentRuns("main", "often"); len(runs) != 2 {
		t.Errorf("a resumed agent made up for its pause: %+v", runs)
	}

	// While a run goes on, the next slot starts none.
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "1500")
	a.tick(t0.Add(66*time.Hour + time.Minute))
	h.WaitFor("the run to start", func() bool { return h.agent("main", "often").Running })
	a.tick(t0.Add(72*time.Hour + time.Minute))
	h.waitRunEnded("main", "often", 3)
	if runs := h.agentRuns("main", "often"); len(runs) != 3 {
		t.Errorf("a slot during a run started another: %d runs", len(runs))
	}
}

// TestSchedulesSwitchedOff: with schedules.enabled off nothing runs on
// its schedule, and Run now still does.
func TestSchedulesSwitchedOff(t *testing.T) {
	h := newHarness(t, `{"schedules": {"enabled": false}}`)
	h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}, Schedules: []manifest.Schedule{
		{Name: "often", When: "every 6h", Prompt: "Look around.", Report: manifest.ReportSilent},
	}})
	t0 := time.Now()
	h.b.agents.tick(t0)
	h.b.agents.tick(t0.Add(7 * time.Hour))
	if runs := h.agentRuns("main", "often"); len(runs) != 0 {
		t.Errorf("ran with schedules.enabled off: %+v", runs)
	}
	if l := h.schedules(); l.Enabled || l.Agents[0].Next != nil {
		t.Errorf("listed: %+v", l)
	}
	if w := h.Do("POST", "/api/schedules/main/often/run", nil); w.Code != http.StatusOK {
		t.Fatalf("run now: %d %s", w.Code, w.Body.String())
	}
	if r := h.waitRunEnded("main", "often", 1); r.Status != sched.StatusOK {
		t.Errorf("run now: %+v", r)
	}
}

// TestMemoryConsolidateIsAScheduledAgent: memory.consolidate shows as
// the agent memory-consolidation, keeps the last run an older bridge
// recorded, and an edit moves it into the schedules list.
func TestMemoryConsolidateIsAScheduledAgent(t *testing.T) {
	h := newHarness(t, "")
	h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}, Memory: &manifest.Memory{Inject: true, Consolidate: "weekly sun 18:00"}})
	list := h.schedules()
	if len(list.Agents) != 1 || list.Agents[0].Name != manifest.ConsolidationSchedule || !list.Agents[0].FromMemory || list.Agents[0].Report != manifest.ReportAlways || list.Agents[0].Problem != "" {
		t.Fatalf("listed: %+v", list.Agents)
	}
	if w := h.Do("PUT", "/api/schedules/main/memory-consolidation", map[string]any{"when": "daily 07:00", "template": "memory-consolidation", "report": "always"}); w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	m := h.Approved()["main"]
	if m.Memory == nil || m.Memory.Consolidate != "" || !m.Memory.Inject || len(m.Schedules) != 1 || m.Schedules[0].When != "daily 07:00" {
		t.Fatalf("after the edit: memory %+v schedules %+v", m.Memory, m.Schedules)
	}

	// Reminders: a standing intent with a time is listed beside them.
	now := time.Now()
	in, err := intent.Compile(intent.Request{Text: "Call the dentist", When: now.Add(time.Hour).Format("2006-01-02T15:04")}, harnessDM, "15551234567", now, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.b.intents.Add("main", in); err != nil {
		t.Fatal(err)
	}
	if r := h.schedules().Reminders; len(r) != 1 || r[0].Text != "Call the dentist" || r[0].Next == nil {
		t.Errorf("reminders: %+v", r)
	}
}

// TestScheduledAgentSecretsAndFailures: an agent with a github grant
// gets gh's login (GH_CONFIG_DIR), from the typed token or minted by the
// GitHub App for exactly its grant, and no other run does; a program on a
// node that is not there fails, and the owner hears of it.
func TestScheduledAgentSecretsAndFailures(t *testing.T) {
	t.Setenv("FRIDOLIN_GITHUB_TOKEN", "tok-for-tests")
	t.Setenv("FRIDOLIN_FAKE_ENV", "GH_CONFIG_DIR")
	h := newHarness(t, "")
	h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}, Schedules: []manifest.Schedule{
		{Name: "review", When: "daily 03:00", Template: "code-review-bugs", Vars: map[string]string{"repos": "you/fridolin"}, GitHub: &chat.GitHubGrant{Repos: []string{"you/fridolin"}, Permissions: map[string]string{"issues": "write", "contents": "read"}}, Report: manifest.ReportAlways, Risk: "irreversible", Preapproved: []string{"file issues on you/fridolin"}},
		{Name: "scrape", When: "daily 21:00", Command: &manifest.ScheduleCommand{Node: "laptop", Argv: []string{"/Users/you/bin/scrape.sh"}}},
	}})
	if w := h.Do("POST", "/api/schedules/main/review/run", nil); w.Code != http.StatusOK {
		t.Fatalf("run: %d %s", w.Code, w.Body.String())
	}
	if r := h.waitRunEnded("main", "review", 1); r.Status != sched.StatusOK || !strings.Contains(r.Result, "file issues on you/fridolin") {
		t.Errorf("the review run: %+v", r)
	}
	logins := filepath.Join(h.DataDir(), runTokensDirName, "main")
	h.WaitSent(harnessDM, "GH_CONFIG_DIR="+logins+string(filepath.Separator)+"run-")
	if left, _ := filepath.Glob(filepath.Join(logins, "run-*.gh")); len(left) != 0 {
		t.Errorf("the run's GitHub login outlived it: %v", left)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "hello"}); !strings.Contains(got, "GH_CONFIG_DIR unset") {
		t.Errorf("an ordinary run got the token: %q", got)
	}

	// A connected GitHub App mints a token for exactly the grant, and
	// revokes it once the run is over.
	fake := useFakeGitHub(t, h.b.github)
	creds, err := h.b.github.client.Convert(context.Background(), ghapptest.Code)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.b.github.secrets.save(creds); err != nil {
		t.Fatal(err)
	}
	if w := h.Do("POST", "/api/schedules/main/review/run", nil); w.Code != http.StatusOK {
		t.Fatalf("run: %d %s", w.Code, w.Body.String())
	}
	h.waitRunEnded("main", "review", 2)
	h.WaitFor("the second review's answer", func() bool { return len(h.SentTo(harnessDM)) >= 3 })
	if m := fake.LastMinted(); fmt.Sprint(m.Repos, m.Permissions) != "[fridolin] map[contents:read issues:write]" {
		t.Errorf("the app minted %+v", m)
	}
	h.WaitFor("the minted token to be revoked", func() bool { return len(fake.Live()) == 0 })

	if w := h.Do("POST", "/api/schedules/main/scrape/run", nil); w.Code != http.StatusOK {
		t.Fatalf("run: %d %s", w.Code, w.Body.String())
	}
	r := h.waitRunEnded("main", "scrape", 1)
	if r.Status != sched.StatusFailed || r.Kind != sched.KindCommand || !strings.Contains(r.Error, "no node") || !r.Sent {
		t.Errorf("the program's run: %+v", r)
	}
	h.WaitSent(harnessDM, "Scheduled agent *scrape* (brain main) failed")
}
