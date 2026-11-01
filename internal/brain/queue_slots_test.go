package brain

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// slotState is what the run slots hold: how many slots are held and how
// many wait, and whether a held one was asked to give way.
func slotState(mgr *Manager) (held, waiting int, stopped bool) {
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
	return br.lane(LaneChat).slotWait != nil
}

// hasRun waits until br runs claude.
func hasRun(t *testing.T, br *Brain) {
	t.Helper()
	testutil.WaitFor(t, 5*time.Second, br.ID()+"'s run", func() bool { r := br.Snapshot().Running; return r != nil && r.PID > 0 })
}

// running counts the brains with a run going.
func running(mgr *Manager) int {
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
			testutil.WaitFor(t, 20*time.Second, "three runs", func() bool {
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
	testutil.WaitFor(t, 5*time.Second, "a's first run", func() bool { r := a.Snapshot().Running; return r != nil && r.PID > 0 })
	for _, m := range []Inbound{dm("a 2"), {Inbound: chat.Inbound{JID: "120363000000000001@g.us", Sender: otherNumber, Text: "c 1"}}} {
		if _, _, _, err := mgr.Submit(m); err != nil {
			t.Fatal(err)
		}
	}
	most := 0
	testutil.WaitFor(t, 20*time.Second, "three runs", func() bool {
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
	testutil.WaitFor(t, 5*time.Second, "b's observe-only run", func() bool { r := b.Snapshot().Running; return r != nil && r.PID > 0 })
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "10")
	if _, _, _, err := mgr.Submit(dm("question")); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 15*time.Second, "both runs to end", func() bool { return d.count() == 2 })
	got := map[string]error{}
	for _, r := range d.all() {
		got[r.msg.Text] = r.res.Err
	}
	if !errors.Is(got["chatter here"], claudecli.ErrInterrupted) || got["question"] != nil {
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
	testutil.WaitFor(t, 5*time.Second, "c to wait for a slot", func() bool { return waitsForSlot(c) })
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "10")
	if _, _, err := c.Submit(groupMsg(groupC, "question for c", false)); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 10*time.Second, "both runs to end", func() bool { return d.count() == 2 })
	got := map[string]error{}
	for _, r := range d.all() {
		got[r.msg.Text] = r.res.Err
	}
	if !errors.Is(got["chatter in b"], claudecli.ErrInterrupted) || got["question for c"] != nil {
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
	testutil.WaitFor(t, 5*time.Second, "a to wait for a slot", func() bool { return waitsForSlot(a) })
	a.retire(false)
	testutil.WaitFor(t, 3*time.Second, "a's worker to end", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return !a.lane(LaneChat).working
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
	testutil.WaitFor(t, 5*time.Second, "b to wait for a slot", func() bool { return waitsForSlot(b) })

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
	testutil.WaitFor(t, 5*time.Second, "b to get the slot", func() bool { held, waiting, _ := slotState(mgr); return held == 1 && waiting == 0 })
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "10")
	if _, _, err := a.Submit(dm("question")); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 5*time.Second, "a to ask b's slot to give way", func() bool { _, _, stopped := slotState(mgr); return stopped })
	b.mu.Unlock()
	locked = false

	testutil.WaitFor(t, 10*time.Second, "a's answer", func() bool {
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
