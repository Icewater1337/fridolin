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
// A request stops an observe-only run only when that gets it a slot: the
// slots already free and those of the runs already asked to stop go to
// the others waiting before it, in the order they are handed out, and
// to no background run past its cap. So two agents that find one
// observe-only run each in their way stop one, not two, when only one of
// them may have a slot.
//
// A task a run delegates and waits for runs in that run's slot (Slot)
// rather than in one of its own: the run waits meanwhile, so the two are
// one run's work, and a scheduled agent's task takes no slot the chats
// were to have. The slot is free once both have ended.
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
	// holders counts the runs in the slot while it is held: its own, and
	// the delegated tasks that share it (Slot.share). It is free once the
	// last of them has released it.
	holders int
}

// Slot is a run's run slot, as the run hands it to a task it delegates
// (RunSpec.Slot, Inbound.Slot): the task's run goes in it while the run
// that waits for the task holds it, rather than wait for a slot of its
// own. nil without run slots.
type Slot struct{ slot *runSlot }

// slotOf is s as a Slot, nil for none.
func slotOf(s *runSlot) *Slot {
	if s == nil {
		return nil
	}
	return &Slot{slot: s}
}

// held reports whether the slot is still held, by its run or a task's.
func (s *Slot) held() bool {
	if s == nil {
		return false
	}
	s.slot.slots.mu.Lock()
	defer s.slot.slots.mu.Unlock()
	return s.slot.holders > 0
}

// share has one more run go in the slot while it is held, and returns
// it; nil once the slot is free, when the run waits for one of its own.
// The run releases it as it would its own (release).
func (s *Slot) share() *runSlot {
	if s == nil {
		return nil
	}
	rs := s.slot.slots
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if s.slot.holders == 0 {
		return nil
	}
	s.slot.holders++
	return s.slot
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
	if w.state == slotWaiting && request && rs.yieldGetsLocked(w) {
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
	return rs.capAllowsLocked(s, rs.backgroundHeldLocked())
}

// backgroundHeldLocked counts the slots background runs hold.
func (rs *runSlots) backgroundHeldLocked() int {
	n := 0
	for _, h := range rs.held {
		if h.background {
			n++
		}
	}
	return n
}

// capAllowsLocked reports whether s may get a slot while background runs
// hold n: it is not a background run, or n is below their cap.
func (rs *runSlots) capAllowsLocked(s *runSlot, n int) bool {
	limit := rs.backgroundCapLocked()
	return !s.background || limit == 0 || n < limit
}

// givesWay reports whether the observe-only run in s would give way to a
// request that waits: it was not asked to yet.
func givesWay(s *runSlot) bool { return !s.request && !s.stopped && s.stop != nil }

// givingWay reports whether the observe-only run in s was asked to give
// way: its slot is about to be free.
func givingWay(s *runSlot) bool { return !s.request && s.stopped }

// yieldGetsLocked reports whether one more observe-only run that gives
// way would get w, a request waiting, its slot. The slots free now (a
// pause holds them back) and those of the runs already asked to give way
// go first, in the order grantLocked hands them out, to those waiting
// before w, or to w itself, which then needs no other run stopped; a
// background run past its cap gets none of them. Only the slot after
// those is worth stopping a run for, and only when it would be w's.
// Without a limit there is a slot for everyone once a pause ends.
func (rs *runSlots) yieldGetsLocked(w *slotWait) bool {
	if rs.limit <= 0 {
		return false
	}
	free, background := max(0, rs.limit-len(rs.held)), 0
	for _, h := range rs.held {
		switch {
		case givingWay(h):
			free++
		case h.background:
			background++
		}
	}
	served := map[*slotWait]bool{}
	for range free + 1 {
		i := rs.nextAmongLocked(background, served)
		if i < 0 {
			return false
		}
		next := rs.waiting[i]
		if next == w {
			return len(served) == free
		}
		served[next] = true
		if next.slot.background {
			background++
		}
	}
	return false
}

// upgrade makes w, still waiting, a wait for a request: its brain got one
// while it waited for chatter's slot. It returns what wait does.
func (rs *runSlots) upgrade(w *slotWait) (stopOther func()) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if w.state != slotWaiting || w.slot.request {
		return nil
	}
	w.slot.request = true
	if !rs.yieldGetsLocked(w) {
		return nil
	}
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

// giveUp ends w's wait, for a brain that was retired, or a worker whose
// next message is to go in a slot it shares (see Submit): a slot it got
// and nobody took goes to the next.
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

// release gives the slot back once its run is over: it is free once no
// other run shares it (see Slot).
func (s *runSlot) release() {
	s.slots.mu.Lock()
	defer s.slots.mu.Unlock()
	if s.holders--; s.holders > 0 {
		return
	}
	s.slots.releaseLocked(s)
}

func (rs *runSlots) releaseLocked(s *runSlot) {
	s.holders = 0
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
		w.slot.holders = 1
		w.state = slotGranted
		close(w.ready)
	}
}

// nextLocked returns where in waiting the next to get a slot is, -1 for
// nobody: the first request that may have one, else the first chatter.
func (rs *runSlots) nextLocked() int {
	return rs.nextAmongLocked(rs.backgroundHeldLocked(), nil)
}

// nextAmongLocked is nextLocked while background runs hold n slots,
// passing over those in served.
func (rs *runSlots) nextAmongLocked(n int, served map[*slotWait]bool) int {
	chatter := -1
	for i, w := range rs.waiting {
		switch {
		case served[w] || !rs.capAllowsLocked(w.slot, n):
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
