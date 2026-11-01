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
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// gatedRunner stands in for the CLI: a run whose text has a gate goes on
// until the gate is opened or the run is stopped, any other answers at
// once.
type gatedRunner struct {
	mu      sync.Mutex
	gates   map[string]chan struct{}
	started []string // the texts of the runs, in the order they started
}

func newGatedRunner(gated ...string) *gatedRunner {
	g := &gatedRunner{gates: map[string]chan struct{}{}}
	for _, text := range gated {
		g.gates[text] = make(chan struct{})
	}
	return g
}

func (g *gatedRunner) Run(ctx context.Context, spec RunSpec, c claudecli.Callbacks) claudecli.Result {
	text := spec.Message.Text
	g.mu.Lock()
	g.started = append(g.started, text)
	gate := g.gates[text]
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
// one lane's run; the dashboard's stop (StopCurrent) stops both, and with
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
		if !a.StopLane(LaneBackground) {
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
		if !a.StopLane(LaneChat) {
			t.Error("StopLane(chat) stopped nothing")
		}
		interrupted(t, d, "hello")
		waitRunning(t, a, LaneChat, "more")
		if got := runningIn(a, LaneBackground); got != "the upkeep" {
			t.Errorf("background runs %q, want the agent's run going on", got)
		}
	})
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
		if mgr.Get("a").StopLane(LaneBackground) {
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
