package brain

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

// slotScript drives runSlots through a test: named waits, and what
// happened to them.
type slotScript struct {
	t       *testing.T
	rs      *runSlots
	waits   map[string]*slotWait
	slots   map[string]*runSlot
	stopped []string // the holders asked to give way, in order
}

func newSlotScript(t *testing.T, limit int) *slotScript {
	return &slotScript{t: t, rs: newRunSlots(limit), waits: map[string]*slotWait{}, slots: map[string]*runSlot{}}
}

// do runs one step: "+name" waits for a request, "~name" for chatter,
// "*name" for a background lane's run (a scheduled agent's), "-name"
// releases name's slot, "^name" upgrades name's wait, "xname" gives
// name's wait up and "=n" sets the limit to n. A slot that is ready is
// taken at once.
func (s *slotScript) do(step string) {
	s.t.Helper()
	name := step[1:]
	var stopOther func()
	switch step[0] {
	case '+', '~', '*':
		stop := func() { s.stopped = append(s.stopped, name) }
		s.waits[name], stopOther = s.rs.wait(step[0] != '~', step[0] == '*', stop)
	case '-':
		s.slots[name].release()
		delete(s.slots, name)
	case '^':
		stopOther = s.rs.upgrade(s.waits[name])
	case 'x':
		s.rs.giveUp(s.waits[name])
	case '=':
		n, err := strconv.Atoi(name)
		if err != nil {
			s.t.Fatal(err)
		}
		s.rs.setLimit(n)
	default:
		s.t.Fatalf("bad step %q", step)
	}
	if stopOther != nil {
		stopOther()
	}
	for n, w := range s.waits {
		select {
		case <-w.ready:
			if slot, ok := s.rs.take(w); ok {
				s.slots[n] = slot
			}
			delete(s.waits, n)
		default:
		}
	}
}

// running lists who holds a slot, sorted.
func (s *slotScript) running() string {
	var out []string
	for n := range s.slots {
		out = append(out, n)
	}
	slices.Sort(out)
	return strings.Join(out, " ")
}

func TestRunSlots(t *testing.T) {
	cases := []struct {
		name    string
		limit   int
		steps   []string
		running string // who holds a slot at the end
		stopped string // who was asked to give way
	}{
		{"no more than the limit", 2, []string{"+a", "+b", "+c"}, "a b", ""},
		{"a release lets the next in", 2, []string{"+a", "+b", "+c", "-a"}, "b c", ""},
		{"no limit", 0, []string{"+a", "+b", "+c", "~d"}, "a b c d", ""},
		{"in the order they asked", 1, []string{"+a", "+b", "+c", "-a"}, "b", ""},
		{"requests before chatter", 1, []string{"+a", "~b", "+c", "-a"}, "c", ""},
		{"chatter once no request waits", 1, []string{"+a", "~b", "+c", "-a", "-c"}, "b", ""},
		{"a request stops chatter that holds a slot", 1, []string{"~a", "+b"}, "a", "a"},
		{"each chatter run is asked once", 1, []string{"~a", "+b", "+c"}, "a", "a"},
		{"two requests stop two chatter runs", 2, []string{"~a", "~b", "+c", "+d"}, "a b", "a b"},
		{"a request never stops a request", 1, []string{"+a", "+b"}, "a", ""},
		{"chatter never stops anything", 1, []string{"~a", "~b"}, "a", ""},
		{"the stopped run's slot goes to the request", 1, []string{"~a", "~b", "+c", "-a"}, "c", "a"},
		{"an upgraded wait goes ahead of chatter", 1, []string{"+a", "~b", "~c", "^c", "-a"}, "c", ""},
		{"an upgrade stops chatter", 1, []string{"~a", "~b", "^b"}, "a", "a"},
		{"a wait given up leaves the queue", 1, []string{"+a", "+b", "+c", "xb", "-a"}, "c", ""},
		{"a raised limit lets those waiting in", 1, []string{"+a", "+b", "+c", "=3"}, "a b c", ""},
		{"a lowered limit holds the next back", 3, []string{"+a", "+b", "=1", "+c", "-a"}, "b", ""},
		// Scheduled agents' runs (background lanes) leave a slot for the
		// chats: they hold at most limit-1 together.
		{"agents leave a slot for the chats", 2, []string{"*a", "*b", "+c"}, "a c", ""},
		{"agents take up to limit-1", 3, []string{"*a", "*b", "*c"}, "a b", ""},
		{"no limit, no cap", 0, []string{"*a", "*b", "*c"}, "a b c", ""},
		{"a limit of one has nothing to keep free", 1, []string{"*a", "+b"}, "a", ""},
		{"an agent at its cap waits for an agent's slot", 2, []string{"*a", "*b", "+c", "-c"}, "a", ""},
		{"a chat's request passes an agent at its cap", 2, []string{"*a", "+c", "*b", "+d", "-c"}, "a d", ""},
		{"the agent goes once an agent's slot is free", 2, []string{"*a", "+c", "*b", "+d", "-c", "-a"}, "b d", ""},
		{"an agent below its cap in the order asked", 2, []string{"+a", "+b", "*c", "+d", "-a"}, "b c", ""},
		{"agents before chatter", 2, []string{"+a", "+b", "~c", "*d", "-a"}, "b d", ""},
		{"an agent stops chatter for a slot", 1, []string{"~a", "*b"}, "a", "a"},
		{"an agent at its cap stops no chatter", 2, []string{"*a", "~b", "*c"}, "a b", ""},
		{"a raised limit lets agents in", 2, []string{"*a", "*b", "*c", "=4"}, "a b c", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSlotScript(t, c.limit)
			for _, step := range c.steps {
				s.do(step)
			}
			if got := s.running(); got != c.running {
				t.Errorf("running %q, want %q", got, c.running)
			}
			if got := strings.Join(s.stopped, " "); got != c.stopped {
				t.Errorf("asked to give way: %q, want %q", got, c.stopped)
			}
		})
	}
}

// A run asked for now waits when every slot is taken, unless it is a
// request and one of them is chatter's that gives way to it.
func TestRunSlotsBusy(t *testing.T) {
	cases := []struct {
		name             string
		limit            int
		steps            []string
		request, chatter bool // whether a request or chatter would wait
	}{
		{"no limit", 0, []string{"+a", "+b"}, false, false},
		{"a slot free", 2, []string{"+a"}, false, false},
		{"taken by a request", 1, []string{"+a"}, true, true},
		{"taken by chatter, which gives way", 1, []string{"~a"}, false, true},
		{"taken by chatter already asked to give way", 1, []string{"~a", "+b"}, true, true},
		{"chatter beside a request", 2, []string{"+a", "~b"}, false, true},
		{"a raised limit frees one", 1, []string{"+a", "=2"}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSlotScript(t, c.limit)
			for _, step := range c.steps {
				s.do(step)
			}
			if got := s.rs.busy(true, false); got != c.request {
				t.Errorf("a request waits: %v, want %v", got, c.request)
			}
			if got := s.rs.busy(false, false); got != c.chatter {
				t.Errorf("chatter waits: %v, want %v", got, c.chatter)
			}
		})
	}
	var none *runSlots
	if none.busy(true, false) {
		t.Error("no slots at all keep a request waiting")
	}
}

// A scheduled agent's run waits once the agents' runs hold limit-1
// slots, while a chat's request finds the slot they leave.
func TestRunSlotsBusyForAgents(t *testing.T) {
	cases := []struct {
		name        string
		limit       int
		steps       []string
		agent, chat bool // whether an agent's run or a chat's request would wait
	}{
		{"nothing runs", 2, nil, false, false},
		{"an agent holds its share", 2, []string{"*a"}, true, false},
		{"a chat holds one", 2, []string{"+a"}, false, false},
		{"all taken", 2, []string{"*a", "+b"}, true, true},
		{"a limit of one", 1, []string{"+a"}, true, true},
		{"no limit", 0, []string{"*a", "*b"}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSlotScript(t, c.limit)
			for _, step := range c.steps {
				s.do(step)
			}
			if got := s.rs.busy(true, true); got != c.agent {
				t.Errorf("an agent's run waits: %v, want %v", got, c.agent)
			}
			if got := s.rs.busy(true, false); got != c.chat {
				t.Errorf("a chat's request waits: %v, want %v", got, c.chat)
			}
		})
	}
}

// A slot granted to a brain that was retired before it took it goes to
// the next; and chatter whose slot was asked to give way before its run
// began does not start.
func TestRunSlotsHandOverWhatIsNotUsed(t *testing.T) {
	rs := newRunSlots(1)
	a, _ := rs.wait(true, false, nil)
	sa, _ := rs.take(a)
	b, _ := rs.wait(true, false, nil)
	c, _ := rs.wait(true, false, nil)
	sa.release()
	<-b.ready
	rs.giveUp(b)
	if _, ok := rs.take(b); ok {
		t.Error("a wait given up took its slot")
	}
	select {
	case <-c.ready:
	default:
		t.Fatal("the slot b gave up did not go to c")
	}
	sc, _ := rs.take(c)

	d, _ := rs.wait(false, false, func() {})
	sc.release()
	sd, ok := rs.take(d)
	if !ok {
		t.Fatal("d got no slot")
	}
	if _, stopOther := rs.wait(true, false, nil); stopOther == nil {
		t.Fatal("a request did not ask d's slot to give way")
	}
	if sd.running(false) {
		t.Error("chatter started in a slot asked to give way")
	}
	if !sd.running(true) {
		t.Error("a request may run in any slot")
	}
}
