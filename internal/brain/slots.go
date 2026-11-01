package brain

import (
	"slices"
	"sync"
)

// runSlots bounds how many Claude runs go at once across all brains
// (max_concurrent_runs). Each run is a full Claude Code agent with tools,
// and every brain works on its own queue, so without a bound five busy
// brains are five runs at once, on the CPU and on the account's rate
// limits that the next request depends on.
//
// A brain's worker, one for each of its lanes, waits for a slot before it
// takes its next message. Slots go to work addressed to a brain before
// untagged chatter (an observe-only run), and otherwise in the order the
// workers asked, so a brain that just had a run asks again behind the
// others. A request that finds every slot taken stops an observe-only run,
// in whichever brain, as one does within its own brain: the chatter is in
// the history, where the next run in its chat reads it.
//
// The background lanes' runs, scheduled agents' that can take hours, hold
// at most limit-1 slots together (backgroundCap): with a limit of two or
// more, one slot is always free or held by a chat's run, so a chat's
// message never waits for agents alone, only behind other chats' runs.
// Among requests, a background run still gets its slot in the order it
// asked, ahead of the chats' requests that asked after it, unless the
// background runs hold their share: chats cannot starve it either, and it
// waits at most for the runs that asked before it, and, at its cap, for
// the first agent's run to end. With a limit of one there is nothing to
// keep free: every run waits for the one that goes, as the limit says.
//
// It calls nothing while it holds its lock. What stops an observe-only run
// is returned to the caller, who calls it once it holds no lock of its own.
type runSlots struct {
	mu      sync.Mutex
	limit   int // 0 for no limit
	paused  int // replay holds grants until queued edits/deletions apply
	held    []*runSlot
	waiting []*slotWait
}

// runSlot is a slot a brain holds for one run.
type runSlot struct {
	slots *runSlots
	// request is set while the slot runs work addressed to the brain,
	// which no other request may stop.
	request bool
	// background is set for a brain's background lane (see Lane), whose
	// runs hold at most backgroundCap slots.
	background bool
	// stop stops the observe-only run in the slot, for a request that
	// waits; stopped is set once that was asked.
	stop    func()
	stopped bool
}

// slotWait is a brain waiting for a slot.
type slotWait struct {
	slot  *runSlot
	ready chan struct{} // closed once it got the slot or gave up
	state int           // one of the slot states, under runSlots.mu
}

// The states of a slotWait.
const (
	slotWaiting = iota
	slotGranted
	slotTaken  // its worker has the slot
	slotGaveUp // its brain was retired while it waited
)

func newRunSlots(limit int) *runSlots { return &runSlots{limit: limit} }

// pause holds new grants until the returned function is called. Replay
// uses it so restoring a queued message cannot start a run before a later
// persisted deletion or edit has been applied to that message.
func (rs *runSlots) pause() func() {
	rs.mu.Lock()
	rs.paused++
	rs.mu.Unlock()
	return sync.OnceFunc(func() {
		rs.mu.Lock()
		defer rs.mu.Unlock()
		rs.paused--
		rs.grantLocked()
	})
}

// setLimit changes the limit. More slots go to those waiting at once; with
// fewer, runs going on finish and the next waits until one is free. On no
// slots at all, a manager's that runs nothing, it does nothing.
func (rs *runSlots) setLimit(n int) {
	if rs == nil {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.limit = n
	rs.grantLocked()
}

// wait asks for a slot for a request (or, with request false, for chatter),
// for a background lane's run with background, whose observe-only run
// stop stops. The slot comes when w.ready is closed, at once when one is
// free. Call the stop it returns, unless nil, with no lock held: it stops
// an observe-only run that gives way.
func (rs *runSlots) wait(request, background bool, stop func()) (w *slotWait, stopOther func()) {
	w = &slotWait{slot: &runSlot{slots: rs, request: request, background: background, stop: stop}, ready: make(chan struct{})}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.waiting = append(rs.waiting, w)
	rs.grantLocked()
	if w.state == slotWaiting && request && rs.withinCapLocked(w.slot) {
		stopOther = rs.yieldLocked()
	}
	return w, stopOther
}

// busy reports whether a run asked for now, for a request or for chatter,
// and for a background lane's run with background, would wait for its
// slot: every slot is taken (or every one background runs may hold) and,
// for a request, none of them by an observe-only run that would give way
// to it. A brain that has nothing else to do tells a request's sender so,
// as when it works on another message: other runs can take minutes. On no
// slots at all, it reports false.
func (rs *runSlots) busy(request, background bool) bool {
	if rs == nil {
		return false
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if background && !rs.withinCapLocked(&runSlot{background: true}) {
		return true
	}
	if rs.limit <= 0 || len(rs.held) < rs.limit {
		return false
	}
	return !request || !slices.ContainsFunc(rs.held, givesWay)
}

// backgroundCapLocked is how many slots the background lanes' runs may
// hold together: one fewer than the limit, so that one is left for the
// chats, but at least one; 0 for no limit.
func (rs *runSlots) backgroundCapLocked() int {
	if rs.limit <= 0 {
		return 0
	}
	return max(1, rs.limit-1)
}

// withinCapLocked reports whether s may get a slot as far as
// backgroundCap goes: it is not a background run, or the background runs
// hold fewer than their cap.
func (rs *runSlots) withinCapLocked(s *runSlot) bool {
	limit := rs.backgroundCapLocked()
	if !s.background || limit == 0 {
		return true
	}
	n := 0
	for _, h := range rs.held {
		if h.background {
			n++
		}
	}
	return n < limit
}

// givesWay reports whether the observe-only run in s would give way to a
// request that waits: it was not asked to yet.
func givesWay(s *runSlot) bool { return !s.request && !s.stopped && s.stop != nil }

// upgrade makes w, still waiting, a wait for a request: its brain got one
// while it waited for chatter's slot. It returns what wait does.
func (rs *runSlots) upgrade(w *slotWait) (stopOther func()) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if w.state != slotWaiting || w.slot.request {
		return nil
	}
	w.slot.request = true
	return rs.yieldLocked()
}

// take returns the slot w waited for, once w.ready is closed, or false
// when its brain gave up waiting.
func (rs *runSlots) take(w *slotWait) (*runSlot, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if w.state != slotGranted {
		return nil, false
	}
	w.state = slotTaken
	return w.slot, true
}

// giveUp ends w's wait, for a brain that was retired: a slot it got and
// nobody took goes to the next.
func (rs *runSlots) giveUp(w *slotWait) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	switch w.state {
	case slotWaiting:
		for i, o := range rs.waiting {
			if o == w {
				rs.waiting = append(rs.waiting[:i], rs.waiting[i+1:]...)
				break
			}
		}
		close(w.ready)
	case slotGranted:
		rs.releaseLocked(w.slot)
	default:
		return
	}
	w.state = slotGaveUp
}

// running says what the slot runs, once its brain picked the message. It
// reports false for chatter when a request asked the slot to give way
// before its run began: the run would be stopped as it starts.
func (s *runSlot) running(request bool) bool {
	s.slots.mu.Lock()
	defer s.slots.mu.Unlock()
	s.request = request
	return request || !s.stopped
}

// release frees the slot once its run is over.
func (s *runSlot) release() {
	s.slots.mu.Lock()
	defer s.slots.mu.Unlock()
	s.slots.releaseLocked(s)
}

func (rs *runSlots) releaseLocked(s *runSlot) {
	for i, h := range rs.held {
		if h == s {
			rs.held = append(rs.held[:i], rs.held[i+1:]...)
			break
		}
	}
	rs.grantLocked()
}

// grantLocked hands free slots to those waiting: requests first, then
// chatter, each in the order they asked, passing over background runs
// while theirs hold all they may (backgroundCap).
func (rs *runSlots) grantLocked() {
	if rs.paused > 0 {
		return
	}
	for rs.limit <= 0 || len(rs.held) < rs.limit {
		i := rs.nextLocked()
		if i < 0 {
			return
		}
		w := rs.waiting[i]
		rs.waiting = append(rs.waiting[:i], rs.waiting[i+1:]...)
		rs.held = append(rs.held, w.slot)
		w.state = slotGranted
		close(w.ready)
	}
}

// nextLocked returns where in waiting the next to get a slot is, -1 for
// nobody: the first request that may have one, else the first chatter.
func (rs *runSlots) nextLocked() int {
	chatter := -1
	for i, w := range rs.waiting {
		switch {
		case !rs.withinCapLocked(w.slot):
		case w.slot.request:
			return i
		case chatter < 0:
			chatter = i
		}
	}
	return chatter
}

// yieldLocked picks an observe-only run that gives way to a request
// waiting for a slot, and returns what stops it, or nil when there is
// none. Each such run is asked once.
func (rs *runSlots) yieldLocked() func() {
	for _, h := range rs.held {
		if givesWay(h) {
			h.stopped = true
			return h.stop
		}
	}
	return nil
}
