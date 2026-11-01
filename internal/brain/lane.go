package brain

import (
	"maps"
	"slices"

	"github.com/Icewater1337/fridolin/internal/chat"
)

// Lane is one of a brain's two queues. Each runs one message at a time,
// with a worker and a run of its own, and the two run beside each other
// in the brain's folder, each in a run slot (see runSlots).
type Lane int

// The lanes. A scheduled agent's own chat (schedule:<name>) runs in the
// background lane: its scheduled runs, and the runs FrIDE's news starts
// for the work it handed over, which can take hours. Everything else, the
// brain's chats and the turns of its live session among them, runs in the
// chat lane, which therefore never waits for an agent.
const (
	LaneChat Lane = iota
	LaneBackground
	laneCount
)

// String names the lane as the API and the tools do: chat or background.
func (l Lane) String() string {
	if l == LaneBackground {
		return "background"
	}
	return "chat"
}

// ParseLane returns the lane name names, and false for no lane.
func ParseLane(name string) (Lane, bool) {
	switch name {
	case "chat":
		return LaneChat, true
	case "background":
		return LaneBackground, true
	}
	return LaneChat, false
}

// LaneOf returns the lane a message for chat jid runs in.
func LaneOf(jid string) Lane {
	if chat.IsScheduleChat(jid) {
		return LaneBackground
	}
	return LaneChat
}

// lane is a brain's queue in one Lane, under Brain.mu.
type lane struct {
	which   Lane // fixed when the Brain is made (newLanes)
	current *brainRun
	// pending holds what waits, in the order it came in. In the chat
	// lane, each chat's messages in it run in order, and which chat goes
	// next is nextIn's choice; the background lane runs them in the order
	// they came in.
	pending []Inbound
	// turns is, by chat, when its last run started, counted by turn: the
	// chat whose turn is longest ago goes next. Chats with nothing waiting
	// are forgotten once that no longer decides anything (takeTurn).
	turns   map[string]uint64
	turn    uint64
	working bool // a worker goroutine is draining pending
	// slotWait is set while the worker waits for a run slot.
	slotWait *slotWait
	// last is the last run that finished in this lane.
	last *RunInfo
}

// newLanes returns a new Brain's lanes, empty.
func newLanes() [laneCount]lane {
	var lanes [laneCount]lane
	for l := range lanes {
		lanes[l].which = Lane(l)
	}
	return lanes
}

// lane returns the brain's lane l.
func (br *Brain) lane(l Lane) *lane { return &br.lanes[l] }

// laneFor returns the lane a message for chat jid runs in.
func (br *Brain) laneFor(jid string) *lane { return br.lane(LaneOf(jid)) }

// eachLane returns the brain's lanes, the chat lane first.
func (br *Brain) eachLane() []*lane { return []*lane{br.lane(LaneChat), br.lane(LaneBackground)} }

// background reports whether the lane is the background lane.
func (q *lane) background() bool { return q.which == LaneBackground }

// next returns where in pending the message to run next is: in the chat
// lane nextIn's choice, in the background lane the first that came in.
func (q *lane) next() int {
	if q.background() {
		return 0
	}
	return nextIn(q.pending, q.turns)
}

// takeTurn records that a run for chat starts now. A chat with nothing
// waiting whose turn came before every waiting chat's is forgotten: it
// would go before all of them anyway, as one that never had a turn does,
// so the record is no longer needed.
func (q *lane) takeTurn(chatJID string) {
	if q.turns == nil {
		q.turns = map[string]uint64{}
	}
	q.turn++
	q.turns[chatJID] = q.turn
	low := q.turn
	waiting := map[string]bool{}
	for _, p := range q.pending {
		waiting[p.JID] = true
		low = min(low, q.turns[p.JID])
	}
	for c, t := range q.turns {
		if !waiting[c] && t < low {
			delete(q.turns, c)
		}
	}
}

// order returns the indexes of pending in the order the messages will
// run as things stand (see next). In the chat lane, messages that come
// later can still go before some of them, in a chat whose turn comes
// first.
func (q *lane) order() []int {
	idx := make([]int, len(q.pending))
	for i := range idx {
		idx[i] = i
	}
	if q.background() {
		return idx
	}
	rest := slices.Clone(q.pending)
	turns := maps.Clone(q.turns)
	if turns == nil {
		turns = map[string]uint64{}
	}
	turn := q.turn
	order := make([]int, 0, len(rest))
	for len(rest) > 0 {
		i := nextIn(rest, turns)
		order = append(order, idx[i])
		turn++
		turns[rest[i].JID] = turn
		rest = slices.Delete(rest, i, i+1)
		idx = slices.Delete(idx, i, i+1)
	}
	return order
}

// hasRoom reports whether the lane takes m: it has a free place, or one m
// makes by what it replaces, or untagged chatter to drop. Each lane has
// max_pending places of its own, so that agents' work waiting never turns
// a chat's message away, nor the other way round.
func (q *lane) hasRoom(m Inbound, s *Settings) bool {
	_, replaces := supersedes(m, s.QueueMode)
	n, silent := 0, false
	for _, p := range q.pending {
		if !replaces(p) {
			n++
			silent = silent || p.Silent
		}
	}
	return n < s.MaxPending || silent
}

// takeWhereLocked takes the messages waiting in either lane that match
// out of their queues and returns them, the chat lane's first. Brain.mu is
// held.
func (br *Brain) takeWhereLocked(match func(Inbound) bool) []Inbound {
	var gone []Inbound
	for _, q := range br.eachLane() {
		kept := q.pending[:0]
		for _, p := range q.pending {
			if match(p) {
				gone = append(gone, p)
				continue
			}
			kept = append(kept, p)
		}
		q.pending = kept
	}
	return gone
}

// runWhereLocked returns the runs in flight, at most one in each lane,
// whose message matches. Brain.mu is held.
func (br *Brain) runWhereLocked(match func(Inbound) bool) []*brainRun {
	var runs []*brainRun
	for _, q := range br.eachLane() {
		if cur := q.current; cur != nil && match(cur.msg) {
			runs = append(runs, cur)
		}
	}
	return runs
}
