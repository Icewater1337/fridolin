package app

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/sched"
)

// TestScheduledAgentGates: an agent's gate runs before its brain run, in
// the brain's folder: {"wake": false} ends the run as gated with no
// model run, {"wake": true, "data": …} starts the brain run with the data
// in its task, and a gate that gives no decision, exits non-zero, takes
// too long or writes too much fails the run, whose chat hears of it. A
// lockdown refuses it. Its output is in the run's log.
func TestScheduledAgentGates(t *testing.T) {
	t.Parallel()
	runs := fakeRuns(t)
	h := newHarness(t, "")
	gate := func(argv ...string) *manifest.ScheduleGate { return &manifest.ScheduleGate{Argv: argv} }
	dir := h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}, Schedules: []manifest.Schedule{
		{Name: "inbox", When: "every 15m", Prompt: "Sort the new files.", Report: manifest.ReportAlways, Gate: gate("./gate.sh")},
		{Name: "slow", When: "every 15m", Prompt: "Never.", Report: manifest.ReportSilent, Gate: &manifest.ScheduleGate{Argv: []string{"/bin/sh", "-c", "sleep 30; echo '{\"wake\": true}'"}, TimeoutSeconds: 1}},
		{Name: "loud", When: "every 15m", Prompt: "Never.", Report: manifest.ReportSilent, Gate: gate("/bin/sh", "-c", "head -c 2000000 /dev/zero | tr '\\0' x")},
		{Name: "remote", When: "every 15m", Prompt: "Never.", Report: manifest.ReportSilent, Gate: &manifest.ScheduleGate{Argv: []string{"/Users/you/bin/ci-changed"}, Node: "laptop"}},
	}})
	script := "#!/bin/sh\necho checking the inbox\necho a warning >&2\nexit_with=$(cat exit 2>/dev/null || echo 0)\ncat answer\nexit $exit_with\n"
	if err := os.WriteFile(filepath.Join(dir, "gate.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	answer := func(text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "answer"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runNow := func(name string, n int) sched.Run {
		t.Helper()
		if w := h.Do("POST", "/api/schedules/main/"+name+"/run", nil); w.Code != http.StatusOK {
			t.Fatalf("run %s: %d %s", name, w.Code, w.Body.String())
		}
		return h.waitRunEnded("main", name, n)
	}
	logOf := func(name, id string) string {
		t.Helper()
		var lg struct {
			Log string `json:"log"`
		}
		w := h.Do("GET", "/api/schedules/main/"+name+"/runs/"+id+"/log", nil)
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &lg) != nil {
			t.Fatalf("log: %d %s", w.Code, w.Body.String())
		}
		return lg.Log
	}

	// Not woken: no model run, nothing sent.
	answer(`{"wake": false}`)
	sent := len(h.SentTo(harnessDM))
	r := runNow("inbox", 1)
	if r.Status != sched.StatusGated || r.Sent || r.ExitCode == nil || *r.ExitCode != 0 {
		t.Fatalf("a gate that does not wake: %+v", r)
	}
	if len(runs()) != 0 {
		t.Errorf("the brain ran: %+v", runs())
	}
	if lg := logOf("inbox", r.ID); !strings.Contains(lg, "Gate: $ ./gate.sh") || !strings.Contains(lg, "checking the inbox") || !strings.Contains(lg, "a warning") || !strings.Contains(lg, "did not wake") {
		t.Errorf("the log:\n%s", lg)
	}

	// Woken, with data for the task.
	answer(`{"wake": true, "data": {"files": ["invoice-17.pdf"]}}`)
	r = runNow("inbox", 2)
	if r.Status != sched.StatusOK || !r.Sent {
		t.Fatalf("a gate that wakes: %+v", r)
	}
	if got := runs(); len(got) != 1 || !strings.Contains(got[0].Prompt, "Sort the new files.") || !strings.Contains(got[0].Prompt, "invoice-17.pdf") || !strings.Contains(got[0].Prompt, "The agent's gate") {
		t.Errorf("the brain run: %+v", got)
	}

	// No decision, or a failure: a failed run, which the chat hears of.
	for i, c := range []struct{ answer, exit, why string }{
		{"all good", "0", "gave no decision"},
		{`{"wake": true}`, "3", "exited with 3"},
	} {
		answer(c.answer)
		if err := os.WriteFile(filepath.Join(dir, "exit"), []byte(c.exit), 0o644); err != nil {
			t.Fatal(err)
		}
		r = runNow("inbox", 3+i)
		if r.Status != sched.StatusFailed || !strings.Contains(r.Error, c.why) {
			t.Errorf("%q, exit %s: %+v", c.answer, c.exit, r)
		}
	}
	h.WaitFor("the failures told", func() bool { return len(h.SentTo(harnessDM)) >= sent+3 })
	if got := h.SentTo(harnessDM)[sent+1]; !strings.Contains(got, "inbox") || !strings.Contains(got, "gave no decision") {
		t.Errorf("told %q", got)
	}
	if len(runs()) != 1 {
		t.Errorf("a failed gate ran the brain: %d runs", len(runs()))
	}

	// Too long, too much: stopped, whole process group and all.
	start := time.Now()
	if r = runNow("slow", 1); r.Status != sched.StatusFailed || !strings.Contains(r.Error, "took longer than 1s") {
		t.Errorf("a slow gate: %+v", r)
	}
	if took := time.Since(start); took > 15*time.Second {
		t.Errorf("a gate with a timeout of 1s took %v", took)
	}
	if r = runNow("loud", 1); r.Status != sched.StatusFailed || !strings.Contains(r.Error, "more than 1024 KiB") {
		t.Errorf("a loud gate: %+v", r)
	}

	// On a node that is not there.
	if r = runNow("remote", 1); r.Status != sched.StatusFailed || !strings.Contains(r.Error, `its gate did not run: there is no node "laptop"`) {
		t.Errorf("a gate on a node that is not there: %+v", r)
	}

	// A lockdown refuses it.
	if w := h.Do("POST", "/api/policy/lockdown", map[string]bool{"on": true}); w.Code != http.StatusOK {
		t.Fatalf("lockdown: %d %s", w.Code, w.Body.String())
	}
	answer(`{"wake": true}`)
	if r = runNow("inbox", 5); r.Status != sched.StatusRefused || len(runs()) != 1 {
		t.Errorf("in a lockdown: %+v", r)
	}
}

// TestAGateThatLeavesAProcessBehind: a gate that exits 0 with an answer
// but leaves a process holding its stdout (a daemon, an ssh
// ControlPersist) wakes the brain within a few seconds instead of failing
// with WaitDelay's error, and what it left behind is stopped (#734).
func TestAGateThatLeavesAProcessBehind(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the gate runs /bin/sh")
	}
	runs := fakeRuns(t)
	h := newHarness(t, "")
	dir := h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}, Schedules: []manifest.Schedule{
		{Name: "daemon", When: "every 15m", Prompt: "Look.", Report: manifest.ReportSilent, Gate: &manifest.ScheduleGate{
			Argv: []string{"/bin/sh", "-c", `sleep 30 & echo $! > sleep.pid; echo '{"wake": true}'`}, TimeoutSeconds: 20}},
	}})
	start := time.Now()
	if w := h.Do("POST", "/api/schedules/main/daemon/run", nil); w.Code != http.StatusOK {
		t.Fatalf("run: %d %s", w.Code, w.Body.String())
	}
	if r := h.waitRunEnded("main", "daemon", 1); r.Status != sched.StatusOK {
		t.Fatalf("a gate that left a process behind: %+v", r)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("the run took %v", took)
	}
	if len(runs()) != 1 {
		t.Errorf("the brain runs: %+v", runs())
	}
	data, err := os.ReadFile(filepath.Join(dir, "sleep.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	h.WaitFor("the gate's sleep to be stopped", func() bool { return !platform.ProcessAlive(pid) })
}

// TestAGateThatExitsJustBeforeItsTimeout: a gate that exits 1 s before
// its timeout while a child holds its stdout, so that Run returns after
// the deadline, keeps its own exit: 0 keeps its answer, 3 fails as an exit
// of 3, and neither counts as timed out (#847).
func TestAGateThatExitsJustBeforeItsTimeout(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the gate runs /bin/sh")
	}
	runs := fakeRuns(t)
	h := newHarness(t, "")
	gate := func(exit string) *manifest.ScheduleGate {
		return &manifest.ScheduleGate{Argv: []string{"/bin/sh", "-c", `sleep 30 & sleep 2; echo '{"wake": true}'; exit ` + exit}, TimeoutSeconds: 3}
	}
	h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}, Schedules: []manifest.Schedule{
		{Name: "ok", When: "every 15m", Prompt: "Look.", Report: manifest.ReportSilent, Gate: gate("0")},
		{Name: "fails", When: "every 15m", Prompt: "Look.", Report: manifest.ReportSilent, Gate: gate("3")},
	}})
	for _, name := range []string{"ok", "fails"} {
		if w := h.Do("POST", "/api/schedules/main/"+name+"/run", nil); w.Code != http.StatusOK {
			t.Fatalf("run %s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if r := h.waitRunEnded("main", "ok", 1); r.Status != sched.StatusOK {
		t.Errorf("a gate that answered in time: %+v", r)
	}
	if r := h.waitRunEnded("main", "fails", 1); r.Status != sched.StatusFailed || !strings.Contains(r.Error, "exited with 3") {
		t.Errorf("a gate that exited 3 in time: %+v", r)
	}
	if len(runs()) != 1 {
		t.Errorf("the brain runs: %+v", runs())
	}
}

// TestGateVerdict: what a gate's program did, as the run takes it.
func TestGateVerdict(t *testing.T) {
	t.Parallel()
	g := manifest.ScheduleGate{Argv: []string{"x"}, TimeoutSeconds: 5}
	for _, c := range []struct {
		out  gateOutcome
		wake bool
		why  string
	}{
		{gateOutcome{stdout: `{"wake": true}`}, true, ""},
		{gateOutcome{stdout: "x\n{\"wake\": false}\n"}, false, ""},
		{gateOutcome{stdout: `{"wake": true}`, exit: 1}, false, "exited with 1"},
		{gateOutcome{timedOut: true, exit: -1}, false, "took longer than 5s"},
		{gateOutcome{over: true, exit: -1}, false, "more than 1024 KiB"},
		{gateOutcome{stdout: `{"wake": true}`, cut: true}, false, "max_output_bytes"},
		{gateOutcome{err: os.ErrNotExist}, false, "did not run"},
		{gateOutcome{stdout: "{}"}, false, `no "wake"`},
	} {
		d, why := gateVerdict(g, c.out)
		if d.Wake != c.wake || (c.why == "") != (why == "") || !strings.Contains(why, c.why) {
			t.Errorf("%+v: %+v %q, want wake %v, %q", c.out, d, why, c.wake, c.why)
		}
	}
}

// TestAGateOnANode: a gate with a node runs there, through the node's
// exec and its allowlist; the owner's approval names it, and what it
// finds goes into the brain run's task.
func TestAGateOnANode(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the node runs echo")
	}
	runs := fakeRuns(t)
	h := newHarness(t, `{"nodes": {"enabled": true}}`)
	addr := h.StartNodes()
	dir := h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
	node := h.execNode("laptop", addr, []nodeproto.ExecRule{{Name: "echo"}})
	h.approveNode(node)
	h.WaitFor("the node running commands", func() bool { return len(h.b.execNodeViews()) == 1 })

	writeAgentFile(t, dir, "ci", `{"when": "every 10m", "prompt": "Look at what turned red.", "report": "silent", "gate": {"node": "laptop", "argv": ["echo", "{\"wake\": true, \"data\": {\"failed\": \"build-17\"}}"], "timeout_seconds": 20}}`)
	a := h.agent("main", "ci")
	if a.Review == nil || !slices.Contains(a.Risky, `runs a program on node laptop before each run, as its gate: echo {"wake": true, "data": {"failed": "build-17"}}`) {
		t.Fatalf("the review: %+v %q", a.Review, a.Risky)
	}
	var facts []string
	for _, it := range h.b.waiting(context.Background()) {
		if it.kind == pendingSchedule && it.msg.Ref == "main/ci" {
			facts = it.msg.Facts
		}
	}
	if !slices.ContainsFunc(facts, func(f string) bool {
		return strings.HasPrefix(f, "Gate: echo") && strings.HasSuffix(f, "on node laptop, before each run")
	}) {
		t.Errorf("the approval's facts: %q", facts)
	}
	if w := h.Do("POST", "/api/schedules/main/ci/approve", map[string]any{"schedule": a.Review.Disk}); w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("POST", "/api/schedules/main/ci/run", nil); w.Code != http.StatusOK {
		t.Fatalf("run: %d %s", w.Code, w.Body.String())
	}
	r := h.waitRunEnded("main", "ci", 1)
	if r.Status != sched.StatusOK || r.Node != node {
		t.Fatalf("the run: %+v", r)
	}
	if got := runs(); len(got) != 1 || !strings.Contains(got[0].Prompt, "build-17") {
		t.Errorf("the brain run: %+v", got)
	}
}
