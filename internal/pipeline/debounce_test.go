package pipeline

import (
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

type flushes struct {
	mu  sync.Mutex
	got []brain.Inbound
	at  []time.Time
}

func (f *flushes) add(m brain.Inbound) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, m)
	f.at = append(f.at, time.Now())
}

func (f *flushes) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

func newTestDebouncer(quiet, maxWait time.Duration) (*Debouncer, *flushes) {
	f := &flushes{}
	return NewDebouncer(func() time.Duration { return quiet }, func() time.Duration { return maxWait }, f.add), f
}

func TestDebounceMergesBurstAndKeepsTrigger(t *testing.T) {
	d, f := newTestDebouncer(80*time.Millisecond, time.Second)
	base := brain.Inbound{Inbound: chat.Inbound{JID: "g@g.us", Sender: "1"}}
	m1 := base
	m1.Media, m1.Silent = []string{"/m/1.jpg"}, false
	m2 := base
	m2.Text, m2.Silent = "untagged chatter", true
	m3 := base
	m3.Media = []string{"/m/2.jpg"}
	d.Add(m1)
	d.Add(m2)
	d.Add(m3)
	testutil.WaitFor(t, 2*time.Second, "flush", func() bool { return f.n() == 1 })
	got := f.got[0]
	if len(got.Media) != 2 || got.Media[0] != "/m/1.jpg" || got.Media[1] != "/m/2.jpg" {
		t.Errorf("media = %v", got.Media)
	}
	if got.Silent {
		t.Error("a batch with any tagged message must not be silent")
	}
	time.Sleep(150 * time.Millisecond)
	if f.n() != 1 {
		t.Error("flushed more than once")
	}
}

func TestDebounceMaxWaitCapsContinuousInput(t *testing.T) {
	d, f := newTestDebouncer(50*time.Millisecond, 120*time.Millisecond)
	start := time.Now()
	stop := time.After(400 * time.Millisecond)
loop:
	for {
		select {
		case <-stop:
			break loop
		default:
			d.Add(brain.Inbound{Inbound: chat.Inbound{JID: "c", Sender: "1", Text: "x"}})
			time.Sleep(10 * time.Millisecond)
		}
	}
	testutil.WaitFor(t, time.Second, "flushes", func() bool { return f.n() >= 3 })
	f.mu.Lock()
	first := f.at[0].Sub(start)
	f.mu.Unlock()
	if first > 200*time.Millisecond {
		t.Errorf("first flush after %v; max wait is 120ms", first)
	}
}

func TestDebounceSeparatesSendersAndChats(t *testing.T) {
	d, f := newTestDebouncer(40*time.Millisecond, time.Second)
	d.Add(brain.Inbound{Inbound: chat.Inbound{JID: "g", Sender: "1", Text: "a"}})
	d.Add(brain.Inbound{Inbound: chat.Inbound{JID: "g", Sender: "2", Text: "b"}})
	d.Add(brain.Inbound{Inbound: chat.Inbound{JID: "h", Sender: "1", Text: "c"}})
	testutil.WaitFor(t, time.Second, "three flushes", func() bool { return f.n() == 3 })
}

// Two hand-offs for different brains that answer in the same chat, from the
// same default sender, must stay two runs, each in its own brain.
func TestDebounceKeepsTargetBrainsApart(t *testing.T) {
	d, f := newTestDebouncer(40*time.Millisecond, time.Second)
	d.Add(brain.Inbound{Inbound: chat.Inbound{JID: "dm", Brain: "up", Sender: "upload", Text: "a"}})
	d.Add(brain.Inbound{Inbound: chat.Inbound{JID: "dm", Brain: "notes", Sender: "upload", Text: "b"}})
	testutil.WaitFor(t, time.Second, "two flushes", func() bool { return f.n() == 2 })
	f.mu.Lock()
	defer f.mu.Unlock()
	got := map[string]string{}
	for _, m := range f.got {
		got[m.Brain] = m.Text
	}
	if got["up"] != "a" || got["notes"] != "b" {
		t.Errorf("flushed %+v, want up:a and notes:b", f.got)
	}
}

func TestDebounceOffAndDrop(t *testing.T) {
	d, f := newTestDebouncer(0, 0)
	d.Add(brain.Inbound{Inbound: chat.Inbound{JID: "g", Sender: "1", Text: "now"}})
	if f.n() != 1 {
		t.Error("quiet 0 must flush immediately")
	}
	d2, f2 := newTestDebouncer(50*time.Millisecond, time.Second)
	d2.Add(brain.Inbound{Inbound: chat.Inbound{JID: "g", Sender: "1", Text: "a"}})
	d2.Drop("g")
	time.Sleep(120 * time.Millisecond)
	if f2.n() != 0 {
		t.Error("a dropped batch must not flush")
	}
}

// A timer that goes off while Add holds the lock, merging a message into
// its batch, is too late to be stopped. It must leave the batch to the
// timer Add arms for that message, and not flush it at once, before the
// message's quiet period.
func TestDebounceTimerThatFiredDuringAddDoesNotFlushEarly(t *testing.T) {
	d, f := newTestDebouncer(10*time.Millisecond, 0)
	d.Add(brain.Inbound{Inbound: chat.Inbound{JID: "c", Sender: "1", Text: "first"}})

	// What Add does for the second message, with the first message's timer
	// gone off meanwhile and waiting for the lock.
	d.mu.Lock()
	time.Sleep(100 * time.Millisecond)
	if _, due := d.addLocked(brain.Inbound{Inbound: chat.Inbound{JID: "c", Sender: "1", Text: "second"}}, 10*time.Second, time.Now()); due {
		d.mu.Unlock()
		t.Fatal("the batch was due at once")
	}
	d.mu.Unlock()

	time.Sleep(200 * time.Millisecond)
	if n := f.n(); n != 0 {
		t.Fatalf("%d flushes right after the second message, want none before its quiet period", n)
	}
	if d.Pending() != 1 {
		t.Errorf("%d batches waiting, want the merged one", d.Pending())
	}
	d.Drop("c")
}

// Drain takes out every batch, the oldest first, for the bridge's stop to
// keep, and nothing of them flushes after. It waits for a flush a timer has
// begun, and a message added after it flushes at once.
func TestDebounceDrain(t *testing.T) {
	release := make(chan struct{})
	f := &flushes{}
	d := NewDebouncer(func() time.Duration { return 50 * time.Millisecond }, func() time.Duration { return 0 }, func(m brain.Inbound) {
		if m.Text == "slow" {
			<-release
		}
		f.add(m)
	})
	d.Add(brain.Inbound{Inbound: chat.Inbound{JID: "c", Sender: "1", Text: "slow"}})
	testutil.WaitFor(t, time.Second, "the flush to begin", func() bool { return d.Pending() == 0 })
	d.Add(brain.Inbound{Inbound: chat.Inbound{JID: "c", Sender: "2", Text: "older"}})
	time.Sleep(5 * time.Millisecond)
	d.Add(brain.Inbound{Inbound: chat.Inbound{JID: "c", Sender: "3", Text: "newer"}})

	drained := make(chan []brain.Inbound, 1)
	go func() { drained <- d.Drain() }()
	select {
	case <-drained:
		t.Fatal("Drain returned while a flush was under way")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	got := <-drained
	if len(got) != 2 || got[0].Text != "older" || got[1].Text != "newer" {
		t.Errorf("drained %+v, want the two batches, oldest first", got)
	}
	time.Sleep(100 * time.Millisecond)
	if f.n() != 1 {
		t.Errorf("%d flushes, want only the one under way", f.n())
	}
	d.Add(brain.Inbound{Inbound: chat.Inbound{JID: "c", Sender: "4", Text: "after"}})
	if f.n() != 2 {
		t.Error("a message added after Drain did not flush at once")
	}
}

// A deletion or an edit of a message the debouncer still holds reaches
// the brain with the batch: the deleted part is gone, the edited one has
// its new text.
func TestDebouncerAppliesChanges(t *testing.T) {
	d, f := newTestDebouncer(80*time.Millisecond, time.Second)
	base := brain.Inbound{Inbound: chat.Inbound{JID: "15551234567@s.whatsapp.net", Sender: "15551234567"}}
	only := base
	only.Text, only.ID = "a secret", "A"
	d.Add(only)
	if !d.Change(chat.Change{Kind: chat.ChangeRevoke, Chat: "15551234567@s.whatsapp.net", Sender: "15551234567", Target: "A"}) {
		t.Fatal("the deletion did not find the message")
	}
	if d.Pending() != 0 {
		t.Errorf("%d batches wait, want the emptied one dropped", d.Pending())
	}

	first, second := base, base
	first.Text, first.ID = "book Tuesday", "B"
	second.Text, second.ID = "for four", "C"
	d.Add(first)
	d.Add(second)
	d.Change(chat.Change{Kind: chat.ChangeEdit, Chat: "15551234567@s.whatsapp.net", Sender: "15551234567", Target: "B", Text: "book Wednesday"})
	testutil.WaitFor(t, 2*time.Second, "the flush", func() bool { return f.n() == 1 })
	if got := f.got[0]; got.Text != "book Wednesday\n\nfor four" {
		t.Errorf("flushed %q, want the edited batch", got.Text)
	}
}
