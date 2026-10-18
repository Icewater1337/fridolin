package main

import (
	"slices"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
)

// Debouncer merges messages that one person sends in quick succession in one
// chat — five photos, or a file followed by "what is this?" — into a single
// brain run. A batch is flushed once the chat has been quiet for the quiet
// period, and never later than the max wait after its first message.
type Debouncer struct {
	quiet   func() time.Duration
	maxWait func() time.Duration
	flush   func(Inbound)

	mu      sync.Mutex
	batches map[string]*pendingBatch
	// drained is set by Drain: the batches are gone, and Add flushes at
	// once from then on.
	drained bool
	// flushing counts the flushes a timer has begun, which Drain waits
	// for.
	flushing sync.WaitGroup
}

type pendingBatch struct {
	msg     Inbound
	started time.Time
	timer   *time.Timer
	// gen counts the messages merged into the batch, and each timer knows
	// the one it was armed for. A timer that went off while Add held d.mu
	// finds the batch moved on and leaves it to the timer Add armed:
	// stopping it is too late by then, and it would flush the batch with
	// the message just merged before that message's quiet period.
	gen uint64
}

// NewDebouncer creates a debouncer. quiet and maxWait are read for every
// message, so configuration changes apply immediately.
func NewDebouncer(quiet, maxWait func() time.Duration, flush func(Inbound)) *Debouncer {
	return &Debouncer{quiet: quiet, maxWait: maxWait, flush: flush, batches: map[string]*pendingBatch{}}
}

// batchKey includes the target brain: two hand-offs for different brains that
// answer in the same chat must not merge into one run in one brain.
func batchKey(m Inbound) string { return m.Brain + "|" + m.JID + "|" + m.Sender }

// Add queues m, merging it into the sender's open batch for the chat.
func (d *Debouncer) Add(m Inbound) {
	quiet := d.quiet()
	d.mu.Lock()
	if quiet <= 0 || d.drained {
		d.mu.Unlock()
		d.flush(m)
		return
	}
	msg, due := d.addLocked(m, quiet, time.Now())
	d.mu.Unlock()
	if due {
		d.flush(msg)
	}
}

// addLocked merges m into its batch and arms the batch's timer for quiet
// from now, or takes the batch out when its max wait is over: it returns
// the batch's message then, for Add to flush once d.mu is free. d.mu is
// held.
func (d *Debouncer) addLocked(m Inbound, quiet time.Duration, now time.Time) (Inbound, bool) {
	key := batchKey(m)
	b, ok := d.batches[key]
	if ok {
		b.msg = b.msg.merge(m)
		b.timer.Stop()
	} else {
		b = &pendingBatch{msg: m, started: now}
		d.batches[key] = b
	}
	b.gen++
	wait := quiet
	if limit := d.maxWait(); limit > 0 {
		if left := b.started.Add(limit).Sub(now); left < wait {
			wait = left
		}
	}
	if wait <= 0 {
		delete(d.batches, key)
		return b.msg, true
	}
	gen := b.gen
	b.timer = time.AfterFunc(wait, func() { d.fire(key, b, gen) })
	return Inbound{}, false
}

// fire flushes the batch b whose timer for its gen-th message went off,
// unless something else took it out or a later message moved it on.
func (d *Debouncer) fire(key string, b *pendingBatch, gen uint64) {
	d.mu.Lock()
	if d.batches[key] != b || b.gen != gen {
		d.mu.Unlock()
		return
	}
	delete(d.batches, key)
	msg := b.msg
	d.flushing.Add(1)
	d.mu.Unlock()
	defer d.flushing.Done()
	d.flush(msg)
}

// Change applies an edit or a deletion to the open batch that holds the
// message it changes, and reports whether one did. A batch left empty by
// a deletion is dropped: nothing of it reaches a brain.
func (d *Debouncer) Change(c chat.Change) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, b := range d.batches {
		if !b.msg.changes(c) {
			continue
		}
		msg, _, keep := b.msg.applyChange(c)
		if !keep {
			b.timer.Stop()
			delete(d.batches, k)
			return true
		}
		b.msg = msg
		return true
	}
	return false
}

// Drop discards every open batch for a chat (after !reset or !stop).
func (d *Debouncer) Drop(jid string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, b := range d.batches {
		if b.msg.JID == jid {
			b.timer.Stop()
			delete(d.batches, k)
		}
	}
}

// Drain takes out every open batch and returns their messages, the oldest
// batch first, for the bridge's stop to keep (Bridge.Stop). It waits for
// a flush a timer has begun: that message is in a brain's queue once Drain
// returns. From then on, Add flushes each message at once.
func (d *Debouncer) Drain() []Inbound {
	d.mu.Lock()
	d.drained = true
	batches := make([]*pendingBatch, 0, len(d.batches))
	for k, b := range d.batches {
		b.timer.Stop()
		delete(d.batches, k)
		batches = append(batches, b)
	}
	d.mu.Unlock()
	d.flushing.Wait()
	slices.SortFunc(batches, func(a, b *pendingBatch) int { return a.started.Compare(b.started) })
	out := make([]Inbound, len(batches))
	for i, b := range batches {
		out[i] = b.msg
	}
	return out
}

// Pending returns how many batches are waiting.
func (d *Debouncer) Pending() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.batches)
}
