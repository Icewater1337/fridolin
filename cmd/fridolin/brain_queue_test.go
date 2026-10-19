package main

import (
	"encoding/json"
	"errors"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"log"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/logx"
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

// queueTestManager builds a manager with folder brains a (DMs) and b (a
// group) running the fake CLI. b observes its group with runs, so that
// untagged chatter there queues like the rest.
func queueTestManager(t *testing.T, sleepMS int, extra string) (*BrainManager, *deliveries, string) {
	t.Helper()
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	claude := fakeClaudeEnv(t, sleepMS)
	cfg, err := loadConfig(writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data",
		"claude_path": "`+claude+`"`+brainDirsJSON(t,
		brainFolder(t, home+"/a", manifest.Manifest{ID: "a", Routing: manifest.Routing{HandlesDMs: true}}),
		brainFolder(t, home+"/b", manifest.Manifest{ID: "b", Routing: manifest.Routing{Groups: []string{"120363000000000000@g.us"}}, Observe: manifest.ObserveRun}),
	)+extra+`
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
	return Inbound{Inbound: chat.Inbound{JID: "15551234567@s.whatsapp.net", Sender: "15551234567", Text: text}}
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
	// The answers go into the history once they are delivered, which is
	// the bridge's part (see TestReplyIsRecordedOnceDelivered).
	hist, _ := os.ReadFile(filepath.Join(home, "a", "conversation-history.jsonl"))
	if strings.Count(string(hist), `"type":"in"`) != 3 || strings.Count(string(hist), `"type":"out"`) != 0 {
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

// StopHandOffs stops only what was handed to a brain and answers in the chat:
// not the brain's own work, not hand-offs that answer elsewhere, and not the
// chat's own brain (that one !stop stops with StopChat).
func TestStopHandOffsLeavesOtherWorkAlone(t *testing.T) {
	const (
		chatJID = "15551234567@s.whatsapp.net"
		other   = "120363000000000001@g.us"
		group   = "120363000000000000@g.us"
	)
	handOff := func(jid, text string) Inbound {
		return Inbound{Inbound: chat.Inbound{JID: jid, Brain: "b", Sender: "upload", Text: text}}
	}
	for _, c := range []struct {
		name    string
		running Inbound   // b's run in flight
		queued  []Inbound // waiting behind it
		stopped bool
		dropped int    // hand-offs that were waiting
		left    string // b's work afterwards, running or waiting
	}{
		{"a hand-off answering in the chat is running", handOff(chatJID, "handed"),
			[]Inbound{{Inbound: chat.Inbound{JID: group, Text: "group"}}, handOff(chatJID, "handed again"), handOff(other, "elsewhere")},
			true, 1, "group,elsewhere"},
		{"the brain's own chat is running", Inbound{Inbound: chat.Inbound{JID: group, Text: "group"}},
			[]Inbound{handOff(chatJID, "handed"), handOff(other, "elsewhere")},
			false, 1, "group,elsewhere"},
	} {
		t.Run(c.name, func(t *testing.T) {
			mgr, _, _ := queueTestManager(t, 30000, "")
			a, b := mgr.Get("a"), mgr.Get("b")
			_, _, _, _ = mgr.Submit(dm("a's own"))
			_, _, _, _ = mgr.Submit(c.running)
			waitFor(t, 5*time.Second, "both runs", func() bool {
				ra, rb := a.Snapshot().Running, b.Snapshot().Running
				return ra != nil && ra.PID > 0 && rb != nil && rb.PID > 0
			})
			for _, m := range c.queued {
				if _, _, _, err := mgr.Submit(m); err != nil {
					t.Fatal(err)
				}
			}

			if stopped, dropped := mgr.StopHandOffs(chatJID); stopped != c.stopped || dropped != c.dropped {
				t.Errorf("StopHandOffs = %v, %d, want %v, %d", stopped, dropped, c.stopped, c.dropped)
			}
			rt := b.Snapshot()
			var left []string
			if rt.Running != nil {
				left = append(left, rt.Running.Preview)
			}
			for _, q := range rt.Queue {
				left = append(left, q.Preview)
			}
			if got := strings.Join(left, ","); got != c.left {
				t.Errorf("b's work left = %q, want %q", got, c.left)
			}
			if a.Snapshot().Running == nil {
				t.Error("a's own run in the chat was stopped")
			}
		})
	}
}

func TestInterruptModeSupersedes(t *testing.T) {
	mgr, d, _ := queueTestManager(t, 30000, `, "queue": {"mode": "interrupt"}`)
	_, _, _, _ = mgr.Submit(dm("old"))
	waitFor(t, 5*time.Second, "old run", func() bool { r := mgr.Get("a").Snapshot().Running; return r != nil && r.PID > 0 })
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "10")
	_, _, _, _ = mgr.Submit(dm("new"))
	waitFor(t, 10*time.Second, "two deliveries", func() bool { return d.count() == 2 })
	runs := d.all()
	if runs[0].res.Err == nil || runs[1].res.Err != nil || !strings.HasSuffix(runs[1].res.Text, "says: new") {
		t.Errorf("runs = %+v", runs)
	}
}

// In interrupt mode only a request from the same person in the same chat
// replaces the run in flight, and untagged chatter never interrupts a run:
// chatter has no answer to give, and the request it killed would never be
// answered either.
func TestInterruptModeOnlyReplacesTheSendersOwnRun(t *testing.T) {
	const group = "120363000000000000@g.us" // brain b's group
	g := func(sender, text string, silent bool) Inbound {
		return Inbound{Inbound: chat.Inbound{JID: group, Sender: sender, Text: text, Silent: silent}}
	}
	otherDM := Inbound{Inbound: chat.Inbound{JID: "15559876543@s.whatsapp.net", Sender: "15559876543", Text: "unrelated"}} // also brain a
	// ingest is a file handed to brain a through /ingest, answered in the DM.
	ingest := func(sender, text string) Inbound {
		return Inbound{Inbound: chat.Inbound{JID: "15551234567@s.whatsapp.net", Sender: sender, Text: text, Source: chat.SourceIngest}}
	}
	cases := []struct {
		name          string
		running, next Inbound
		waiting       []Inbound // queued before next arrives
		interrupts    bool
		queue         []string // what waits afterwards, if not just next
	}{
		{"a newer request from the same person", dm("old"), dm("new"), nil, true, nil},
		{"untagged chatter after a request", g("15551234567", "question", false), g("15559876543", "lol nice", true), nil, false, nil},
		{"the asker's own chatter", g("15551234567", "question", false), g("15551234567", "brb", true), nil, false, nil},
		{"a request after untagged chatter", g("15551234567", "chatter", true), g("15559876543", "question", false), nil, true, nil},
		{"another member's request", g("15551234567", "question", false), g("15559876543", "other question", false), nil, false, nil},
		{"a request from another chat", dm("old"), otherDM, nil, false, nil},
		// The newer request replaces the earlier one in the queue; the run
		// of the other chat is left alone.
		{"a newer request while the earlier one waits", otherDM, dm("q1 again"), []Inbound{dm("q1")}, false, nil},
		{"a second ingested file", ingest("plaud", "recording 1"), ingest("plaud", "recording 2"), nil, false, nil},
		{"a second ingested file while the first waits", otherDM, ingest("plaud", "recording 2"), []Inbound{ingest("plaud", "recording 1")}, false, []string{"recording 1", "recording 2"}},
		{"a request after a file ingested in the sender's name", ingest("15551234567", "notes"), dm("new"), nil, false, nil},
		{"a file ingested in the sender's name after their request", dm("question"), ingest("15551234567", "notes"), nil, false, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mgr, d, _ := queueTestManager(t, 30000, `, "queue": {"mode": "interrupt"}`)
			br, _, _, _ := mgr.Submit(c.running)
			waitFor(t, 5*time.Second, "the first run", func() bool { r := br.Snapshot().Running; return r != nil && r.PID > 0 })
			pid := br.Snapshot().Running.PID
			t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "10")
			for _, w := range c.waiting {
				if _, _, _, err := mgr.Submit(w); err != nil {
					t.Fatal(err)
				}
			}
			_, pos, busy, err := mgr.Submit(c.next)
			if err != nil {
				t.Fatal(err)
			}

			if c.interrupts {
				if busy {
					t.Errorf("busy = true at #%d, want the message to take over the run", pos)
				}
				waitFor(t, 10*time.Second, "two deliveries", func() bool { return d.count() == 2 })
				runs := d.all()
				if !errors.Is(runs[0].res.Err, errBrainInterrupted) || runs[1].res.Err != nil || !strings.HasSuffix(runs[1].res.Text, "says: "+c.next.Text) {
					t.Errorf("runs = %+v, want the first interrupted and the second answered", runs)
				}
				return
			}
			want := c.queue
			if want == nil {
				want = []string{c.next.Text}
			}
			if !busy || pos != len(want) {
				t.Errorf("busy = %v at #%d, want it waiting at #%d", busy, pos, len(want))
			}
			time.Sleep(300 * time.Millisecond)
			rt := br.Snapshot()
			if rt.Running == nil || rt.Running.PID != pid || d.count() != 0 {
				t.Errorf("running = %+v after %d deliveries, want the first run untouched", rt.Running, d.count())
			}
			var queued []string
			for _, q := range rt.Queue {
				queued = append(queued, q.Preview)
			}
			if strings.Join(queued, "|") != strings.Join(want, "|") {
				t.Errorf("queue = %q, want %q", queued, want)
			}
		})
	}
}

// A request that interrupts its sender's own run drops only what that sender
// asked for before and the chat's chatter; other people, and other chats
// served by the same brain, keep their place.
func TestInterruptModeKeepsOthersWaiting(t *testing.T) {
	g := func(sender, text string, silent bool) Inbound {
		return Inbound{Inbound: chat.Inbound{JID: "120363000000000000@g.us", Sender: sender, Text: text, Silent: silent}}
	}
	otherDM := func(text string) Inbound { // brain a, like dm
		return Inbound{Inbound: chat.Inbound{JID: "15559876543@s.whatsapp.net", Sender: "15559876543", Text: text}}
	}
	cases := []struct {
		name    string
		running Inbound
		waiting []Inbound
		next    Inbound
		want    []string // the answered runs after the interrupted one
	}{
		{"another member of the group", g("15551234567", "q1", false),
			[]Inbound{g("15559876543", "q2", false), g("15559876543", "chatter", true)},
			g("15551234567", "q1 again", false), []string{"q2", "q1 again"}},
		{"another chat of the same brain", dm("old"),
			[]Inbound{otherDM("unrelated")},
			dm("new"), []string{"unrelated", "new"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mgr, d, _ := queueTestManager(t, 30000, `, "queue": {"mode": "interrupt"}`)
			br, _, _, _ := mgr.Submit(c.running)
			waitFor(t, 5*time.Second, "the first run", func() bool { r := br.Snapshot().Running; return r != nil && r.PID > 0 })
			t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "10")
			for _, w := range c.waiting {
				_, _, _, _ = mgr.Submit(w)
			}
			_, pos, busy, _ := mgr.Submit(c.next)
			if !busy || pos != 2 {
				t.Errorf("busy = %v at #%d, want it waiting behind %q at #2", busy, pos, c.want[0])
			}

			waitFor(t, 10*time.Second, "three deliveries", func() bool { return d.count() == 3 })
			runs := d.all()
			if !errors.Is(runs[0].res.Err, errBrainInterrupted) {
				t.Errorf("%s: %+v, want it interrupted", c.running.Text, runs[0].res)
			}
			for i, want := range c.want {
				if r := runs[i+1]; r.res.Err != nil || !strings.HasSuffix(r.res.Text, "says: "+want) {
					t.Errorf("run %d = %+v, want %q answered", i+1, r.res, want)
				}
			}
		})
	}
}

// max_pending bounds the queue in interrupt mode too: requests from other
// chats replace nothing, so they can fill it.
func TestInterruptModeQueueIsBounded(t *testing.T) {
	mgr, _, _ := queueTestManager(t, 30000, `, "queue": {"mode": "interrupt", "max_pending": 2}`)
	br := mgr.Get("a")
	if _, _, err := br.Submit(dm("long task")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "the first run", func() bool { r := br.Snapshot().Running; return r != nil && r.PID > 0 })
	for _, jid := range []string{"120363000000000001@g.us", "120363000000000002@g.us"} {
		if _, _, err := br.Submit(Inbound{Inbound: chat.Inbound{JID: jid, Sender: "15559876543", Text: "question"}}); err != nil {
			t.Fatalf("%s: %v", jid, err)
		}
	}
	_, _, err := br.Submit(Inbound{Inbound: chat.Inbound{JID: "120363000000000003@g.us", Sender: "15559876543", Text: "one more"}})
	if !errors.Is(err, errQueueFull) {
		t.Errorf("err = %v, want the queue full", err)
	}
}

func TestSilentMessagesAreSupersededAndQueueOverflows(t *testing.T) {
	mgr, d, _ := queueTestManager(t, 500, `, "queue": {"max_pending": 2}`)
	g := func(text string, silent bool) Inbound {
		return Inbound{Inbound: chat.Inbound{JID: "120363000000000000@g.us", Sender: "15551234567", Text: text, Silent: silent}}
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
	mgr, d, home := queueTestManager(t, 0, "")
	// Keep both real child processes running until reload has retired b.
	// A fixed sleep lets them finish before a loaded runner applies config.
	release := filepath.Join(home, "release-runs")
	t.Setenv("FRIDOLIN_FAKE_WAIT_FILE", release)
	a, b := mgr.Get("a"), mgr.Get("b")
	_, _, _, _ = mgr.Submit(dm("keep going"))
	_, _, _, _ = mgr.Submit(Inbound{Inbound: chat.Inbound{JID: "120363000000000000@g.us", Sender: "15551234567", Text: "doomed"}})
	waitFor(t, 5*time.Second, "both processes running", func() bool {
		ar, br := a.Snapshot().Running, b.Snapshot().Running
		return ar != nil && ar.PID > 0 && br != nil && br.PID > 0
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
	cfg, err := loadConfig(writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data",
		"claude_path": "`+os.Args[0]+`",
		"brain_model": "new-model"`+brainDirsJSON(t,
		brainFolder(t, home+"/a", manifest.Manifest{ID: "a", Routing: manifest.Routing{HandlesDMs: true}}),
		brainFolder(t, home+"/c", manifest.Manifest{ID: "c", Routing: manifest.Routing{Groups: []string{"120363000000000000@g.us"}}}),
	)+`
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
	waitFor(t, 10*time.Second, "the removed brain's delivery", func() bool { return d.count() == 1 })
	if run := a.Snapshot().Running; run == nil || run.PID == 0 {
		t.Fatal("a's process must stay running through the reload")
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
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
	if !strings.Contains(mustProfile(t, mgr.profiler, "a"), filepath.Join(home, "c")) {
		t.Error("a's sandbox profile must deny the new brain c")
	}
}

// What a brain had waiting when a reload took it out goes on: in the brain
// in its new folder, or in the brain its chat goes to once it is removed.
// Its history line is there once, whether the new folder has the old
// history (moved) or not (a copy, or another brain).
func TestReloadHandsOnWhatWaited(t *testing.T) {
	tests := []struct {
		name string
		// reload changes the brains, the DM brain a among them, and
		// returns the folder whose brain should answer.
		reload func(t *testing.T, home string) (brains []config.BrainRef, answers string)
	}{
		{"moved", func(t *testing.T, home string) ([]config.BrainRef, string) {
			if err := os.Rename(filepath.Join(home, "a"), filepath.Join(home, "a-moved")); err != nil {
				t.Fatal(err)
			}
			return []config.BrainRef{
				brainFolder(t, home+"/a-moved", manifest.Manifest{ID: "a", Routing: manifest.Routing{HandlesDMs: true}}),
				brainFolder(t, home+"/b", manifest.Manifest{ID: "b", Routing: manifest.Routing{Default: true}}),
			}, filepath.Join(home, "a-moved")
		}},
		{"another folder", func(t *testing.T, home string) ([]config.BrainRef, string) {
			return []config.BrainRef{
				brainFolder(t, home+"/a-copy", manifest.Manifest{ID: "a", Routing: manifest.Routing{HandlesDMs: true}}),
				brainFolder(t, home+"/b", manifest.Manifest{ID: "b", Routing: manifest.Routing{Default: true}}),
			}, filepath.Join(home, "a-copy")
		}},
		{"removed", func(t *testing.T, home string) ([]config.BrainRef, string) {
			return []config.BrainRef{
				brainFolder(t, home+"/b", manifest.Manifest{ID: "b", Routing: manifest.Routing{Default: true}}),
			}, filepath.Join(home, "b")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := fsx.Canonical(t.TempDir())
			t.Setenv("HOME", home)
			claude := fakeClaudeEnv(t, 30000)
			configValue := func(refs ...config.BrainRef) *config.Config {
				cfg, err := loadConfig(writeConfig(t, `{
					"data_dir": "`+home+`/data",
					"claude_path": "`+claude+`"`+brainDirsJSON(t, refs...)+`
				}`))
				if err != nil {
					t.Fatal(err)
				}
				return cfg
			}
			mgr := newTestManager(t, configValue(
				brainFolder(t, home+"/a", manifest.Manifest{ID: "a", Routing: manifest.Routing{HandlesDMs: true}}),
				brainFolder(t, home+"/b", manifest.Manifest{ID: "b", Routing: manifest.Routing{Default: true}}),
			))
			d := &deliveries{}
			mgr.SetHooks(BrainHooks{Deliver: d.add})
			t.Cleanup(mgr.Stop)
			if _, _, _, err := mgr.Submit(dm("first")); err != nil {
				t.Fatal(err)
			}
			hasRun(t, mgr.Get("a"))
			t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "10")
			if _, _, _, err := mgr.Submit(dm("second")); err != nil {
				t.Fatal(err)
			}

			refs, answers := tc.reload(t, home)
			if err := mgr.Apply(configValue(refs...)); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 10*time.Second, "both runs to end", func() bool { return d.count() == 2 })
			for _, r := range d.all() {
				switch r.msg.Text {
				case "first":
					if !errors.Is(r.res.Err, errBrainInterrupted) {
						t.Errorf("first: %q %v, want its run stopped", r.res.Text, r.res.Err)
					}
				case "second":
					if r.res.Err != nil || !strings.HasPrefix(r.res.Text, "echo:"+answers+":") {
						t.Errorf("second: %q %v, want it answered in %s", r.res.Text, r.res.Err, answers)
					}
				}
			}
			var lines int
			for _, l := range historyLines(t, answers) {
				if strings.Contains(l, `"text":"second"`) {
					lines++
				}
			}
			if lines != 1 {
				t.Errorf("the history in %s has %d lines for the second message, want one:\n%s", answers, lines, strings.Join(historyLines(t, answers), "\n"))
			}
		})
	}
}

// A reload runs the manifests you approved: brain b that gives itself the
// DMs in its own brain.json keeps its routing, and the change shows as
// drift until you approve it.
func TestReloadRunsTheApprovedManifest(t *testing.T) {
	const dmChat = "15551234567@s.whatsapp.net"
	tb := newTestBridge(t, "")
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A", Routing: manifest.Routing{HandlesDMs: true}})
	dir := tb.addFolderBrain(t, manifest.Manifest{ID: "b", Name: "B"})
	grab := manifest.Manifest{ID: "b", Name: "B", Routing: manifest.Routing{HandlesDMs: true}}
	if err := manifest.Write(dir, &grab, true); err != nil {
		t.Fatal(err)
	}
	if err := tb.brainMgr.Apply(tb.cfgs.Get()); err != nil {
		t.Fatal(err)
	}
	if br := tb.brainMgr.BrainFor(dmChat, false); br == nil || br.ID() != "a" {
		t.Errorf("DMs go to %v, want a", br)
	}
	if b := tb.brainMgr.Get("b"); b == nil || b.Snapshot().Routing.HandlesDMs {
		t.Errorf("b = %v, want it running without the DMs", b)
	}
	drift := false
	for _, is := range tb.brainMgr.Issues() {
		drift = drift || is.ID == "b" && is.Kind == "drift"
	}
	if !drift {
		t.Errorf("issues = %+v, want b's drift", tb.brainMgr.Issues())
	}
}

// reload refuses a brain set that clashes and leaves the brains as they
// were. ResolveBrains leaves such brains out, so this guards any other way
// a set reaches the manager.
func TestReloadRefusesAClashingSet(t *testing.T) {
	const group = "120363000000000000@g.us"
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	cfg, err := loadConfig(writeConfig(t, `{"data_dir": "`+home+`/data"`+brainDirsJSON(t,
		brainFolder(t, "~/a", manifest.Manifest{ID: "a", Routing: manifest.Routing{HandlesDMs: true}}),
		brainFolder(t, "~/b", manifest.Manifest{ID: "b", Routing: manifest.Routing{Groups: []string{group}}}),
	)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestManager(t, cfg)
	t.Cleanup(mgr.Stop)
	a, b := mgr.Get("a"), mgr.Get("b")
	specs, issues := ResolveBrains(cfg)
	if len(specs) != 2 || len(issues) != 0 {
		t.Fatalf("specs = %+v, issues = %+v", specs, issues)
	}
	specs[1].Manifest.Routing.HandlesDMs = true
	if err := mgr.reload(specs, issues, cfg, nil); err == nil || !strings.Contains(err.Error(), "both handle DMs") {
		t.Errorf("reload = %v, want the clash refused", err)
	}
	if mgr.Get("a") != a || mgr.Get("b") != b || mgr.BrainFor("15551234567@s.whatsapp.net", false) != a || mgr.BrainFor(group, true) != b {
		t.Error("a refused reload changed the brains or their routing")
	}
}

// TestClaudeArgs: a brain loads only its own folder's Claude Code setup,
// with the folder's .mcp.json passed explicitly and the instructions above
// it excluded, unless inherit_claude_user_config asks for the old
// behaviour. The other settings map to their flags, and -p comes last,
// without the prompt, which goes on stdin.
func TestClaudeArgs(t *testing.T) {
	const mcp = "/brains/a/.mcp.json"
	const settings = `{"claudeMdExcludes":["/brains/CLAUDE.md"]}`
	cases := []struct {
		name                string
		s                   brainSettings
		mcpConfig, settings string
		want, dontWant      []string
	}{
		{"own setup only", brainSettings{}, "", "",
			[]string{"--setting-sources project,local", "--strict-mcp-config"},
			[]string{"--settings", "--mcp-config", "--dangerously-skip-permissions", "--model", "--effort", "--disallowed-tools"}},
		{"with the folder's .mcp.json", brainSettings{}, mcp, "",
			[]string{"--setting-sources project,local", "--strict-mcp-config --mcp-config " + mcp}, nil},
		{"with the instructions above excluded", brainSettings{}, mcp, settings,
			[]string{"--setting-sources project,local", "--settings " + settings, "--mcp-config " + mcp}, nil},
		{"inherit_claude_user_config", brainSettings{inheritUser: true}, mcp, settings,
			nil, []string{"--setting-sources", "--strict-mcp-config", "--settings", "--mcp-config"}},
		{"model, effort, tools, permissions", brainSettings{model: "m", effort: "high", disallowedTools: []string{"WebFetch", "WebSearch"}, dangerously: true}, mcp, settings,
			[]string{"--model m", "--effort high", "--disallowed-tools WebFetch,WebSearch", "--dangerously-skip-permissions"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := claudeArgs(&tc.s, tc.mcpConfig, tc.settings)
			got := strings.Join(args, " ")
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("args %q lack %q", got, w)
				}
			}
			for _, w := range tc.dontWant {
				if strings.Contains(got, w) {
					t.Errorf("args %q have %q", got, w)
				}
			}
			if !strings.HasSuffix(got, " -p") {
				t.Errorf("args %q: -p must come last, the prompt goes on stdin", got)
			}
			// --mcp-config takes several files, so the CLI would take
			// anything after the file that is not an option for another.
			if i := slices.Index(args, "--mcp-config"); i >= 0 && !strings.HasPrefix(args[i+2], "-") {
				t.Errorf("args %q: %q follows --mcp-config's file", got, args[i+2])
			}
		})
	}
}

// The prompt reaches the CLI on stdin, not in its arguments, where every
// process listing shows it and ARG_MAX bounds it: a message of a megabyte
// and more, a few long pasted texts, still runs.
func TestPromptGoesOnStdin(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"a short message", "MSG-ON-STDIN"},
		{"over ARG_MAX", "MSG-ON-STDIN " + strings.Repeat("all of this was pasted in. ", 50000)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mgr, d, home := queueTestManager(t, 0, "")
			argsLog := filepath.Join(home, "args.log")
			t.Setenv("FRIDOLIN_FAKE_ARGS_LOG", argsLog)
			if _, _, _, err := mgr.Submit(dm(c.text)); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 10*time.Second, "the run", func() bool { return d.count() == 1 })
			if res := d.all()[0].res; res.Err != nil || !strings.HasSuffix(res.Text, "says: "+c.text) {
				t.Errorf("run = %v, %.80q; want the message echoed from stdin", res.Err, res.Text)
			}
			args, _ := os.ReadFile(argsLog)
			if strings.Contains(string(args), "MSG-ON-STDIN") || !strings.HasSuffix(strings.TrimSpace(string(args)), " -p") {
				t.Errorf("args = %.200q, want them to end in -p, without the prompt", args)
			}
		})
	}
}

// TestInstructionsAbove: the claudeMdExcludes for a brain name the
// instruction files of every folder above it, up to /, in each spelling the
// CLI may meet them, and your user memory for a brain outside your home
// folder too, but nothing of the brain's own folder.
func TestInstructionsAbove(t *testing.T) {
	mkdir := func(t *testing.T, p string) {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name string
		// layout makes the folders below root and returns the brain's
		// folder and HOME.
		layout func(t *testing.T, root string) (dir, home string)
		// folders above the brain whose four patterns must be there, and
		// patterns that must not, below root.
		above, without []string
	}{
		{"a brain in the home folder", func(t *testing.T, root string) (string, string) {
			mkdir(t, filepath.Join(root, "home", "brains", "a"))
			return filepath.Join(root, "home", "brains", "a"), filepath.Join(root, "home")
		}, []string{"home/brains", "home", ""},
			[]string{"home/brains/a/CLAUDE.md", "home/brains/a/CLAUDE.local.md", "home/brains/a/.claude/CLAUDE.md", "home/brains/a/.claude/rules/**"}},
		{"a brain outside the home folder", func(t *testing.T, root string) (string, string) {
			mkdir(t, filepath.Join(root, "home"))
			mkdir(t, filepath.Join(root, "elsewhere", "a"))
			return filepath.Join(root, "elsewhere", "a"), filepath.Join(root, "home")
		}, []string{"elsewhere", ""},
			// Only the user memory of the home folder: no brain loads the rest.
			[]string{"home/CLAUDE.md", "home/CLAUDE.local.md"}},
		{"a brains folder linked from another volume", func(t *testing.T, root string) (string, string) {
			mkdir(t, filepath.Join(root, "volume", "brains", "a"))
			mkdir(t, filepath.Join(root, "home"))
			if err := os.Symlink(filepath.Join(root, "volume", "brains"), filepath.Join(root, "home", "brains")); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(root, "home", "brains", "a"), filepath.Join(root, "home")
		}, []string{"home/brains", "home", "volume/brains", "volume", ""}, nil},
		{"folders with glob characters", func(t *testing.T, root string) (string, string) {
			dir := filepath.Join(root, "home", "notes (old) [1] {x,y}", "*?!+@|", "a")
			mkdir(t, dir)
			return dir, filepath.Join(root, "home")
		}, []string{"home/notes (old) [1] {x,y}/*?!+@|", "home/notes (old) [1] {x,y}", "home", ""}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := fsx.Canonical(t.TempDir())
			dir, home := tc.layout(t, root)
			got := instructionsAbove(dir, home)
			for _, p := range got {
				if !strings.HasPrefix(p, "/") {
					t.Errorf("relative pattern %q", p)
				}
			}
			for _, f := range tc.above {
				d := globEscape(filepath.Join(root, f))
				for _, want := range []string{"CLAUDE.md", "CLAUDE.local.md", ".claude/CLAUDE.md", ".claude/rules/**"} {
					if want = filepath.Join(d, want); !slices.Contains(got, want) {
						t.Errorf("patterns lack %q:\n%s", want, strings.Join(got, "\n"))
					}
				}
			}
			for _, f := range []string{"/CLAUDE.md", "/.claude/rules/**", filepath.Join(globEscape(home), ".claude", "CLAUDE.md"), filepath.Join(globEscape(home), ".claude", "rules", "**")} {
				if !slices.Contains(got, f) {
					t.Errorf("patterns lack %q", f)
				}
			}
			for _, f := range tc.without {
				if p := filepath.Join(globEscape(root), f); slices.Contains(got, p) {
					t.Errorf("patterns have %q", p)
				}
			}
		})
	}
}

// TestGlobEscape: a path becomes a picomatch pattern that matches it alone.
// Checked against CLI 2.1.302 with a parent folder named after each case:
// its CLAUDE.md files and rules stayed out with the escaped pattern, and
// without it "{x,y}" and "@(y)" let the rules through. The characters in
// the last case it matched unescaped.
func TestGlobEscape(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/Users/you/brains", "/Users/you/brains"},
		{"/Users/you/notes (old)", "/Users/you/notes [(]old[)]"},
		{"/x/[a]{b,c}", "/x/[[]a[]][{]b,c[}]"},
		{"/x/b]x/b[x", "/x/b[]]x/b[[]x"},
		{"/x/*?!+@|", "/x/[*][?][!][+][@][|]"},
		{"/x/!(y)+(y)@(y)", "/x/[!][(]y[)][+][(]y[)][@][(]y[)]"},
		{"/x/^$.#%&~,=;'\"` é", "/x/^$.#%&~,=;'\"` é"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := globEscape(tc.path); got != tc.want {
				t.Errorf("globEscape(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// TestBrainRunsWithItsOwnClaudeSetup: a brain's run gets the flags that
// keep your Claude Code setup out, the instructions above its folder
// excluded, and its folder's .mcp.json only when the CLI would start with
// it (see TestMCPConfigOf). inherit_claude_user_config drops them all
// again.
func TestBrainRunsWithItsOwnClaudeSetup(t *testing.T) {
	write := func(body string) func(t *testing.T, p string) {
		return func(t *testing.T, p string) {
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	isolated := "--setting-sources project,local --strict-mcp-config"
	cases := []struct {
		name, extra    string
		mcp            func(t *testing.T, p string) // lays out the brain's .mcp.json at p
		want, dontWant []string
	}{
		{"no .mcp.json", "", nil, []string{isolated + " --settings <settings>"}, []string{"--mcp-config"}},
		{"a .mcp.json", "", write(`{"mcpServers": {}}`), []string{isolated + " --settings <settings> --mcp-config <mcp>"}, nil},
		{"a .mcp.json that is not JSON", "", write(`{"mcpServers": `), []string{isolated}, []string{"--mcp-config"}},
		{"a symlinked .mcp.json", "", func(t *testing.T, p string) {
			target := filepath.Join(t.TempDir(), "servers.json")
			write(`{"mcpServers": {}}`)(t, target)
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
		}, []string{isolated}, []string{"--mcp-config"}},
		{"a .mcp.json folder", "", func(t *testing.T, p string) {
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
		}, []string{isolated}, []string{"--mcp-config"}},
		{"a .mcp.json with a server turned off", "", func(t *testing.T, p string) {
			write(`{"mcpServers": {"keep": {"command": "k"}, "off": {"command": "o"}}}`)(t, p)
			if err := os.Mkdir(filepath.Join(filepath.Dir(p), ".claude"), 0o700); err != nil {
				t.Fatal(err)
			}
			write(`{"disabledMcpjsonServers": ["off"]}`)(t, filepath.Join(filepath.Dir(p), ".claude", "settings.local.json"))
		}, []string{isolated + ` --settings <settings> --mcp-config {"mcpServers":{"keep":{"command":"k"}}}`}, []string{"<mcp>"}},
		{"inherit_claude_user_config", `, "inherit_claude_user_config": true`, write(`{"mcpServers": {}}`),
			nil, []string{"--setting-sources", "--strict-mcp-config", "--settings", "--mcp-config"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr, d, home := queueTestManager(t, 0, tc.extra)
			argsLog := filepath.Join(home, "args.log")
			t.Setenv("FRIDOLIN_FAKE_ARGS_LOG", argsLog)
			mcp := filepath.Join(home, "a", ".mcp.json")
			if tc.mcp != nil {
				tc.mcp(t, mcp)
			}
			if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 10*time.Second, "the run", func() bool { return d.count() == 1 })
			if err := d.all()[0].res.Err; err != nil {
				t.Fatalf("run: %v", err)
			}
			data, err := os.ReadFile(argsLog)
			if err != nil {
				t.Fatal(err)
			}
			got := string(data)
			fill := strings.NewReplacer("<mcp>", mcp, "<settings>", excludeInstructionsAbove(filepath.Join(home, "a"))).Replace
			for _, w := range tc.want {
				if w = fill(w); !strings.Contains(got, w) {
					t.Errorf("args %q lack %q", got, w)
				}
			}
			for _, w := range tc.dontWant {
				if w = fill(w); strings.Contains(got, w) {
					t.Errorf("args %q have %q", got, w)
				}
			}
		})
	}
}

// TestMCPConfigOf: the brain's .mcp.json is passed only when the CLI starts
// with it as an --mcp-config file, which it does not for anything but a
// JSON object with an "mcpServers" object (checked against CLI 2.1.302),
// and the bridge log says why it left one out. The bridge reads the file
// outside the sandbox, so it follows no symlink and reads no FIFO, folder
// or huge file.
func TestMCPConfigOf(t *testing.T) {
	write := func(body string) func(t *testing.T, p string) {
		return func(t *testing.T, p string) {
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	cases := []struct {
		name    string
		mcp     func(t *testing.T, p string) // lays out the .mcp.json at p
		pass    bool
		wantLog string // in the log line when it is left out
	}{
		{"no .mcp.json", func(*testing.T, string) {}, false, ""},
		{"no servers", write(`{"mcpServers": {}}`), true, ""},
		// The CLI starts anyway and reports the server as failed.
		{"broken servers", write(`{"mcpServers": {"x": 5, "y": [], "z": {"type": "bogus"}}}`), true, ""},
		{"other keys too", write(`{"mcpServers": {}, "other": 1}`), true, ""},
		{"byte order mark", write("\uFEFF" + `{"mcpServers": {}}`), true, ""},
		{"not JSON", write(`{"mcpServers": `), false, "not a JSON object"},
		{"trailing text", write(`{"mcpServers": {}} x`), false, "not a JSON object"},
		{"empty object", write(`{}`), false, `no "mcpServers" object`},
		{"array", write(`[]`), false, "not a JSON object"},
		{"null", write(`null`), false, "not a JSON object"},
		{"mcpServers an array", write(`{"mcpServers": []}`), false, "mcpServers is not an object"},
		{"mcpServers null", write(`{"mcpServers": null}`), false, `no "mcpServers" object`},
		{"misspelt key", write(`{"mcpServer": {}}`), false, `no "mcpServers" object`},
		{"key in other case", write(`{"mcpservers": {}}`), false, `no "mcpServers" object`},
		// Like the CLI, the last of two equal keys wins.
		{"key given twice", write(`{"mcpServers": {}, "mcpServers": null}`), false, `no "mcpServers" object`},
		{"symlink", func(t *testing.T, p string) {
			target := filepath.Join(t.TempDir(), "servers.json")
			write(`{"mcpServers": {}}`)(t, target)
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
		}, false, "symlink"},
		// Even one that stays in the brain's folder.
		{"symlink inside the folder", func(t *testing.T, p string) {
			target := filepath.Join(filepath.Dir(p), "servers.json")
			write(`{"mcpServers": {}}`)(t, target)
			if err := os.Symlink("servers.json", p); err != nil {
				t.Fatal(err)
			}
		}, false, "symlink"},
		{"folder", func(t *testing.T, p string) {
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
		}, false, "not a regular file"},
		// Opening it to read would block until something writes to it.
		{"FIFO", func(t *testing.T, p string) {
			if err := syscall.Mkfifo(p, 0o600); err != nil {
				t.Fatal(err)
			}
		}, false, "not a regular file"},
		{"huge", write(`{"mcpServers": {}, "pad": "` + strings.Repeat("x", fsx.MaxBrainFile) + `"}`), false, "larger than"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ring := logx.NewRing(10)
			prev := log.Writer()
			log.SetOutput(ring)
			t.Cleanup(func() { log.SetOutput(prev) })

			dir := t.TempDir()
			p := filepath.Join(dir, ".mcp.json")
			tc.mcp(t, p)
			done := make(chan string, 1)
			go func() { done <- mcpConfigOf("a", dir) }()
			var got string
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("mcpConfigOf hangs")
			}
			if want := map[bool]string{true: p}[tc.pass]; got != want {
				t.Errorf("mcpConfigOf = %q, want %q", got, want)
			}
			lines, _ := ring.Since(0, 10)
			var logged []string
			for _, l := range lines {
				logged = append(logged, l.Text)
			}
			if tc.wantLog == "" {
				if len(logged) > 0 {
					t.Errorf("unexpected log %q", logged)
				}
				return
			}
			// The test's name is in p, so only the reason after it counts.
			_, reason, ok := strings.Cut(strings.Join(logged, "\n"), "brain a: leaving out "+p+": ")
			if len(logged) != 1 || !ok || !strings.Contains(reason, tc.wantLog) {
				t.Errorf("log = %q, want one line leaving out %s for %q", logged, p, tc.wantLog)
			}
		})
	}
}

// TestMCPConfigOfDropsServersTurnedOff: a server the brain folder's
// settings turn off with disabledMcpjsonServers stays off, as it did for a
// project .mcp.json, although --mcp-config would start it: the bridge
// passes the rest as JSON, or nothing when no server is left. A settings
// file it cannot read leaves out every server, since nothing can tell
// which ones it turns off. enabledMcpjsonServers changes nothing.
func TestMCPConfigOfDropsServersTurnedOff(t *testing.T) {
	const servers = `{"mcpServers": {"keep": {"command": "k", "args": ["<&>"]}, "off": {"command": "o"}}, "other": 1}`
	cases := []struct {
		name           string
		project, local string   // the .claude/settings.json and settings.local.json; "" for none
		want           []string // the servers passed as JSON; nil for the file itself
		leftOut        string   // in the log line when the file is left out
	}{
		{"no settings", "", "", nil, ""},
		{"nothing turned off", `{"permissions": {}}`, "", nil, ""},
		{"an unknown server turned off", `{"disabledMcpjsonServers": ["gone"]}`, "", nil, ""},
		{"turned off in settings.json", `{"disabledMcpjsonServers": ["off"]}`, "", []string{"keep"}, ""},
		{"turned off in settings.local.json", "", `{"disabledMcpjsonServers": ["off"]}`, []string{"keep"}, ""},
		{"enabled elsewhere", `{"disabledMcpjsonServers": ["off"]}`, `{"enabledMcpjsonServers": ["off"]}`, []string{"keep"}, ""},
		{"only enabledMcpjsonServers", `{"enabledMcpjsonServers": ["keep"]}`, "", nil, ""},
		{"all turned off", `{"disabledMcpjsonServers": ["off"]}`, `{"disabledMcpjsonServers": ["keep"]}`, []string{}, ""},
		{"list is null", `{"disabledMcpjsonServers": null}`, "", nil, ""},
		{"settings not JSON", `{"disabledMcpjsonServers": ["off"],}`, "", []string{}, "cannot tell which servers"},
		{"list not a list", `{"disabledMcpjsonServers": "off"}`, "", []string{}, "cannot tell which servers"},
		{"settings not an object", "", `[]`, []string{}, "cannot tell which servers"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ring := logx.NewRing(10)
			prev := log.Writer()
			log.SetOutput(ring)
			t.Cleanup(func() { log.SetOutput(prev) })

			dir := t.TempDir()
			p := filepath.Join(dir, ".mcp.json")
			for path, body := range map[string]string{
				p: servers,
				filepath.Join(dir, ".claude", "settings.json"):       tc.project,
				filepath.Join(dir, ".claude", "settings.local.json"): tc.local,
			} {
				if body == "" {
					continue
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got := mcpConfigOf("a", dir)
			lines, _ := ring.Since(0, 10)
			switch {
			case tc.want == nil:
				if got != p {
					t.Errorf("mcpConfigOf = %q, want the file %q", got, p)
				}
			case len(tc.want) == 0:
				if got != "" {
					t.Errorf("mcpConfigOf = %q, want nothing", got)
				}
			default:
				var cfg struct {
					MCPServers map[string]json.RawMessage `json:"mcpServers"`
				}
				if err := json.Unmarshal([]byte(got), &cfg); err != nil {
					t.Fatalf("mcpConfigOf = %q, want JSON: %v", got, err)
				}
				if names := slices.Sorted(maps.Keys(cfg.MCPServers)); !slices.Equal(names, tc.want) {
					t.Errorf("servers passed = %q, want %q", names, tc.want)
				}
				// A kept server is passed as it was written.
				var kept, orig any
				_ = json.Unmarshal(cfg.MCPServers["keep"], &kept)
				_ = json.Unmarshal([]byte(`{"command": "k", "args": ["<&>"]}`), &orig)
				if !reflect.DeepEqual(kept, orig) {
					t.Errorf("keep = %s, want it unchanged", cfg.MCPServers["keep"])
				}
			}
			var logged []string
			for _, l := range lines {
				logged = append(logged, l.Text)
			}
			if tc.leftOut == "" && len(logged) > 0 {
				t.Errorf("unexpected log %q", logged)
			}
			if tc.leftOut != "" && (len(logged) != 1 || !strings.Contains(logged[0], "leaving out "+p+": "+tc.leftOut)) {
				t.Errorf("log = %q, want one line leaving out %s for %q", logged, p, tc.leftOut)
			}
		})
	}
}

// TestMCPConfigOfStaysInTheBrainFolder: the bridge reads a brain's
// settings outside the sandbox, so it follows no symlink out of the
// brain's folder. A .claude folder linked to another brain's would
// otherwise tell the brain, by which of its servers start, what that
// brain turns off. Such a folder counts as settings it cannot read, so the
// brain gets no server. A link that stays inside the folder is followed.
func TestMCPConfigOfStaysInTheBrainFolder(t *testing.T) {
	cases := []struct {
		name    string
		link    func(dir, other string) string // where the brain's .claude points
		want    []string                       // the servers passed; nil for none
		leftOut bool                           // the log says none is passed
	}{
		{"inside the folder", func(string, string) string { return "conf" }, []string{"keep"}, false},
		{"another brain's .claude", func(_, other string) string { return filepath.Join(other, ".claude") }, nil, true},
		{"up and out", func(_, other string) string { return filepath.Join("..", filepath.Base(other), ".claude") }, nil, true},
		// os.Root does not tell a path that comes back from one that stays out.
		{"out and back in", func(dir, _ string) string { return filepath.Join("..", filepath.Base(dir), "conf") }, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ring := logx.NewRing(10)
			prev := log.Writer()
			log.SetOutput(ring)
			t.Cleanup(func() { log.SetOutput(prev) })

			parent := t.TempDir()
			dir, other := filepath.Join(parent, "brain"), filepath.Join(parent, "other")
			off := []byte(`{"disabledMcpjsonServers": ["off"]}`)
			for _, d := range []string{filepath.Join(dir, "conf"), filepath.Join(other, ".claude")} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(d, "settings.local.json"), off, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			p := filepath.Join(dir, ".mcp.json")
			if err := os.WriteFile(p, []byte(`{"mcpServers": {"keep": {"command": "k"}, "off": {"command": "o"}}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(tc.link(dir, other), filepath.Join(dir, ".claude")); err != nil {
				t.Fatal(err)
			}

			got := mcpConfigOf("a", dir)
			var names []string
			if got != "" {
				var cfg struct {
					MCPServers map[string]json.RawMessage `json:"mcpServers"`
				}
				if err := json.Unmarshal([]byte(got), &cfg); err != nil {
					t.Fatalf("mcpConfigOf = %q, want JSON: %v", got, err)
				}
				names = slices.Sorted(maps.Keys(cfg.MCPServers))
			}
			if !slices.Equal(names, tc.want) {
				t.Errorf("servers passed = %q, want %q", names, tc.want)
			}
			lines, _ := ring.Since(0, 10)
			var logged []string
			for _, l := range lines {
				logged = append(logged, l.Text)
			}
			if !tc.leftOut && len(logged) > 0 {
				t.Errorf("unexpected log %q", logged)
			}
			if tc.leftOut && (len(logged) != 1 || !strings.Contains(logged[0], "leaving out "+p+": cannot tell which servers")) {
				t.Errorf("log = %q, want one line leaving out %s", logged, p)
			}
		})
	}
}

func TestBuildPromptListsAllAttachments(t *testing.T) {
	br := bareBrain(t)
	p := br.buildPrompt(&brainSettings{}, Inbound{Inbound: chat.Inbound{JID: "x@s.whatsapp.net", Sender: "1", Text: "file these", Media: []string{"/m/1.jpg", "/m/2.pdf"}}})
	if !strings.Contains(p, `The 2 files in its "files" came with it`) || !strings.Contains(p, `"files":["/m/1.jpg","/m/2.pdf"]`) {
		t.Errorf("prompt:\n%s", p)
	}
	if got := chat.HistoryText("hi", []string{"/m/1.jpg"}); got != "hi\n[attachment: /m/1.jpg]" {
		t.Errorf("history text = %q", got)
	}
}

// TestBuildPromptNamesTheHistorySearch: the chat history in the prompt
// tells the brain how to look further back, with the command every run
// can reach. It used to name tools/history_search.py, relative to the
// brain's folder, where there is no tools/.
func TestBuildPromptNamesTheHistorySearch(t *testing.T) {
	br := bareBrain(t)
	line, _ := json.Marshal(history.Entry{TS: "2026-01-15T10:00:00Z", From: "15551234567", Chat: "15551234567@s.whatsapp.net", Text: "earlier", Type: "in"})
	if err := os.WriteFile(filepath.Join(br.workDir, history.FileName), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	p := br.buildPrompt(&brainSettings{}, dm("hello"))
	if !strings.Contains(p, "<chat_history>") || !strings.Contains(p, `with `+"`"+`"$FRIDOLIN_BIN" history QUERY`+"`") || strings.Contains(p, "tools/") {
		t.Errorf("prompt:\n%s", p)
	}
}

func TestStopKillsProcessesClaudeStarted(t *testing.T) {
	// A command Claude started keeps the output pipe open; stopping must kill
	// the whole process group, not just the CLI, or the stop hangs.
	mgr, d, _ := queueTestManager(t, 30000, "")
	t.Setenv("FRIDOLIN_FAKE_CHILD", "1")
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

func TestBrainsDoNotSeeTheDotEnv(t *testing.T) {
	// The bridge reads no .env. Older versions exported the one next to
	// config.json into the bridge's environment, which every brain
	// inherits, so any brain could read it with `env`.
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	clearEnv(t, "MODEL", "FRIDOLIN_TEST_DOTENV_ONLY")
	envLog := filepath.Join(home, "brain.env")
	t.Setenv("FRIDOLIN_FAKE_ENV_LOG", envLog)
	claude := fakeClaudeEnv(t, 0)
	path := writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data",
		"claude_path": "`+claude+`"`+brainDirsJSON(t,
		brainFolder(t, home+"/a", manifest.Manifest{ID: "a", Routing: manifest.Routing{HandlesDMs: true}}),
	)+`
	}`)
	// One key older versions read as a setting, one they only exported.
	dotenv := "MODEL=from-dotenv\nFRIDOLIN_TEST_DOTENV_ONLY=secret\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), ".env"), []byte(dotenv), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestManager(t, cfg)
	d := &deliveries{}
	mgr.SetHooks(BrainHooks{Deliver: d.add})
	t.Cleanup(mgr.Stop)

	if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the run", func() bool { return d.count() == 1 })
	if err := d.all()[0].res.Err; err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Split(string(logged), "\n")
	if !slices.Contains(env, "HOME="+home) {
		t.Fatalf("the logged environment lacks the HOME the brain inherits:\n%s", logged)
	}
	for _, kv := range env {
		for _, k := range []string{"MODEL", "FRIDOLIN_TEST_DOTENV_ONLY"} {
			if strings.HasPrefix(kv, k+"=") {
				t.Errorf("the brain sees %s from the .env", kv)
			}
		}
	}
}

// Brains are told the data dir and the DB the bridge runs with, absolute,
// so the Python tools find them without guessing between the data dir's
// names or knowing data_dir and db_path. What the bridge's own environment
// has under those names does not reach them.
func TestBrainsAreToldTheDataDir(t *testing.T) {
	cases := []struct {
		name     string
		settings string // config.json settings besides the brain
		legacy   bool   // the home has only the pre-rename data dir
		want     func(home string) (dataDir, dbPath string)
	}{
		{"default", ``, false, func(home string) (string, string) {
			return home + "/.fridolin", home + "/.fridolin/bridge.db"
		}},
		{"pre-rename data dir", ``, true, func(home string) (string, string) {
			return home + "/.whatsapp-claude-bridge", home + "/.whatsapp-claude-bridge/bridge.db"
		}},
		{"data_dir", `"data_dir": "~/data",`, false, func(home string) (string, string) {
			return home + "/data", home + "/data/bridge.db"
		}},
		{"db_path elsewhere", `"data_dir": "~/data", "db_path": "~/db/bridge.db",`, false, func(home string) (string, string) {
			return home + "/data", home + "/db/bridge.db"
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := fsx.Canonical(t.TempDir())
			t.Setenv("HOME", home)
			t.Setenv("BRIDGE_DATA_DIR", "/from/the/bridge/env")
			t.Setenv("BRIDGE_DB_PATH", "/from/the/bridge/env/bridge.db")
			if c.legacy {
				if err := os.Mkdir(home+"/.whatsapp-claude-bridge", 0o700); err != nil {
					t.Fatal(err)
				}
			}
			envLog := filepath.Join(home, "brain.env")
			t.Setenv("FRIDOLIN_FAKE_ENV_LOG", envLog)
			claude := fakeClaudeEnv(t, 0)
			path := writeConfig(t, `{`+c.settings+`
				"claude_path": "`+claude+`"`+brainDirsJSON(t,
				brainFolder(t, home+"/a", manifest.Manifest{ID: "a", Routing: manifest.Routing{HandlesDMs: true}}),
			)+`
			}`)
			cfg, err := loadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			mgr := newTestManager(t, cfg)
			d := &deliveries{}
			mgr.SetHooks(BrainHooks{Deliver: d.add})
			t.Cleanup(mgr.Stop)

			if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 10*time.Second, "the run", func() bool { return d.count() == 1 })
			if err := d.all()[0].res.Err; err != nil {
				t.Fatal(err)
			}
			logged, err := os.ReadFile(envLog)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]string{}
			for _, kv := range strings.Split(string(logged), "\n") {
				if k, v, ok := strings.Cut(kv, "="); ok {
					got[k] = v
				}
			}
			dataDir, dbPath := c.want(home)
			if got["BRIDGE_DATA_DIR"] != dataDir {
				t.Errorf("BRIDGE_DATA_DIR=%q, want %q", got["BRIDGE_DATA_DIR"], dataDir)
			}
			if got["BRIDGE_DB_PATH"] != dbPath {
				t.Errorf("BRIDGE_DB_PATH=%q, want %q", got["BRIDGE_DB_PATH"], dbPath)
			}
		})
	}
}

// A brain runs in its own folder, so a relative data_dir or db_path, which
// the bridge opens relative to its working directory, reaches it absolute.
func TestDataEnvIsAbsolute(t *testing.T) {
	cwd := fsx.Canonical(t.TempDir())
	t.Chdir(cwd)
	s := settingsFor(&config.Config{DataDir: "rel", DBPath: "rel/bridge.db"}, manifest.Spec{})
	want := []string{"BRIDGE_DATA_DIR=" + cwd + "/rel", "BRIDGE_DB_PATH=" + cwd + "/rel/bridge.db"}
	if got := dataEnv(s); !slices.Equal(got, want) {
		t.Errorf("dataEnv = %q, want %q", got, want)
	}
}

// TestPanicsInARunAreContained: a panic while a run is prepared, while
// claude runs, while its output is read, or while its result is
// delivered, is logged and recorded as that run's failure; the brain goes
// on with the next message, the bridge keeps running, and no process of
// the run is left behind.
func TestPanicsInARunAreContained(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		notice   time.Duration // workingNoticeAfter, when set
		panicked bool          // the first run ends with errRunPanicked
		hooks    func(d *deliveries, first *sync.Once) BrainHooks
	}{
		{"while the run is prepared", nil, 0, true, func(d *deliveries, first *sync.Once) BrainHooks {
			return BrainHooks{Deliver: d.add, ExtraArgs: func(string) []string {
				panicked := false
				first.Do(func() { panicked = true })
				if panicked {
					panic("boom in ExtraArgs")
				}
				return nil
			}}
		}},
		{"while claude runs", map[string]string{"FRIDOLIN_FAKE_SLEEP_MS": "1000"}, 100 * time.Millisecond, true, func(d *deliveries, first *sync.Once) BrainHooks {
			// The working notice comes from invoke itself.
			return BrainHooks{Deliver: d.add, Notice: func(string, string) {
				first.Do(func() { panic("boom in the working notice") })
			}}
		}},
		{"while its output is read", map[string]string{"FRIDOLIN_FAKE_ASK_ON": "says: one"}, 0, true, func(d *deliveries, first *sync.Once) BrainHooks {
			// The question goes out from the goroutine that reads the output.
			return BrainHooks{Deliver: d.add, Question: func(*Brain, string, string) bool {
				first.Do(func() { panic("boom in sending the question") })
				return true
			}}
		}},
		{"while it is delivered", nil, 0, false, func(d *deliveries, first *sync.Once) BrainHooks {
			return BrainHooks{Deliver: func(br *Brain, m Inbound, res RunResult) {
				d.add(br, m, res)
				first.Do(func() { panic("boom in Deliver") })
			}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr, _, home := queueTestManager(t, 10, "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			pidLog := filepath.Join(home, "pids")
			t.Setenv("FRIDOLIN_FAKE_PID_LOG", pidLog)
			if tc.notice > 0 {
				prev := workingNoticeAfter
				workingNoticeAfter = tc.notice
				t.Cleanup(func() { workingNoticeAfter = prev })
			}
			d := &deliveries{}
			var first sync.Once
			mgr.SetHooks(tc.hooks(d, &first))
			logs := captureLog(t)
			done := make(chan error, 2)
			for _, text := range []string{"one", "two"} {
				m := dm(text)
				m.Done = func(err error) { done <- err }
				if _, _, _, err := mgr.Submit(m); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("a message was never finished")
				}
			}
			runs := d.all()
			if len(runs) != 2 || runs[1].res.Err != nil || !strings.HasSuffix(runs[1].res.Text, "says: two") {
				t.Errorf("runs = %+v, want the second answered", runs)
			}
			if !strings.Contains(strings.Join(logs(), "\n"), "panicked: boom") {
				t.Error("the panic was not logged")
			}
			if tc.panicked && (len(runs) == 0 || !errors.Is(runs[0].res.Err, errRunPanicked)) {
				t.Errorf("runs = %+v, want the first to end with errRunPanicked", runs)
			}
			// Each run's process is gone, reaped too: a zombie still
			// answers signal 0.
			data, _ := os.ReadFile(pidLog)
			for _, line := range strings.Fields(string(data)) {
				pid, _ := strconv.Atoi(line)
				if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
					t.Errorf("process %d of a run is still there (%v)", pid, err)
				}
			}
		})
	}
}

// TestRunTimeoutStopsAStuckRun: a run that takes longer than
// run_timeout_minutes is stopped, process group and all, and ends with an
// error that says so, so that its brain goes on with the next message. A
// question it sent before it hung does not make it an answer: the chat
// still hears that the run was stopped.
func TestRunTimeoutStopsAStuckRun(t *testing.T) {
	prev := runTimeoutUnit
	runTimeoutUnit = 100 * time.Millisecond
	t.Cleanup(func() { runTimeoutUnit = prev })
	cases := []struct {
		name     string
		sleepMS  int
		minutes  string // run_timeout_minutes, in runTimeoutUnit
		env      map[string]string
		question bool // the question goes out before the deadline
	}{
		// With a command holding the output open too.
		{"before any output", 30000, "3", map[string]string{"FRIDOLIN_FAKE_CHILD": "1"}, false},
		// Time enough for the question to go out first.
		{"after asking a question", 0, "20", map[string]string{"FRIDOLIN_FAKE_ASK_ON": "says: stuck", "FRIDOLIN_FAKE_HANG_AFTER_ASK": "1"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr, d, _ := queueTestManager(t, tc.sleepMS, `, "run_timeout_minutes": `+tc.minutes)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			var mu sync.Mutex
			var sent []string
			say := func(_, text string) {
				mu.Lock()
				defer mu.Unlock()
				sent = append(sent, text)
			}
			mgr.SetHooks(BrainHooks{Deliver: d.add, Notice: say, Question: func(_ *Brain, jid, text string) bool {
				say(jid, text)
				return true
			}})
			start := time.Now()
			if _, _, _, err := mgr.Submit(dm("stuck")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 10*time.Second, "the run to be stopped", func() bool { return d.count() == 1 })
			res := d.all()[0].res
			if !errors.Is(res.Err, errRunTimedOut) || !strings.Contains(res.Err.Error(), "run_timeout_minutes") {
				t.Errorf("result = %+v, want errRunTimedOut", res)
			}
			if el := time.Since(start); el > 5*time.Second {
				t.Errorf("the run took %v to stop", el)
			}
			if last := mgr.Get("a").Snapshot().Last; last == nil || last.Outcome != outcomeError {
				t.Errorf("last run = %+v, want outcome error", last)
			}
			mu.Lock()
			defer mu.Unlock()
			if asked := slices.ContainsFunc(sent, func(s string) bool { return strings.HasPrefix(s, outboundmsg.QuestionPrefix) }); asked != tc.question {
				t.Errorf("sent to the chat mid-run: %q, want a question: %v", sent, tc.question)
			}
		})
	}
}

// TestRunEndsSoonAfterItsResult: a command Claude left running with the
// output open does not keep the brain busy once the result is in: once
// the CLI has exited, Wait stops waiting for the output after
// runWaitDelay, the rest of the run is stopped, and the answer goes out.
func TestRunEndsSoonAfterItsResult(t *testing.T) {
	prevGrace, prevDelay := postResultGrace, runWaitDelay
	postResultGrace, runWaitDelay = 300*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { postResultGrace, runWaitDelay = prevGrace, prevDelay })
	mgr, d, _ := queueTestManager(t, 10, "")
	t.Setenv("FRIDOLIN_FAKE_CHILD", "1") // sleeps 30 s holding the output open
	start := time.Now()
	if _, _, _, err := mgr.Submit(dm("quick answer")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the answer", func() bool { return d.count() == 1 })
	if res := d.all()[0].res; res.Err != nil || !strings.HasSuffix(res.Text, "says: quick answer") {
		t.Errorf("result = %+v, want the answer", res)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Errorf("the run took %v, held open by the command it left", el)
	}
}

// TestRunTimeoutKeepsAnAnswerThatCame: a deadline that comes in the grace
// after the result stops what is left of the run, but the answer that is
// in still goes out.
func TestRunTimeoutKeepsAnAnswerThatCame(t *testing.T) {
	prevUnit, prevGrace := runTimeoutUnit, postResultGrace
	runTimeoutUnit, postResultGrace = 100*time.Millisecond, 10*time.Second
	t.Cleanup(func() { runTimeoutUnit, postResultGrace = prevUnit, prevGrace })
	mgr, d, _ := queueTestManager(t, 0, `, "run_timeout_minutes": 20`)
	t.Setenv("FRIDOLIN_FAKE_CHILD", "1") // sleeps 30 s holding the output open
	if _, _, _, err := mgr.Submit(dm("answer in time")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the run", func() bool { return d.count() == 1 })
	if res := d.all()[0].res; res.Err != nil || !strings.HasSuffix(res.Text, "says: answer in time") {
		t.Errorf("result = %+v, want the answer that came before the deadline", res)
	}
}

// TestRunTimeoutSetting: run_timeout_minutes defaults to 30, takes 0 for no
// limit, and refuses what makes no sense.
func TestRunTimeoutSetting(t *testing.T) {
	cases := []struct {
		json    string
		want    time.Duration
		wantErr bool
	}{
		{``, 30 * time.Minute, false},
		{`, "run_timeout_minutes": 0`, 0, false},
		{`, "run_timeout_minutes": 90`, 90 * time.Minute, false},
		{`, "run_timeout_minutes": -1`, 0, true},
		{`, "run_timeout_minutes": 100000`, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.json, func(t *testing.T) {
			home := fsx.Canonical(t.TempDir())
			t.Setenv("HOME", home)
			cfg, err := loadConfig(writeConfig(t, `{"brain_mode": true, "data_dir": "`+home+`/data"`+tc.json+`}`))
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "run_timeout_minutes") {
					t.Errorf("err = %v, want run_timeout_minutes refused", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := runTimeout(cfg); got != tc.want {
				t.Errorf("RunTimeout = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestStreamLogIsRotatedAndNamesEachRun: a full activity log is moved to
// claude-stream.1.log when the next run starts, and each run's part of it
// starts with the chat, the sender, the process and the model, in a line
// runHeadingPID reads back.
func TestStreamLogIsRotatedAndNamesEachRun(t *testing.T) {
	mgr, d, home := queueTestManager(t, 10, `, "brain_model": "sonnet"`)
	logPath := filepath.Join(home, "a", streamLogName)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(streamLogMax); err != nil { // sparse: takes no room
		t.Fatal(err)
	}
	f.Close()
	if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the run", func() bool { return d.count() == 1 })
	if fi, err := os.Stat(filepath.Join(home, "a", streamLogOld)); err != nil || fi.Size() != streamLogMax {
		t.Errorf("rotated log: %v, %v; want the full one", fi, err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(string(data), "\n")
	if len(data) > 4096 || !strings.Contains(first, "Run for 15551234567@s.whatsapp.net from 15551234567, pid ") || !strings.Contains(first, "model sonnet") {
		t.Errorf("new log starts %q (%d bytes), want the run named", first, len(data))
	}
	// The Build page finds the run's part of the log by this heading.
	if pid, ok := runHeadingPID(first); !ok || pid <= 0 {
		t.Errorf("runHeadingPID(%q) = %d, %v; want the run's pid", first, pid, ok)
	}
}

// A message's history line is written once the brain takes the message,
// and says when it was sent: not when the debouncer let it through. A
// message the brain turns away leaves no line, or the sender's retry, or
// the brain it is routed to instead, would put it in the history twice.
func TestHistoryLineIsWrittenOnceTakenAtItsOwnTime(t *testing.T) {
	sent := time.Now().Add(-time.Hour).Truncate(time.Second)
	cases := []struct {
		name  string
		setup func(t *testing.T, br *Brain)
		m     Inbound
		err   error
		want  []string // the history lines' text and time, "text@ts"
	}{
		{"a message, at its time", nil, Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "hi", At: sent}},
			nil, []string{"hi@" + sent.Format(time.RFC3339)}},
		{"each part at its own time", nil, Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "photo", At: sent}}.merge(
			Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "what is it?", At: sent.Add(5 * time.Second)}}),
			nil, []string{"photo@" + sent.Format(time.RFC3339), "what is it?@" + sent.Add(5*time.Second).Format(time.RFC3339)}},
		{"turned away by a full queue", func(t *testing.T, br *Brain) {
			if _, _, err := br.Submit(dm("long task")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 5*time.Second, "the first run", func() bool { r := br.Snapshot().Running; return r != nil && r.PID > 0 })
			if _, _, err := br.Submit(Inbound{Inbound: chat.Inbound{JID: testGroup, Sender: otherNumber, Text: "waiting"}}); err != nil {
				t.Fatal(err)
			}
		}, Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "one more", At: sent}}, errQueueFull, []string{"long task", "waiting"}},
		{"chatter that finds no place", func(t *testing.T, br *Brain) {
			// b observes its group with runs, and its one place is taken.
			if _, _, err := br.Submit(Inbound{Inbound: chat.Inbound{JID: "120363000000000000@g.us", Sender: otherNumber, Text: "long task"}}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 5*time.Second, "the first run", func() bool { r := br.Snapshot().Running; return r != nil && r.PID > 0 })
			if _, _, err := br.Submit(Inbound{Inbound: chat.Inbound{JID: "120363000000000000@g.us", Sender: otherNumber, Text: "waiting"}}); err != nil {
				t.Fatal(err)
			}
		}, Inbound{Inbound: chat.Inbound{JID: "120363000000000000@g.us", Sender: "15551234567", Text: "chatter", At: sent, Silent: true}},
			nil, []string{"long task", "waiting", "chatter@" + sent.Format(time.RFC3339)}},
		{"turned away by a retired brain", func(t *testing.T, br *Brain) {
			br.mu.Lock()
			br.retired = true
			br.mu.Unlock()
		}, Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "too late", At: sent}}, errBrainRetired, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mgr, _, _ := queueTestManager(t, 30000, `, "queue": {"max_pending": 1}`)
			br := mgr.Get("a")
			if c.m.IsGroup() {
				br = mgr.Get("b")
			}
			if c.setup != nil {
				c.setup(t, br)
			}
			if _, _, err := br.Submit(c.m); !errors.Is(err, c.err) {
				t.Fatalf("Submit: %v, want %v", err, c.err)
			}
			var got []string
			for _, e := range readHistory(t, br) {
				if e.TS == sent.Format(time.RFC3339) || e.TS == sent.Add(5*time.Second).Format(time.RFC3339) {
					got = append(got, e.Text+"@"+e.TS)
				} else {
					got = append(got, e.Text)
				}
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("history = %q, want %q", got, c.want)
			}
		})
	}
}

// Each chat's messages wait in a lane of their own, which runs in order,
// and the chats take turns: the chat whose last run started longest ago
// goes next, one that never had a run first. Work addressed to the brain
// goes before untagged chatter whatever the turns.
func TestQueueOrderGivesEachChatItsTurn(t *testing.T) {
	const x, y, z = "x@s.whatsapp.net", "y@s.whatsapp.net", "z@g.us"
	msg := func(chatJID, text string) Inbound { return Inbound{Inbound: chat.Inbound{JID: chatJID, Text: text}} }
	chatter := func(chatJID, text string) Inbound {
		return Inbound{Inbound: chat.Inbound{JID: chatJID, Text: text, Silent: true}}
	}
	cases := []struct {
		name    string
		turns   map[string]uint64
		pending []Inbound
		want    string
	}{
		{"one chat runs in order", nil, []Inbound{msg(x, "x1"), msg(x, "x2"), msg(x, "x3")}, "x1 x2 x3"},
		{"chats alternate", nil, []Inbound{msg(x, "x1"), msg(x, "x2"), msg(y, "y1"), msg(y, "y2")}, "x1 y1 x2 y2"},
		{"the chat that just ran goes last", map[string]uint64{x: 3}, []Inbound{msg(x, "x2"), msg(y, "y1"), msg(z, "z1")}, "y1 z1 x2"},
		{"the longest ago goes first", map[string]uint64{x: 3, y: 1, z: 2}, []Inbound{msg(x, "x1"), msg(z, "z1"), msg(y, "y1")}, "y1 z1 x1"},
		{"requests before chatter", nil, []Inbound{chatter(z, "z1"), msg(x, "x1"), msg(x, "x2"), msg(y, "y1")}, "x1 y1 x2 z1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			br := &Brain{pending: c.pending, turns: maps.Clone(c.turns), turn: 10}
			var got []string
			for _, i := range br.queueOrderLocked() {
				got = append(got, c.pending[i].Text)
			}
			if strings.Join(got, " ") != c.want {
				t.Errorf("order %q, want %q", got, c.want)
			}
			// The worker takes them in that order.
			got = nil
			for len(br.pending) > 0 {
				i := nextIn(br.pending, br.turns)
				got = append(got, br.pending[i].Text)
				chatJID := br.pending[i].JID
				br.pending = slices.Delete(br.pending, i, i+1)
				br.takeTurnLocked(chatJID)
			}
			if strings.Join(got, " ") != c.want {
				t.Errorf("ran %q, want %q", got, c.want)
			}
			if len(br.turns) != 1 {
				t.Errorf("turns = %v once all ran, want only the last chat's", br.turns)
			}
		})
	}
}

// Through the brain: a chat that sends three messages while the brain
// works does not keep another chat's one waiting behind all three, and
// Submit says where each waits.
func TestBusyChatDoesNotHoldUpAnother(t *testing.T) {
	mgr, d, _ := queueTestManager(t, 400, "")
	br := mgr.Get("a")
	other := Inbound{Inbound: chat.Inbound{JID: otherNumber + "@s.whatsapp.net", Sender: otherNumber, Text: "other 1"}}
	if _, _, err := br.Submit(dm("dm 1")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "the first run", func() bool { r := br.Snapshot().Running; return r != nil && r.PID > 0 })
	var pos []int
	for _, m := range []Inbound{dm("dm 2"), dm("dm 3"), other} {
		p, busy, err := br.Submit(m)
		if err != nil || !busy {
			t.Fatalf("%s: busy %v, %v", m.Text, busy, err)
		}
		pos = append(pos, p)
	}
	if !slices.Equal(pos, []int{1, 2, 1}) {
		t.Errorf("positions %v, want dm 2 and 3 at #1 and #2, then the other chat's at #1, ahead of them", pos)
	}
	if q := queuePreviews(br); strings.Join(q, "|") != "other 1|dm 2|dm 3" {
		t.Errorf("queue = %q, want the other chat's message first", q)
	}
	waitFor(t, 20*time.Second, "four runs", func() bool { return d.count() == 4 })
	var ran []string
	for _, r := range d.all() {
		ran = append(ran, r.msg.Text)
	}
	if strings.Join(ran, "|") != "dm 1|other 1|dm 2|dm 3" {
		t.Errorf("ran %q, want the other chat's message right after the run in flight", ran)
	}
}

// limitTestManager is queueTestManager with a third brain, c, for the
// group 120363000000000001@g.us, which observes it with runs like b, and
// runs limited to limit at once.
func limitTestManager(t *testing.T, sleepMS, limit int) (*BrainManager, *deliveries) {
	t.Helper()
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	claude := fakeClaudeEnv(t, sleepMS)
	cfg, err := loadConfig(writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data",
		"claude_path": "`+claude+`",
		"max_concurrent_runs": `+strconv.Itoa(limit)+brainDirsJSON(t,
		brainFolder(t, home+"/a", manifest.Manifest{ID: "a", Routing: manifest.Routing{HandlesDMs: true}}),
		brainFolder(t, home+"/b", manifest.Manifest{ID: "b", Routing: manifest.Routing{Groups: []string{"120363000000000000@g.us"}}, Observe: manifest.ObserveRun}),
		brainFolder(t, home+"/c", manifest.Manifest{ID: "c", Routing: manifest.Routing{Groups: []string{"120363000000000001@g.us"}}, Observe: manifest.ObserveRun}),
	)+`
	}`))
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestManager(t, cfg)
	d := &deliveries{}
	mgr.SetHooks(BrainHooks{Deliver: d.add})
	t.Cleanup(mgr.Stop)
	return mgr, d
}

// The chats of limitTestManager's brains b and c, and messages there.
const (
	groupB = "120363000000000000@g.us"
	groupC = "120363000000000001@g.us"
)

func groupMsg(chatJID, text string, chatter bool) Inbound {
	return Inbound{Inbound: chat.Inbound{JID: chatJID, Sender: otherNumber, Text: text, Silent: chatter}}
}

// slotState is what the run slots hold: how many slots are held and how
// many wait, and whether a held one was asked to give way.
func slotState(mgr *BrainManager) (held, waiting int, stopped bool) {
	rs := mgr.slots
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, h := range rs.held {
		stopped = stopped || h.stopped
	}
	return len(rs.held), len(rs.waiting), stopped
}

// waitsForSlot reports whether br's worker waits for a run slot.
func waitsForSlot(br *Brain) bool {
	br.mu.Lock()
	defer br.mu.Unlock()
	return br.slotWait != nil
}

// hasRun waits until br runs claude.
func hasRun(t *testing.T, br *Brain) {
	t.Helper()
	waitFor(t, 5*time.Second, br.ID()+"'s run", func() bool { r := br.Snapshot().Running; return r != nil && r.PID > 0 })
}

// running counts the brains with a run going.
func running(mgr *BrainManager) int {
	n := 0
	for _, br := range mgr.Brains() {
		if br.Snapshot().Running != nil {
			n++
		}
	}
	return n
}

// max_concurrent_runs bounds the runs of all brains together: three busy
// brains with a limit of two run two at a time, and the third gets its
// turn once one is done.
func TestRunLimitBoundsRunsAcrossBrains(t *testing.T) {
	cases := []struct {
		limit, most int
	}{
		{1, 1},
		{2, 2},
		{0, 3}, // no limit
	}
	for _, c := range cases {
		t.Run("limit "+strconv.Itoa(c.limit), func(t *testing.T) {
			mgr, d := limitTestManager(t, 700, c.limit)
			for _, m := range []Inbound{
				dm("for a"),
				{Inbound: chat.Inbound{JID: "120363000000000000@g.us", Sender: otherNumber, Text: "for b"}},
				{Inbound: chat.Inbound{JID: "120363000000000001@g.us", Sender: otherNumber, Text: "for c"}},
			} {
				if _, _, _, err := mgr.Submit(m); err != nil {
					t.Fatal(err)
				}
			}
			most := 0
			waitFor(t, 20*time.Second, "three runs", func() bool {
				most = max(most, running(mgr))
				return d.count() == 3
			})
			if most != c.most {
				t.Errorf("at most %d runs at once, want %d", most, c.most)
			}
			for _, r := range d.all() {
				if r.res.Err != nil {
					t.Errorf("brain %s: %v", r.brain, r.res.Err)
				}
			}
		})
	}
}

// A brain that just had a run asks for the next slot behind the brains
// already waiting, so a brain with a long queue does not keep the slot.
func TestRunSlotsGoToBrainsInTurn(t *testing.T) {
	mgr, d := limitTestManager(t, 300, 1)
	a := mgr.Get("a")
	if _, _, err := a.Submit(dm("a 1")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "a's first run", func() bool { r := a.Snapshot().Running; return r != nil && r.PID > 0 })
	for _, m := range []Inbound{dm("a 2"), {Inbound: chat.Inbound{JID: "120363000000000001@g.us", Sender: otherNumber, Text: "c 1"}}} {
		if _, _, _, err := mgr.Submit(m); err != nil {
			t.Fatal(err)
		}
	}
	most := 0
	waitFor(t, 20*time.Second, "three runs", func() bool {
		most = max(most, running(mgr))
		return d.count() == 3
	})
	var ran []string
	for _, r := range d.all() {
		ran = append(ran, r.msg.Text)
	}
	if strings.Join(ran, "|") != "a 1|c 1|a 2" || most != 1 {
		t.Errorf("ran %q, %d at most at once; want c's message between a's, one at a time", ran, most)
	}
}

// Work addressed to a brain does not wait for another brain's
// observe-only run to end: with every slot taken by chatter, it stops one.
func TestRequestStopsChatterInAnotherBrainForASlot(t *testing.T) {
	mgr, d := limitTestManager(t, 30000, 1)
	b := mgr.Get("b")
	if _, _, err := b.Submit(Inbound{Inbound: chat.Inbound{JID: "120363000000000000@g.us", Sender: otherNumber, Text: "chatter here", Silent: true}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "b's observe-only run", func() bool { r := b.Snapshot().Running; return r != nil && r.PID > 0 })
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "10")
	if _, _, _, err := mgr.Submit(dm("question")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, "both runs to end", func() bool { return d.count() == 2 })
	got := map[string]error{}
	for _, r := range d.all() {
		got[r.msg.Text] = r.res.Err
	}
	if !errors.Is(got["chatter here"], errBrainInterrupted) || got["question"] != nil {
		t.Errorf("results %v, want the chatter's run stopped and the question answered", got)
	}
}

// A request that has to wait for a run slot other brains' runs take says
// so, as one that waits behind its own brain's work does: with the limit
// on by default, an idle brain can wait for minutes, and the sender would
// hear nothing until its run began.
func TestRequestWaitingForARunSlotIsBusy(t *testing.T) {
	cases := []struct {
		name    string
		limit   int
		running Inbound // in another brain
		busy    bool
	}{
		{"every slot taken by a request", 1, groupMsg(groupC, "for c", false), true},
		{"a slot free", 2, groupMsg(groupC, "for c", false), false},
		{"the slot taken by chatter, which gives way", 1, groupMsg(groupB, "chatter here", true), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mgr, _ := limitTestManager(t, 30000, c.limit)
			other, _, _, err := mgr.Submit(c.running)
			if err != nil {
				t.Fatal(err)
			}
			hasRun(t, other)
			_, pos, busy, err := mgr.Submit(dm("question"))
			if err != nil || pos != 1 || busy != c.busy {
				t.Errorf("Submit = #%d, busy %v, %v; want #1, busy %v", pos, busy, err, c.busy)
			}
		})
	}
}

// A brain that waits for a slot for chatter and then gets a request waits
// for one for the request, which stops another brain's observe-only run
// for it, as a request that asks for a slot does.
func TestRequestUpgradesAWaitForChatter(t *testing.T) {
	mgr, d := limitTestManager(t, 30000, 1)
	b, c := mgr.Get("b"), mgr.Get("c")
	if _, _, err := b.Submit(groupMsg(groupB, "chatter in b", true)); err != nil {
		t.Fatal(err)
	}
	hasRun(t, b)
	if _, _, err := c.Submit(groupMsg(groupC, "chatter in c", true)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "c to wait for a slot", func() bool { return waitsForSlot(c) })
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "10")
	if _, _, err := c.Submit(groupMsg(groupC, "question for c", false)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "both runs to end", func() bool { return d.count() == 2 })
	got := map[string]error{}
	for _, r := range d.all() {
		got[r.msg.Text] = r.res.Err
	}
	if !errors.Is(got["chatter in b"], errBrainInterrupted) || got["question for c"] != nil {
		t.Errorf("results %v, want b's chatter stopped and c's question answered", got)
	}
}

// A brain removed while it waits for a run slot gives up its place: its
// worker ends at once, rather than when a slot comes free for nothing.
func TestRetiredBrainGivesUpItsWaitForASlot(t *testing.T) {
	mgr, _ := limitTestManager(t, 30000, 1)
	a, c := mgr.Get("a"), mgr.Get("c")
	if _, _, err := c.Submit(groupMsg(groupC, "for c", false)); err != nil {
		t.Fatal(err)
	}
	hasRun(t, c)
	if _, _, err := a.Submit(dm("for a")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "a to wait for a slot", func() bool { return waitsForSlot(a) })
	a.retire(false)
	waitFor(t, 3*time.Second, "a's worker to end", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return !a.working
	})
	if held, waiting, _ := slotState(mgr); held != 1 || waiting != 0 {
		t.Errorf("%d slots held, %d waits; want c's slot and no wait", held, waiting)
	}
}

// Chatter whose slot a request in another brain asked to give way before
// its run began never starts that run: it would only be stopped.
func TestChatterAskedToGiveWayBeforeItsRunDoesNotRun(t *testing.T) {
	mgr, d := limitTestManager(t, 30000, 1)
	a, b, c := mgr.Get("a"), mgr.Get("b"), mgr.Get("c")
	if _, _, err := c.Submit(groupMsg(groupC, "for c", false)); err != nil {
		t.Fatal(err)
	}
	hasRun(t, c)
	if _, _, err := b.Submit(groupMsg(groupB, "chatter in b", true)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "b to wait for a slot", func() bool { return waitsForSlot(b) })

	// Holding b's lock keeps its worker between the slot it is granted and
	// the run, while a's request asks the slot to give way.
	b.mu.Lock()
	locked := true
	defer func() {
		if locked {
			b.mu.Unlock()
		}
	}()
	c.StopCurrent(false)
	waitFor(t, 5*time.Second, "b to get the slot", func() bool { held, waiting, _ := slotState(mgr); return held == 1 && waiting == 0 })
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "10")
	if _, _, err := a.Submit(dm("question")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "a to ask b's slot to give way", func() bool { _, _, stopped := slotState(mgr); return stopped })
	b.mu.Unlock()
	locked = false

	waitFor(t, 10*time.Second, "a's answer", func() bool {
		return slices.ContainsFunc(d.all(), func(r delivered) bool { return r.msg.Text == "question" })
	})
	time.Sleep(300 * time.Millisecond)
	for _, r := range d.all() {
		if r.brain == "b" {
			t.Errorf("b ran its chatter: %v", r.res.Err)
		}
	}
	if last := b.Snapshot().Last; last != nil {
		t.Errorf("b's last run = %+v, want none", last)
	}
}

// The limit is read when the configuration changes, and refused outside
// 0 (no limit) to 64.
func TestRunLimitSetting(t *testing.T) {
	cases := []struct {
		json string
		want int
		err  string
	}{
		{``, 2, ""},
		{`, "max_concurrent_runs": 0`, 0, ""},
		{`, "max_concurrent_runs": 5`, 5, ""},
		{`, "max_concurrent_runs": -1`, 0, "max_concurrent_runs must be between 0 (no limit) and 64, not -1"},
		{`, "max_concurrent_runs": 65`, 0, "max_concurrent_runs must be between 0 (no limit) and 64, not 65"},
	}
	for _, c := range cases {
		t.Run(c.json, func(t *testing.T) {
			home := t.TempDir()
			cfg, err := loadConfig(writeConfig(t, `{"brain_mode": true, "data_dir": "`+home+`/data"`+c.json+`}`))
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Errorf("err = %v, want %q", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.RunLimit(); got != c.want {
				t.Errorf("RunLimit = %d, want %d", got, c.want)
			}
			mgr := newTestManager(t, cfg)
			if mgr.slots.limit != c.want {
				t.Errorf("slots limit = %d, want %d", mgr.slots.limit, c.want)
			}
			n := 7
			next := *cfg
			next.MaxConcurrentRuns = &n
			if err := mgr.Apply(&next); err != nil {
				t.Fatal(err)
			}
			if mgr.slots.limit != 7 {
				t.Errorf("slots limit after a change = %d, want 7", mgr.slots.limit)
			}
		})
	}
}
