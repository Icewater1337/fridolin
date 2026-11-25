package brain

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// gatedRunner stands in for the CLI: a run whose text has a gate goes on
// until the gate is opened or the run is stopped, any other answers at
// once.
type gatedRunner struct {
	mu      sync.Mutex
	gates   map[string]chan struct{}
	reached map[string]chan struct{} // closed once a gated run's Run recorded its slot
	started []string                 // the texts of the runs, in the order they started
	slots   map[string]*Slot         // each run's slot (RunSpec.Slot), by its text
}

func newGatedRunner(gated ...string) *gatedRunner {
	g := &gatedRunner{gates: map[string]chan struct{}{}, reached: map[string]chan struct{}{}, slots: map[string]*Slot{}}
	for _, text := range gated {
		g.gates[text] = make(chan struct{})
		g.reached[text] = make(chan struct{})
	}
	return g
}

func (g *gatedRunner) Run(ctx context.Context, spec RunSpec, c claudecli.Callbacks) claudecli.Result {
	text := spec.Message.Text
	g.mu.Lock()
	g.started = append(g.started, text)
	_, again := g.slots[text]
	g.slots[text] = spec.Slot
	gate := g.gates[text]
	if r := g.reached[text]; r != nil && !again {
		close(r)
	}
	g.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return claudecli.Result{Err: claudecli.ErrInterrupted}
		}
	}
	return claudecli.Result{Text: "answer to " + text}
}

// open lets the gated run for text end.
func (g *gatedRunner) open(text string) { close(g.gates[text]) }

// ran lists the texts of the runs that started, in order.
func (g *gatedRunner) ran() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.started)
}

// slot returns the slot the gated run for text went in, as its RunSpec had
// it. It waits for the run to reach the runner: the lane makes a run its
// current one before it calls Run, so waitRunning alone does not mean the
// slot was recorded yet (#744).
func (g *gatedRunner) slot(t *testing.T, text string) *Slot {
	t.Helper()
	select {
	case <-g.reached[text]:
	case <-time.After(5 * time.Second):
		t.Fatalf("the run for %q never reached the runner", text)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.slots[text]
	if s == nil {
		t.Fatalf("the run for %q had no slot", text)
	}
	return s
}

// gate makes every brain of mgr run with g.
func (g *gatedRunner) gate(mgr *Manager) *gatedRunner {
	for _, br := range mgr.Brains() {
		br.runner = g
	}
	return g
}

// agentMsg is a scheduled agent's run of brain, in its own chat.
func agentMsg(brain, agent, text string) Inbound {
	return Inbound{Inbound: chat.Inbound{JID: chat.ScheduleChatPrefix + agent, Brain: brain, Sender: "schedule", Text: text, Source: chat.SourceScheduled}}
}

// runningIn returns the text of what runs in br's lane l, "" for nothing.
func runningIn(br *Brain, l Lane) string {
	br.mu.Lock()
	defer br.mu.Unlock()
	if cur := br.lane(l).current; cur != nil {
		return cur.msg.Text
	}
	return ""
}

// waitRunning waits until text runs in br's lane l.
func waitRunning(t *testing.T, br *Brain, l Lane, text string) {
	t.Helper()
	testutil.WaitFor(t, 5*time.Second, br.ID()+"'s "+l.String()+" run of "+text, func() bool { return runningIn(br, l) == text })
}

// result returns the delivered result of the run for text, and whether it
// was delivered.
func result(d *deliveries, text string) (claudecli.Result, bool) {
	for _, r := range d.all() {
		if r.msg.Text == text {
			return r.res, true
		}
	}
	return claudecli.Result{}, false
}

// waitDelivered waits for the run for text to be delivered and returns its
// result.
func waitDelivered(t *testing.T, d *deliveries, text string) claudecli.Result {
	t.Helper()
	var res claudecli.Result
	testutil.WaitFor(t, 5*time.Second, "the answer to "+text, func() bool {
		r, ok := result(d, text)
		res = r
		return ok
	})
	return res
}

// A scheduled agent's run, however long, keeps no chat waiting: the DM is
// answered while it goes on, and the dashboard shows both.
func TestADMIsAnsweredWhileAScheduledRunGoes(t *testing.T) {
	for _, limit := range []int{0, 2} {
		t.Run("max_concurrent_runs "+map[int]string{0: "off", 2: "2"}[limit], func(t *testing.T) {
			mgr, d := limitTestManager(t, 0, limit)
			g := newGatedRunner("the upkeep").gate(mgr)
			a := mgr.Get("a")
			if _, pos, busy, err := mgr.Submit(agentMsg("a", "upkeep", "the upkeep")); err != nil || pos != 1 || busy {
				t.Fatalf("agent Submit = #%d busy %v, %v", pos, busy, err)
			}
			waitRunning(t, a, LaneBackground, "the upkeep")

			_, pos, busy, err := mgr.Submit(dm("hello"))
			if err != nil || pos != 1 || busy {
				t.Errorf("DM Submit = #%d busy %v, %v; want #1, not busy", pos, busy, err)
			}
			if res := waitDelivered(t, d, "hello"); res.Err != nil {
				t.Fatal(res.Err)
			}
			rt := a.Snapshot()
			if rt.Background == nil || rt.Background.Chat != chat.ScheduleChatPrefix+"upkeep" || rt.Running != nil {
				t.Errorf("snapshot: running %+v, background %+v; want only the agent's run in the background", rt.Running, rt.Background)
			}
			if rt.Last == nil || rt.Last.Chat != testDMChat {
				t.Errorf("last run = %+v, want the DM's", rt.Last)
			}
			g.open("the upkeep")
			if res := waitDelivered(t, d, "the upkeep"); res.Err != nil {
				t.Fatal(res.Err)
			}
			if !a.WaitIdle(context.Background(), 5*time.Second) {
				t.Error("the brain is not idle once both lanes are done")
			}
		})
	}
}

// One brain runs its scheduled agents one at a time, in the order they
// came in, beside its chats.
func TestScheduledRunsOfABrainStaySerial(t *testing.T) {
	mgr, d := limitTestManager(t, 0, 0)
	g := newGatedRunner("first agent", "second agent").gate(mgr)
	a := mgr.Get("a")
	if _, _, _, err := mgr.Submit(agentMsg("a", "one", "first agent")); err != nil {
		t.Fatal(err)
	}
	waitRunning(t, a, LaneBackground, "first agent")
	if _, pos, busy, err := mgr.Submit(agentMsg("a", "two", "second agent")); err != nil || pos != 1 || !busy {
		t.Errorf("second agent's Submit = #%d busy %v, %v; want #1 behind the first", pos, busy, err)
	}
	if q := a.Snapshot().BackgroundQueue; len(q) != 1 || q[0].Chat != chat.ScheduleChatPrefix+"two" {
		t.Errorf("background queue = %+v, want the second agent", q)
	}
	time.Sleep(100 * time.Millisecond)
	if got := g.ran(); !slices.Equal(got, []string{"first agent"}) {
		t.Fatalf("ran %q while the first agent runs, want it alone", got)
	}
	g.open("first agent")
	waitRunning(t, a, LaneBackground, "second agent")
	g.open("second agent")
	waitDelivered(t, d, "second agent")
	if got := g.ran(); !slices.Equal(got, []string{"first agent", "second agent"}) {
		t.Errorf("ran %q, want the agents one after the other", got)
	}
}

// Each lane has max_pending places of its own: agents' work waiting does
// not turn a chat's message away.
func TestEachLaneHasItsOwnPlaces(t *testing.T) {
	mgr, _ := limitTestManager(t, 0, 0)
	newGatedRunner("first agent", "hello").gate(mgr)
	a := mgr.Get("a")
	s := *a.settings.Load()
	s.MaxPending = 1
	a.settings.Store(&s)
	if _, _, _, err := mgr.Submit(agentMsg("a", "one", "first agent")); err != nil {
		t.Fatal(err)
	}
	waitRunning(t, a, LaneBackground, "first agent")
	if _, _, _, err := mgr.Submit(agentMsg("a", "two", "second agent")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Submit(agentMsg("a", "three", "third agent")); !errors.Is(err, ErrQueueFull) {
		t.Errorf("a third agent's run: %v, want the background lane full", err)
	}
	if _, pos, _, err := mgr.Submit(dm("hello")); err != nil || pos != 1 {
		t.Errorf("DM Submit = #%d, %v; want it taken", pos, err)
	}
	waitRunning(t, a, LaneChat, "hello")
	if _, _, _, err := mgr.Submit(dm("again")); err != nil {
		t.Errorf("a DM waiting behind the chat's run: %v", err)
	}
	if _, _, _, err := mgr.Submit(dm("and again")); !errors.Is(err, ErrQueueFull) {
		t.Errorf("a second DM waiting: %v, want the chat lane full", err)
	}
}

// !stop in a chat stops that chat's run, not the agent's; StopLane stops
// one lane's run, and with clearQueue empties that lane's queue; the
// dashboard's stop without a lane (StopCurrent) stops both, and with
// clearQueue empties both queues.
func TestStoppingTheLanes(t *testing.T) {
	start := func(t *testing.T) (*Brain, *deliveries) {
		mgr, d := limitTestManager(t, 0, 0)
		newGatedRunner("the upkeep", "hello", "the report", "more").gate(mgr)
		a := mgr.Get("a")
		for _, m := range []Inbound{agentMsg("a", "upkeep", "the upkeep"), dm("hello"), agentMsg("a", "report", "the report"), dm("more")} {
			if _, _, _, err := mgr.Submit(m); err != nil {
				t.Fatal(err)
			}
		}
		waitRunning(t, a, LaneBackground, "the upkeep")
		waitRunning(t, a, LaneChat, "hello")
		return a, d
	}
	interrupted := func(t *testing.T, d *deliveries, text string) {
		t.Helper()
		if res := waitDelivered(t, d, text); !errors.Is(res.Err, claudecli.ErrInterrupted) {
			t.Errorf("%s: %v, want it stopped", text, res.Err)
		}
	}
	queued := func(br *Brain) (chats, agents int) {
		rt := br.Snapshot()
		return len(rt.Queue), len(rt.BackgroundQueue)
	}

	t.Run("a chat's stop", func(t *testing.T) {
		a, d := start(t)
		if stopped, n := a.StopChat(testDMChat); !stopped || n != 1 {
			t.Errorf("StopChat = %v, %d; want the DM's run and its waiting message", stopped, n)
		}
		interrupted(t, d, "hello")
		if got := runningIn(a, LaneBackground); got != "the upkeep" {
			t.Errorf("background runs %q, want the agent's run going on", got)
		}
		if chats, agents := queued(a); chats != 0 || agents != 1 {
			t.Errorf("%d chat messages and %d agents' wait, want 0 and 1", chats, agents)
		}
	})
	t.Run("the background lane's run", func(t *testing.T) {
		a, d := start(t)
		if !a.StopLane(LaneBackground, false) {
			t.Error("StopLane(background) stopped nothing")
		}
		interrupted(t, d, "the upkeep")
		// The next agent's run goes on, and so does the chat.
		waitRunning(t, a, LaneBackground, "the report")
		if got := runningIn(a, LaneChat); got != "hello" {
			t.Errorf("the chat lane runs %q, want the DM's run going on", got)
		}
	})
	t.Run("the chat lane's run", func(t *testing.T) {
		a, d := start(t)
		if !a.StopLane(LaneChat, false) {
			t.Error("StopLane(chat) stopped nothing")
		}
		interrupted(t, d, "hello")
		waitRunning(t, a, LaneChat, "more")
		if got := runningIn(a, LaneBackground); got != "the upkeep" {
			t.Errorf("background runs %q, want the agent's run going on", got)
		}
	})
	// A lane's stop and clear empties that lane's queue only.
	for _, c := range []struct {
		lane                     Lane
		stopped, kept            string // the run stopped, the run that goes on
		dropped, stillWaiting    string // what waited in each lane
		wantChats, wantAgentWait int
	}{
		{LaneBackground, "the upkeep", "hello", "the report", "more", 1, 0},
		{LaneChat, "hello", "the upkeep", "more", "the report", 0, 1},
	} {
		t.Run("the "+c.lane.String()+" lane's run and queue", func(t *testing.T) {
			a, d := start(t)
			if !a.StopLane(c.lane, true) {
				t.Errorf("StopLane(%s, clear) stopped nothing", c.lane)
			}
			interrupted(t, d, c.stopped)
			other := LaneChat
			if c.lane == LaneChat {
				other = LaneBackground
			}
			if got := runningIn(a, other); got != c.kept {
				t.Errorf("the %s lane runs %q, want %q going on", other, got, c.kept)
			}
			testutil.WaitFor(t, 5*time.Second, "the stopped lane to be idle", func() bool { return runningIn(a, c.lane) == "" })
			if chats, agents := queued(a); chats != c.wantChats || agents != c.wantAgentWait {
				t.Errorf("%d chat messages and %d agents' wait, want %d and %d", chats, agents, c.wantChats, c.wantAgentWait)
			}
			time.Sleep(50 * time.Millisecond)
			if _, ok := result(d, c.dropped); ok {
				t.Errorf("%q, which the stop cleared, ran", c.dropped)
			}
			if _, ok := result(d, c.stillWaiting); ok {
				t.Errorf("%q, waiting in the other lane, ran", c.stillWaiting)
			}
		})
	}
	t.Run("the dashboard's stop", func(t *testing.T) {
		a, d := start(t)
		if !a.StopCurrent(false) {
			t.Error("StopCurrent stopped nothing")
		}
		interrupted(t, d, "hello")
		interrupted(t, d, "the upkeep")
		// What waits runs next, in both lanes.
		waitRunning(t, a, LaneChat, "more")
		waitRunning(t, a, LaneBackground, "the report")
	})
	t.Run("the dashboard's stop and clear", func(t *testing.T) {
		a, d := start(t)
		if !a.StopCurrent(true) {
			t.Error("StopCurrent stopped nothing")
		}
		interrupted(t, d, "hello")
		interrupted(t, d, "the upkeep")
		if !a.WaitIdle(context.Background(), 5*time.Second) {
			t.Fatal("the brain is still busy after a stop that cleared its queues")
		}
		if _, ok := result(d, "more"); ok {
			t.Error("a message the stop cleared ran")
		}
		if _, ok := result(d, "the report"); ok {
			t.Error("an agent's run the stop cleared ran")
		}
	})
	t.Run("nothing in the lane", func(t *testing.T) {
		mgr, _ := limitTestManager(t, 0, 0)
		newGatedRunner().gate(mgr)
		if mgr.Get("a").StopLane(LaneBackground, false) {
			t.Error("StopLane stopped a run in an idle lane")
		}
	})
}

// The bridge's stop gives both lanes' runs their grace, interrupts both,
// and waits for both workers.
func TestShutdownStopsBothLanes(t *testing.T) {
	mgr, d := limitTestManager(t, 0, 0)
	newGatedRunner("the upkeep", "hello").gate(mgr)
	a := mgr.Get("a")
	for _, m := range []Inbound{agentMsg("a", "upkeep", "the upkeep"), dm("hello")} {
		if _, _, _, err := mgr.Submit(m); err != nil {
			t.Fatal(err)
		}
	}
	waitRunning(t, a, LaneBackground, "the upkeep")
	waitRunning(t, a, LaneChat, "hello")
	mgr.Stop()
	for _, text := range []string{"hello", "the upkeep"} {
		if res, ok := result(d, text); !ok || !errors.Is(res.Err, claudecli.ErrInterrupted) {
			t.Errorf("%s: delivered %v, %v; want it interrupted before Stop returned", text, ok, res.Err)
		}
	}
}

// Run slots with max_concurrent_runs: the agents' runs of every brain
// hold at most limit-1 together, so a chat finds a slot while they go on;
// with a limit of one, a chat waits for the one run there may be.
func TestScheduledRunsLeaveASlotForTheChats(t *testing.T) {
	t.Run("limit 2", func(t *testing.T) {
		mgr, d := limitTestManager(t, 0, 2)
		g := newGatedRunner("a's agent", "c's agent").gate(mgr)
		a, c := mgr.Get("a"), mgr.Get("c")
		if _, _, _, err := mgr.Submit(agentMsg("a", "upkeep", "a's agent")); err != nil {
			t.Fatal(err)
		}
		waitRunning(t, a, LaneBackground, "a's agent")
		if _, pos, busy, err := mgr.Submit(agentMsg("c", "upkeep", "c's agent")); err != nil || pos != 1 || !busy {
			t.Errorf("c's agent Submit = #%d busy %v, %v; want it to wait for a slot", pos, busy, err)
		}
		testutil.WaitFor(t, 5*time.Second, "c's agent to wait for a slot", func() bool {
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.lane(LaneBackground).slotWait != nil
		})
		if _, pos, busy, err := mgr.Submit(dm("hello")); err != nil || pos != 1 || busy {
			t.Errorf("DM Submit = #%d busy %v, %v; want it to run at once", pos, busy, err)
		}
		waitDelivered(t, d, "hello")
		if got := runningIn(c, LaneBackground); got != "" {
			t.Errorf("c's agent runs (%q) beside a's: the agents took every slot", got)
		}
		g.open("a's agent")
		waitRunning(t, c, LaneBackground, "c's agent")
		g.open("c's agent")
		waitDelivered(t, d, "c's agent")
	})
	t.Run("limit 1", func(t *testing.T) {
		mgr, d := limitTestManager(t, 0, 1)
		g := newGatedRunner("a's agent").gate(mgr)
		a := mgr.Get("a")
		if _, _, _, err := mgr.Submit(agentMsg("a", "upkeep", "a's agent")); err != nil {
			t.Fatal(err)
		}
		waitRunning(t, a, LaneBackground, "a's agent")
		if _, pos, busy, err := mgr.Submit(dm("hello")); err != nil || pos != 1 || !busy {
			t.Errorf("DM Submit = #%d busy %v, %v; want it to wait for the one slot", pos, busy, err)
		}
		time.Sleep(100 * time.Millisecond)
		if _, ok := result(d, "hello"); ok {
			t.Fatal("the DM ran beside the agent with a limit of one")
		}
		g.open("a's agent")
		waitDelivered(t, d, "hello")
	})
}

// An agent waiting for a slot is not passed over by chats' requests that
// asked after it: once an agent's slot is free, it goes first.
func TestAWaitingAgentIsNotStarvedByChats(t *testing.T) {
	mgr, d := limitTestManager(t, 0, 2)
	g := newGatedRunner("a's agent", "c's agent", "for b", "hello").gate(mgr)
	a, b, c := mgr.Get("a"), mgr.Get("b"), mgr.Get("c")
	if _, _, _, err := mgr.Submit(agentMsg("a", "upkeep", "a's agent")); err != nil {
		t.Fatal(err)
	}
	waitRunning(t, a, LaneBackground, "a's agent")
	if _, _, _, err := mgr.Submit(groupMsg(groupB, "for b", false)); err != nil {
		t.Fatal(err)
	}
	waitRunning(t, b, LaneChat, "for b")
	// Every slot is taken: c's agent asks first, then a's DM.
	if _, _, _, err := mgr.Submit(agentMsg("c", "upkeep", "c's agent")); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 5*time.Second, "c's agent to wait", func() bool { _, waiting, _ := slotState(mgr); return waiting == 1 })
	if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 5*time.Second, "the DM to wait", func() bool { _, waiting, _ := slotState(mgr); return waiting == 2 })
	g.open("a's agent")
	waitRunning(t, c, LaneBackground, "c's agent")
	if got := runningIn(a, LaneChat); got != "" {
		t.Errorf("the DM runs (%q) before c's agent, which asked first, got a slot", got)
	}
	// The DM gets the chats' slot as b's run ends.
	g.open("for b")
	waitRunning(t, a, LaneChat, "hello")
	g.open("hello")
	g.open("c's agent")
	waitDelivered(t, d, "c's agent")
	waitDelivered(t, d, "hello")
}

// StopLaneRun stops a lane's run only when it is the one that started at
// the time given: once that one has ended and the next started, a stop
// aimed at the first leaves the next running.
func TestStopLaneRunStopsOnlyTheRunAskedFor(t *testing.T) {
	mgr, d := limitTestManager(t, 0, 0)
	newGatedRunner("first agent", "second agent").gate(mgr)
	a := mgr.Get("a")
	if _, err := a.StopLaneRun(LaneBackground, time.Now()); !errors.Is(err, ErrNoRun) {
		t.Errorf("an idle lane: %v, want ErrNoRun", err)
	}
	for _, m := range []Inbound{agentMsg("a", "one", "first agent"), agentMsg("a", "two", "second agent")} {
		if _, _, _, err := mgr.Submit(m); err != nil {
			t.Fatal(err)
		}
	}
	waitRunning(t, a, LaneBackground, "first agent")
	first := a.Snapshot().Background.Started

	// The first one ends some other way, and the second starts.
	if !a.StopLane(LaneBackground, false) {
		t.Fatal("StopLane stopped nothing")
	}
	waitRunning(t, a, LaneBackground, "second agent")
	now, err := a.StopLaneRun(LaneBackground, first)
	if !errors.Is(err, ErrOtherRun) || now.Chat != chat.ScheduleChatPrefix+"two" {
		t.Fatalf("a stop aimed at the first run: %+v, %v; want ErrOtherRun naming the second", now, err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := runningIn(a, LaneBackground); got != "second agent" {
		t.Fatalf("the second run is %q after a stop aimed at the first", got)
	}
	if _, err := a.StopLaneRun(LaneChat, now.Started); !errors.Is(err, ErrNoRun) {
		t.Errorf("the chat lane: %v, want ErrNoRun", err)
	}

	stopped, err := a.StopLaneRun(LaneBackground, now.Started)
	if err != nil || stopped.Chat != chat.ScheduleChatPrefix+"two" {
		t.Fatalf("a stop aimed at the second run: %+v, %v", stopped, err)
	}
	if res := waitDelivered(t, d, "second agent"); !errors.Is(res.Err, claudecli.ErrInterrupted) {
		t.Errorf("the second run: %v, want it stopped", res.Err)
	}
}

// delegatedTask is a task a run of brain from delegated to brain to, run
// in slot, the slot of the run that waits for it.
func delegatedTask(from, to, text string, slot *Slot) Inbound {
	return Inbound{Inbound: chat.Inbound{JID: chat.DelegateChatPrefix + "r1", Brain: to, Sender: from, Text: text, Source: chat.SourceDelegate}, Slot: slot}
}

// heldSlots counts the run slots held, by runs and the tasks that share
// them.
func heldSlots(mgr *Manager) int {
	held, _, _ := slotState(mgr)
	return held
}

// A task a run delegates and waits for goes in that run's slot: a
// scheduled agent and its task take one slot, of the agents' share, so a
// chat still finds the one left (limit 2), and the task does not wait
// for the slot of the run that waits for it (limit 1). The task runs in
// its brain's chat lane. The slot is free once both have ended; a task
// whose run has ended already waits for a slot of its own.
func TestADelegatedTaskGoesInItsRunsSlot(t *testing.T) {
	t.Run("limit 2: a chat finds its slot", func(t *testing.T) {
		mgr, d := limitTestManager(t, 0, 2)
		g := newGatedRunner("the upkeep", "the task").gate(mgr)
		a, b := mgr.Get("a"), mgr.Get("b")
		if _, _, _, err := mgr.Submit(agentMsg("a", "upkeep", "the upkeep")); err != nil {
			t.Fatal(err)
		}
		waitRunning(t, a, LaneBackground, "the upkeep")
		if _, pos, busy, err := mgr.Submit(delegatedTask("a", "b", "the task", g.slot(t, "the upkeep"))); err != nil || pos != 1 || busy {
			t.Errorf("the task's Submit = #%d busy %v, %v; want it to run at once", pos, busy, err)
		}
		waitRunning(t, b, LaneChat, "the task")
		if n := heldSlots(mgr); n != 1 {
			t.Errorf("the agent and its task hold %d slots, want one", n)
		}
		if _, pos, busy, err := mgr.Submit(dm("hello")); err != nil || pos != 1 || busy {
			t.Errorf("DM Submit = #%d busy %v, %v; want it to run at once", pos, busy, err)
		}
		waitDelivered(t, d, "hello")
		// The agent's run ends first: the slot stays the task's.
		g.open("the upkeep")
		waitDelivered(t, d, "the upkeep")
		if n := heldSlots(mgr); n != 1 {
			t.Errorf("%d slots held while the task runs on, want its one", n)
		}
		g.open("the task")
		waitDelivered(t, d, "the task")
		testutil.WaitFor(t, 5*time.Second, "the slot to be free", func() bool { return heldSlots(mgr) == 0 })
	})
	t.Run("limit 1: the task does not wait for its run's slot", func(t *testing.T) {
		mgr, d := limitTestManager(t, 0, 1)
		g := newGatedRunner("the upkeep", "the task").gate(mgr)
		a, b := mgr.Get("a"), mgr.Get("b")
		if _, _, _, err := mgr.Submit(agentMsg("a", "upkeep", "the upkeep")); err != nil {
			t.Fatal(err)
		}
		waitRunning(t, a, LaneBackground, "the upkeep")
		if _, _, _, err := mgr.Submit(delegatedTask("a", "b", "the task", g.slot(t, "the upkeep"))); err != nil {
			t.Fatal(err)
		}
		waitRunning(t, b, LaneChat, "the task")
		if _, _, busy, err := mgr.Submit(dm("hello")); err != nil || !busy {
			t.Errorf("DM Submit busy %v, %v; want it to wait for the one slot", busy, err)
		}
		g.open("the upkeep")
		waitDelivered(t, d, "the upkeep")
		time.Sleep(100 * time.Millisecond)
		if _, ok := result(d, "hello"); ok {
			t.Fatal("the DM ran while the task held the one slot")
		}
		g.open("the task")
		waitDelivered(t, d, "hello")
	})
	t.Run("a worker that waits for chatter's slot runs the task", func(t *testing.T) {
		mgr, d := limitTestManager(t, 0, 1)
		g := newGatedRunner("the upkeep", "the task").gate(mgr)
		a, b := mgr.Get("a"), mgr.Get("b")
		if _, _, _, err := mgr.Submit(agentMsg("a", "upkeep", "the upkeep")); err != nil {
			t.Fatal(err)
		}
		waitRunning(t, a, LaneBackground, "the upkeep")
		if _, _, _, err := mgr.Submit(groupMsg(groupB, "chatter", true)); err != nil {
			t.Fatal(err)
		}
		testutil.WaitFor(t, 5*time.Second, "b to wait for a slot", func() bool { return waitsForSlot(b) })
		if _, _, _, err := mgr.Submit(delegatedTask("a", "b", "the task", g.slot(t, "the upkeep"))); err != nil {
			t.Fatal(err)
		}
		waitRunning(t, b, LaneChat, "the task")
		if n := heldSlots(mgr); n != 1 {
			t.Errorf("%d slots held, want the agent's one", n)
		}
		g.open("the task")
		waitDelivered(t, d, "the task")
		time.Sleep(50 * time.Millisecond)
		if _, ok := result(d, "chatter"); ok {
			t.Error("the chatter ran beside the agent with a limit of one")
		}
		g.open("the upkeep")
		waitDelivered(t, d, "chatter")
	})
	t.Run("a task whose run has ended waits for a slot", func(t *testing.T) {
		mgr, d := limitTestManager(t, 0, 1)
		g := newGatedRunner("the upkeep", "hello").gate(mgr)
		a, b := mgr.Get("a"), mgr.Get("b")
		if _, _, _, err := mgr.Submit(agentMsg("a", "upkeep", "the upkeep")); err != nil {
			t.Fatal(err)
		}
		waitRunning(t, a, LaneBackground, "the upkeep")
		slot := g.slot(t, "the upkeep")
		g.open("the upkeep")
		waitDelivered(t, d, "the upkeep")
		if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
			t.Fatal(err)
		}
		waitRunning(t, a, LaneChat, "hello")
		if _, _, busy, err := mgr.Submit(delegatedTask("a", "b", "the task", slot)); err != nil || !busy {
			t.Errorf("the task's Submit busy %v, %v; want it to wait for a slot", busy, err)
		}
		time.Sleep(100 * time.Millisecond)
		if got := runningIn(b, LaneChat); got != "" {
			t.Fatalf("the task runs (%q) in a slot its run no longer holds", got)
		}
		g.open("hello")
		waitDelivered(t, d, "the task")
	})
}

// In interrupt mode a request replaces its sender's own run in the chat
// lane, never the agent's run beside it; and an agent's message, handed
// in, interrupts nothing: it waits for the agent's run before it.
func TestInterruptModeStaysInItsLane(t *testing.T) {
	mgr, d := limitTestManager(t, 0, 0)
	g := newGatedRunner("the upkeep", "first", "second", "the report").gate(mgr)
	a := mgr.Get("a")
	s := *a.settings.Load()
	s.QueueMode = QueueInterrupt
	a.settings.Store(&s)
	for _, m := range []Inbound{agentMsg("a", "upkeep", "the upkeep"), dm("first")} {
		if _, _, _, err := mgr.Submit(m); err != nil {
			t.Fatal(err)
		}
	}
	waitRunning(t, a, LaneBackground, "the upkeep")
	waitRunning(t, a, LaneChat, "first")

	if _, pos, busy, err := mgr.Submit(dm("second")); err != nil || pos != 1 || busy {
		t.Errorf("the second DM's Submit = #%d busy %v, %v; want it to replace the first at once", pos, busy, err)
	}
	if res := waitDelivered(t, d, "first"); !errors.Is(res.Err, claudecli.ErrInterrupted) {
		t.Errorf("the first DM's run: %v, want it interrupted", res.Err)
	}
	waitRunning(t, a, LaneChat, "second")
	if got := runningIn(a, LaneBackground); got != "the upkeep" {
		t.Errorf("background runs %q, want the agent's run going on", got)
	}

	if _, pos, busy, err := mgr.Submit(agentMsg("a", "report", "the report")); err != nil || pos != 1 || !busy {
		t.Errorf("an agent's Submit = #%d busy %v, %v; want it to wait behind the agent's run", pos, busy, err)
	}
	time.Sleep(50 * time.Millisecond)
	if runningIn(a, LaneChat) != "second" || runningIn(a, LaneBackground) != "the upkeep" {
		t.Errorf("after an agent's message: chat lane %q, background %q; want both runs going on", runningIn(a, LaneChat), runningIn(a, LaneBackground))
	}
	g.open("the upkeep")
	if res := waitDelivered(t, d, "the upkeep"); res.Err != nil {
		t.Errorf("the agent's run: %v", res.Err)
	}
	g.open("the report")
	g.open("second")
	waitDelivered(t, d, "the report")
	waitDelivered(t, d, "second")
}

// relimit applies mgr's brains again with max_concurrent_runs n, as a
// dashboard save does.
func relimit(t *testing.T, mgr *Manager, n int) {
	t.Helper()
	var specs []manifest.Spec
	for _, br := range mgr.Brains() {
		specs = append(specs, manifest.Spec{Dir: br.Dir(), Manifest: br.Manifest()})
	}
	if f, err := mgr.Apply(specs, Globals{RunLimit: n, Defaults: Settings{QueueMode: "queue", MaxPending: 100}}, ApplyOptions{}); err != nil || len(f) > 0 {
		t.Fatalf("apply: %v %v", f, err)
	}
}

// A limit lowered while agents hold slots stops none of them; once one
// ends, its slot goes to the chat that waits, not to another agent, while
// the agents still hold their new share; the next agent goes once the
// other agent's run ends.
func TestALoweredLimitWhileAgentsHoldSlots(t *testing.T) {
	mgr, d := limitTestManager(t, 0, 3)
	g := newGatedRunner("a's agent", "c's agent", "b's agent", "for b").gate(mgr)
	a, b, c := mgr.Get("a"), mgr.Get("b"), mgr.Get("c")
	for _, m := range []Inbound{agentMsg("a", "upkeep", "a's agent"), agentMsg("c", "upkeep", "c's agent")} {
		if _, _, _, err := mgr.Submit(m); err != nil {
			t.Fatal(err)
		}
	}
	waitRunning(t, a, LaneBackground, "a's agent")
	waitRunning(t, c, LaneBackground, "c's agent")

	relimit(t, mgr, 2)
	if _, _, busy, err := mgr.Submit(agentMsg("b", "upkeep", "b's agent")); err != nil || !busy {
		t.Errorf("b's agent's Submit busy %v, %v; want it to wait", busy, err)
	}
	testutil.WaitFor(t, 5*time.Second, "b's agent to wait", func() bool { _, waiting, _ := slotState(mgr); return waiting == 1 })
	if _, _, busy, err := mgr.Submit(groupMsg(groupB, "for b", false)); err != nil || !busy {
		t.Errorf("b's chat's Submit busy %v, %v; want it to wait: the agents hold both slots", busy, err)
	}
	testutil.WaitFor(t, 5*time.Second, "b's chat to wait", func() bool { _, waiting, _ := slotState(mgr); return waiting == 2 })
	if runningIn(a, LaneBackground) != "a's agent" || runningIn(c, LaneBackground) != "c's agent" {
		t.Fatal("a lowered limit stopped an agent's run")
	}

	g.open("a's agent")
	waitRunning(t, b, LaneChat, "for b")
	time.Sleep(50 * time.Millisecond)
	if got := runningIn(b, LaneBackground); got != "" {
		t.Errorf("b's agent runs (%q) beside c's with a limit of two", got)
	}
	g.open("c's agent")
	waitRunning(t, b, LaneBackground, "b's agent")
	g.open("for b")
	g.open("b's agent")
	waitDelivered(t, d, "for b")
	waitDelivered(t, d, "b's agent")
}

// An edit or a deletion in a chat, or !stop's stop of the hand-offs that
// answer there, leaves what waits in the background lane alone, an id
// alike or not: a scheduled agent's message is in its own chat. What
// names that chat reaches the lane: an edit there changes the message
// that waits, a deletion drops it, and a stop of the hand-offs in it
// drops what waits without stopping the agent's run in flight.
func TestChangesOfAMessageWaitingInTheBackgroundLane(t *testing.T) {
	mgr, d := limitTestManager(t, 0, 0)
	g := newGatedRunner("first agent").gate(mgr)
	a := mgr.Get("a")
	if _, _, _, err := mgr.Submit(agentMsg("a", "one", "first agent")); err != nil {
		t.Fatal(err)
	}
	waitRunning(t, a, LaneBackground, "first agent")
	ended := map[string]chan error{}
	for _, id := range []string{"two", "three"} {
		m := agentMsg("a", id, id+" agent")
		m.ID = "N-" + id
		done := make(chan error, 1)
		m.Done = func(err error) { done <- err }
		ended[id] = done
		if _, _, _, err := mgr.Submit(m); err != nil {
			t.Fatal(err)
		}
	}
	waits := func() []string {
		var out []string
		for _, q := range a.Snapshot().BackgroundQueue {
			out = append(out, q.Chat+" "+q.Preview)
		}
		return out
	}
	before := waits()

	for _, c := range []chat.Change{
		{Kind: chat.ChangeEdit, Chat: testDMChat, Sender: "schedule", Target: "N-two", Text: "changed"},
		{Kind: chat.ChangeRevoke, Chat: testDMChat, Sender: "schedule", Target: "N-three"},
	} {
		if waiting, _ := a.Change(c); waiting {
			t.Errorf("the %s in the DM changed a message waiting in the background lane", c.Kind)
		}
	}
	if stopped, n := a.StopHandOffs(testDMChat); stopped || n != 0 {
		t.Errorf("StopHandOffs(DM) = %v, %d; want nothing of the agents' stopped", stopped, n)
	}
	if got := waits(); len(got) != 2 || !slices.Equal(got, before) {
		t.Errorf("the background queue is %q, want it as it was, %q", got, before)
	}

	waiting, edited := a.Change(chat.Change{Kind: chat.ChangeEdit, Chat: chat.ScheduleChatPrefix + "two", Sender: "schedule", Target: "N-two", Text: "two agent, edited"})
	if !waiting || edited == nil || edited.Text != "two agent, edited" {
		t.Errorf("an edit in the agent's chat: waiting %v, edited %+v", waiting, edited)
	}
	if waiting, _ := a.Change(chat.Change{Kind: chat.ChangeRevoke, Chat: chat.ScheduleChatPrefix + "three", Sender: "schedule", Target: "N-three"}); !waiting {
		t.Error("a deletion in the agent's chat found nothing waiting")
	}
	if err := <-ended["three"]; !errors.Is(err, ErrDropped) {
		t.Errorf("the deleted message: %v, want it dropped", err)
	}
	if stopped, n := a.StopHandOffs(chat.ScheduleChatPrefix + "two"); stopped || n != 1 {
		t.Errorf("StopHandOffs(schedule:two) = %v, %d; want what waits dropped and no run stopped", stopped, n)
	}
	if err := <-ended["two"]; !errors.Is(err, ErrDropped) {
		t.Errorf("the edited message: %v, want it dropped", err)
	}
	if got := runningIn(a, LaneBackground); got != "first agent" {
		t.Errorf("background runs %q, want the first agent's run going on", got)
	}
	g.open("first agent")
	waitDelivered(t, d, "first agent")
	if !a.WaitIdle(context.Background(), 5*time.Second) {
		t.Fatal("the brain is still busy")
	}
	if got := g.ran(); !slices.Equal(got, []string{"first agent"}) {
		t.Errorf("ran %q, want only the first agent", got)
	}
}
