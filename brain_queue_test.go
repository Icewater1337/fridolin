package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// deliveries records what brains hand back to the bridge.
type deliveries struct {
	mu   sync.Mutex
	runs []delivered
}

type delivered struct {
	brain string
	msg   Inbound
	res   RunResult
}

func (d *deliveries) add(br *Brain, m Inbound, res RunResult) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.runs = append(d.runs, delivered{br.ID(), m, res})
}

func (d *deliveries) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.runs)
}

func (d *deliveries) all() []delivered {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]delivered(nil), d.runs...)
}

// queueTestManager builds a manager with inline brains a (DMs) and b (a group)
// running the fake CLI.
func queueTestManager(t *testing.T, sleepMS int, extra string) (*BrainManager, *deliveries, string) {
	t.Helper()
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	claude := fakeClaudeEnv(t, sleepMS)
	cfg, err := LoadConfig(writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data",
		"claude_path": "`+claude+`",
		"brains": [
			{"name": "a", "work_dir": "`+home+`/a", "handles_dms": true},
			{"name": "b", "work_dir": "`+home+`/b", "groups": ["120363000000000000@g.us"]}
		]`+extra+`
	}`))
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestManager(t, cfg)
	d := &deliveries{}
	mgr.SetHooks(BrainHooks{Deliver: d.add})
	t.Cleanup(mgr.Stop)
	return mgr, d, home
}

func dm(text string) Inbound {
	return Inbound{JID: "15551234567@s.whatsapp.net", Sender: "15551234567", Text: text}
}

func TestBrainRunsAndDeliversInOrder(t *testing.T) {
	mgr, d, home := queueTestManager(t, 50, "")
	for _, txt := range []string{"one", "two", "three"} {
		if _, _, _, err := mgr.Submit(dm(txt)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 10*time.Second, "three runs", func() bool { return d.count() == 3 })
	for i, want := range []string{"one", "two", "three"} {
		r := d.all()[i]
		if r.res.Err != nil {
			t.Fatalf("run %d: %v", i, r.res.Err)
		}
		if !strings.HasSuffix(r.res.Text, "says: "+want) || !strings.Contains(r.res.Text, filepath.Join(home, "a")) {
			t.Errorf("run %d = %q, want an echo of %q from brain a's folder", i, r.res.Text, want)
		}
	}
	hist, _ := os.ReadFile(filepath.Join(home, "a", "conversation-history.jsonl"))
	if strings.Count(string(hist), `"type":"in"`) != 3 || strings.Count(string(hist), `"type":"out"`) != 3 {
		t.Errorf("history:\n%s", hist)
	}
}

func TestBusyReportedAndQueuePosition(t *testing.T) {
	mgr, d, _ := queueTestManager(t, 400, "")
	_, _, busy, _ := mgr.Submit(dm("first"))
	if busy {
		t.Error("an idle brain reported busy")
	}
	waitFor(t, 5*time.Second, "first run to start", func() bool { return mgr.Get("a").Snapshot().Running != nil })
	_, pos, busy, _ := mgr.Submit(dm("second"))
	if !busy || pos != 1 {
		t.Errorf("second: busy=%v pos=%d, want busy at #1", busy, pos)
	}
	waitFor(t, 10*time.Second, "both runs", func() bool { return d.count() == 2 })
}

func TestStopKillsTheRunningProcess(t *testing.T) {
	// The private fork's queue never recorded the current run, so !stop and
	// !reset could not kill it. Regression test.
	mgr, d, _ := queueTestManager(t, 30000, "")
	_, _, _, _ = mgr.Submit(dm("long task"))
	_, _, _, _ = mgr.Submit(dm("queued"))
	br := mgr.Get("a")
	waitFor(t, 5*time.Second, "run with a pid", func() bool {
		rt := br.Snapshot()
		return rt.Running != nil && rt.Running.PID > 0
	})
	start := time.Now()
	if !br.StopCurrent(true) {
		t.Fatal("StopCurrent reported nothing running")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("stopping took too long — the process was not killed")
	}
	waitFor(t, 5*time.Second, "interrupted delivery", func() bool { return d.count() == 1 })
	if r := d.all()[0]; r.res.Err == nil || !strings.Contains(r.res.Err.Error(), "interrupted") {
		t.Errorf("result = %+v, want interrupted", r.res)
	}
	time.Sleep(200 * time.Millisecond)
	if d.count() != 1 || br.Snapshot().Running != nil {
		t.Error("the cleared queue still ran")
	}
}

func TestInterruptModeSupersedes(t *testing.T) {
	mgr, d, _ := queueTestManager(t, 30000, `, "queue": {"mode": "interrupt"}`)
	_, _, _, _ = mgr.Submit(dm("old"))
	waitFor(t, 5*time.Second, "old run", func() bool { r := mgr.Get("a").Snapshot().Running; return r != nil && r.PID > 0 })
	t.Setenv("WCB_FAKE_SLEEP_MS", "10")
	_, _, _, _ = mgr.Submit(dm("new"))
	waitFor(t, 10*time.Second, "two deliveries", func() bool { return d.count() == 2 })
	runs := d.all()
	if runs[0].res.Err == nil || runs[1].res.Err != nil || !strings.HasSuffix(runs[1].res.Text, "says: new") {
		t.Errorf("runs = %+v", runs)
	}
}

func TestSilentMessagesAreSupersededAndQueueOverflows(t *testing.T) {
	mgr, d, _ := queueTestManager(t, 500, `, "queue": {"max_pending": 2}`)
	g := func(text string, silent bool) Inbound {
		return Inbound{JID: "120363000000000000@g.us", Sender: "15551234567", Text: text, Silent: silent}
	}
	_, _, _, _ = mgr.Submit(g("running", false))
	waitFor(t, 5*time.Second, "run", func() bool { return mgr.Get("b").Snapshot().Running != nil })
	_, _, _, _ = mgr.Submit(g("chatter 1", true))
	_, _, _, _ = mgr.Submit(g("chatter 2", true)) // supersedes chatter 1
	if q := mgr.Get("b").Snapshot().Queue; len(q) != 1 || q[0].Preview != "chatter 2" {
		t.Errorf("queue = %+v, want only the newest silent message", q)
	}
	_, _, _, _ = mgr.Submit(g("question", false)) // supersedes chatter 2 too
	_, _, _, _ = mgr.Submit(g("question 2", false))
	if _, _, _, err := mgr.Submit(g("question 3", false)); err != errQueueFull {
		t.Errorf("err = %v, want queue full", err)
	}
	waitFor(t, 10*time.Second, "runs", func() bool { return d.count() == 3 })
}

func TestReloadKeepsRunningBrainAndRetiresRemoved(t *testing.T) {
	mgr, d, home := queueTestManager(t, 800, "")
	a := mgr.Get("a")
	_, _, _, _ = mgr.Submit(dm("keep going"))
	_, _, _, _ = mgr.Submit(Inbound{JID: "120363000000000000@g.us", Sender: "15551234567", Text: "doomed"})
	waitFor(t, 5*time.Second, "both running", func() bool {
		return a.Snapshot().Running != nil && mgr.Get("b").Snapshot().Running != nil
	})

	// Concurrent routing while reloading must be race-free (run with -race).
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = mgr.BrainFor("120363000000000000@g.us", true)
					_ = mgr.Brains()
				}
			}
		}()
	}

	// Remove b, give a a new model, add c.
	cfg, err := LoadConfig(writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data",
		"claude_path": "`+os.Args[0]+`",
		"brain_model": "new-model",
		"brains": [
			{"name": "a", "work_dir": "`+home+`/a", "handles_dms": true},
			{"name": "c", "work_dir": "`+home+`/c", "groups": ["120363000000000000@g.us"]}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()

	if mgr.Get("a") != a {
		t.Error("an unchanged brain must keep its *Brain (and its running process)")
	}
	if a.settings.Load().model != "new-model" {
		t.Error("settings were not swapped")
	}
	if br := mgr.BrainFor("120363000000000000@g.us", true); br == nil || br.ID() != "c" {
		t.Error("the group must route to the new brain")
	}
	if mgr.Get("b") != nil {
		t.Error("b should be gone")
	}
	waitFor(t, 10*time.Second, "both deliveries", func() bool { return d.count() == 2 })
	for _, r := range d.all() {
		switch r.brain {
		case "a":
			if r.res.Err != nil {
				t.Errorf("a's run must finish normally, got %v", r.res.Err)
			}
		case "b":
			if r.res.Err == nil {
				t.Error("b's run must be interrupted when b is removed")
			}
		}
	}
	if !strings.Contains(mgr.profiler.Profile("a"), filepath.Join(home, "c")) {
		t.Error("a's sandbox profile must deny the new brain c")
	}
}

func TestClaudeArgsIncludeEffortAndTools(t *testing.T) {
	args := claudeArgs(&brainSettings{model: "m", effort: "high", disallowedTools: []string{"WebFetch", "WebSearch"}, dangerously: true}, "hi")
	got := strings.Join(args, " ")
	for _, want := range []string{"--model m", "--effort high", "--disallowed-tools WebFetch,WebSearch", "--dangerously-skip-permissions", "-p hi"} {
		if !strings.Contains(got, want) {
			t.Errorf("args %q missing %q", got, want)
		}
	}
	if !strings.HasSuffix(got, "-p hi") {
		t.Error("the prompt must come last")
	}
}

func TestBuildPromptListsAllAttachments(t *testing.T) {
	br := &Brain{id: "a", workDir: t.TempDir()}
	br.historyFile = filepath.Join(br.workDir, "h.jsonl")
	p := br.buildPrompt(&brainSettings{}, Inbound{JID: "x@s.whatsapp.net", Sender: "1", Text: "file these", Media: []string{"/m/1.jpg", "/m/2.pdf"}})
	if !strings.Contains(p, "2 files were shared together") || !strings.Contains(p, "- /m/2.pdf") {
		t.Errorf("prompt:\n%s", p)
	}
	if got := historyTextWithMedia("hi", []string{"/m/1.jpg"}); got != "hi\n[attachment: /m/1.jpg]" {
		t.Errorf("history text = %q", got)
	}
}

func TestOTPMarkersConfigurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.jsonl")
	_ = os.WriteFile(path, []byte(`{"type":"out","from":"a","text":"Please send the Examplebank code"}`+"\n"), 0o600)
	if recentOutboundLooksLikeOTPRequest(path, "a") {
		t.Error("default markers should not match a bank name")
	}
	if !recentOutboundLooksLikeOTPRequest(path, "a", "Examplebank") {
		t.Error("a configured marker should arm the guard")
	}
}

func TestParseStreamReportsErrorText(t *testing.T) {
	br := &Brain{id: "a", workDir: t.TempDir()}
	stream := `{"type":"assistant","message":{"content":[{"type":"text","text":"Failed to authenticate. API Error: 401"}]}}
{"type":"result","is_error":true}
`
	res := br.parseStream("x", strings.NewReader(stream))
	if res.Err == nil || !strings.Contains(res.Err.Error(), "Failed to authenticate") {
		t.Errorf("err = %v, want the CLI's own message", res.Err)
	}
}

func TestStopKillsProcessesClaudeStarted(t *testing.T) {
	// A command Claude started keeps the output pipe open; stopping must kill
	// the whole process group, not just the CLI, or the stop hangs.
	mgr, d, _ := queueTestManager(t, 30000, "")
	t.Setenv("WCB_FAKE_CHILD", "1")
	_, _, _, _ = mgr.Submit(dm("long task with a child"))
	br := mgr.Get("a")
	waitFor(t, 5*time.Second, "run with a pid", func() bool {
		rt := br.Snapshot()
		return rt.Running != nil && rt.Running.PID > 0
	})
	time.Sleep(200 * time.Millisecond) // let the child start
	start := time.Now()
	br.StopCurrent(false)
	waitFor(t, 4*time.Second, "interrupted delivery", func() bool { return d.count() == 1 })
	if el := time.Since(start); el > 3*time.Second {
		t.Errorf("stop took %v — the child kept the run alive", el)
	}
}
